// Package httpkit is the HTTP server kit every service uses: one server with
// the platform's timeouts and graceful shutdown, and the middleware chain that
// makes a request observable and safe.
//
// Services do not re-implement any of this (CLAUDE.md §6). The standard chain,
// outermost first, is:
//
//	httpkit.Chain(
//	    httpkit.CorrelationID(),        // mint or accept the correlation id
//	    otelkit.HTTPMiddleware,         // trace the request, tag it with the id
//	    httpkit.AccessLog(logger),      // one line per request
//	    httpkit.Recover(logger),        // a panic becomes an A§20 500
//	    authn.Middleware(...),          // verify the bearer token
//	)
//
// CorrelationID is outermost so every later middleware — and every error body
// they write — carries the id. Recover sits inside AccessLog so a panicking
// request is still logged with its real status.
//
// There is no separate request id: the correlation id *is* the request id. A
// second identifier would have to be joined to the first in every log query for
// no extra information (docs/conventions.md §3).
package httpkit

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/canhtoanptit/collection-platform/platform/apierror"
	"github.com/canhtoanptit/collection-platform/platform/ids"
)

// CorrelationHeader is the request and response header carrying the correlation
// id (contracts/openapi/common.v1.yaml).
const CorrelationHeader = apierror.CorrelationHeader

// Middleware wraps a handler. Compose with Chain.
type Middleware func(http.Handler) http.Handler

// Chain composes middleware into one, applied left to right: the first argument
// is the outermost wrapper and sees the request first.
func Chain(middleware ...Middleware) Middleware {
	return func(next http.Handler) http.Handler {
		for i := len(middleware) - 1; i >= 0; i-- {
			if middleware[i] != nil {
				next = middleware[i](next)
			}
		}
		return next
	}
}

// CorrelationID accepts an inbound X-Correlation-Id, or mints one, puts it in
// the request context, and echoes it on the response before the handler runs —
// so the header is present even when the handler writes immediately.
//
// An inbound value is only accepted when it is a bare ULID. That is not
// fussiness: the id is copied verbatim into the correlationId of every event the
// request produces, and the frozen envelope schema accepts nothing else
// (contracts/README §6). A missing, malformed or prefixed value is replaced,
// which also stops a caller injecting arbitrary text into log lines and headers.
func CorrelationID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(CorrelationHeader)
			if !ids.IsULID(id) {
				id = ids.NewULID()
			}
			w.Header().Set(CorrelationHeader, id)
			next.ServeHTTP(w, r.WithContext(apierror.ContextWithCorrelationID(r.Context(), id)))
		})
	}
}

// ContextWithCorrelationID returns ctx carrying id. Use it in a worker or a
// consumer, where there is no inbound request to inherit from.
func ContextWithCorrelationID(ctx context.Context, id string) context.Context {
	return apierror.ContextWithCorrelationID(ctx, id)
}

// CorrelationIDFrom returns the correlation id carried by ctx, or "" when the
// request did not pass through CorrelationID.
func CorrelationIDFrom(ctx context.Context) string {
	return apierror.CorrelationIDFrom(ctx)
}

// Recover turns a panic into the A§20 500 body, with the stack and the
// correlation id on one error log line.
//
// It is a backstop, not a strategy (CLAUDE.md §3): a panic reaching it is a bug
// to fix, and the log line exists to make that bug findable. A nil logger uses
// slog.Default(). http.ErrAbortHandler is re-panicked — it is the documented way
// to drop a connection and must not become a 500.
func Recover(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rw := wrap(w)
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}
				if recovered == http.ErrAbortHandler {
					panic(recovered)
				}

				log := loggerOr(logger)
				log.ErrorContext(r.Context(), "panic recovered while serving a request",
					slog.Any("panic", recovered),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.String("correlation_id", CorrelationIDFrom(r.Context())),
					slog.String("stack", string(debug.Stack())),
				)

				// A handler that panicked mid-response has already committed a
				// status; appending an error body would corrupt it.
				if rw.wroteHeader {
					return
				}
				apierror.Write(rw, r, apierror.Internal(apierror.CodeInternal, ""))
			}()
			next.ServeHTTP(rw, r)
		})
	}
}

// AccessLog logs one line per request: method, path, status, duration and
// correlation id. 5xx responses log at error level so an alert can key on them.
// A nil logger uses slog.Default().
//
// The query string is deliberately not logged: filters carry account and
// customer identifiers, and access logs are the least controlled place they
// could land.
func AccessLog(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rw := wrap(w)
			start := time.Now()

			defer func() {
				level := slog.LevelInfo
				if rw.status >= http.StatusInternalServerError {
					level = slog.LevelError
				}
				loggerOr(logger).Log(r.Context(), level, "http request",
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.Int("status", rw.status),
					slog.Int64("duration_ms", time.Since(start).Milliseconds()),
					slog.Int("bytes", rw.bytes),
					slog.String("correlation_id", CorrelationIDFrom(r.Context())),
				)
			}()

			next.ServeHTTP(rw, r)
		})
	}
}

// loggerOr falls back to the process default so a service that has not wired a
// logger yet still emits the line.
func loggerOr(l *slog.Logger) *slog.Logger {
	if l == nil {
		return slog.Default()
	}
	return l
}

// recorder observes the status and size of a response without buffering it.
type recorder struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

// wrap returns w as a recorder, reusing an existing one so a chain of
// middleware shares a single view of the response.
func wrap(w http.ResponseWriter) *recorder {
	if rec, ok := w.(*recorder); ok {
		return rec
	}
	return &recorder{ResponseWriter: w, status: http.StatusOK}
}

func (r *recorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.status, r.wroteHeader = status, true
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Unwrap exposes the underlying writer to http.ResponseController, which is how
// a handler reaches Flush, SetReadDeadline and Hijack through a wrapper.
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
