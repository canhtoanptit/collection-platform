// Package otelkit wires OpenTelemetry once, the same way, in every service:
// traces to an OTLP collector, Prometheus-scrapable metrics, structured logs
// enriched from the context, and correlation propagation across HTTP and Kafka.
//
// The three signals share one identity — service.name, service.version,
// deployment.environment.name — so a trace, a metric and a log line about the
// same request join up in Grafana (ADR-0015). The join key an operator actually
// starts from is the correlation id (A§97): it is on every log line, on every
// server span, in every Kafka header, and in every A§20 error body.
//
// A service's main does:
//
//	shutdown, err := otelkit.Init(ctx, otelkit.ServiceInfo{Name: cfg.ServiceName, Version: build, Env: cfg.Env})
//	defer shutdown(context.WithoutCancel(ctx))
//	slog.SetDefault(otelkit.NewLogger(os.Stdout, cfg.LogLevel))
//	// serve otelkit.MetricsHandler() on cfg.MetricsAddr
//
// and then never touches the OTel API again: spans come from the HTTP and Kafka
// middleware, log lines from Logger(ctx).
package otelkit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// EndpointEnvVar is the environment variable Init reads the OTLP collector
// address from. It is the OpenTelemetry standard name, and the same one
// config.Base.OTLPEndpoint binds, so the SDK and the service's configuration
// can never disagree about where traces go.
const EndpointEnvVar = "OTEL_EXPORTER_OTLP_ENDPOINT"

// ServiceInfo identifies the process in every signal it emits.
type ServiceInfo struct {
	// Name is the deployable's name in kebab-case — the same string used as the
	// event envelope's producer and the Kafka consumer group.
	Name string
	// Version is the build version (a git sha or a tag). Empty is allowed:
	// a local run has no meaningful version.
	Version string
	// Env is the deployment environment: dev, staging, prod.
	Env string
}

// registry holds the Prometheus registry Init created, so MetricsHandler can
// serve it. It is process-global because the OTel meter provider is too:
// a second Init replaces both.
var (
	registryMu sync.RWMutex
	registry   *prometheus.Registry
)

// Init configures the global tracer provider, meter provider and propagators,
// and returns the shutdown function that flushes them.
//
// Traces go to the OTLP gRPC collector named by OTEL_EXPORTER_OTLP_ENDPOINT.
// When that variable is empty — unit tests, a local run, a CronJob nobody is
// tracing — the tracer provider is still installed and still samples, so spans,
// trace ids and propagation all work; nothing is exported. That matters: the
// alternative (a no-op provider) silently drops the trace id from every log line
// and every Kafka header, so code that works locally behaves differently in the
// cluster.
//
// Metrics are always collected and exposed through MetricsHandler; there is no
// push path, because Prometheus scrapes (ADR-0015).
//
// The returned shutdown flushes pending spans and stops the readers. Call it
// with a context that is *not* already cancelled — context.WithoutCancel(ctx) —
// or the final flush is dropped.
func Init(ctx context.Context, info ServiceInfo) (func(context.Context) error, error) {
	if info.Name == "" {
		return nil, errors.New("initialising telemetry: ServiceInfo.Name is required — it is the join key for every signal")
	}

	res, err := buildResource(ctx, info)
	if err != nil {
		return nil, err
	}

	tracerProvider, err := newTracerProvider(ctx, res)
	if err != nil {
		return nil, err
	}

	meterProvider, reg, err := newMeterProvider(res)
	if err != nil {
		// Nothing has been published globally yet, so undo the tracer provider
		// rather than leave half of the telemetry stack installed.
		return nil, errors.Join(err, tracerProvider.Shutdown(ctx))
	}

	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)
	// tracecontext carries the W3C traceparent; baggage carries user-defined
	// key/values across hops. Both are needed for a trace to survive the
	// HTTP -> Kafka -> HTTP path.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	registryMu.Lock()
	registry = reg
	registryMu.Unlock()

	return func(shutdownCtx context.Context) error {
		return errors.Join(
			tracerProvider.Shutdown(shutdownCtx),
			meterProvider.Shutdown(shutdownCtx),
		)
	}, nil
}

// buildResource describes this process to the collector.
func buildResource(ctx context.Context, info ServiceInfo) (*resource.Resource, error) {
	attrs := []attribute.KeyValue{semconv.ServiceName(info.Name)}
	if info.Version != "" {
		attrs = append(attrs, semconv.ServiceVersion(info.Version))
	}
	if info.Env != "" {
		attrs = append(attrs, semconv.DeploymentEnvironmentNameKey.String(info.Env))
	}

	res, err := resource.New(ctx,
		// Host and process detectors are deliberately omitted: pod names are
		// ephemeral and add cardinality without adding information a
		// Kubernetes-aware Grafana does not already have.
		resource.WithSchemaURL(semconv.SchemaURL),
		resource.WithAttributes(attrs...),
	)
	if err != nil {
		return nil, fmt.Errorf("describing the telemetry resource for %s: %w", info.Name, err)
	}
	return res, nil
}

// newTracerProvider builds the tracer provider, with an OTLP exporter when an
// endpoint is configured.
func newTracerProvider(ctx context.Context, res *resource.Resource) (*sdktrace.TracerProvider, error) {
	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		// Always sample: this platform's traffic is low and an incident
		// investigation that hits a sampled-away trace is worthless. Revisit
		// with a parent-based sampler if volume ever justifies it.
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	}

	endpoint := endpointFromEnv()
	if endpoint == "" {
		return sdktrace.NewTracerProvider(opts...), nil
	}

	exporter, err := newTraceExporter(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	return sdktrace.NewTracerProvider(append(opts, sdktrace.WithBatcher(exporter))...), nil
}

// newTraceExporter builds the OTLP gRPC exporter. An endpoint with a scheme is
// passed through as a URL (so https:// gets TLS); a bare host:port is treated as
// an in-cluster collector and dialled without TLS, which is what a sidecar or a
// cluster-local service is.
func newTraceExporter(ctx context.Context, endpoint string) (*otlptrace.Exporter, error) {
	var opts []otlptracegrpc.Option
	if strings.Contains(endpoint, "://") {
		opts = append(opts, otlptracegrpc.WithEndpointURL(endpoint))
	} else {
		opts = append(opts, otlptracegrpc.WithEndpoint(endpoint), otlptracegrpc.WithInsecure())
	}

	exporter, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("creating the OTLP trace exporter for %s: %w", endpoint, err)
	}
	return exporter, nil
}

// newMeterProvider builds the meter provider over a Prometheus registry.
func newMeterProvider(res *resource.Resource) (*sdkmetric.MeterProvider, *prometheus.Registry, error) {
	reg := prometheus.NewRegistry()
	// Go runtime and process metrics come for free and are the first thing an
	// operator looks at; the default registry is not used because a global
	// registry makes tests order-dependent.
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	exporter, err := otelprom.New(otelprom.WithRegisterer(reg))
	if err != nil {
		return nil, nil, fmt.Errorf("creating the Prometheus metrics exporter: %w", err)
	}
	return sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(exporter),
	), reg, nil
}

// MetricsHandler serves the Prometheus endpoint for the registry Init created.
// The caller mounts it on config.Base.MetricsAddr — a separate port, so metrics
// are never exposed through an ingress that fronts the API.
//
// Before Init, or after a failed Init, it answers 503: an empty 200 would make a
// scrape look healthy while reporting nothing.
func MetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		registryMu.RLock()
		reg := registry
		registryMu.RUnlock()

		if reg == nil {
			http.Error(w, "telemetry is not initialised", http.StatusServiceUnavailable)
			return
		}
		promhttp.HandlerFor(reg, promhttp.HandlerOpts{}).ServeHTTP(w, r)
	})
}

// endpointFromEnv reads the collector endpoint, trimming whitespace so a
// values-file typo like "  " counts as unset.
func endpointFromEnv() string {
	return strings.TrimSpace(os.Getenv(EndpointEnvVar))
}
