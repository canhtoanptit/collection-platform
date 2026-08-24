package kafka

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/canhtoanptit/collection-platform/platform/health"
	"github.com/canhtoanptit/collection-platform/platform/otelkit"
)

// Publisher publishes one record to one topic.
//
// It is an interface so the outbox relay (LIB-C) can be tested against a fake
// and so a broker swap stays inside this package (D§58). Note what it is *not*:
// there is no "publish this envelope" method, because a service must not publish
// an envelope directly — it enqueues one in the outbox, inside the transaction
// that produced it, and the relay calls this (CLAUDE.md §5).
//
// Implementations must be safe for concurrent use.
type Publisher interface {
	// Publish sends value to topic under key, waits for the brokers to
	// acknowledge it, and returns an error if they did not.
	//
	// key must be the aggregate id named in the CON-2 topic/key map (A§26):
	// ordering is per key, so the wrong key silently loses the ordering
	// guarantee a consumer is relying on. An empty key means "no ordering
	// guarantee wanted" and the record is distributed round-robin.
	//
	// headers are added on top of the trace and correlation headers the
	// implementation injects from ctx; a key present in both wins here.
	Publish(ctx context.Context, topic, key string, value []byte, headers map[string]string) error
}

// FranzPublisher is the franz-go [Publisher]: acks=all, idempotent producer, and
// a synchronous Publish that only returns once the record is durable.
//
// Publish is synchronous on purpose. The outbox relay marks a row published
// *after* the broker acknowledges it, so an asynchronous send would let the relay
// mark rows for records that never landed — the one failure mode the outbox
// exists to prevent. Throughput comes from batching (Linger) and from the relay
// publishing a batch of goroutines, not from fire-and-forget.
type FranzPublisher struct {
	client  *kgo.Client
	metrics *metrics
	tracer  trace.Tracer

	closeOnce sync.Once
}

// Compile-time proof that the concrete type satisfies the interface services
// depend on. A signature drift is then a build failure here, not at a call site
// in another module.
var _ Publisher = (*FranzPublisher)(nil)

// NewPublisher builds the publisher. It does not dial: franz-go connects lazily,
// so a broker that is briefly unavailable at startup does not prevent a pod from
// coming up and reporting itself unready (see [FranzPublisher.ReadyCheck]).
func NewPublisher(cfg Config) (*FranzPublisher, error) {
	opts, err := cfg.connectionOpts()
	if err != nil {
		return nil, fmt.Errorf("creating the Kafka publisher: %w", err)
	}
	opts = append(opts, cfg.producerOpts()...)

	client, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("creating the Kafka publisher: %w", err)
	}

	m, err := newMetrics()
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("creating the Kafka publisher: %w", err)
	}

	return &FranzPublisher{client: client, metrics: m, tracer: newTracer()}, nil
}

// Publish implements [Publisher].
func (p *FranzPublisher) Publish(
	ctx context.Context,
	topic, key string,
	value []byte,
	headers map[string]string,
) error {
	if topic == "" {
		return errors.New("publishing: no topic")
	}
	if len(value) == 0 {
		return fmt.Errorf("publishing to %s: empty value", topic)
	}

	ctx, span := p.tracer.Start(ctx, "kafka.publish "+topic,
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "kafka"),
			attribute.String("messaging.destination.name", topic),
		))
	defer span.End()

	// The propagation headers are read from the *span's* context, so a consumer
	// continues this publish span rather than the caller's parent. Caller-supplied
	// headers are applied afterwards and win: the outbox relay replays a record's
	// original headers, and a replay must not be re-parented onto the relay.
	wire := otelkit.KafkaHeaders(ctx)
	for k, v := range headers {
		wire[k] = v
	}

	record := &kgo.Record{
		Topic:   topic,
		Key:     []byte(key),
		Value:   value,
		Headers: toRecordHeaders(wire),
	}

	if err := p.client.ProduceSync(ctx, record).FirstErr(); err != nil {
		p.metrics.recordPublishFailed(ctx, topic)
		span.RecordError(err)
		span.SetStatus(codes.Error, "publish failed")
		return fmt.Errorf("publishing to %s (key %q): %w", topic, key, err)
	}
	p.metrics.recordPublished(ctx, topic)
	return nil
}

// Close flushes buffered records and shuts the client down. It is safe to call
// more than once, which matters because it is normally both deferred in main and
// called by a shutdown path.
func (p *FranzPublisher) Close() {
	p.closeOnce.Do(p.client.Close)
}

// ReadyCheck reports the broker connection as a readiness dependency.
//
// The probe is a metadata request to one broker. A publisher that cannot reach
// the cluster must take the pod out of the Service: its commands would otherwise
// succeed at the API and then fail in the relay, which reads to a caller as data
// loss.
func (p *FranzPublisher) ReadyCheck() health.Check {
	return health.Check{
		Name: "kafka-publisher",
		Probe: func(ctx context.Context) error {
			if err := p.client.Ping(ctx); err != nil {
				return fmt.Errorf("pinging the Kafka brokers: %w", err)
			}
			return nil
		},
	}
}
