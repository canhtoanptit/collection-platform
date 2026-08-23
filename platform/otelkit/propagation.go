package otelkit

import (
	"context"
	"net/http"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/canhtoanptit/collection-platform/platform/httpkit"
)

// Kafka message headers carrying the A§97 correlation chain alongside the W3C
// traceparent. Lowercase by convention, and matched case-insensitively on the
// way in because a header set by another runtime may not agree.
const (
	// HeaderCorrelationID carries the correlation id of the flow this message
	// belongs to. It duplicates the envelope's correlationId on purpose: a
	// consumer reading headers can route or log a message without parsing the
	// value, and the DLQ keeps it when the value is unparseable.
	HeaderCorrelationID = "x-correlation-id"
	// HeaderCausationID carries the id of the event or command that caused this
	// message.
	HeaderCausationID = "x-causation-id"
	// HeaderTraceparent is the W3C trace context header, written by the
	// configured propagator rather than by hand.
	HeaderTraceparent = "traceparent"
)

// correlationAttribute is the span attribute an operator filters traces by when
// they start from a correlation id in a log line or an error body.
const correlationAttribute = "correlation_id"

// causationKey is the context key for the causation id.
//
// It lives here rather than in httpkit because a causation id is not an HTTP
// concept: it is the id of the event or command being handled, and this package
// is what carries it across the Kafka boundary. The correlation id, which *does*
// enter over HTTP, stays in httpkit.
type causationKey struct{}

// ContextWithCausationID returns ctx carrying the id of the event or command
// being handled. A consumer sets it from the envelope's eventId before invoking
// its handler, so every event the handler emits records what caused it.
func ContextWithCausationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, causationKey{}, id)
}

// CausationIDFrom returns the causation id carried by ctx, or "".
func CausationIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(causationKey{}).(string)
	return id
}

// HTTPMiddleware traces inbound requests and tags each server span with the
// request's correlation id.
//
// It is used as a httpkit.Middleware, inside httpkit.CorrelationID so the id is
// already in the context:
//
//	httpkit.Chain(httpkit.CorrelationID(), otelkit.HTTPMiddleware, ...)
//
// otelhttp extracts an inbound traceparent through the global propagator, so a
// request arriving from another service continues that trace rather than
// starting a new one.
func HTTPMiddleware(next http.Handler) http.Handler {
	return otelhttp.NewHandler(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if id := httpkit.CorrelationIDFrom(r.Context()); id != "" {
				trace.SpanFromContext(r.Context()).SetAttributes(attribute.String(correlationAttribute, id))
			}
			next.ServeHTTP(w, r)
		}),
		"http.server",
		// Span names must be low cardinality: the route pattern, never the
		// path, or every case id becomes its own span name.
		otelhttp.WithSpanNameFormatter(spanName),
	)
}

// spanName names a server span "METHOD /route/pattern", falling back to the
// operation when the mux has not matched a pattern.
func spanName(operation string, r *http.Request) string {
	if r == nil {
		return operation
	}
	if pattern := r.Pattern; pattern != "" {
		return r.Method + " " + pattern
	}
	return r.Method + " " + operation
}

// KafkaHeaders renders the propagation headers for a message published from ctx:
// the W3C traceparent (plus tracestate and baggage when present) and the A§97
// correlation and causation ids.
//
// Both platform/kafka's publisher and the outbox relay call it, so a message is
// never published without them.
func KafkaHeaders(ctx context.Context) map[string]string {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)

	if id := httpkit.CorrelationIDFrom(ctx); id != "" {
		carrier[HeaderCorrelationID] = id
	}
	if id := CausationIDFrom(ctx); id != "" {
		carrier[HeaderCausationID] = id
	}
	return carrier
}

// ContextFromKafkaHeaders is the inverse: it continues the producer's trace and
// restores the correlation and causation ids into ctx, so a consumer's log lines
// and any events it emits stay on the same chain.
//
// Header names are matched case-insensitively. A message with no propagation
// headers — an external producer, an old message — returns ctx unchanged rather
// than failing: the correlation id is then minted by whatever handles it, which
// is the same rule the HTTP edge follows.
func ContextFromKafkaHeaders(ctx context.Context, headers map[string]string) context.Context {
	carrier := propagation.MapCarrier{}
	for k, v := range headers {
		carrier[strings.ToLower(k)] = v
	}

	ctx = otel.GetTextMapPropagator().Extract(ctx, carrier)
	if id := carrier.Get(HeaderCorrelationID); id != "" {
		ctx = httpkit.ContextWithCorrelationID(ctx, id)
	}
	if id := carrier.Get(HeaderCausationID); id != "" {
		ctx = ContextWithCausationID(ctx, id)
	}
	return ctx
}
