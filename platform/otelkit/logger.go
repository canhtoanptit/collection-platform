package otelkit

import (
	"context"
	"io"
	"log/slog"

	"go.opentelemetry.io/otel/trace"

	"github.com/canhtoanptit/collection-platform/platform/httpkit"
)

// Log attribute names. They are snake_case and stable because Loki queries and
// Grafana dashboards are written against them (ADR-0015): renaming one breaks
// dashboards, so they are part of the platform's contract with its operators.
const (
	logTraceID       = "trace_id"
	logSpanID        = "span_id"
	logCorrelationID = "correlation_id"
)

// NewLogger returns the platform's JSON logger. A service's main installs it as
// the process default:
//
//	slog.SetDefault(otelkit.NewLogger(os.Stdout, cfg.LogLevel))
//
// JSON to stdout, because that is what the log collector reads; no other sink,
// no rotation, no file paths — the container runtime owns all of that.
func NewLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}

// Logger returns a logger that adds this context's trace_id, span_id and
// correlation_id to every line, on top of the process default logger.
//
// Handlers and consumers use it instead of slog.Default() so an incident that
// starts as "correlation_id = 01M0…" in Loki can be followed to its trace, and
// a trace can be followed back to its log lines. Attributes that are not
// available are omitted rather than logged empty, so a line from a CronJob with
// no trace does not carry a field of zeros.
func Logger(ctx context.Context) *slog.Logger {
	attrs := contextAttrs(ctx)
	if len(attrs) == 0 {
		return slog.Default()
	}
	return slog.Default().With(attrs...)
}

// contextAttrs collects the correlation attributes present in ctx.
func contextAttrs(ctx context.Context) []any {
	if ctx == nil {
		return nil
	}

	var attrs []any
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		attrs = append(attrs,
			slog.String(logTraceID, sc.TraceID().String()),
			slog.String(logSpanID, sc.SpanID().String()),
		)
	}
	if id := httpkit.CorrelationIDFrom(ctx); id != "" {
		attrs = append(attrs, slog.String(logCorrelationID, id))
	}
	return attrs
}
