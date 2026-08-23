package otelkit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/canhtoanptit/collection-platform/platform/httpkit"
	"github.com/canhtoanptit/collection-platform/platform/otelkit"
)

// recordSpans installs an in-memory exporter and the platform's propagators for
// one test, and returns the recorder holding every finished span.
func recordSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(recorder),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	previousProvider := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previousPropagator)
		_ = provider.Shutdown(context.WithoutCancel(t.Context()))
	})
	return recorder
}

// TestHTTPToKafkaToContextRoundTrip is the LIB-3 acceptance criterion: an
// in-memory exporter proves that HTTP -> ctx -> Kafka headers -> ctx preserves
// both the trace and the correlation id.
func TestHTTPToKafkaToContextRoundTrip(t *testing.T) {
	const correlationID = "01M0MEKBHXV37E3S3E28JT97KB"
	const causationID = "01M0MEKCV46CQ643DZVMXXQKFB"

	recorder := recordSpans(t)

	var (
		headers       map[string]string
		serverTraceID trace.TraceID
		serverSpanID  trace.SpanID
	)

	// 1. An inbound HTTP request: correlation middleware, then tracing.
	handler := httpkit.Chain(
		httpkit.CorrelationID(),
		otelkit.HTTPMiddleware,
	)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		sc := trace.SpanContextFromContext(ctx)
		serverTraceID, serverSpanID = sc.TraceID(), sc.SpanID()

		// 2. The handler publishes an event: headers are rendered from ctx.
		headers = otelkit.KafkaHeaders(otelkit.ContextWithCausationID(ctx, causationID))
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/cases", nil)
	req.Header.Set(httpkit.CorrelationHeader, correlationID)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if !serverTraceID.IsValid() {
		t.Fatal("the HTTP middleware did not create a span")
	}
	if got := headers[otelkit.HeaderTraceparent]; !strings.Contains(got, serverTraceID.String()) {
		t.Errorf("traceparent %q does not carry the server trace id %s", got, serverTraceID)
	}
	if got := headers[otelkit.HeaderCorrelationID]; got != correlationID {
		t.Errorf("%s = %q, want %q", otelkit.HeaderCorrelationID, got, correlationID)
	}
	if got := headers[otelkit.HeaderCausationID]; got != causationID {
		t.Errorf("%s = %q, want %q", otelkit.HeaderCausationID, got, causationID)
	}

	// 3. A consumer restores the context from the headers.
	consumerCtx := otelkit.ContextFromKafkaHeaders(t.Context(), headers)

	sc := trace.SpanContextFromContext(consumerCtx)
	if sc.TraceID() != serverTraceID {
		t.Errorf("consumer trace id = %s, want the producer's %s", sc.TraceID(), serverTraceID)
	}
	if sc.SpanID() != serverSpanID {
		t.Errorf("consumer parent span id = %s, want the producer's %s", sc.SpanID(), serverSpanID)
	}
	if !sc.IsRemote() {
		t.Error("the extracted span context is not marked remote")
	}
	if got := httpkit.CorrelationIDFrom(consumerCtx); got != correlationID {
		t.Errorf("consumer correlation id = %q, want %q", got, correlationID)
	}
	if got := otelkit.CausationIDFrom(consumerCtx); got != causationID {
		t.Errorf("consumer causation id = %q, want %q", got, causationID)
	}

	// 4. A span started by the consumer joins the producer's trace, which is
	// the whole point of carrying the header.
	_, span := otel.Tracer("test").Start(consumerCtx, "handle CaseCreated")
	span.End()

	var found bool
	for _, s := range recorder.Ended() {
		if s.Name() == "handle CaseCreated" {
			found = true
			if got := s.SpanContext().TraceID(); got != serverTraceID {
				t.Errorf("the consumer span is on trace %s, want %s", got, serverTraceID)
			}
			if got := s.Parent().SpanID(); got != serverSpanID {
				t.Errorf("the consumer span's parent is %s, want %s", got, serverSpanID)
			}
		}
	}
	if !found {
		t.Fatal("the consumer span was not recorded")
	}
}

// TestHTTPMiddlewareTagsTheSpanWithTheCorrelationID: an operator who starts from
// a correlation id in a log line must be able to filter traces by it.
func TestHTTPMiddlewareTagsTheSpanWithTheCorrelationID(t *testing.T) {
	const correlationID = "01M0MEKBHXV37E3S3E28JT97KB"

	recorder := recordSpans(t)

	handler := httpkit.Chain(
		httpkit.CorrelationID(),
		otelkit.HTTPMiddleware,
	)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	req := httptest.NewRequest(http.MethodGet, "/v1/cases/01M0KK4P3G0MQSQ3A1X2PMA6VX", nil)
	req.Header.Set(httpkit.CorrelationHeader, correlationID)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}

	var found bool
	for _, attr := range spans[0].Attributes() {
		if string(attr.Key) == "correlation_id" {
			found = true
			if attr.Value.AsString() != correlationID {
				t.Errorf("correlation_id attribute = %q, want %q", attr.Value.AsString(), correlationID)
			}
		}
	}
	if !found {
		t.Errorf("the server span carries no correlation_id attribute: %v", spans[0].Attributes())
	}
}

// TestHTTPMiddlewareSpanNamesStayLowCardinality: naming a span after the path
// would make every case id its own span name and blow up the trace backend's
// index.
func TestHTTPMiddlewareSpanNamesStayLowCardinality(t *testing.T) {
	recorder := recordSpans(t)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/cases/{caseId}", func(http.ResponseWriter, *http.Request) {})

	handler := otelkit.HTTPMiddleware(mux)
	for _, id := range []string{"01M0KK4P3G0MQSQ3A1X2PMA6VX", "01M0KK4P3G0MQSQ3A1X2PMA6VY"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/cases/"+id, nil))
	}

	names := make(map[string]int)
	for _, s := range recorder.Ended() {
		names[s.Name()]++
	}
	if len(names) != 1 {
		t.Fatalf("two requests to one route produced %d span names: %v", len(names), names)
	}
	for name := range names {
		if !strings.Contains(name, "{caseId}") {
			t.Errorf("span name = %q, want the route pattern", name)
		}
		if strings.Contains(name, "01M0KK4P3G0MQSQ3A1X2PMA6VX") {
			t.Errorf("span name %q contains a resource identifier", name)
		}
	}
}

// TestHTTPMiddlewareContinuesAnInboundTrace: a request from another service must
// extend that service's trace, not start a new one.
func TestHTTPMiddlewareContinuesAnInboundTrace(t *testing.T) {
	recordSpans(t)

	const (
		inboundTrace = "4bf92f3577b34da6a3ce929d0e0e4736"
		inboundSpan  = "00f067aa0ba902b7"
	)

	var got trace.SpanContext
	handler := otelkit.HTTPMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = trace.SpanContextFromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/cases", nil)
	req.Header.Set("traceparent", "00-"+inboundTrace+"-"+inboundSpan+"-01")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if id := got.TraceID().String(); id != inboundTrace {
		t.Errorf("trace id = %s, want the inbound %s", id, inboundTrace)
	}
}

func TestKafkaHeaders(t *testing.T) {
	const (
		correlationID = "01M0MEKBHXV37E3S3E28JT97KB"
		causationID   = "01M0MEKCV46CQ643DZVMXXQKFB"
	)
	recordSpans(t)

	tests := []struct {
		name       string
		ctx        func(context.Context) context.Context
		wantKeys   []string
		absentKeys []string
	}{
		{
			name:       "a bare context carries nothing",
			ctx:        func(ctx context.Context) context.Context { return ctx },
			absentKeys: []string{otelkit.HeaderTraceparent, otelkit.HeaderCorrelationID, otelkit.HeaderCausationID},
		},
		{
			name: "correlation only",
			ctx: func(ctx context.Context) context.Context {
				return httpkit.ContextWithCorrelationID(ctx, correlationID)
			},
			wantKeys:   []string{otelkit.HeaderCorrelationID},
			absentKeys: []string{otelkit.HeaderCausationID},
		},
		{
			name: "correlation and causation",
			ctx: func(ctx context.Context) context.Context {
				return otelkit.ContextWithCausationID(httpkit.ContextWithCorrelationID(ctx, correlationID), causationID)
			},
			wantKeys: []string{otelkit.HeaderCorrelationID, otelkit.HeaderCausationID},
		},
		{
			name: "empty ids are omitted rather than sent blank",
			ctx: func(ctx context.Context) context.Context {
				return otelkit.ContextWithCausationID(httpkit.ContextWithCorrelationID(ctx, ""), "")
			},
			absentKeys: []string{otelkit.HeaderCorrelationID, otelkit.HeaderCausationID},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			headers := otelkit.KafkaHeaders(tc.ctx(t.Context()))

			for _, k := range tc.wantKeys {
				if headers[k] == "" {
					t.Errorf("header %s is missing: %v", k, headers)
				}
			}
			for _, k := range tc.absentKeys {
				if _, ok := headers[k]; ok {
					t.Errorf("header %s should be absent: %v", k, headers)
				}
			}
		})
	}
}

func TestContextFromKafkaHeaders(t *testing.T) {
	const (
		correlationID = "01M0MEKBHXV37E3S3E28JT97KB"
		causationID   = "01M0MEKCV46CQ643DZVMXXQKFB"
		traceparent   = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	)
	recordSpans(t)

	tests := []struct {
		name            string
		headers         map[string]string
		wantCorrelation string
		wantCausation   string
		wantTrace       string
	}{
		{
			name:            "the headers this platform writes",
			headers:         map[string]string{"traceparent": traceparent, "x-correlation-id": correlationID, "x-causation-id": causationID},
			wantCorrelation: correlationID, wantCausation: causationID,
			wantTrace: "4bf92f3577b34da6a3ce929d0e0e4736",
		},
		{
			name:            "header names are matched case-insensitively",
			headers:         map[string]string{"TraceParent": traceparent, "X-Correlation-Id": correlationID, "X-Causation-ID": causationID},
			wantCorrelation: correlationID, wantCausation: causationID,
			wantTrace: "4bf92f3577b34da6a3ce929d0e0e4736",
		},
		{
			name:    "no headers at all leaves the context alone",
			headers: nil,
		},
		{
			name:    "a nil map is not a failure",
			headers: map[string]string{},
		},
		{
			name:            "correlation without a trace",
			headers:         map[string]string{"x-correlation-id": correlationID},
			wantCorrelation: correlationID,
		},
		{
			name:      "a trace without correlation",
			headers:   map[string]string{"traceparent": traceparent},
			wantTrace: "4bf92f3577b34da6a3ce929d0e0e4736",
		},
		{
			name:    "a malformed traceparent is ignored, not fatal",
			headers: map[string]string{"traceparent": "garbage", "x-correlation-id": correlationID},
			// The correlation id still survives, which is what an operator needs.
			wantCorrelation: correlationID,
		},
		{
			name:    "an empty correlation header is not restored",
			headers: map[string]string{"x-correlation-id": ""},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := otelkit.ContextFromKafkaHeaders(t.Context(), tc.headers)

			if got := httpkit.CorrelationIDFrom(ctx); got != tc.wantCorrelation {
				t.Errorf("correlation id = %q, want %q", got, tc.wantCorrelation)
			}
			if got := otelkit.CausationIDFrom(ctx); got != tc.wantCausation {
				t.Errorf("causation id = %q, want %q", got, tc.wantCausation)
			}
			sc := trace.SpanContextFromContext(ctx)
			switch {
			case tc.wantTrace == "" && sc.IsValid():
				t.Errorf("trace id = %s, want none", sc.TraceID())
			case tc.wantTrace != "" && sc.TraceID().String() != tc.wantTrace:
				t.Errorf("trace id = %s, want %s", sc.TraceID(), tc.wantTrace)
			}
		})
	}
}

// TestKafkaHeadersRoundTripThroughThemselves is a property test over the pair:
// whatever KafkaHeaders writes, ContextFromKafkaHeaders must read back.
func TestKafkaHeadersRoundTripThroughThemselves(t *testing.T) {
	recordSpans(t)

	const (
		correlationID = "01M0MEKBHXV37E3S3E28JT97KB"
		causationID   = "01M0MEKCV46CQ643DZVMXXQKFB"
	)

	ctx, span := otel.Tracer("test").Start(t.Context(), "publish")
	defer span.End()
	ctx = otelkit.ContextWithCausationID(httpkit.ContextWithCorrelationID(ctx, correlationID), causationID)

	// Two hops, as an event that is consumed and re-published would take.
	first := otelkit.ContextFromKafkaHeaders(t.Context(), otelkit.KafkaHeaders(ctx))
	second := otelkit.ContextFromKafkaHeaders(t.Context(), otelkit.KafkaHeaders(first))

	if got := httpkit.CorrelationIDFrom(second); got != correlationID {
		t.Errorf("correlation id after two hops = %q, want %q", got, correlationID)
	}
	if got := otelkit.CausationIDFrom(second); got != causationID {
		t.Errorf("causation id after two hops = %q, want %q", got, causationID)
	}
	want := trace.SpanContextFromContext(ctx).TraceID()
	if got := trace.SpanContextFromContext(second).TraceID(); got != want {
		t.Errorf("trace id after two hops = %s, want %s", got, want)
	}
}

func TestCausationIDContext(t *testing.T) {
	const id = "01M0MEKCV46CQ643DZVMXXQKFB"

	tests := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"round trip", otelkit.ContextWithCausationID(context.Background(), id), id},
		{"absent", context.Background(), ""},
		{"nil context", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := otelkit.CausationIDFrom(tc.ctx); got != tc.want {
				t.Errorf("CausationIDFrom = %q, want %q", got, tc.want)
			}
		})
	}
}
