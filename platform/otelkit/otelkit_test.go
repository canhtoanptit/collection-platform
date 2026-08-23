package otelkit_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/canhtoanptit/collection-platform/platform/otelkit"
)

// TestInitWithoutAnEndpointStillProducesUsableTraces is the property that keeps
// local runs and unit tests behaving like the cluster: with no collector
// configured, spans are still created and still carry a valid trace id, so
// propagation and log enrichment work identically.
func TestInitWithoutAnEndpointStillProducesUsableTraces(t *testing.T) {
	t.Setenv(otelkit.EndpointEnvVar, "")

	shutdown, err := otelkit.Init(t.Context(), otelkit.ServiceInfo{
		Name:    "case-service",
		Version: "abc1234",
		Env:     "dev",
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		if err := shutdown(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})

	// Trace a request through the middleware and read the ids back out.
	var traceID string
	handler := otelkit.HTTPMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		traceID = otelkit.KafkaHeaders(r.Context())[otelkit.HeaderTraceparent]
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/cases", nil))

	if traceID == "" {
		t.Fatal("no traceparent was produced with an empty OTLP endpoint — spans are not being sampled")
	}
	if !strings.HasPrefix(traceID, "00-") {
		t.Errorf("traceparent = %q, want a W3C version-00 header", traceID)
	}
}

func TestInitWithAnEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
	}{
		{"bare host:port is dialled insecurely", "127.0.0.1:4317"},
		{"an http URL", "http://127.0.0.1:4317"},
		{"an https URL", "https://collector.internal:4317"},
		{"whitespace counts as unset", "   "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(otelkit.EndpointEnvVar, tc.endpoint)

			// The gRPC exporter connects lazily, so Init must succeed even
			// though nothing is listening — a service must not fail to start
			// because its collector is down.
			shutdown, err := otelkit.Init(t.Context(), otelkit.ServiceInfo{Name: "case-service", Env: "dev"})
			if err != nil {
				t.Fatalf("Init: %v", err)
			}

			shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
			defer cancel()
			if err := shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("shutdown: %v", err)
			}
		})
	}
}

func TestInitRequiresAServiceName(t *testing.T) {
	t.Setenv(otelkit.EndpointEnvVar, "")

	shutdown, err := otelkit.Init(t.Context(), otelkit.ServiceInfo{Env: "dev"})
	if err == nil {
		t.Fatal("Init accepted an empty service name")
	}
	if shutdown != nil {
		t.Error("Init returned a shutdown function alongside the error")
	}
	if !strings.Contains(err.Error(), "ServiceInfo.Name") {
		t.Errorf("error = %q, want it to name the missing field", err)
	}
}

func TestMetricsHandler(t *testing.T) {
	t.Setenv(otelkit.EndpointEnvVar, "")

	shutdown, err := otelkit.Init(t.Context(), otelkit.ServiceInfo{Name: "case-service", Env: "dev"})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.WithoutCancel(t.Context())) })

	rec := httptest.NewRecorder()
	otelkit.MetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if got, want := rec.Code, http.StatusOK; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	body := rec.Body.String()
	// Go runtime metrics prove the registry is real and populated rather than
	// an empty 200 a scrape would report as healthy.
	for _, want := range []string{"go_goroutines", "target_info"} {
		if !strings.Contains(body, want) {
			t.Errorf("the metrics endpoint does not expose %s:\n%s", want, truncate(body))
		}
	}
	// The resource identity must be on the scrape, or metrics cannot be joined
	// to traces and logs.
	if !strings.Contains(body, `service_name="case-service"`) {
		t.Errorf("the metrics endpoint does not carry service_name:\n%s", truncate(body))
	}
}

func truncate(s string) string {
	if len(s) > 2000 {
		return s[:2000] + "\n…"
	}
	return s
}
