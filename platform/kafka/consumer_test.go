package kafka

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/canhtoanptit/collection-platform/contracts"
	"github.com/canhtoanptit/collection-platform/platform/events"
)

// validConsumerConfig is the minimum that must be accepted. Tests mutate a copy
// so each row states exactly one thing that is wrong.
func validConsumerConfig(t *testing.T) ConsumerConfig {
	t.Helper()
	registry, err := events.NewRegistry(contracts.FS)
	if err != nil {
		t.Fatalf("compiling the event registry: %v", err)
	}
	return ConsumerConfig{
		Brokers:  []string{"localhost:9092"},
		Group:    "case-service",
		Topics:   []string{"collections.delinquency"},
		Registry: registry,
		Handler:  func(context.Context, events.Envelope) error { return nil },
		DLQTopic: "collections.dlq.case-service",
	}
}

// TestConsumerConfigRejects is the fail-complete check. Every one of these is a
// wiring mistake that would otherwise become a runtime surprise: no registry
// means unvalidated JSON reaching business code, and no DLQ topic means a poison
// message has nowhere to go but a blocked partition or the bin.
func TestConsumerConfigRejects(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*ConsumerConfig)
		wantMsg string
	}{
		{"no brokers", func(c *ConsumerConfig) { c.Brokers = nil }, "KAFKA_BROKERS"},
		{"no group", func(c *ConsumerConfig) { c.Group = "" }, "KAFKA_CONSUMER_GROUP"},
		{"no topics", func(c *ConsumerConfig) { c.Topics = nil }, "KAFKA_TOPICS"},
		{"no registry", func(c *ConsumerConfig) { c.Registry = nil }, "every message must be validated"},
		{"no handler", func(c *ConsumerConfig) { c.Handler = nil }, "no handler"},
		{"no DLQ topic", func(c *ConsumerConfig) { c.DLQTopic = "" }, "KAFKA_DLQ_TOPIC"},
		{
			name:    "subscribing to its own DLQ",
			mutate:  func(c *ConsumerConfig) { c.Topics = append(c.Topics, c.DLQTopic) },
			wantMsg: "reads its own dead letters",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConsumerConfig(t)
			tc.mutate(&cfg)

			if _, err := cfg.validated(); err == nil {
				t.Fatal("validated() accepted the configuration")
			} else if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error %q does not mention %q", err, tc.wantMsg)
			}

			if _, err := NewConsumer(cfg); err == nil {
				t.Error("NewConsumer accepted the configuration")
			}
		})
	}
}

// TestConsumerConfigReportsEveryProblemAtOnce: fixing five variables through
// five restarts of a Deployment is a bad afternoon (the platform/config rule).
func TestConsumerConfigReportsEveryProblemAtOnce(t *testing.T) {
	_, err := ConsumerConfig{}.validated()
	if err == nil {
		t.Fatal("an empty ConsumerConfig was accepted")
	}
	for _, want := range []string{
		"KAFKA_BROKERS", "KAFKA_CONSUMER_GROUP", "KAFKA_TOPICS",
		"registry", "handler", "KAFKA_DLQ_TOPIC",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
}

func TestConsumerConfigDefaults(t *testing.T) {
	cfg, err := validConsumerConfig(t).validated()
	if err != nil {
		t.Fatalf("validated(): %v", err)
	}

	tests := []struct {
		field string
		got   any
		want  any
	}{
		{"MaxRetries", cfg.MaxRetries, defaultMaxRetries},
		{"Backoff", cfg.Backoff, defaultBackoff},
		{"MaxBackoff", cfg.MaxBackoff, defaultMaxBackoff},
		{"MaxPollRecords", cfg.MaxPollRecords, defaultMaxPollRecords},
		{"DrainTimeout", cfg.DrainTimeout, defaultDrainTimeout},
		{"ClientID defaults to Group", cfg.ClientID, "case-service"},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.field, tc.got, tc.want)
		}
	}
}

// TestConsumerConfigNormalisation covers the values a human can plausibly set
// that must not become nonsense: "no retries" and a backoff ceiling below the
// floor.
func TestConsumerConfigNormalisation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ConsumerConfig)
		assert func(t *testing.T, cfg ConsumerConfig)
	}{
		{
			name:   "a negative MaxRetries means no retries, not a negative budget",
			mutate: func(c *ConsumerConfig) { c.MaxRetries = -1 },
			assert: func(t *testing.T, cfg ConsumerConfig) {
				if cfg.MaxRetries != 0 {
					t.Errorf("MaxRetries = %d, want 0", cfg.MaxRetries)
				}
			},
		},
		{
			name:   "an explicit retry budget is kept",
			mutate: func(c *ConsumerConfig) { c.MaxRetries = 7 },
			assert: func(t *testing.T, cfg ConsumerConfig) {
				if cfg.MaxRetries != 7 {
					t.Errorf("MaxRetries = %d, want 7", cfg.MaxRetries)
				}
			},
		},
		{
			name: "MaxBackoff below Backoff is raised rather than shrinking the first wait",
			mutate: func(c *ConsumerConfig) {
				c.Backoff = 2 * time.Second
				c.MaxBackoff = 100 * time.Millisecond
			},
			assert: func(t *testing.T, cfg ConsumerConfig) {
				if cfg.MaxBackoff != 2*time.Second {
					t.Errorf("MaxBackoff = %v, want 2s", cfg.MaxBackoff)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConsumerConfig(t)
			tc.mutate(&cfg)
			normalised, err := cfg.validated()
			if err != nil {
				t.Fatalf("validated(): %v", err)
			}
			tc.assert(t, normalised)
		})
	}
}

// TestNewConsumerAcceptsAValidConfig also proves construction does not dial:
// these brokers do not exist, and NewConsumer must still succeed so a pod can
// start and report itself unready.
func TestNewConsumerAcceptsAValidConfig(t *testing.T) {
	consumer, err := NewConsumer(validConsumerConfig(t))
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	t.Cleanup(consumer.Close)

	check := consumer.ReadyCheck()
	if check.Name != "kafka-consumer" {
		t.Errorf("Check.Name = %q, want kafka-consumer", check.Name)
	}
}

// TestConsumerCloseIsIdempotent: Run closes the client on return, and a caller
// that also defers Close must not hit franz-go's panic on a double close.
func TestConsumerCloseIsIdempotent(t *testing.T) {
	consumer, err := NewConsumer(validConsumerConfig(t))
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	consumer.Close()
	consumer.Close()
}

// TestConsumerReadyCheckFailsWithoutAGroup asserts the half of readiness that is
// easy to forget: a client that cannot reach a broker has not joined the group,
// and a probe that only checked reachability would still be wrong for a member
// stuck outside the group.
func TestConsumerReadyCheckFailsWithoutAGroup(t *testing.T) {
	cfg := validConsumerConfig(t)
	cfg.Brokers = []string{"127.0.0.1:1"}

	consumer, err := NewConsumer(cfg)
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	t.Cleanup(consumer.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := consumer.ReadyCheck().Probe(ctx); err == nil {
		t.Fatal("the probe reported ready without a broker or a group")
	}
}

// TestRunReturnsOnAnAlreadyCancelledContext is the shutdown fast path: a
// consumer whose context is cancelled before it ever polls must return, not
// block.
func TestRunReturnsOnAnAlreadyCancelledContext(t *testing.T) {
	cfg := validConsumerConfig(t)
	cfg.Brokers = []string{"127.0.0.1:1"}

	consumer, err := NewConsumer(cfg)
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	done := make(chan error, 1)
	go func() { done <- consumer.Run(ctx) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run on a cancelled context returned %v, want nil", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return for a cancelled context")
	}
}

// TestRunReturnsWhenTheClientIsClosed is the other shutdown path: Close is
// called (by a supervisor, or by Run's own defer on a second Run) while the loop
// is polling. franz-go reports it on the fetch, and Run must treat it as a clean
// stop rather than an error.
func TestRunReturnsWhenTheClientIsClosed(t *testing.T) {
	cfg := validConsumerConfig(t)
	cfg.Brokers = []string{"127.0.0.1:1"}

	consumer, err := NewConsumer(cfg)
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	consumer.Close()

	done := make(chan error, 1)
	go func() { done <- consumer.Run(t.Context()) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run on a closed client returned %v, want nil", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return for a closed client")
	}
}

// TestLogFetchErrors covers the fetch-error path without a broker. A partition
// that keeps erroring is invisible without it, and a shutdown must not log its
// own cancellation as a broker problem.
func TestLogFetchErrors(t *testing.T) {
	consumer, err := NewConsumer(validConsumerConfig(t))
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	t.Cleanup(consumer.Close)

	tests := []struct {
		name string
		err  error
	}{
		{"a broker-side failure", errors.New("NOT_LEADER_FOR_PARTITION")},
		{"a cancelled poll, which is a shutdown and not a problem", context.Canceled},
		{"an expired batch budget", context.DeadlineExceeded},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// The assertion is that every kind of fetch error is handled without
			// panicking; which of them reach the log is a judgement recorded in
			// logFetchErrors, not a contract a caller depends on.
			consumer.logFetchErrors(t.Context(), kgo.NewErrFetch(tc.err))
		})
	}
}

func TestSleepRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := sleep(ctx, time.Hour); err == nil {
		t.Fatal("sleep ignored a cancelled context and would have waited an hour")
	}
	if err := sleep(t.Context(), time.Millisecond); err != nil {
		t.Fatalf("sleep on a live context: %v", err)
	}
}
