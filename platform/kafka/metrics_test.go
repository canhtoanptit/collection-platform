package kafka

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// TestMetricNamesOnTheScrapeEndpoint asserts the names an operator and a Grafana
// dashboard actually see, after the OTLP-to-Prometheus translation. Asserting the
// instrument names instead would prove nothing: the translator appends _total to
// a monotonic counter and rewrites the unit, so the name in the code is not the
// name in a query.
func TestMetricNamesOnTheScrapeEndpoint(t *testing.T) {
	registry := prometheus.NewRegistry()
	exporter, err := otelprom.New(otelprom.WithRegisterer(registry))
	if err != nil {
		t.Fatalf("creating the Prometheus exporter: %v", err)
	}
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))
	t.Cleanup(func() {
		if err := provider.Shutdown(t.Context()); err != nil {
			t.Logf("shutting the meter provider down: %v", err)
		}
	})

	// newMetrics reads the *global* provider on purpose (otelkit.Init installs
	// it), so the provider has to be installed before the instruments are made.
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() { otel.SetMeterProvider(previous) })

	m, err := newMetrics()
	if err != nil {
		t.Fatalf("newMetrics: %v", err)
	}

	// Record one of everything, so every instrument appears in a scrape.
	ctx := t.Context()
	const group, topic = "case-service", "collections.delinquency"
	m.recordConsumed(ctx, group, topic)
	m.recordSkipped(ctx, group, topic)
	m.recordRetry(ctx, group, topic)
	m.recordDeadLettered(ctx, group, topic, reasonSchema)
	m.recordDeadLettered(ctx, group, topic, reasonHandler)
	m.recordUnsettled(ctx, group, topic)
	m.recordHandleDuration(ctx, group, topic, 12*time.Millisecond)
	m.recordPublished(ctx, topic)
	m.recordPublishFailed(ctx, topic)

	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gathering metrics: %v", err)
	}
	names := make([]string, 0, len(families))
	for _, f := range families {
		names = append(names, f.GetName())
	}

	for _, want := range []string{
		"colx_kafka_consumer_records_consumed_total",
		"colx_kafka_consumer_records_skipped_total",
		"colx_kafka_consumer_handler_retries_total",
		"colx_kafka_consumer_records_dead_lettered_total",
		"colx_kafka_consumer_records_unsettled_total",
		"colx_kafka_consumer_handle_duration_seconds",
		"colx_kafka_publisher_records_published_total",
		"colx_kafka_publisher_errors_total",
	} {
		if !slices.Contains(names, want) {
			t.Errorf("metric %s is missing from the scrape endpoint; got %v", want, names)
		}
	}
}

// TestMetricLabelsAreBounded is a cardinality guard. topic, group and reason are
// bounded by the topic map (A§25) and by two constants; an event type or a
// message key is not, and an unbounded Prometheus label is how a monitoring
// stack falls over.
func TestMetricLabelsAreBounded(t *testing.T) {
	registry := prometheus.NewRegistry()
	exporter, err := otelprom.New(otelprom.WithRegisterer(registry))
	if err != nil {
		t.Fatalf("creating the Prometheus exporter: %v", err)
	}
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))
	t.Cleanup(func() {
		if err := provider.Shutdown(t.Context()); err != nil {
			t.Logf("shutting the meter provider down: %v", err)
		}
	})

	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() { otel.SetMeterProvider(previous) })

	m, err := newMetrics()
	if err != nil {
		t.Fatalf("newMetrics: %v", err)
	}
	m.recordDeadLettered(t.Context(), "case-service", "collections.delinquency", reasonHandler)

	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gathering metrics: %v", err)
	}

	allowed := []string{attrTopic, attrGroup, attrReason, "otel_scope_name", "otel_scope_version", "otel_scope_schema_url"}
	for _, f := range families {
		if !strings.HasPrefix(f.GetName(), "colx_kafka_") {
			continue
		}
		for _, metric := range f.GetMetric() {
			for _, label := range metric.GetLabel() {
				if !slices.Contains(allowed, label.GetName()) {
					t.Errorf("%s carries an unexpected label %q — check its cardinality before allowing it",
						f.GetName(), label.GetName())
				}
			}
		}
	}
}
