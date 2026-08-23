package otelkit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/canhtoanptit/collection-platform/platform/httpkit"
	"github.com/canhtoanptit/collection-platform/platform/otelkit"
)

// installLogger makes otelkit.Logger write into buf for one test.
func installLogger(t *testing.T, buf *bytes.Buffer, level slog.Level) {
	t.Helper()

	previous := slog.Default()
	slog.SetDefault(otelkit.NewLogger(buf, level))
	t.Cleanup(func() { slog.SetDefault(previous) })
}

// logged decodes the single line written to buf.
func logged(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("log output is not one JSON object (%q): %v", buf.String(), err)
	}
	return line
}

func TestNewLoggerEmitsJSON(t *testing.T) {
	var buf bytes.Buffer
	otelkit.NewLogger(&buf, slog.LevelInfo).Info("case created", slog.String("case_id", "01M0KK4P3G0MQSQ3A1X2PMA6VX"))

	line := logged(t, &buf)
	if got := line["msg"]; got != "case created" {
		t.Errorf("msg = %v", got)
	}
	if got := line["level"]; got != "INFO" {
		t.Errorf("level = %v, want INFO", got)
	}
	if got := line["case_id"]; got != "01M0KK4P3G0MQSQ3A1X2PMA6VX" {
		t.Errorf("case_id = %v", got)
	}
	if _, ok := line["time"]; !ok {
		t.Error("no time field")
	}
}

func TestNewLoggerHonoursItsLevel(t *testing.T) {
	tests := []struct {
		name     string
		level    slog.Level
		wantLine bool
	}{
		{"debug logs at debug level", slog.LevelDebug, true},
		{"debug is dropped at info level", slog.LevelInfo, false},
		{"debug is dropped at error level", slog.LevelError, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			otelkit.NewLogger(&buf, tc.level).Debug("noisy")

			if got := buf.Len() > 0; got != tc.wantLine {
				t.Errorf("wrote a line = %t, want %t", got, tc.wantLine)
			}
		})
	}
}

func TestLoggerEnrichment(t *testing.T) {
	const correlationID = "01M0MEKBHXV37E3S3E28JT97KB"
	recordSpans(t)

	traced, span := otel.Tracer("test").Start(t.Context(), "handle")
	defer span.End()
	sc := trace.SpanContextFromContext(traced)

	tests := []struct {
		name   string
		ctx    context.Context
		want   map[string]string
		absent []string
	}{
		{
			name:   "a bare context adds nothing",
			ctx:    context.Background(),
			absent: []string{"trace_id", "span_id", "correlation_id"},
		},
		{
			name:   "a nil context adds nothing",
			ctx:    nil,
			absent: []string{"trace_id", "span_id", "correlation_id"},
		},
		{
			name:   "correlation id only",
			ctx:    httpkit.ContextWithCorrelationID(context.Background(), correlationID),
			want:   map[string]string{"correlation_id": correlationID},
			absent: []string{"trace_id", "span_id"},
		},
		{
			name:   "trace only",
			ctx:    traced,
			want:   map[string]string{"trace_id": sc.TraceID().String(), "span_id": sc.SpanID().String()},
			absent: []string{"correlation_id"},
		},
		{
			name: "trace and correlation id",
			ctx:  httpkit.ContextWithCorrelationID(traced, correlationID),
			want: map[string]string{
				"trace_id":       sc.TraceID().String(),
				"span_id":        sc.SpanID().String(),
				"correlation_id": correlationID,
			},
		},
		{
			name:   "an empty correlation id is not logged blank",
			ctx:    httpkit.ContextWithCorrelationID(context.Background(), ""),
			absent: []string{"correlation_id"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			installLogger(t, &buf, slog.LevelDebug)

			otelkit.Logger(tc.ctx).InfoContext(context.Background(), "case created")

			line := logged(t, &buf)
			for k, v := range tc.want {
				if got, _ := line[k].(string); got != v {
					t.Errorf("%s = %v, want %q", k, line[k], v)
				}
			}
			for _, k := range tc.absent {
				if _, ok := line[k]; ok {
					t.Errorf("%s is present (%v) but there is nothing in the context to fill it", k, line[k])
				}
			}
		})
	}
}

// TestLoggerIsUsableBeforeAnyWiring: a library that logs must not depend on main
// having installed a logger first.
func TestLoggerIsUsableBeforeAnyWiring(t *testing.T) {
	if otelkit.Logger(t.Context()) == nil {
		t.Fatal("Logger returned nil")
	}
	if otelkit.Logger(nilContext()) == nil {
		t.Fatal("Logger(nil) returned nil")
	}
}

// nilContext returns a nil context.Context. Logger must tolerate it: a library
// that logs on an error path must not turn a bad context into a panic.
func nilContext() context.Context { return nil }

// TestLoggerFieldNamesAreStable pins the names Loki queries and Grafana
// dashboards are written against: renaming one silently breaks them.
func TestLoggerFieldNamesAreStable(t *testing.T) {
	const correlationID = "01M0MEKBHXV37E3S3E28JT97KB"
	recordSpans(t)

	ctx, span := otel.Tracer("test").Start(t.Context(), "handle")
	defer span.End()
	ctx = httpkit.ContextWithCorrelationID(ctx, correlationID)

	var buf bytes.Buffer
	installLogger(t, &buf, slog.LevelDebug)
	otelkit.Logger(ctx).Info("case created")

	line := logged(t, &buf)
	for _, want := range []string{"trace_id", "span_id", "correlation_id"} {
		if _, ok := line[want]; !ok {
			t.Errorf("field %q is missing from %v", want, line)
		}
	}
}
