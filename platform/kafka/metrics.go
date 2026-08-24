package kafka

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// scopeName is the instrumentation scope for this package's spans and metrics.
// It is the import path by convention, so a Grafana query can attribute a metric
// to the library that emitted it.
const scopeName = "github.com/canhtoanptit/collection-platform/platform/kafka"

// Instrument names. The `colx_` prefix is the platform's Prometheus convention
// (deployment/observability); the OTLP-to-Prometheus translator turns dots into
// underscores and appends _total to a monotonic counter, so
// colx_kafka_consumer_records_consumed becomes
// colx_kafka_consumer_records_consumed_total on the scrape endpoint.
const (
	metricConsumed     = "colx_kafka_consumer_records_consumed"
	metricSkipped      = "colx_kafka_consumer_records_skipped"
	metricRetried      = "colx_kafka_consumer_handler_retries"
	metricDeadLettered = "colx_kafka_consumer_records_dead_lettered"
	metricUnsettled    = "colx_kafka_consumer_records_unsettled"
	metricHandle       = "colx_kafka_consumer_handle_duration_seconds"
	metricPublished    = "colx_kafka_publisher_records_published"
	metricPublishError = "colx_kafka_publisher_errors"
)

// Metric attribute keys. Deliberately few: topic and consumer group are bounded
// by the topic map (A§25), while an event type or a key is not, and a Prometheus
// label with unbounded cardinality is how a monitoring stack falls over.
const (
	attrTopic  = "topic"
	attrGroup  = "group"
	attrReason = "reason"
)

// Reasons a record was dead-lettered, used as the `reason` label. Two values,
// because they call for different operator responses: a schema violation is a
// producer bug or a contract drift, a handler failure is a consumer problem.
const (
	reasonSchema  = "schema_violation"
	reasonHandler = "handler_failed"
)

// metrics is the instrument set shared by the publisher and the consumer.
//
// Instruments are created per publisher/consumer rather than in a package-level
// once: the global meter provider is installed by otelkit.Init, and a
// package-level cache built before it would freeze whatever provider happened to
// be current when the first instrument was made — which in a test is whatever
// ran first.
type metrics struct {
	consumed     metric.Int64Counter
	skipped      metric.Int64Counter
	retried      metric.Int64Counter
	deadLettered metric.Int64Counter
	unsettled    metric.Int64Counter
	handle       metric.Float64Histogram
	published    metric.Int64Counter
	publishErr   metric.Int64Counter
}

func newMetrics() (*metrics, error) {
	meter := otel.GetMeterProvider().Meter(scopeName)

	var errs []error
	counter := func(name, description string) metric.Int64Counter {
		c, err := meter.Int64Counter(name, metric.WithDescription(description), metric.WithUnit("1"))
		if err != nil {
			errs = append(errs, fmt.Errorf("creating the %s counter: %w", name, err))
		}
		return c
	}

	m := &metrics{
		consumed: counter(metricConsumed,
			"Records read from a subscribed topic, before any decision about them."),
		skipped: counter(metricSkipped,
			"Records skipped because their (eventType, eventVersion) is not in the registry (contracts/README §13)."),
		retried: counter(metricRetried,
			"Handler invocations that were retries of a previous failure."),
		deadLettered: counter(metricDeadLettered,
			"Records produced to the DLQ, labelled by reason (A§27)."),
		unsettled: counter(metricUnsettled,
			"Records that could neither be handled nor dead-lettered. Non-zero means events are being replayed, not lost."),
		published: counter(metricPublished,
			"Records acknowledged by the brokers."),
		publishErr: counter(metricPublishError,
			"Publish attempts that the brokers did not acknowledge."),
	}

	handle, err := meter.Float64Histogram(metricHandle,
		metric.WithDescription("Wall time of one handler invocation, including a failed one."),
		metric.WithUnit("s"))
	if err != nil {
		errs = append(errs, fmt.Errorf("creating the %s histogram: %w", metricHandle, err))
	}
	m.handle = handle

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *metrics) recordPublished(ctx context.Context, topic string) {
	m.published.Add(ctx, 1, metric.WithAttributes(attribute.String(attrTopic, topic)))
}

func (m *metrics) recordPublishFailed(ctx context.Context, topic string) {
	m.publishErr.Add(ctx, 1, metric.WithAttributes(attribute.String(attrTopic, topic)))
}

// consumerAttrs is the label set every consumer metric carries.
func consumerAttrs(group, topic string) metric.MeasurementOption {
	return metric.WithAttributes(
		attribute.String(attrGroup, group),
		attribute.String(attrTopic, topic),
	)
}

func (m *metrics) recordConsumed(ctx context.Context, group, topic string) {
	m.consumed.Add(ctx, 1, consumerAttrs(group, topic))
}

func (m *metrics) recordSkipped(ctx context.Context, group, topic string) {
	m.skipped.Add(ctx, 1, consumerAttrs(group, topic))
}

func (m *metrics) recordRetry(ctx context.Context, group, topic string) {
	m.retried.Add(ctx, 1, consumerAttrs(group, topic))
}

func (m *metrics) recordDeadLettered(ctx context.Context, group, topic, reason string) {
	m.deadLettered.Add(ctx, 1, metric.WithAttributes(
		attribute.String(attrGroup, group),
		attribute.String(attrTopic, topic),
		attribute.String(attrReason, reason),
	))
}

func (m *metrics) recordUnsettled(ctx context.Context, group, topic string) {
	m.unsettled.Add(ctx, 1, consumerAttrs(group, topic))
}

func (m *metrics) recordHandleDuration(ctx context.Context, group, topic string, d time.Duration) {
	m.handle.Record(ctx, d.Seconds(), consumerAttrs(group, topic))
}

// newTracer returns this package's tracer from whatever provider is installed
// now, for the same reason newMetrics reads the meter provider late.
func newTracer() trace.Tracer {
	return otel.GetTracerProvider().Tracer(scopeName)
}
