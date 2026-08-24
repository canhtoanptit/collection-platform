package kafka

import (
	"context"
	"crypto/tls"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/canhtoanptit/collection-platform/platform/config"
)

// TestConfigDefaults pins the environment contract. These variable names appear
// in Deployment manifests, so a rename is a silent production change.
func TestConfigDefaults(t *testing.T) {
	t.Setenv("KAFKA_BROKERS", "b-1:9092,b-2:9092")

	cfg, err := config.Load[Config]()
	if err != nil {
		t.Fatalf("loading kafka.Config: %v", err)
	}

	tests := []struct {
		field string
		got   any
		want  any
	}{
		{"TLS", cfg.TLS, false},
		{"SASLIAMRegion", cfg.SASLIAMRegion, ""},
		{"Linger", cfg.Linger, defaultLinger},
		{"MaxBufferedRecords", cfg.MaxBufferedRecords, defaultMaxBufferedRecords},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("Config.%s = %v, want %v", tc.field, tc.got, tc.want)
		}
	}
	if len(cfg.Brokers) != 2 {
		t.Errorf("Brokers = %v, want two entries", cfg.Brokers)
	}
}

// TestConnectionOpts covers the three deployment shapes the same struct has to
// express, and asserts them through the client rather than by inspecting the
// option slice — an option that is built but not accepted by kgo is worthless.
func TestConnectionOpts(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantTLS bool
		assert  func(t *testing.T, cl *kgo.Client)
	}{
		{
			name: "local redpanda: plaintext, no auth",
			cfg:  Config{Brokers: []string{"localhost:9092"}},
		},
		{
			name:    "a TLS listener without authentication",
			cfg:     Config{Brokers: []string{"kafka:9093"}, TLS: true},
			wantTLS: true,
		},
		{
			name: "MSK IAM implies TLS even when TLS is not set",
			cfg: Config{
				Brokers:       []string{"b-1.colx.abc123.c2.kafka.eu-west-1.amazonaws.com:9098"},
				SASLIAMRegion: "eu-west-1",
			},
			wantTLS: true,
		},
		{
			name: "the client id reaches the brokers",
			cfg:  Config{Brokers: []string{"localhost:9092"}, ClientID: "case-service"},
			assert: func(t *testing.T, cl *kgo.Client) {
				if got := cl.OptValue(kgo.ClientID); got != "case-service" {
					t.Errorf("ClientID = %v, want case-service", got)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := tc.cfg.connectionOpts()
			if err != nil {
				t.Fatalf("connectionOpts: %v", err)
			}
			client, err := kgo.NewClient(opts...)
			if err != nil {
				t.Fatalf("kgo rejected the options: %v", err)
			}
			defer client.Close()

			dialTLS, _ := client.OptValue(kgo.DialTLSConfig).(*tls.Config)
			if tc.wantTLS {
				if dialTLS == nil {
					t.Fatal("no TLS config: credentials or data would go over plaintext")
				}
				if dialTLS.MinVersion != tlsMinVersion {
					t.Errorf("TLS MinVersion = %x, want %x", dialTLS.MinVersion, tlsMinVersion)
				}
			} else if dialTLS != nil {
				t.Error("TLS was configured for a plaintext deployment")
			}
			if tc.assert != nil {
				tc.assert(t, client)
			}
		})
	}
}

func TestConnectionOptsRequiresBrokers(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{"no brokers at all", Config{}},
		{"an empty list", Config{Brokers: []string{}}},
		{"only blank entries, as a trailing comma produces", Config{Brokers: []string{"", "  "}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.cfg.connectionOpts(); err == nil {
				t.Fatal("connectionOpts accepted a configuration with no brokers")
			}
		})
	}
}

// TestProducerDurability is the acks=all + idempotent-producer assertion, read
// back off a real client. These two settings are the entire reason a published
// event can be trusted, and both are easy to lose in a refactor.
func TestProducerDurability(t *testing.T) {
	cfg := Config{Brokers: []string{"localhost:9092"}, Linger: 7 * time.Millisecond, MaxBufferedRecords: 42}

	opts, err := cfg.connectionOpts()
	if err != nil {
		t.Fatalf("connectionOpts: %v", err)
	}
	client, err := kgo.NewClient(append(opts, cfg.producerOpts()...)...)
	if err != nil {
		t.Fatalf("kgo rejected the producer options: %v", err)
	}
	defer client.Close()

	if got := client.OptValue(kgo.RequiredAcks); got != kgo.AllISRAcks() {
		t.Errorf("RequiredAcks = %v, want all in-sync replicas", got)
	}
	if disabled, _ := client.OptValue(kgo.DisableIdempotentWrite).(bool); disabled {
		t.Error("the idempotent producer is disabled — a retried produce could append twice")
	}
	if got := client.OptValue(kgo.ProducerLinger); got != 7*time.Millisecond {
		t.Errorf("ProducerLinger = %v, want 7ms", got)
	}
	if got := client.OptValue(kgo.MaxBufferedRecords); got != int64(42) {
		t.Errorf("MaxBufferedRecords = %v (%T), want 42", got, got)
	}
}

func TestProducerOptsFallBackToDefaults(t *testing.T) {
	cfg := Config{Brokers: []string{"localhost:9092"}}

	opts, err := cfg.connectionOpts()
	if err != nil {
		t.Fatalf("connectionOpts: %v", err)
	}
	client, err := kgo.NewClient(append(opts, cfg.producerOpts()...)...)
	if err != nil {
		t.Fatalf("kgo rejected the producer options: %v", err)
	}
	defer client.Close()

	if got := client.OptValue(kgo.ProducerLinger); got != defaultLinger {
		t.Errorf("ProducerLinger = %v, want %v", got, defaultLinger)
	}
	if got := client.OptValue(kgo.MaxBufferedRecords); got != int64(defaultMaxBufferedRecords) {
		t.Errorf("MaxBufferedRecords = %v (%T), want %d", got, got, defaultMaxBufferedRecords)
	}
}

// TestNewPublisherRejectsBadConfig proves construction fails before anything
// dials, and that a caller gets a message naming the variable to fix.
func TestNewPublisherRejectsBadConfig(t *testing.T) {
	_, err := NewPublisher(Config{})
	if err == nil {
		t.Fatal("NewPublisher accepted a configuration with no brokers")
	}
	if !strings.Contains(err.Error(), "KAFKA_BROKERS") {
		t.Errorf("the error does not name KAFKA_BROKERS: %v", err)
	}
}

// TestPublishRejectsUnusableRecords covers the checks that happen before a
// produce is attempted. An empty value is always a bug — an A§24 envelope is
// never zero bytes — and finding out at the broker costs a round trip and a
// confusing error.
func TestPublishRejectsUnusableRecords(t *testing.T) {
	publisher, err := NewPublisher(Config{Brokers: []string{"127.0.0.1:1"}})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	defer publisher.Close()

	tests := []struct {
		name  string
		topic string
		value []byte
	}{
		{"no topic", "", []byte(`{"eventId":"x"}`)},
		{"no value", "collections.case", nil},
		{"an empty value", "collections.case", []byte{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := publisher.Publish(t.Context(), tc.topic, "KEY", tc.value, nil)
			if err == nil {
				t.Fatal("Publish accepted an unusable record")
			}
		})
	}
}

// TestPublisherCloseIsIdempotent matters because Close is normally both deferred
// in main and called by a shutdown path; franz-go's Close panics on a second
// call.
func TestPublisherCloseIsIdempotent(t *testing.T) {
	publisher, err := NewPublisher(Config{Brokers: []string{"127.0.0.1:1"}})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	publisher.Close()
	publisher.Close()
}

// TestPublisherReadyCheck proves the probe fails when the brokers are
// unreachable. It is the case that matters: a probe that only ever answers "ok"
// would keep a disconnected pod in the Service.
func TestPublisherReadyCheck(t *testing.T) {
	publisher, err := NewPublisher(Config{Brokers: []string{"127.0.0.1:1"}})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	defer publisher.Close()

	check := publisher.ReadyCheck()
	if check.Name != "kafka-publisher" {
		t.Errorf("Check.Name = %q, want kafka-publisher", check.Name)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := check.Probe(ctx); err == nil {
		t.Fatal("the probe reported ready with no reachable broker")
	}
}

func TestNonEmpty(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want int
	}{
		{"a clean list", []string{"a:9092", "b:9092"}, 2},
		{"a trailing comma in a values file", []string{"a:9092", ""}, 1},
		{"padded entries", []string{" a:9092 ", "\tb:9092"}, 2},
		{"nothing at all", nil, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := nonEmpty(tc.in)
			if len(got) != tc.want {
				t.Fatalf("nonEmpty(%q) = %q, want %d entries", tc.in, got, tc.want)
			}
			for _, v := range got {
				if v != strings.TrimSpace(v) || v == "" {
					t.Errorf("entry %q was not trimmed or is empty", v)
				}
			}
		})
	}
}
