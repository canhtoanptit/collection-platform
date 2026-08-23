package apierror_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/canhtoanptit/collection-platform/platform/apierror"
)

// The correlation id used by the contracts' A§20 example, so the golden file is
// the documented body byte for byte.
const exampleCorrelationID = "01M0MEKBHXV37E3S3E28JT97KB"

// Patterns from contracts/openapi/common.v1.yaml. An error body that does not
// match them fails request validation in every client.
var (
	codePattern          = regexp.MustCompile(`^[A-Z][A-Z0-9_]{2,63}$`)
	correlationIDPattern = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)
)

// TestWriteGoldenA20ErrorBody is the byte-exact contract test for the A§20 error
// body. If this fails, either the contract changed (it is frozen) or the shape
// drifted — nothing else.
func TestWriteGoldenA20ErrorBody(t *testing.T) {
	golden, err := os.ReadFile("testdata/a20-validation-error.golden.json")
	if err != nil {
		t.Fatalf("reading the golden file: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/arrangements", nil)
	req = req.WithContext(apierror.ContextWithCorrelationID(req.Context(), exampleCorrelationID))

	apierror.Write(rec, req, apierror.Validation(
		"ARRANGEMENT_INVALID",
		"Arrangement schedule is invalid",
		apierror.Detail{Field: "firstPaymentDate", Reason: "DATE_IN_PAST"},
	))

	if got := rec.Body.String(); got != string(golden) {
		t.Errorf("body is not byte-exact\ngot:  %q\nwant: %q", got, golden)
	}
	if got, want := rec.Code, http.StatusBadRequest; got != want {
		t.Errorf("status = %d, want %d", got, want)
	}
	if got, want := rec.Header().Get("Content-Type"), "application/json"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	if got, want := rec.Header().Get(apierror.CorrelationHeader), exampleCorrelationID; got != want {
		t.Errorf("%s = %q, want %q", apierror.CorrelationHeader, got, want)
	}

	// The golden body must also satisfy the contract's own field patterns.
	var body apierror.Error
	if err := json.Unmarshal(golden, &body); err != nil {
		t.Fatalf("the golden body is not valid JSON: %v", err)
	}
	if !codePattern.MatchString(body.Code) {
		t.Errorf("code %q does not match the contract pattern %s", body.Code, codePattern)
	}
	if !correlationIDPattern.MatchString(body.CorrelationID) {
		t.Errorf("correlationId %q does not match the contract pattern %s", body.CorrelationID, correlationIDPattern)
	}

	// And exactly the four contract fields, no more.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(golden, &fields); err != nil {
		t.Fatalf("the golden body is not an object: %v", err)
	}
	want := map[string]bool{"code": true, "message": true, "correlationId": true, "details": true}
	for k := range fields {
		if !want[k] {
			t.Errorf("unexpected field %q in the error body", k)
		}
	}
	if len(fields) != len(want) {
		t.Errorf("body has %d fields, want %d (%v)", len(fields), len(want), fields)
	}
}

func TestConstructorsMapToStatuses(t *testing.T) {
	tests := []struct {
		name       string
		err        *apierror.Error
		wantStatus int
	}{
		{"Validation", apierror.Validation("BAD_REQUEST", "malformed"), http.StatusBadRequest},
		{"Unauthorized", apierror.Unauthorized(apierror.CodeUnauthenticated, "no token"), http.StatusUnauthorized},
		{"Forbidden", apierror.Forbidden(apierror.CodeForbidden, "insufficient scope"), http.StatusForbidden},
		{"NotFound", apierror.NotFound("CASE_NOT_FOUND", "no such case"), http.StatusNotFound},
		{"Conflict", apierror.Conflict("CASE_ALREADY_OPEN", "already open"), http.StatusConflict},
		{"PreconditionFailed", apierror.PreconditionFailed("VERSION_STALE", "stale If-Match"), http.StatusPreconditionFailed},
		{"Unprocessable", apierror.Unprocessable("IDEMPOTENCY_MISMATCH", "different body"), http.StatusUnprocessableEntity},
		{"Unavailable", apierror.Unavailable("EVENT_BUS_UNAVAILABLE", "broker down"), http.StatusServiceUnavailable},
		{"Internal", apierror.Internal(apierror.CodeInternal, "boom"), http.StatusInternalServerError},
		{"zero value fails closed", &apierror.Error{}, http.StatusInternalServerError},
		{"nil receiver", nil, http.StatusInternalServerError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Status(); got != tc.wantStatus {
				t.Errorf("Status() = %d, want %d", got, tc.wantStatus)
			}
			if got := apierror.StatusOf(tc.err); tc.err != nil && got != tc.wantStatus {
				t.Errorf("StatusOf() = %d, want %d", got, tc.wantStatus)
			}

			rec := httptest.NewRecorder()
			apierror.Write(rec, httptest.NewRequest(http.MethodGet, "/v1/cases/1", nil), tc.err)
			if got := rec.Code; got != tc.wantStatus {
				t.Errorf("Write status = %d, want %d", got, tc.wantStatus)
			}
		})
	}
}

func TestInternalFillsInASafeDefault(t *testing.T) {
	tests := []struct {
		name        string
		code        string
		message     string
		wantCode    string
		wantMessage string
	}{
		{"both empty", "", "", apierror.CodeInternal, "Internal server error"},
		{"code only", "OUTBOX_STUCK", "", "OUTBOX_STUCK", "Internal server error"},
		{"message only", "", "relay lost its lock", apierror.CodeInternal, "relay lost its lock"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := apierror.Internal(tc.code, tc.message)
			if got.Code != tc.wantCode {
				t.Errorf("Code = %q, want %q", got.Code, tc.wantCode)
			}
			if got.Message != tc.wantMessage {
				t.Errorf("Message = %q, want %q", got.Message, tc.wantMessage)
			}
		})
	}
}

// TestWriteNeverLeaksNonAPIErrors is the security property of this package: a
// plain error becomes a generic 500 with none of its text.
func TestWriteNeverLeaksNonAPIErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"a database error", errors.New(`pq: relation "cases" does not exist`)},
		{"a wrapped database error", errors.Join(
			errors.New("loading case 01M0: dial tcp 10.0.3.44:5432: connect: connection refused"),
			errors.New("secret=hunter2"),
		)},
		{"a sentinel", context.DeadlineExceeded},
		{"nil", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/v1/cases/1", nil)
			req = req.WithContext(apierror.ContextWithCorrelationID(req.Context(), exampleCorrelationID))

			apierror.Write(rec, req, tc.err)

			if got, want := rec.Code, http.StatusInternalServerError; got != want {
				t.Errorf("status = %d, want %d", got, want)
			}

			var body apierror.Error
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not the error contract: %v (%s)", err, rec.Body)
			}
			if body.Code != apierror.CodeInternal {
				t.Errorf("code = %q, want %q", body.Code, apierror.CodeInternal)
			}
			if body.Message != "Internal server error" {
				t.Errorf("message = %q, want the generic message", body.Message)
			}
			if body.CorrelationID != exampleCorrelationID {
				t.Errorf("correlationId = %q, want %q", body.CorrelationID, exampleCorrelationID)
			}
			if len(body.Details) != 0 {
				t.Errorf("details = %v, want none", body.Details)
			}

			// Nothing from the original error may appear anywhere in the body.
			if tc.err != nil {
				for _, leak := range []string{"pq:", "5432", "hunter2", "deadline", "cases\" does not exist"} {
					if strings.Contains(rec.Body.String(), leak) {
						t.Errorf("body leaked %q: %s", leak, rec.Body)
					}
				}
			}
		})
	}
}

// TestWriteKeepsTheCauseOutOfTheBodyButInTheError proves the split: the cause is
// available to logs through the error chain, and absent from the wire.
func TestWriteKeepsTheCauseOutOfTheBodyButInTheError(t *testing.T) {
	cause := errors.New("dial tcp 10.0.3.44:5432: connect: connection refused")
	apiErr := apierror.Unavailable("EVENT_BUS_UNAVAILABLE", "Event bus unavailable").WithCause(cause)

	rec := httptest.NewRecorder()
	apierror.Write(rec, httptest.NewRequest(http.MethodPost, "/v1/cases", nil), apiErr)

	if strings.Contains(rec.Body.String(), "10.0.3.44") {
		t.Errorf("the cause reached the response body: %s", rec.Body)
	}
	if !errors.Is(apiErr, cause) {
		t.Error("errors.Is cannot find the cause through the api error")
	}
	if !strings.Contains(apiErr.Error(), "connection refused") {
		t.Errorf("Error() = %q, want it to include the cause for logs", apiErr)
	}
}

func TestWriteResolvesTheCorrelationID(t *testing.T) {
	const (
		fromContext = "01M0MEKBHXV37E3S3E28JT97KB"
		fromHeader  = "01M0MEKD80M9S346Q3D25VT4F5"
		fromError   = "01M0MEKCV46CQ643DZVMXXQKFB"
	)

	tests := []struct {
		name    string
		prepare func(*http.Request) *http.Request
		err     *apierror.Error
		want    string // "" means "any freshly minted ULID"
	}{
		{
			name: "context wins over the header",
			prepare: func(r *http.Request) *http.Request {
				r.Header.Set(apierror.CorrelationHeader, fromHeader)
				return r.WithContext(apierror.ContextWithCorrelationID(r.Context(), fromContext))
			},
			err:  apierror.NotFound("CASE_NOT_FOUND", "no such case"),
			want: fromContext,
		},
		{
			name: "the error's own id wins over both",
			prepare: func(r *http.Request) *http.Request {
				r.Header.Set(apierror.CorrelationHeader, fromHeader)
				return r.WithContext(apierror.ContextWithCorrelationID(r.Context(), fromContext))
			},
			err:  apierror.NotFound("CASE_NOT_FOUND", "no such case").WithCorrelationID(fromError),
			want: fromError,
		},
		{
			name: "falls back to the inbound header when the middleware is not wired",
			prepare: func(r *http.Request) *http.Request {
				r.Header.Set(apierror.CorrelationHeader, fromHeader)
				return r
			},
			err:  apierror.NotFound("CASE_NOT_FOUND", "no such case"),
			want: fromHeader,
		},
		{
			name:    "mints one when there is nothing to inherit",
			prepare: func(r *http.Request) *http.Request { return r },
			err:     apierror.NotFound("CASE_NOT_FOUND", "no such case"),
			want:    "",
		},
		{
			name: "a non-ULID inbound header is not echoed back",
			prepare: func(r *http.Request) *http.Request {
				r.Header.Set(apierror.CorrelationHeader, "'; DROP TABLE cases; --")
				return r
			},
			err:  apierror.NotFound("CASE_NOT_FOUND", "no such case"),
			want: "",
		},
		{
			name: "a COR_-prefixed id is refused: envelope ids are bare ULIDs",
			prepare: func(r *http.Request) *http.Request {
				r.Header.Set(apierror.CorrelationHeader, "COR_"+fromHeader)
				return r
			},
			err:  apierror.NotFound("CASE_NOT_FOUND", "no such case"),
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			apierror.Write(rec, tc.prepare(httptest.NewRequest(http.MethodGet, "/v1/cases/1", nil)), tc.err)

			var body apierror.Error
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not the error contract: %v", err)
			}
			if !correlationIDPattern.MatchString(body.CorrelationID) {
				t.Fatalf("correlationId %q does not match the contract pattern", body.CorrelationID)
			}
			if tc.want != "" && body.CorrelationID != tc.want {
				t.Errorf("correlationId = %q, want %q", body.CorrelationID, tc.want)
			}
			if got := rec.Header().Get(apierror.CorrelationHeader); got != body.CorrelationID {
				t.Errorf("%s header = %q, body says %q", apierror.CorrelationHeader, got, body.CorrelationID)
			}
		})
	}
}

// TestWriteToleratesANilRequest keeps Write usable from a background writer:
// a panic here would take down the very handler trying to report a failure.
func TestWriteToleratesANilRequest(t *testing.T) {
	rec := httptest.NewRecorder()
	apierror.Write(rec, nil, apierror.Internal(apierror.CodeInternal, ""))

	if got, want := rec.Code, http.StatusInternalServerError; got != want {
		t.Errorf("status = %d, want %d", got, want)
	}
	var body apierror.Error
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not the error contract: %v", err)
	}
	if !correlationIDPattern.MatchString(body.CorrelationID) {
		t.Errorf("correlationId = %q, want a minted ULID", body.CorrelationID)
	}
}

func TestFrom(t *testing.T) {
	notFound := apierror.NotFound("CASE_NOT_FOUND", "no such case")

	tests := []struct {
		name    string
		err     error
		wantOK  bool
		wantErr *apierror.Error
	}{
		{"an api error", notFound, true, notFound},
		{"wrapped once", errors.Join(notFound), true, notFound},
		{"wrapped in a message", newWrapped("loading case: %w", notFound), true, notFound},
		{"a plain error", errors.New("boom"), false, nil},
		{"nil", nil, false, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := apierror.From(tc.err)
			if ok != tc.wantOK {
				t.Fatalf("From ok = %t, want %t", ok, tc.wantOK)
			}
			if ok && got != tc.wantErr {
				t.Errorf("From returned %v, want %v", got, tc.wantErr)
			}
		})
	}
}

func TestWithMethodsDoNotMutateTheReceiver(t *testing.T) {
	base := apierror.Validation("ARRANGEMENT_INVALID", "Arrangement schedule is invalid",
		apierror.Detail{Field: "firstPaymentDate", Reason: "DATE_IN_PAST"})

	derived := base.
		WithCorrelationID(exampleCorrelationID).
		WithDetails(apierror.Detail{Field: "installments", Reason: "OUT_OF_RANGE"}).
		WithCause(errors.New("boom"))

	if base.CorrelationID != "" {
		t.Errorf("WithCorrelationID mutated the receiver: %q", base.CorrelationID)
	}
	if len(base.Details) != 1 {
		t.Errorf("WithDetails mutated the receiver: %v", base.Details)
	}
	if errors.Unwrap(base) != nil {
		t.Error("WithCause mutated the receiver")
	}
	if len(derived.Details) != 2 {
		t.Errorf("derived has %d details, want 2", len(derived.Details))
	}
	if derived.Code != base.Code || derived.Message != base.Message || derived.Status() != base.Status() {
		t.Error("the derived error lost its code, message or kind")
	}
}

// TestDetailsPassedToAConstructorAreCopied stops a caller mutating an error
// after it was built, which would change what a later Write emits.
func TestDetailsPassedToAConstructorAreCopied(t *testing.T) {
	details := []apierror.Detail{{Field: "firstPaymentDate", Reason: "DATE_IN_PAST"}}
	err := apierror.Validation("ARRANGEMENT_INVALID", "invalid", details...)

	details[0].Reason = "MUTATED"

	if err.Details[0].Reason != "DATE_IN_PAST" {
		t.Errorf("the error shares its details slice with the caller: %v", err.Details)
	}
}

func TestErrorString(t *testing.T) {
	tests := []struct {
		name string
		err  *apierror.Error
		want string
	}{
		{"without a cause", apierror.NotFound("CASE_NOT_FOUND", "no such case"), "CASE_NOT_FOUND: no such case"},
		{
			"with a cause",
			apierror.Internal(apierror.CodeInternal, "").WithCause(errors.New("boom")),
			"INTERNAL: Internal server error: boom",
		},
		{"nil", nil, "<nil apierror.Error>"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Errorf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
	if errors.Unwrap((*apierror.Error)(nil)) != nil {
		t.Error("Unwrap on a nil error did not return nil")
	}
}

func TestCorrelationIDContext(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"round trip", apierror.ContextWithCorrelationID(context.Background(), exampleCorrelationID), exampleCorrelationID},
		{"absent", context.Background(), ""},
		{"nil context", nil, ""},
		{
			"overwritten",
			apierror.ContextWithCorrelationID(
				apierror.ContextWithCorrelationID(context.Background(), "01M0MEKD80M9S346Q3D25VT4F5"),
				exampleCorrelationID),
			exampleCorrelationID,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := apierror.CorrelationIDFrom(tc.ctx); got != tc.want {
				t.Errorf("CorrelationIDFrom = %q, want %q", got, tc.want)
			}
		})
	}
}

// newWrapped keeps the %w wrapping in one place so the table above stays flat.
func newWrapped(format string, err error) error {
	return errWrapper{format: format, err: err}
}

type errWrapper struct {
	format string
	err    error
}

func (e errWrapper) Error() string { return strings.Replace(e.format, "%w", e.err.Error(), 1) }
func (e errWrapper) Unwrap() error { return e.err }
