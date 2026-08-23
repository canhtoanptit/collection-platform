// Package apierror is the platform's HTTP error contract — A§20, exactly.
//
//	{"code": "...", "message": "...", "correlationId": "...",
//	 "details": [{"field": "...", "reason": "..."}]}
//
// Every non-2xx response of every service is written through Write: never a
// bare http.Error, never a stack trace, never an internal message in `message`
// (CLAUDE.md §6). `code` and `reason` are stable SCREAMING_SNAKE_CASE vocabulary
// that clients branch on; `message` is a human summary they must not parse.
//
// # Correlation ids
//
// This package owns the correlation-id context key, because the error body is
// the one place a correlation id is *contractually required* and this is the
// lowest-level package that needs it. platform/httpkit's middleware puts the id
// into the context and re-exports these accessors under the names services use;
// nothing else should reach for them directly.
package apierror

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/canhtoanptit/collection-platform/platform/ids"
)

// CorrelationHeader is the request and response header carrying the correlation
// id (contracts/openapi/common.v1.yaml). Its value is a bare ULID.
const CorrelationHeader = "X-Correlation-Id"

// Error codes raised by platform middleware. Services define their own business
// codes; these are the ones no service should have to invent.
const (
	// CodeInternal is the only code an unexpected failure may report. It says
	// nothing about the cause, which stays in logs and traces.
	CodeInternal = "INTERNAL"
	// CodeUnauthenticated reports a missing, malformed, expired or untrusted
	// bearer token.
	CodeUnauthenticated = "UNAUTHENTICATED"
	// CodeForbidden reports an authenticated caller without the scope or group
	// an operation requires.
	CodeForbidden = "FORBIDDEN"
)

// internalMessage is the only message an unexpected failure may return. It is
// deliberately content-free: the correlationId is the handle an operator needs.
const internalMessage = "Internal server error"

// Detail is one field-level reason contributing to an Error (A§20).
type Detail struct {
	// Field locates the offending value: a path into the request body
	// (`schedule[0].amountMinor`), or the parameter name for a header, path or
	// query violation (`If-Match`, `limit`).
	Field string `json:"field"`
	// Reason is a stable SCREAMING_SNAKE_CASE reason: DATE_IN_PAST, REQUIRED,
	// OUT_OF_RANGE, NOT_IN_ENUM.
	Reason string `json:"reason"`
}

// Error is the A§20 error body and a Go error. The exported fields — and only
// these — are serialized; the kind and the cause stay in the process.
type Error struct {
	// Code is the stable machine-readable code clients branch on.
	Code string `json:"code"`
	// Message is a short human summary, safe to log and safe to show an
	// operator. Not localised, not for end customers.
	Message string `json:"message"`
	// CorrelationID is the correlation id of the failed request. Write fills it
	// in from the request when it is empty, so handlers rarely set it.
	CorrelationID string `json:"correlationId"`
	// Details carries per-field reasons on validation and business-rule
	// failures; it is omitted otherwise.
	Details []Detail `json:"details,omitempty"`

	// kind decides the HTTP status. Its zero value is internal, so a
	// zero-valued Error fails closed as a 500 rather than a 200.
	kind kind
	// cause is the underlying error, for logs and errors.Is/As. It is never
	// serialized: an error body must not leak internal detail.
	cause error
}

// kind maps an error to one of the canned A§20 responses.
type kind int

const (
	kindInternal kind = iota
	kindValidation
	kindUnauthorized
	kindForbidden
	kindNotFound
	kindConflict
	kindPreconditionFailed
	kindUnprocessable
	kindUnavailable
)

// Error implements error. It includes the cause, so a log line keeps the full
// story that the response body deliberately omits.
func (e *Error) Error() string {
	if e == nil {
		return "<nil apierror.Error>"
	}
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap exposes the cause to errors.Is and errors.As.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// Status returns the HTTP status for this error's kind.
func (e *Error) Status() int {
	if e == nil {
		return http.StatusInternalServerError
	}
	switch e.kind {
	case kindInternal:
		return http.StatusInternalServerError
	case kindValidation:
		return http.StatusBadRequest
	case kindUnauthorized:
		return http.StatusUnauthorized
	case kindForbidden:
		return http.StatusForbidden
	case kindNotFound:
		return http.StatusNotFound
	case kindConflict:
		return http.StatusConflict
	case kindPreconditionFailed:
		return http.StatusPreconditionFailed
	case kindUnprocessable:
		return http.StatusUnprocessableEntity
	case kindUnavailable:
		return http.StatusServiceUnavailable
	}
	// Unreachable while every kind above returns; a new kind that forgets to
	// fails closed rather than answering 200.
	return http.StatusInternalServerError
}

// newError builds an *Error of a kind. Details are copied so a caller cannot
// mutate the error afterwards through the slice it passed.
func newError(k kind, code, message string, details []Detail) *Error {
	e := &Error{Code: code, Message: message, kind: k}
	if len(details) > 0 {
		e.Details = append(make([]Detail, 0, len(details)), details...)
	}
	return e
}

// Validation reports a malformed request — unparseable body, schema violation,
// a query parameter out of range. 400.
func Validation(code, message string, details ...Detail) *Error {
	return newError(kindValidation, code, message, details)
}

// Unauthorized reports no bearer token, or one that is expired, malformed or
// not from the configured issuer. 401.
func Unauthorized(code, message string, details ...Detail) *Error {
	return newError(kindUnauthorized, code, message, details)
}

// Forbidden reports an authenticated caller lacking a required scope or group.
// Authorization is deny-by-default. 403.
func Forbidden(code, message string, details ...Detail) *Error {
	return newError(kindForbidden, code, message, details)
}

// NotFound reports that the addressed resource does not exist, or is not
// visible to this caller. 404.
func NotFound(code, message string, details ...Detail) *Error {
	return newError(kindNotFound, code, message, details)
}

// Conflict reports a state conflict: a duplicate command in flight, or a
// transition the aggregate's state machine refuses. 409.
func Conflict(code, message string, details ...Detail) *Error {
	return newError(kindConflict, code, message, details)
}

// PreconditionFailed reports a stale If-Match row version. 412.
func PreconditionFailed(code, message string, details ...Detail) *Error {
	return newError(kindPreconditionFailed, code, message, details)
}

// Unprocessable reports a well-formed request that violates a business rule —
// including an Idempotency-Key replayed with a different body (A§21). 422.
func Unprocessable(code, message string, details ...Detail) *Error {
	return newError(kindUnprocessable, code, message, details)
}

// Unavailable reports that a required downstream dependency (broker, model
// provider, data store) is unavailable and the request may be retried. 503.
func Unavailable(code, message string, details ...Detail) *Error {
	return newError(kindUnavailable, code, message, details)
}

// Internal reports an unexpected server-side failure. 500. Prefer
// Internal(CodeInternal, "") and attach the cause with WithCause: the body must
// stay generic, and an empty message is filled in for you.
func Internal(code, message string, details ...Detail) *Error {
	if code == "" {
		code = CodeInternal
	}
	if message == "" {
		message = internalMessage
	}
	return newError(kindInternal, code, message, details)
}

// WithCause returns a copy carrying cause. The cause reaches logs and
// errors.Is/As; it never reaches the response body.
func (e *Error) WithCause(cause error) *Error {
	c := e.clone()
	c.cause = cause
	return c
}

// WithCorrelationID returns a copy pinned to a correlation id. Write does this
// from the request, so call it only when writing an error outside a request —
// a background worker, or an error carried into an event.
func (e *Error) WithCorrelationID(id string) *Error {
	c := e.clone()
	c.CorrelationID = id
	return c
}

// WithDetails returns a copy with details appended.
func (e *Error) WithDetails(details ...Detail) *Error {
	c := e.clone()
	c.Details = append(c.Details, details...)
	return c
}

// clone copies an Error, including its details, so the With* methods never
// mutate a shared value.
func (e *Error) clone() *Error {
	if e == nil {
		return Internal(CodeInternal, internalMessage)
	}
	c := *e
	c.Details = append(make([]Detail, 0, len(e.Details)), e.Details...)
	if len(c.Details) == 0 {
		c.Details = nil
	}
	return &c
}

// From extracts the *Error anywhere in err's chain. It reports false for a
// plain error, which is the signal that the caller must not show it to a
// client.
func From(err error) (*Error, bool) {
	var apiErr *Error
	if errors.As(err, &apiErr) && apiErr != nil {
		return apiErr, true
	}
	return nil, false
}

// StatusOf returns the HTTP status err maps to: the *Error's status if there is
// one in the chain, 500 otherwise.
func StatusOf(err error) int {
	if apiErr, ok := From(err); ok {
		return apiErr.Status()
	}
	return http.StatusInternalServerError
}

// Write renders err as the A§20 body with the matching status.
//
// An err that is not (and does not wrap) an *Error becomes a generic 500:
// nothing from its message reaches the client. That is the whole point — a
// handler that returns a database error still produces a safe body. Log the
// cause where you have it; Write deliberately does not log, so the same error
// is never logged twice (CLAUDE.md §3).
//
// The correlation id is taken from the error, then the request context, then the
// inbound header, and is minted as a last resort — the field is required by the
// contract and an empty one would break every client's schema validation.
func Write(w http.ResponseWriter, r *http.Request, err error) {
	apiErr, ok := From(err)
	if !ok {
		apiErr = Internal(CodeInternal, internalMessage)
	}

	body := *apiErr
	body.CorrelationID = resolveCorrelationID(apiErr.CorrelationID, r)

	encoded, marshalErr := json.Marshal(body)
	if marshalErr != nil {
		// Unreachable for the fields above (all strings), but a partial write
		// of a broken body would be worse than a bare 500.
		http.Error(w, internalMessage, http.StatusInternalServerError)
		return
	}
	encoded = append(encoded, '\n')

	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set(CorrelationHeader, body.CorrelationID)
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(apiErr.Status())
	_, _ = w.Write(encoded)
}

// resolveCorrelationID finds the correlation id to report, preferring the one
// already on the error, then the request's context, then its inbound header.
func resolveCorrelationID(onError string, r *http.Request) string {
	candidates := [3]string{onError}
	if r != nil {
		candidates[1] = CorrelationIDFrom(r.Context())
		candidates[2] = r.Header.Get(CorrelationHeader)
	}
	for _, c := range candidates {
		if ids.IsULID(c) {
			return c
		}
	}
	return ids.NewULID()
}

// correlationKey is the context key for the correlation id. An unexported
// struct type cannot collide with another package's key.
type correlationKey struct{}

// ContextWithCorrelationID returns ctx carrying id. platform/httpkit's
// CorrelationID middleware is the normal caller.
func ContextWithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationKey{}, id)
}

// CorrelationIDFrom returns the correlation id carried by ctx, or "" when there
// is none — which means the request did not pass through the correlation
// middleware.
func CorrelationIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(correlationKey{}).(string)
	return id
}
