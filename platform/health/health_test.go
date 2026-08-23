package health_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/canhtoanptit/collection-platform/platform/health"
)

// body is the shape both probes return.
type body struct {
	Status string   `json:"status"`
	Failed []string `json:"failed"`
}

// probe returns a Check whose probe always returns err.
func probe(name string, err error) health.Check {
	return health.Check{Name: name, Probe: func(context.Context) error { return err }}
}

// get calls a probe path and decodes the response.
func get(t *testing.T, h http.Handler, path string) (int, body, http.Header) {
	t.Helper()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

	var got body
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s body is not JSON (%q): %v", path, rec.Body, err)
		}
	}
	return rec.Code, got, rec.Header()
}

// TestLivenessIgnoresDependencies is the property that stops a database outage
// from turning into a cluster-wide crash loop.
func TestLivenessIgnoresDependencies(t *testing.T) {
	var probed atomic.Bool
	h := health.Handler(health.Check{
		Name: "postgres",
		Probe: func(context.Context) error {
			probed.Store(true)
			return errors.New("connection refused")
		},
	})

	status, got, _ := get(t, h, health.LivePath)

	if status != http.StatusOK {
		t.Errorf("status = %d, want 200 while a dependency is down", status)
	}
	if got.Status != "ok" {
		t.Errorf("status field = %q, want %q", got.Status, "ok")
	}
	if len(got.Failed) != 0 {
		t.Errorf("failed = %v, want none", got.Failed)
	}
	if probed.Load() {
		t.Error("liveness ran a dependency probe")
	}
}

func TestReadiness(t *testing.T) {
	tests := []struct {
		name       string
		checks     []health.Check
		wantStatus int
		wantFailed []string
	}{
		{
			name:       "no checks registered",
			wantStatus: http.StatusOK,
		},
		{
			name:       "every check passes",
			checks:     []health.Check{probe("postgres", nil), probe("kafka", nil)},
			wantStatus: http.StatusOK,
		},
		{
			name:       "one check fails",
			checks:     []health.Check{probe("postgres", nil), probe("kafka", errors.New("no brokers"))},
			wantStatus: http.StatusServiceUnavailable,
			wantFailed: []string{"kafka"},
		},
		{
			name: "every check fails, reported in registration order",
			checks: []health.Check{
				probe("postgres", errors.New("connection refused")),
				probe("kafka", errors.New("no brokers")),
				probe("model-provider", errors.New("timeout")),
			},
			wantStatus: http.StatusServiceUnavailable,
			wantFailed: []string{"postgres", "kafka", "model-provider"},
		},
		{
			name:       "a check with no probe fails closed",
			checks:     []health.Check{{Name: "misconfigured"}},
			wantStatus: http.StatusServiceUnavailable,
			wantFailed: []string{"misconfigured"},
		},
		{
			name: "the middle check fails",
			checks: []health.Check{
				probe("a", nil), probe("b", errors.New("down")), probe("c", nil),
			},
			wantStatus: http.StatusServiceUnavailable,
			wantFailed: []string{"b"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, got, _ := get(t, health.Handler(tc.checks...), health.ReadyPath)

			if status != tc.wantStatus {
				t.Errorf("status = %d, want %d", status, tc.wantStatus)
			}
			wantBody := "ok"
			if tc.wantStatus != http.StatusOK {
				wantBody = "unavailable"
			}
			if got.Status != wantBody {
				t.Errorf("status field = %q, want %q", got.Status, wantBody)
			}
			if len(got.Failed) != len(tc.wantFailed) {
				t.Fatalf("failed = %v, want %v", got.Failed, tc.wantFailed)
			}
			for i, name := range tc.wantFailed {
				if got.Failed[i] != name {
					t.Errorf("failed[%d] = %q, want %q", i, got.Failed[i], name)
				}
			}
		})
	}
}

// TestReadinessNeverLeaksProbeErrorText: /readyz is reachable from anywhere in
// the cluster, and a probe error can carry a connection string.
func TestReadinessNeverLeaksProbeErrorText(t *testing.T) {
	h := health.Handler(probe("postgres",
		errors.New("failed to connect to host=colx-dev.abc123.eu-west-1.rds.amazonaws.com user=colx password=hunter2")))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, health.ReadyPath, nil))

	for _, leak := range []string{"hunter2", "rds.amazonaws.com", "user=colx"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("readiness body leaked %q: %s", leak, rec.Body)
		}
	}
	if !strings.Contains(rec.Body.String(), "postgres") {
		t.Errorf("readiness body does not name the failing check: %s", rec.Body)
	}
}

// TestProbesRunInParallel keeps readiness cheap: three 100ms probes must cost
// about 100ms, not 300ms, or a readiness poll starts timing out as dependencies
// are added.
func TestProbesRunInParallel(t *testing.T) {
	const delay = 100 * time.Millisecond

	slow := func(name string) health.Check {
		return health.Check{
			Name: name,
			Probe: func(ctx context.Context) error {
				select {
				case <-time.After(delay):
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			},
		}
	}

	start := time.Now()
	status, _, _ := get(t, health.Handler(slow("a"), slow("b"), slow("c")), health.ReadyPath)
	elapsed := time.Since(start)

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if elapsed > 2*delay {
		t.Errorf("three %v probes took %v — they are running sequentially", delay, elapsed)
	}
}

// TestProbesReceiveTheRequestContext lets a probe honour the caller's deadline
// instead of hanging the readiness endpoint.
func TestProbesReceiveTheRequestContext(t *testing.T) {
	blocked := health.Handler(health.Check{
		Name: "postgres",
		Probe: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	})

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	rec := httptest.NewRecorder()
	blocked.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, health.ReadyPath, nil).WithContext(ctx))

	if got, want := rec.Code, http.StatusServiceUnavailable; got != want {
		t.Errorf("status = %d, want %d once the probe's context expired", got, want)
	}
}

func TestHandlerRoutes(t *testing.T) {
	h := health.Handler(probe("postgres", nil))

	tests := []struct {
		method     string
		path       string
		wantStatus int
	}{
		{http.MethodGet, health.LivePath, http.StatusOK},
		{http.MethodGet, health.ReadyPath, http.StatusOK},
		{http.MethodGet, "/", http.StatusNotFound},
		{http.MethodGet, "/healthz/extra", http.StatusNotFound},
		{http.MethodGet, "/metrics", http.StatusNotFound},
		{http.MethodPost, health.ReadyPath, http.StatusMethodNotAllowed},
		{http.MethodDelete, health.LivePath, http.StatusMethodNotAllowed},
	}
	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
		})
	}
}

func TestProbeResponsesAreNotCached(t *testing.T) {
	h := health.Handler(probe("postgres", nil))

	for _, path := range []string{health.LivePath, health.ReadyPath} {
		t.Run(path, func(t *testing.T) {
			_, _, headers := get(t, h, path)

			if got, want := headers.Get("Cache-Control"), "no-store"; got != want {
				t.Errorf("Cache-Control = %q, want %q", got, want)
			}
			if got, want := headers.Get("Content-Type"), "application/json"; got != want {
				t.Errorf("Content-Type = %q, want %q", got, want)
			}
		})
	}
}

// TestHandlerCopiesItsChecks: Handler takes its checks variadically, so without
// a copy it would alias the caller's slice — and a later mutation there would
// silently change what readiness probes.
func TestHandlerCopiesItsChecks(t *testing.T) {
	checks := []health.Check{probe("postgres", nil)}

	h := health.Handler(checks...)
	checks[0] = probe("kafka", errors.New("down"))

	status, got, _ := get(t, h, health.ReadyPath)
	if status != http.StatusOK {
		t.Errorf("status = %d (failed: %v), want 200 — the handler aliased the caller's slice", status, got.Failed)
	}
}
