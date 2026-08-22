package feedspec

import (
	"errors"
	"fmt"
	"strings"
)

// Reason is a stable SCREAMING_SNAKE failure code. The codes are contract
// surface (SPEC.md §11): ING-4 persists them on quarantine_row and in
// rejects.jsonl, dashboards group by them and runbooks branch on them, so
// renaming one is a breaking change.
type Reason string

// File-level reasons: the header record.
const (
	ReasonHeaderMissing              Reason = "HEADER_MISSING"
	ReasonHeaderMalformed            Reason = "HEADER_MALFORMED"
	ReasonHeaderRecordType           Reason = "HEADER_RECORD_TYPE"
	ReasonHeaderFeedCodeMismatch     Reason = "HEADER_FEED_CODE_MISMATCH"
	ReasonHeaderBusinessDateInvalid  Reason = "HEADER_BUSINESS_DATE_INVALID"
	ReasonHeaderBusinessDateMismatch Reason = "HEADER_BUSINESS_DATE_MISMATCH"
	ReasonHeaderRecordCountInvalid   Reason = "HEADER_RECORD_COUNT_INVALID"
	ReasonHeaderDuplicate            Reason = "HEADER_DUPLICATE"
)

// File-level reasons: the trailer record.
const (
	ReasonTrailerMissing             Reason = "TRAILER_MISSING"
	ReasonTrailerMalformed           Reason = "TRAILER_MALFORMED"
	ReasonTrailerRecordType          Reason = "TRAILER_RECORD_TYPE"
	ReasonTrailerRecordCountInvalid  Reason = "TRAILER_RECORD_COUNT_INVALID"
	ReasonTrailerControlTotalInvalid Reason = "TRAILER_CONTROL_TOTAL_INVALID"
	ReasonRowAfterTrailer            Reason = "ROW_AFTER_TRAILER"
)

// File-level reasons: the reconciliation controls (A§37).
const (
	ReasonRecordCountMismatch        Reason = "RECORD_COUNT_MISMATCH"
	ReasonHeaderTrailerCountMismatch Reason = "HEADER_TRAILER_COUNT_MISMATCH"
	ReasonControlTotalMismatch       Reason = "CONTROL_TOTAL_MISMATCH"
)

// Row-level reasons.
const (
	ReasonRecordTypeUnknown Reason = "RECORD_TYPE_UNKNOWN"
	ReasonRowUnparseable    Reason = "ROW_UNPARSEABLE"
	ReasonRowFieldCount     Reason = "ROW_FIELD_COUNT"
	ReasonEncodingInvalid   Reason = "ENCODING_INVALID"
)

// Cell-level reasons.
const (
	ReasonRequiredFieldMissing        Reason = "REQUIRED_FIELD_MISSING"
	ReasonPatternMismatch             Reason = "PATTERN_MISMATCH"
	ReasonInvalidInteger              Reason = "INVALID_INTEGER"
	ReasonInvalidDecimal              Reason = "INVALID_DECIMAL"
	ReasonDecimalScaleExceeded        Reason = "DECIMAL_SCALE_EXCEEDED"
	ReasonMinViolation                Reason = "MIN_VIOLATION"
	ReasonMaxViolation                Reason = "MAX_VIOLATION"
	ReasonInvalidDate                 Reason = "INVALID_DATE"
	ReasonEnumInvalid                 Reason = "ENUM_INVALID"
	ReasonDecimalExceedsRulePrecision Reason = "DECIMAL_EXCEEDS_RULE_PRECISION"
)

// Rule-level reasons.
const (
	ReasonBusinessRuleFailed  Reason = "BUSINESS_RULE_FAILED"
	ReasonRuleEvaluationError Reason = "RULE_EVALUATION_ERROR"
)

// Failure is one validation finding: what is wrong, where, and how badly. It is
// the quarantine record ING-4 persists, so the JSON field names are contract.
//
// Detail may quote the offending field value, which means a Failure carries the
// same data classification as the file it came from (D§45): it belongs in the
// quarantine store and in rejects.jsonl, never in a log line or an API error
// body.
type Failure struct {
	// RowNumber is the 1-based physical line of the offending record. 0 means
	// the finding is about the file as a whole (a missing header, a missing
	// trailer). ValidateRow leaves it 0 — the caller that knows the line
	// number stamps it, and Validator/ValidateFile do that for you.
	RowNumber int `json:"row_number"`
	// Column is the column name for cell-level findings, empty otherwise.
	Column string `json:"column,omitempty"`
	// RuleID is the business_rules[].id for rule findings, empty otherwise.
	RuleID string `json:"rule_id,omitempty"`
	// Reason is the stable code (SPEC.md §11).
	Reason Reason `json:"reason"`
	// Severity is ERROR (quarantines the file) or WARN (recorded, continue).
	Severity Severity `json:"severity"`
	// Detail is a human-readable specific. Not stable, never parsed.
	Detail string `json:"detail,omitempty"`
}

// String renders a failure for an error message.
func (f Failure) String() string {
	var b strings.Builder
	b.WriteString(string(f.Severity))
	b.WriteString(" ")
	b.WriteString(string(f.Reason))
	if f.RowNumber > 0 {
		fmt.Fprintf(&b, " row=%d", f.RowNumber)
	}
	if f.Column != "" {
		b.WriteString(" column=" + f.Column)
	}
	if f.RuleID != "" {
		b.WriteString(" rule=" + f.RuleID)
	}
	if f.Detail != "" {
		b.WriteString(": " + f.Detail)
	}
	return b.String()
}

// ValidationError is the error form of a set of ERROR-severity failures, so a
// caller can either branch on the error or, with errors.As, read the structured
// failures. WARN failures never appear in an error.
type ValidationError struct {
	FeedID   string
	Failures []Failure
}

// Error implements error.
func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Failures))
	for _, f := range e.Failures {
		parts = append(parts, f.String())
	}
	return fmt.Sprintf("feed %s: %s", e.FeedID, strings.Join(parts, "; "))
}

// ErrNotLoaded is returned by the validation methods of a Feed that was not
// produced by Load: its rules were never compiled, so validating against it
// would silently accept rows no rule has judged.
var ErrNotLoaded = errors.New("feedspec: feed not loaded (use feedspec.Load)")

// asError wraps the ERROR-severity members of fs in a *ValidationError, or
// returns nil when there are none.
func asError(feedID string, fs []Failure) error {
	errs := make([]Failure, 0, len(fs))
	for _, f := range fs {
		if f.Severity == SeverityError {
			errs = append(errs, f)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return &ValidationError{FeedID: feedID, Failures: errs}
}

// stamp sets the row number on failures that do not have one.
func stamp(row int, fs []Failure) []Failure {
	for i := range fs {
		if fs[i].RowNumber == 0 {
			fs[i].RowNumber = row
		}
	}
	return fs
}
