package feedspec

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

// dateLayout is the source systems' date rendering (D§21, cb_account.open_dt).
const dateLayout = "20060102"

var (
	integerText = regexp.MustCompile(`^[+-]?\d+$`)
	decimalText = regexp.MustCompile(`^[+-]?\d+(\.\d+)?$`)
	countText   = regexp.MustCompile(`^\d+$`)
	dateText    = regexp.MustCompile(`^\d{8}$`)
)

// Value is one validated cell of a DATA row. Present is false when an optional
// column's field was empty — an absent value, which is a SQL NULL downstream,
// never a zero.
type Value struct {
	Column  string
	Type    ColumnType
	Raw     string
	Present bool
	// Int carries a TypeInteger value.
	Int int64
	// Dec carries a TypeDecimal value, exactly.
	Dec decimal.Decimal
	// Date carries a TypeDate value, parsed as a real calendar date in UTC.
	Date time.Time
}

// RowResult is the outcome of validating one DATA record: the typed values (for
// the canonicalizer) and every failure found (for the quarantine record).
type RowResult struct {
	// RowNumber is the physical line of the record, or 0 when the caller has
	// not stamped it (Feed.ValidateRow does not know it).
	RowNumber int
	// Values holds one entry per declared column, in declared order. Empty
	// when the record could not be mapped to the columns at all (wrong field
	// count, wrong record type).
	Values []Value
	// Failures is every finding for this row, ERROR and WARN.
	Failures []Failure
	// RulesEvaluated is false when the business rules were skipped because a
	// column had no usable value — see the package doc.
	RulesEvaluated bool
}

// OK reports whether the row has no ERROR-severity failure. A row that is OK may
// still carry WARN failures.
func (r RowResult) OK() bool {
	for _, f := range r.Failures {
		if f.Severity == SeverityError {
			return false
		}
	}
	return true
}

// Value returns the named column's value and whether the row has one.
func (r RowResult) Value(column string) (Value, bool) {
	for _, v := range r.Values {
		if v.Column == column {
			return v, true
		}
	}
	return Value{}, false
}

// SetRowNumber stamps the row number on the result and on every failure that
// does not have one yet.
func (r *RowResult) SetRowNumber(n int) {
	r.RowNumber = n
	r.Failures = stamp(n, r.Failures)
}

// HeaderRecord is a parsed HEADER line (D§21).
type HeaderRecord struct {
	FeedCode     string
	BusinessDate string
	RecordCount  int
}

// TrailerRecord is a parsed TRAILER line (D§21).
type TrailerRecord struct {
	RecordCount  int
	ControlTotal decimal.Decimal
}

// ---------------------------------------------------------------- stateless API

// ValidateHeader checks a HEADER line against the contract: the record type, the
// field count, the declared feed code, the business date (a real calendar date,
// equal to businessDate when one is given — normally the date captured from the
// file name by MatchFilename) and the declared record count.
//
// It is stateless: the count it declares is cross-checked against the parsed rows
// by Validator.ValidateTrailer, which is the only place that knows both.
func (f *Feed) ValidateHeader(line string, businessDate string) error {
	if !f.loaded {
		return ErrNotLoaded
	}
	rec, err := parseLine(line)
	if err != nil {
		return &ValidationError{FeedID: f.FeedID, Failures: []Failure{{
			Reason: ReasonHeaderMalformed, Severity: SeverityError,
			Detail: fmt.Sprintf("line is not parseable CSV: %v", err),
		}}}
	}
	_, fails := f.checkHeader(rec, businessDate)
	return asError(f.FeedID, fails)
}

// ParseHeader parses and validates a HEADER line, returning the declared values.
func (f *Feed) ParseHeader(line string, businessDate string) (HeaderRecord, error) {
	if !f.loaded {
		return HeaderRecord{}, ErrNotLoaded
	}
	rec, err := parseLine(line)
	if err != nil {
		return HeaderRecord{}, &ValidationError{FeedID: f.FeedID, Failures: []Failure{{
			Reason: ReasonHeaderMalformed, Severity: SeverityError,
			Detail: fmt.Sprintf("line is not parseable CSV: %v", err),
		}}}
	}
	h, fails := f.checkHeader(rec, businessDate)
	return h, asError(f.FeedID, fails)
}

// ParseTrailer parses and validates the structure of a TRAILER line: record
// type, field count, a non-negative record count and a control total written at
// exactly the control column's scale. The cross-checks against the parsed rows
// live in Validator.ValidateTrailer.
func (f *Feed) ParseTrailer(line string) (TrailerRecord, error) {
	if !f.loaded {
		return TrailerRecord{}, ErrNotLoaded
	}
	rec, err := parseLine(line)
	if err != nil {
		return TrailerRecord{}, &ValidationError{FeedID: f.FeedID, Failures: []Failure{{
			Reason: ReasonTrailerMalformed, Severity: SeverityError,
			Detail: fmt.Sprintf("line is not parseable CSV: %v", err),
		}}}
	}
	t, fails := f.checkTrailer(rec)
	return t, asError(f.FeedID, fails)
}

// ValidateRow validates one DATA record — the raw CSV fields including the
// leading "DATA" marker, exactly as encoding/csv yields them.
//
// The order is the D§21 order: record type, field count, mandatory fields, data
// types and constraints, then the business rules. Rules run only when every
// column produced a usable typed value, and a rule that cannot be evaluated is
// an ERROR (see the package doc).
//
// The returned error is not a validation verdict — validation findings are in
// RowResult.Failures. It is non-nil only when the Feed itself is unusable
// (not produced by Load), which is a programming error, not bad data.
func (f *Feed) ValidateRow(record []string) (RowResult, error) {
	if !f.loaded {
		return RowResult{}, ErrNotLoaded
	}
	var res RowResult

	if len(record) == 0 || record[0] != RecordData {
		got := ""
		if len(record) > 0 {
			got = record[0]
		}
		res.Failures = append(res.Failures, Failure{
			Reason: ReasonRecordTypeUnknown, Severity: SeverityError,
			Detail: fmt.Sprintf("expected record type %q, got %q", RecordData, got),
		})
		return res, nil
	}
	if len(record) != f.FieldCount() {
		res.Failures = append(res.Failures, Failure{
			Reason: ReasonRowFieldCount, Severity: SeverityError,
			Detail: fmt.Sprintf("expected %d fields (DATA + %d columns), got %d",
				f.FieldCount(), len(f.Columns), len(record)),
		})
		return res, nil
	}

	res.Values = make([]Value, len(f.Columns))
	usable := true
	for i, col := range f.Columns {
		v, fails := checkCell(col, record[i+1])
		res.Values[i] = v
		res.Failures = append(res.Failures, fails...)
		if !v.Present && col.Required {
			usable = false
		}
		for _, fl := range fails {
			if isParseFailure(fl.Reason) {
				usable = false
			}
		}
	}
	if !usable || len(f.Rules) == 0 {
		return res, nil
	}

	vars, convFails := f.activation(res.Values)
	res.Failures = append(res.Failures, convFails...)
	if len(convFails) > 0 {
		return res, nil
	}
	res.Failures = append(res.Failures, f.evalRules(vars)...)
	res.RulesEvaluated = true
	return res, nil
}

// isParseFailure reports whether a reason means "this cell has no usable typed
// value", which is what makes the business rules unevaluable for the row.
func isParseFailure(r Reason) bool {
	switch r {
	case ReasonRequiredFieldMissing, ReasonInvalidInteger, ReasonInvalidDecimal,
		ReasonInvalidDate, ReasonEncodingInvalid:
		return true
	default:
		// Constraint failures (pattern, enum, min, max, scale) leave a usable
		// value behind, so the rules still say something meaningful about the
		// row and are still evaluated.
		return false
	}
}

// ---------------------------------------------------------------- stateful API

// Validator is the per-file validation state: it remembers the header's declared
// count, counts the parsed rows and accumulates the control total, so the trailer
// can be cross-checked against them. One Validator validates exactly one file.
type Validator struct {
	feed         *Feed
	businessDate string

	headerSeen  bool
	sawRecord   bool
	trailerSeen bool

	headerCount   int
	declaredCount int
	parsed        int
	rejected      int
	warned        int

	total    *ControlTotalAccumulator
	declared decimal.Decimal
	failures []Failure
}

// NewValidator starts validating one file of this feed. businessDate is the date
// captured from the file name (MatchFilename); pass "" to accept whatever the
// header declares, which only the reprocessing path should do.
func (f *Feed) NewValidator(businessDate string) *Validator {
	return &Validator{
		feed:          f,
		businessDate:  businessDate,
		headerCount:   -1,
		declaredCount: -1,
		total:         f.NewControlTotalAccumulator(),
	}
}

// ValidateHeader validates a HEADER line and remembers its declared record count.
func (v *Validator) ValidateHeader(line string) error {
	rec, err := parseLine(line)
	if err != nil {
		return v.record([]Failure{{
			Reason: ReasonHeaderMalformed, Severity: SeverityError,
			Detail: fmt.Sprintf("line is not parseable CSV: %v", err),
		}})
	}
	return v.ValidateHeaderRecord(rec)
}

// ValidateHeaderRecord is ValidateHeader for a record already parsed by
// encoding/csv, which is how the pipeline worker reads the file.
func (v *Validator) ValidateHeaderRecord(rec []string) error {
	if !v.feed.loaded {
		return ErrNotLoaded
	}
	switch {
	case v.headerSeen:
		v.sawRecord = true
		return v.record([]Failure{{
			Reason: ReasonHeaderDuplicate, Severity: SeverityError,
			Detail: "a second HEADER record: exactly one is allowed, as the first record",
		}})
	case v.sawRecord:
		// A header that is not the first record: the file has no header
		// where one is required. Reported once, here, so Result() does not
		// report it a second time.
		v.headerSeen = true
		h, fails := v.feed.checkHeader(rec, v.businessDate)
		v.headerCount = h.RecordCount
		fails = append([]Failure{{
			Reason: ReasonHeaderMissing, Severity: SeverityError,
			Detail: "the first record of the file is not a HEADER record",
		}}, fails...)
		return v.record(fails)
	default:
		v.headerSeen = true
		v.sawRecord = true
		h, fails := v.feed.checkHeader(rec, v.businessDate)
		v.headerCount = h.RecordCount
		return v.record(fails)
	}
}

// ValidateRow validates one DATA record and folds it into the counts and the
// control total.
func (v *Validator) ValidateRow(record []string) (RowResult, error) {
	res, err := v.feed.ValidateRow(record)
	if err != nil {
		return res, err
	}
	v.sawRecord = true

	if v.trailerSeen {
		res.Failures = append(res.Failures, Failure{
			Reason: ReasonRowAfterTrailer, Severity: SeverityError,
			Detail: "a DATA record after the TRAILER record",
		})
	}
	if len(res.Values) > 0 {
		v.parsed++
		if err := v.total.Add(res); err != nil && !errors.Is(err, ErrNoControlValue) {
			return res, err
		}
	}
	if !res.OK() {
		v.rejected++
	}
	for _, f := range res.Failures {
		if f.Severity == SeverityWarn {
			v.warned++
			break
		}
	}
	v.failures = append(v.failures, res.Failures...)
	return res, nil
}

// ValidateTrailer validates the TRAILER line and runs the file-level
// reconciliation controls: declared record count against the parsed rows, the
// header's count against the trailer's, and the declared control total against
// the exact decimal sum of the control column (A§37). Call it after the rows.
func (v *Validator) ValidateTrailer(line string) error {
	rec, err := parseLine(line)
	if err != nil {
		return v.record([]Failure{{
			Reason: ReasonTrailerMalformed, Severity: SeverityError,
			Detail: fmt.Sprintf("line is not parseable CSV: %v", err),
		}})
	}
	return v.ValidateTrailerRecord(rec)
}

// ValidateTrailerRecord is ValidateTrailer for an already-parsed record.
func (v *Validator) ValidateTrailerRecord(rec []string) error {
	if !v.feed.loaded {
		return ErrNotLoaded
	}
	if v.trailerSeen {
		v.sawRecord = true
		return v.record([]Failure{{
			Reason: ReasonTrailerMalformed, Severity: SeverityError,
			Detail: "a second TRAILER record: exactly one is allowed, as the last record",
		}})
	}
	v.sawRecord = true
	v.trailerSeen = true

	t, fails := v.feed.checkTrailer(rec)
	countUsable, totalUsable := true, true
	for _, f := range fails {
		switch f.Reason {
		case ReasonTrailerRecordType, ReasonTrailerMalformed:
			countUsable, totalUsable = false, false
		case ReasonTrailerRecordCountInvalid:
			countUsable = false
		case ReasonTrailerControlTotalInvalid:
			totalUsable = false
		default:
		}
	}

	if countUsable {
		v.declaredCount = t.RecordCount
		if t.RecordCount != v.parsed {
			fails = append(fails, Failure{
				Reason: ReasonRecordCountMismatch, Severity: SeverityError,
				Detail: fmt.Sprintf("trailer declares %d DATA rows, %d parsed", t.RecordCount, v.parsed),
			})
		}
		if v.headerCount >= 0 && v.headerCount != t.RecordCount {
			fails = append(fails, Failure{
				Reason: ReasonHeaderTrailerCountMismatch, Severity: SeverityError,
				Detail: fmt.Sprintf("header declares %d DATA rows, trailer declares %d", v.headerCount, t.RecordCount),
			})
		}
	}
	if totalUsable {
		v.declared = t.ControlTotal
		if !v.total.Matches(t.ControlTotal) {
			fails = append(fails, Failure{
				Column: v.feed.ControlTotalColumn,
				Reason: ReasonControlTotalMismatch, Severity: SeverityError,
				Detail: fmt.Sprintf("trailer declares %s, sum(%s) over %d rows is %s",
					t.ControlTotal.StringFixed(v.feed.ControlTotalScale()),
					v.feed.ControlTotalColumn, v.total.Rows(),
					v.total.Result().StringFixed(v.feed.ControlTotalScale())),
			})
		}
	}
	return v.record(fails)
}

// Result is the file's outcome so far, including the terminal findings (a
// missing header, a missing trailer). It does not mutate the validator, so it may
// be called more than once.
func (v *Validator) Result() FileResult {
	fails := make([]Failure, 0, len(v.failures)+2)
	if !v.headerSeen {
		fails = append(fails, Failure{
			Reason: ReasonHeaderMissing, Severity: SeverityError,
			Detail: "the file has no HEADER record",
		})
	}
	fails = append(fails, v.failures...)
	if !v.trailerSeen {
		fails = append(fails, Failure{
			Reason: ReasonTrailerMissing, Severity: SeverityError,
			Detail: "the file has no TRAILER record",
		})
	}
	return FileResult{
		FeedID:               v.feed.FeedID,
		BusinessDate:         v.businessDate,
		HeaderCount:          v.headerCount,
		DeclaredCount:        v.declaredCount,
		ParsedCount:          v.parsed,
		RejectedCount:        v.rejected,
		WarnCount:            v.warned,
		DeclaredControlTotal: v.declared,
		ComputedControlTotal: v.total.Result(),
		Failures:             fails,
	}
}

// record appends failures to the file's findings and returns the ERROR ones as an
// error.
func (v *Validator) record(fails []Failure) error {
	v.failures = append(v.failures, fails...)
	return asError(v.feed.FeedID, fails)
}

// ---------------------------------------------------------------- whole file

// FileResult is the validated outcome of one file: the declared and observed
// counts and totals (the A§37 reconciliation inputs) plus every failure. ING-4
// writes these onto the file_registry row and the reject records.
type FileResult struct {
	FeedID       string `json:"feed_id"`
	BusinessDate string `json:"business_date"`
	// HeaderCount is the header's declared row count, -1 when the header is
	// absent or its count unparseable.
	HeaderCount int `json:"header_count"`
	// DeclaredCount is the trailer's declared row count, -1 when the trailer
	// is absent or its count unparseable.
	DeclaredCount int `json:"declared_count"`
	// ParsedCount is the number of DATA records whose fields could be mapped
	// to the columns (declared = rejected + loaded, A§37).
	ParsedCount int `json:"parsed_count"`
	// RejectedCount is the number of DATA records with at least one ERROR.
	RejectedCount int `json:"rejected_count"`
	// WarnCount is the number of DATA records with at least one WARN.
	WarnCount int `json:"warn_count"`
	// DeclaredControlTotal is the trailer's total, zero when unparseable.
	DeclaredControlTotal decimal.Decimal `json:"declared_control_total"`
	// ComputedControlTotal is the exact decimal sum of the control column over
	// the rows where it parsed.
	ComputedControlTotal decimal.Decimal `json:"computed_control_total"`
	Failures             []Failure       `json:"failures"`
}

// OK reports whether the file has no ERROR failure — the condition ING-4 requires
// before a file may move to VALIDATED.
func (r FileResult) OK() bool {
	for _, f := range r.Failures {
		if f.Severity == SeverityError {
			return false
		}
	}
	return true
}

// Errors returns the ERROR-severity failures.
func (r FileResult) Errors() []Failure { return r.bySeverity(SeverityError) }

// Warnings returns the WARN-severity failures.
func (r FileResult) Warnings() []Failure { return r.bySeverity(SeverityWarn) }

func (r FileResult) bySeverity(s Severity) []Failure {
	out := make([]Failure, 0, len(r.Failures))
	for _, f := range r.Failures {
		if f.Severity == s {
			out = append(out, f)
		}
	}
	return out
}

// ValidateFile validates a whole feed file read from r, in the D§21 order, and
// returns the file's outcome. businessDate is the date captured from the file
// name (MatchFilename).
//
// The returned error is not a validation verdict: findings are in
// FileResult.Failures, and FileResult.OK() is the verdict. An error means the
// reader itself failed, or the feed was not produced by Load.
//
// Rows are not retained. The pipeline worker, which needs each row's typed values
// to write the canonical copy, drives Validator directly over its own csv.Reader
// instead — this function is the convenience used by tests and small files.
func (f *Feed) ValidateFile(r io.Reader, businessDate string) (FileResult, error) {
	if !f.loaded {
		return FileResult{}, ErrNotLoaded
	}
	v := f.NewValidator(businessDate)

	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1 // record shape is validated per record type
	cr.LazyQuotes = false   // RFC 4180, strictly
	cr.TrimLeadingSpace = false

	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var perr *csv.ParseError
			if errors.As(err, &perr) {
				v.failures = append(v.failures, Failure{
					RowNumber: perr.StartLine,
					Reason:    ReasonRowUnparseable, Severity: SeverityError,
					Detail: fmt.Sprintf("record is not valid RFC4180 CSV: %v", perr.Err),
				})
				// Stop: a reader that failed to tokenise cannot be trusted to
				// resynchronise, and the file is quarantined either way.
				break
			}
			return v.Result(), fmt.Errorf("feedspec: reading feed %s: %w", f.FeedID, err)
		}

		line := 0
		if len(rec) > 0 {
			line, _ = cr.FieldPos(0)
		}

		switch recordType(rec) {
		case RecordHeader:
			v.recordAt(line, func() error { return v.ValidateHeaderRecord(rec) })
		case RecordTrailer:
			v.recordAt(line, func() error { return v.ValidateTrailerRecord(rec) })
		case RecordData:
			before := len(v.failures)
			if _, err := v.ValidateRow(rec); err != nil {
				return v.Result(), err
			}
			stamp(line, v.failures[before:])
		default:
			v.sawRecord = true
			got := ""
			if len(rec) > 0 {
				got = rec[0]
			}
			v.failures = append(v.failures, Failure{
				RowNumber: line,
				Reason:    ReasonRecordTypeUnknown, Severity: SeverityError,
				Detail: fmt.Sprintf("unknown record type %q: expected %s, %s or %s",
					got, RecordHeader, RecordData, RecordTrailer),
			})
		}
	}
	return v.Result(), nil
}

// recordAt runs a validator call and stamps the physical line on whatever
// failures it produced.
func (v *Validator) recordAt(line int, call func() error) {
	before := len(v.failures)
	_ = call() // findings are accumulated on the validator; the error is the same data
	stamp(line, v.failures[before:])
}

func recordType(rec []string) string {
	if len(rec) == 0 {
		return ""
	}
	switch rec[0] {
	case RecordHeader, RecordData, RecordTrailer:
		return rec[0]
	default:
		return ""
	}
}

// ---------------------------------------------------------------- record checks

func (f *Feed) checkHeader(rec []string, businessDate string) (HeaderRecord, []Failure) {
	var h HeaderRecord
	var fails []Failure

	if len(rec) == 0 || rec[0] != RecordHeader {
		got := ""
		if len(rec) > 0 {
			got = rec[0]
		}
		return h, []Failure{{
			Reason: ReasonHeaderRecordType, Severity: SeverityError,
			Detail: fmt.Sprintf("expected record type %q, got %q", RecordHeader, got),
		}}
	}
	want := len(wantHeaderFields) + 1
	if len(rec) != want {
		return h, []Failure{{
			Reason: ReasonHeaderMalformed, Severity: SeverityError,
			Detail: fmt.Sprintf("expected %d fields (HEADER,%s), got %d",
				want, strings.Join(wantHeaderFields, ","), len(rec)),
		}}
	}
	if bad := utf8Failure(rec); bad != nil {
		fails = append(fails, *bad)
	}

	h.FeedCode = rec[1]
	if rec[1] != f.FeedCode {
		fails = append(fails, Failure{
			Reason: ReasonHeaderFeedCodeMismatch, Severity: SeverityError,
			Detail: fmt.Sprintf("expected feed code %q, got %q", f.FeedCode, rec[1]),
		})
	}

	h.BusinessDate = rec[2]
	switch {
	case !validDate(rec[2]):
		fails = append(fails, Failure{
			Reason: ReasonHeaderBusinessDateInvalid, Severity: SeverityError,
			Detail: fmt.Sprintf("%q is not a YYYYMMDD calendar date", rec[2]),
		})
	case businessDate != "" && rec[2] != businessDate:
		fails = append(fails, Failure{
			Reason: ReasonHeaderBusinessDateMismatch, Severity: SeverityError,
			Detail: fmt.Sprintf("header declares business date %s, file name says %s", rec[2], businessDate),
		})
	}

	if n, ok := parseCount(rec[3]); ok {
		h.RecordCount = n
	} else {
		h.RecordCount = -1
		fails = append(fails, Failure{
			Reason: ReasonHeaderRecordCountInvalid, Severity: SeverityError,
			Detail: fmt.Sprintf("%q is not a non-negative record count", rec[3]),
		})
	}
	return h, fails
}

func (f *Feed) checkTrailer(rec []string) (TrailerRecord, []Failure) {
	var t TrailerRecord
	var fails []Failure

	if len(rec) == 0 || rec[0] != RecordTrailer {
		got := ""
		if len(rec) > 0 {
			got = rec[0]
		}
		return t, []Failure{{
			Reason: ReasonTrailerRecordType, Severity: SeverityError,
			Detail: fmt.Sprintf("expected record type %q, got %q", RecordTrailer, got),
		}}
	}
	want := len(wantTrailerFields) + 1
	if len(rec) != want {
		return t, []Failure{{
			Reason: ReasonTrailerMalformed, Severity: SeverityError,
			Detail: fmt.Sprintf("expected %d fields (TRAILER,%s), got %d",
				want, strings.Join(wantTrailerFields, ","), len(rec)),
		}}
	}
	if bad := utf8Failure(rec); bad != nil {
		fails = append(fails, *bad)
	}

	if n, ok := parseCount(rec[1]); ok {
		t.RecordCount = n
	} else {
		t.RecordCount = -1
		fails = append(fails, Failure{
			Reason: ReasonTrailerRecordCountInvalid, Severity: SeverityError,
			Detail: fmt.Sprintf("%q is not a non-negative record count", rec[1]),
		})
	}

	scale := f.ControlTotalScale()
	if d, ok := parseDecimal(rec[2], int(scale), true); ok {
		t.ControlTotal = d
	} else {
		fails = append(fails, Failure{
			Column: f.ControlTotalColumn,
			Reason: ReasonTrailerControlTotalInvalid, Severity: SeverityError,
			Detail: fmt.Sprintf("%q is not a decimal written at exactly %d decimal places", rec[2], scale),
		})
	}
	return t, fails
}

// checkCell type-checks and constraint-checks one field against its column.
func checkCell(col Column, raw string) (Value, []Failure) {
	v := Value{Column: col.Name, Type: col.Type, Raw: raw}
	if !utf8.ValidString(raw) {
		return v, []Failure{{
			Column: col.Name, Reason: ReasonEncodingInvalid, Severity: SeverityError,
			Detail: "field is not valid UTF-8",
		}}
	}
	if raw == "" {
		if col.Required {
			return v, []Failure{{
				Column: col.Name, Reason: ReasonRequiredFieldMissing, Severity: SeverityError,
				Detail: "field is empty",
			}}
		}
		return v, nil // absent optional value
	}
	v.Present = true

	var fails []Failure
	switch col.Type {
	case TypeString:
		if col.pattern != nil && !col.pattern.MatchString(raw) {
			fails = append(fails, Failure{
				Column: col.Name, Reason: ReasonPatternMismatch, Severity: SeverityError,
				Detail: fmt.Sprintf("%q does not match %s", raw, col.Pattern),
			})
		}
	case TypeEnum:
		found := false
		for _, e := range col.Enum {
			if raw == e {
				found = true
				break
			}
		}
		if !found {
			fails = append(fails, Failure{
				Column: col.Name, Reason: ReasonEnumInvalid, Severity: SeverityError,
				Detail: fmt.Sprintf("%q is not one of [%s]", raw, strings.Join(col.Enum, " ")),
			})
		}
	case TypeDate:
		if !validDate(raw) {
			v.Present = false
			fails = append(fails, Failure{
				Column: col.Name, Reason: ReasonInvalidDate, Severity: SeverityError,
				Detail: fmt.Sprintf("%q is not a YYYYMMDD calendar date", raw),
			})
			break
		}
		v.Date, _ = time.ParseInLocation(dateLayout, raw, time.UTC)
	case TypeInteger:
		n, err := parseInteger(raw)
		if err != nil {
			v.Present = false
			fails = append(fails, Failure{
				Column: col.Name, Reason: ReasonInvalidInteger, Severity: SeverityError,
				Detail: fmt.Sprintf("%q is not an integer: %v", raw, err),
			})
			break
		}
		v.Int = n
		fails = append(fails, checkBounds(col, decimal.NewFromInt(n))...)
	case TypeDecimal:
		d, ok := parseDecimal(raw, col.Scale, false)
		if !ok {
			v.Present = false
			fails = append(fails, Failure{
				Column: col.Name, Reason: ReasonInvalidDecimal, Severity: SeverityError,
				Detail: fmt.Sprintf("%q is not a plain decimal number", raw),
			})
			break
		}
		v.Dec = d
		if -d.Exponent() > int32(col.Scale) { //nolint:gosec // scale is validated 0..9 at load
			fails = append(fails, Failure{
				Column: col.Name, Reason: ReasonDecimalScaleExceeded, Severity: SeverityError,
				Detail: fmt.Sprintf("%q has more than %d decimal places", raw, col.Scale),
			})
		}
		fails = append(fails, checkBounds(col, d)...)
	default:
		// Unreachable: Load rejects unknown column types.
		fails = append(fails, Failure{
			Column: col.Name, Reason: ReasonRowUnparseable, Severity: SeverityError,
			Detail: fmt.Sprintf("column has unknown type %q", col.Type),
		})
	}
	return v, fails
}

func checkBounds(col Column, d decimal.Decimal) []Failure {
	var fails []Failure
	if col.Min != nil && d.LessThan(*col.Min) {
		fails = append(fails, Failure{
			Column: col.Name, Reason: ReasonMinViolation, Severity: SeverityError,
			Detail: fmt.Sprintf("%s is below the inclusive minimum %s", d, col.Min),
		})
	}
	if col.Max != nil && d.GreaterThan(*col.Max) {
		fails = append(fails, Failure{
			Column: col.Name, Reason: ReasonMaxViolation, Severity: SeverityError,
			Detail: fmt.Sprintf("%s is above the inclusive maximum %s", d, col.Max),
		})
	}
	return fails
}

// ---------------------------------------------------------------- primitives

// parseLine parses one CSV line into its fields.
func parseLine(line string) ([]string, error) {
	cr := csv.NewReader(strings.NewReader(strings.TrimRight(line, "\r\n")))
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = false
	rec, err := cr.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("empty line")
		}
		return nil, err
	}
	return rec, nil
}

// validDate reports whether s is eight digits and a real calendar date: 20260231
// is not, 20240229 is.
func validDate(s string) bool {
	if !dateText.MatchString(s) {
		return false
	}
	_, err := time.ParseInLocation(dateLayout, s, time.UTC)
	return err == nil
}

// parseCount parses a record count: digits only, no sign, fits in an int.
func parseCount(s string) (int, bool) {
	if !countText.MatchString(s) {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

func parseInteger(s string) (int64, error) {
	if !integerText.MatchString(s) {
		return 0, errors.New("expected an optionally signed run of digits")
	}
	return strconv.ParseInt(s, 10, 64)
}

// parseDecimal parses a plain decimal — optional sign, digits, optional
// fractional part, no exponent, no thousands separator. When exactScale is true
// the text must carry exactly scale decimal places, which is how the trailer's
// control total must be written (SPEC.md §1).
func parseDecimal(s string, scale int, exactScale bool) (decimal.Decimal, bool) {
	if !decimalText.MatchString(s) {
		return decimal.Decimal{}, false
	}
	if exactScale {
		frac := 0
		if i := strings.IndexByte(s, '.'); i >= 0 {
			frac = len(s) - i - 1
		}
		if frac != scale {
			return decimal.Decimal{}, false
		}
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Decimal{}, false
	}
	return d, true
}

// utf8Failure reports the first field of a header/trailer record that is not
// valid UTF-8.
func utf8Failure(rec []string) *Failure {
	for i, f := range rec {
		if !utf8.ValidString(f) {
			return &Failure{
				Reason: ReasonEncodingInvalid, Severity: SeverityError,
				Detail: fmt.Sprintf("field %d is not valid UTF-8", i+1),
			}
		}
	}
	return nil
}
