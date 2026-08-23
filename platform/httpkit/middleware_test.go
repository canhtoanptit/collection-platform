package httpkit_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/canhtoanptit/collection-platform/platform/apierror"
	"github.com/canhtoanptit/collection-platform/platform/httpkit"
	"github.com/canhtoanptit/collection-platform/platform/ids"
)

// captureLogger returns a JSON logger writing into buf, so a test can assert on
// the structured fields rather than on formatted text.
func captureLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// logLines decodes every line the logger wrote.
func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()

	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON (%q): %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// writeToMap reproduces the most common production panic — a write to a map that
// was never made — so the Recover table can cover a genuine runtime panic and not
// only an explicit panic() call.
func writeToMap(m map[string]string, k, v string) { m[k] = v }

// echoCorrelationID reports the correlation id it sees in the context.
var echoCorrelationID = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	_, _ = io.WriteString(w, httpkit.CorrelationIDFrom(r.Context()))
})

func TestCorrelationID(t *testing.T) {
	const inbound = "01M0MEKBHXV37E3S3E28JT97KB"

	tests := []struct {
		name       string
		header     string
		wantEchoed bool // true: the inbound value must survive
	}{
		{"absent mints one", "", false},
		{"a bare ULID is accepted", inbound, true},
		{"empty string mints one", "", false},
		{"lowercase ULID is refused", strings.ToLower(inbound), false},
		{"COR_-prefixed id is refused (envelope ids are bare ULIDs)", "COR_" + inbound, false},
		{"a UUID is refused", "3f2504e0-4f89-11d3-9a0c-0305e82c3301", false},
		{"header injection attempt is refused", "01M0MEK\r\nX-Admin: true", false},
		{"an over-long value is refused", strings.Repeat("A", 512), false},
		{"whitespace-padded ULID is refused", " " + inbound + " ", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/cases", nil)
			if tc.header != "" {
				// Set the raw value directly: http.Header.Set would reject
				// nothing, and this is what an untrusted client can send.
				req.Header[http.CanonicalHeaderKey(httpkit.CorrelationHeader)] = []string{tc.header}
			}
			rec := httptest.NewRecorder()

			httpkit.CorrelationID()(echoCorrelationID).ServeHTTP(rec, req)

			fromCtx := rec.Body.String()
			if !ids.IsULID(fromCtx) {
				t.Fatalf("context correlation id = %q, want a bare ULID", fromCtx)
			}
			if got := rec.Header().Get(httpkit.CorrelationHeader); got != fromCtx {
				t.Errorf("response header = %q, context has %q", got, fromCtx)
			}
			if tc.wantEchoed && fromCtx != tc.header {
				t.Errorf("inbound id %q was replaced by %q", tc.header, fromCtx)
			}
			if !tc.wantEchoed && fromCtx == tc.header {
				t.Errorf("untrusted inbound value %q was accepted", tc.header)
			}
		})
	}
}

// TestCorrelationIDSetsTheHeaderBeforeTheHandlerRuns matters because a handler
// that writes its status immediately would otherwise ship a response with no
// correlation header — which the contract requires on every response.
func TestCorrelationIDSetsTheHeaderBeforeTheHandlerRuns(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	rec := httptest.NewRecorder()

	httpkit.CorrelationID()(handler).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/cases", nil))

	if got, want := rec.Code, http.StatusAccepted; got != want {
		t.Errorf("status = %d, want %d", got, want)
	}
	if got := rec.Header().Get(httpkit.CorrelationHeader); !ids.IsULID(got) {
		t.Errorf("%s = %q, want a bare ULID", httpkit.CorrelationHeader, got)
	}
}

// TestCorrelationIDMintsADistinctIDPerRequest guards the obvious mistake of
// minting once at construction.
func TestCorrelationIDMintsADistinctIDPerRequest(t *testing.T) {
	mw := httpkit.CorrelationID()(echoCorrelationID)

	seen := make(map[string]bool, 50)
	for range 50 {
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/cases", nil))
		id := rec.Body.String()
		if seen[id] {
			t.Fatalf("correlation id %q was reused across requests", id)
		}
		seen[id] = true
	}
}

// TestRecoverWritesTheErrorContract is the LIB-2 acceptance criterion: a panic
// becomes a 500 carrying the A§20 body and the request's correlation id.
func TestRecoverWritesTheErrorContract(t *testing.T) {
	const inbound = "01M0MEKBHXV37E3S3E28JT97KB"

	panicking := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("nil map write in the case aggregate")
	})

	var buf bytes.Buffer
	handler := httpkit.Chain(
		httpkit.CorrelationID(),
		httpkit.Recover(captureLogger(&buf)),
	)(panicking)

	req := httptest.NewRequest(http.MethodPost, "/v1/cases", nil)
	req.Header.Set(httpkit.CorrelationHeader, inbound)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusInternalServerError; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}

	var body apierror.Error
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not the A§20 contract: %v (%s)", err, rec.Body)
	}
	if body.Code != apierror.CodeInternal {
		t.Errorf("code = %q, want %q", body.Code, apierror.CodeInternal)
	}
	if body.CorrelationID != inbound {
		t.Errorf("correlationId = %q, want the inbound %q", body.CorrelationID, inbound)
	}
	if strings.Contains(rec.Body.String(), "nil map write") {
		t.Errorf("the panic value reached the response body: %s", rec.Body)
	}
	if strings.Contains(rec.Body.String(), "goroutine") {
		t.Errorf("a stack trace reached the response body: %s", rec.Body)
	}

	// The stack and the panic value belong on the log line instead.
	lines := logLines(t, &buf)
	if len(lines) != 1 {
		t.Fatalf("logged %d lines, want 1: %s", len(lines), buf.String())
	}
	line := lines[0]
	if got := line["level"]; got != "ERROR" {
		t.Errorf("level = %v, want ERROR", got)
	}
	if got, _ := line["correlation_id"].(string); got != inbound {
		t.Errorf("correlation_id = %v, want %q", line["correlation_id"], inbound)
	}
	if got, _ := line["panic"].(string); got != "nil map write in the case aggregate" {
		t.Errorf("panic = %v, want the panic value", line["panic"])
	}
	if stack, _ := line["stack"].(string); !strings.Contains(stack, "runtime/debug.Stack") {
		t.Errorf("stack = %q, want a real stack trace", stack)
	}
	if got, _ := line["path"].(string); got != "/v1/cases" {
		t.Errorf("path = %v, want /v1/cases", line["path"])
	}
}

func TestRecoverPanicValues(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{"a string", "boom"},
		{"an error", errors.New("boom")},
		{"an integer", 42},
		{"a runtime panic rather than an explicit panic()", nil}, // see writeToMap
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler := httpkit.Recover(captureLogger(&bytes.Buffer{}))(http.HandlerFunc(
				func(http.ResponseWriter, *http.Request) {
					if tc.value == nil {
						writeToMap(nil, "boom", "now") // real runtime panic
						return
					}
					panic(tc.value)
				}))

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/cases", nil))

			if got, want := rec.Code, http.StatusInternalServerError; got != want {
				t.Errorf("status = %d, want %d", got, want)
			}
			if !json.Valid(rec.Body.Bytes()) {
				t.Errorf("body is not JSON: %s", rec.Body)
			}
		})
	}
}

// TestRecoverLeavesACommittedResponseAlone: a handler that panics after writing
// has already sent a status, so appending an error body would corrupt the
// response rather than improve it.
func TestRecoverLeavesACommittedResponseAlone(t *testing.T) {
	handler := httpkit.Recover(captureLogger(&bytes.Buffer{}))(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"caseId":"01M0KK4P3G0MQSQ3A1X2PMA6VX"}`)
			panic("after the response was committed")
		}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/cases", nil))

	if got, want := rec.Code, http.StatusCreated; got != want {
		t.Errorf("status = %d, want the already-committed %d", got, want)
	}
	if got, want := rec.Body.String(), `{"caseId":"01M0KK4P3G0MQSQ3A1X2PMA6VX"}`; got != want {
		t.Errorf("body = %q, want the already-written %q", got, want)
	}
}

// TestRecoverRepanicsErrAbortHandler keeps the documented way to drop a
// connection working: http.ErrAbortHandler must not become a 500.
func TestRecoverRepanicsErrAbortHandler(t *testing.T) {
	handler := httpkit.Recover(captureLogger(&bytes.Buffer{}))(http.HandlerFunc(
		func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }))

	defer func() {
		if recovered := recover(); recovered != http.ErrAbortHandler {
			t.Errorf("recovered %v, want http.ErrAbortHandler to propagate", recovered)
		}
	}()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/cases", nil))
}

func TestRecoverPassesThroughWhenNothingPanics(t *testing.T) {
	var buf bytes.Buffer
	handler := httpkit.Recover(captureLogger(&buf))(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/v1/cases/1", nil))

	if got, want := rec.Code, http.StatusNoContent; got != want {
		t.Errorf("status = %d, want %d", got, want)
	}
	if buf.Len() != 0 {
		t.Errorf("logged something for a healthy request: %s", buf.String())
	}
}

func TestAccessLog(t *testing.T) {
	const inbound = "01M0MEKBHXV37E3S3E28JT97KB"

	tests := []struct {
		name       string
		method     string
		target     string
		handler    http.HandlerFunc
		wantStatus int
		wantLevel  string
		wantBytes  int
	}{
		{
			name: "200 logs at info", method: http.MethodGet, target: "/v1/cases",
			handler:    func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"items":[]}`) },
			wantStatus: http.StatusOK, wantLevel: "INFO", wantBytes: 12,
		},
		{
			name: "an implicit 200 is recorded", method: http.MethodGet, target: "/v1/cases",
			handler:    func(http.ResponseWriter, *http.Request) {},
			wantStatus: http.StatusOK, wantLevel: "INFO", wantBytes: 0,
		},
		{
			name: "404 still logs at info", method: http.MethodGet, target: "/v1/cases/missing",
			handler:    func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) },
			wantStatus: http.StatusNotFound, wantLevel: "INFO",
		},
		{
			name: "500 logs at error", method: http.MethodPost, target: "/v1/cases",
			handler:    func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
			wantStatus: http.StatusInternalServerError, wantLevel: "ERROR",
		},
		{
			name: "503 logs at error", method: http.MethodGet, target: "/readyz",
			handler:    func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) },
			wantStatus: http.StatusServiceUnavailable, wantLevel: "ERROR",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			handler := httpkit.Chain(
				httpkit.CorrelationID(),
				httpkit.AccessLog(captureLogger(&buf)),
			)(tc.handler)

			req := httptest.NewRequest(tc.method, tc.target, nil)
			req.Header.Set(httpkit.CorrelationHeader, inbound)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}

			lines := logLines(t, &buf)
			if len(lines) != 1 {
				t.Fatalf("logged %d lines, want 1: %s", len(lines), buf.String())
			}
			line := lines[0]
			if got := line["msg"]; got != "http request" {
				t.Errorf("msg = %v", got)
			}
			if got := line["level"]; got != tc.wantLevel {
				t.Errorf("level = %v, want %v", got, tc.wantLevel)
			}
			if got := line["method"]; got != tc.method {
				t.Errorf("method = %v, want %v", got, tc.method)
			}
			if got := line["path"]; got != tc.target {
				t.Errorf("path = %v, want %v", got, tc.target)
			}
			if got, _ := line["status"].(float64); int(got) != tc.wantStatus {
				t.Errorf("status = %v, want %d", line["status"], tc.wantStatus)
			}
			if got := line["correlation_id"]; got != inbound {
				t.Errorf("correlation_id = %v, want %q", got, inbound)
			}
			if _, ok := line["duration_ms"]; !ok {
				t.Error("duration_ms is missing")
			}
			if tc.wantBytes > 0 {
				if got, _ := line["bytes"].(float64); int(got) != tc.wantBytes {
					t.Errorf("bytes = %v, want %d", line["bytes"], tc.wantBytes)
				}
			}
		})
	}
}

// TestAccessLogOmitsTheQueryString: list filters carry account and customer
// identifiers, and an access log is the least controlled place they could land.
func TestAccessLogOmitsTheQueryString(t *testing.T) {
	var buf bytes.Buffer
	handler := httpkit.AccessLog(captureLogger(&buf))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	handler.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/v1/cases?customerId=01M0KK4K5RM5CNE9ZZQ52EJAC0", nil))

	if got := buf.String(); strings.Contains(got, "01M0KK4K5RM5CNE9ZZQ52EJAC0") {
		t.Errorf("the access log leaked a query parameter: %s", got)
	}
}

// TestAccessLogAndRecoverShareOneResponseView: chained together, the access log
// must report the 500 the recover middleware wrote, not the default 200.
func TestAccessLogAndRecoverShareOneResponseView(t *testing.T) {
	var buf bytes.Buffer
	logger := captureLogger(&buf)

	handler := httpkit.Chain(
		httpkit.CorrelationID(),
		httpkit.AccessLog(logger),
		httpkit.Recover(logger),
	)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/cases", nil))

	if got, want := rec.Code, http.StatusInternalServerError; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}

	var access map[string]any
	for _, line := range logLines(t, &buf) {
		if line["msg"] == "http request" {
			access = line
		}
	}
	if access == nil {
		t.Fatalf("no access log line was written: %s", buf.String())
	}
	if got, _ := access["status"].(float64); int(got) != http.StatusInternalServerError {
		t.Errorf("access log status = %v, want 500", access["status"])
	}
}

func TestChain(t *testing.T) {
	// Each middleware appends its name, so the response body records the order
	// requests actually traverse them.
	tag := func(name string) httpkit.Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, name+">")
				next.ServeHTTP(w, r)
			})
		}
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "handler")
	})

	tests := []struct {
		name       string
		middleware []httpkit.Middleware
		want       string
	}{
		{"empty chain", nil, "handler"},
		{"single", []httpkit.Middleware{tag("a")}, "a>handler"},
		{"first argument is outermost", []httpkit.Middleware{tag("a"), tag("b"), tag("c")}, "a>b>c>handler"},
		{"nil entries are skipped", []httpkit.Middleware{tag("a"), nil, tag("b")}, "a>b>handler"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			httpkit.Chain(tc.middleware...)(handler).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

			if got := rec.Body.String(); got != tc.want {
				t.Errorf("traversal order = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCorrelationIDContextHelpers(t *testing.T) {
	const id = "01M0MEKBHXV37E3S3E28JT97KB"

	ctx := httpkit.ContextWithCorrelationID(t.Context(), id)
	if got := httpkit.CorrelationIDFrom(ctx); got != id {
		t.Errorf("CorrelationIDFrom = %q, want %q", got, id)
	}
	if got := httpkit.CorrelationIDFrom(t.Context()); got != "" {
		t.Errorf("CorrelationIDFrom on a bare context = %q, want empty", got)
	}
	// httpkit and apierror must agree: apierror.Write reads what httpkit set.
	if got := apierror.CorrelationIDFrom(ctx); got != id {
		t.Errorf("apierror.CorrelationIDFrom = %q, want %q — the packages disagree on the context key", got, id)
	}
}

// TestResponseWriterStaysUsableThroughTheChain proves the recorder does not
// break http.ResponseController, which handlers need for streaming.
func TestResponseWriterStaysUsableThroughTheChain(t *testing.T) {
	handler := httpkit.Chain(
		httpkit.AccessLog(captureLogger(&bytes.Buffer{})),
		httpkit.Recover(captureLogger(&bytes.Buffer{})),
	)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "chunk")
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("Flush through the wrapper: %v", err)
		}
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/stream", nil))

	if got := rec.Body.String(); got != "chunk" {
		t.Errorf("body = %q, want %q", got, "chunk")
	}
	if !rec.Flushed {
		t.Error("Flush did not reach the underlying writer")
	}
}
