package feedspec

import (
	"regexp"

	"github.com/google/cel-go/cel"
	"github.com/shopspring/decimal"
)

// Record types, field 1 of every line (D§21).
const (
	RecordHeader  = "HEADER"
	RecordData    = "DATA"
	RecordTrailer = "TRAILER"
)

// ColumnType is the declared type of a column (SPEC.md §5).
type ColumnType string

const (
	// TypeString is free text; an optional pattern constrains it.
	TypeString ColumnType = "string"
	// TypeInteger is a signed decimal integer that fits in an int64.
	TypeInteger ColumnType = "integer"
	// TypeDecimal is a fixed-scale decimal; scale is mandatory.
	TypeDecimal ColumnType = "decimal"
	// TypeDate is a real calendar date rendered YYYYMMDD, as the source
	// systems emit them (cb_account.open_dt int).
	TypeDate ColumnType = "date_yyyymmdd"
	// TypeEnum is a closed, case-sensitive vocabulary.
	TypeEnum ColumnType = "enum"
)

// Severity decides what a failure does to the file: ERROR quarantines it whole,
// WARN is recorded and the file continues (ING-4 step 4).
type Severity string

const (
	// SeverityError quarantines the whole file.
	SeverityError Severity = "ERROR"
	// SeverityWarn is recorded per row; the file continues.
	SeverityWarn Severity = "WARN"
)

// CountRule is the reconciliation.count control (A§37).
type CountRule string

// CountDeclaredEqualsParsed requires the declared record count to equal the
// number of parsed DATA rows.
const CountDeclaredEqualsParsed CountRule = "declared_equals_parsed"

// AmountSource is the declared side of the reconciliation.amount control.
type AmountSource string

// AmountVsTrailerControlTotal compares the summed column against the trailer's
// control total.
const AmountVsTrailerControlTotal AmountSource = "trailer_control_total"

// Column is one declared field of a DATA record. Position in Feed.Columns is the
// physical field position in the line (after the leading DATA field).
//
// Every field is read-only after Load: the compiled pattern and the CEL programs
// are derived from them.
type Column struct {
	Name        string
	Type        ColumnType
	Required    bool
	Description string

	// Pattern applies to TypeString only. The whole field must match.
	Pattern string
	// Enum is the closed vocabulary of a TypeEnum column.
	Enum []string
	// Scale is the exact maximum number of decimal places of a TypeDecimal
	// column (2 for money).
	Scale int
	// Min and Max are inclusive bounds for TypeInteger and TypeDecimal.
	Min *decimal.Decimal
	Max *decimal.Decimal

	pattern *regexp.Regexp
}

// Rule is a business rule: a CEL predicate over the row's columns that is true
// for valid data (SPEC.md §6).
type Rule struct {
	ID          string
	Expr        string
	Severity    Severity
	Description string

	program cel.Program
}

// SLA is the arrival window of the feed (D§81). Both times are UTC wall times,
// HH:MM.
type SLA struct {
	ExpectedBy string
	LateBy     string
	Timezone   string
}

// AmountRecon is the amount half of the reconciliation controls.
type AmountRecon struct {
	Column string
	Vs     AmountSource
}

// Reconciliation is the explicit control set for the feed: pipeline success is
// never evidence that the data is correct (D§38).
type Reconciliation struct {
	Count  CountRule
	Amount AmountRecon
}

// Feed is a loaded, validated file-feed contract. Construct it only with Load or
// LoadAll: a zero Feed has no compiled rules and its validation methods return
// ErrNotLoaded rather than silently accepting everything.
type Feed struct {
	// Path is the contract path Load read it from, for error messages.
	Path string

	FeedID      string
	Version     int
	SourceID    string
	Description string

	FilenameRegex string
	Encoding      string
	Format        string

	// FeedCode is the literal that must appear as field 2 of the HEADER
	// record. Source systems use their own short codes, so it is declared,
	// not derived from FeedID.
	FeedCode      string
	HeaderFields  []string
	TrailerFields []string

	// ControlTotalColumn is the decimal column whose sum the trailer
	// declares.
	ControlTotalColumn string

	Columns        []Column
	Rules          []Rule
	SLA            SLA
	Reconciliation Reconciliation

	filenameRE *regexp.Regexp
	byName     map[string]int
	ctIndex    int
	loaded     bool
}

// Column returns the named column and whether it exists.
func (f *Feed) Column(name string) (Column, bool) {
	if f == nil || f.byName == nil {
		return Column{}, false
	}
	i, ok := f.byName[name]
	if !ok {
		return Column{}, false
	}
	return f.Columns[i], true
}

// ControlTotalScale is the scale (decimal places) the control total is compared
// at — always the scale of the control-total column, 2 for money.
func (f *Feed) ControlTotalScale() int32 {
	return int32(f.Columns[f.ctIndex].Scale) //nolint:gosec // scale is validated 0..9 at load
}

// FieldCount is the number of fields a well-formed DATA line has: the leading
// DATA marker plus one field per column.
func (f *Feed) FieldCount() int { return len(f.Columns) + 1 }

// MatchFilename applies filename_regex to a file's base name and returns the
// captured business_date (YYYYMMDD). ok is false when the name does not match in
// full, or when the captured date is not eight digits.
//
// It is deliberately name-only: the caller (ING-4) knows the directory it
// listed, and a contract must never depend on the layout of an SFTP chroot.
func (f *Feed) MatchFilename(name string) (businessDate string, ok bool) {
	if f == nil || f.filenameRE == nil {
		return "", false
	}
	m := f.filenameRE.FindStringSubmatchIndex(name)
	if m == nil || m[0] != 0 || m[1] != len(name) {
		return "", false
	}
	for i, sub := range f.filenameRE.SubexpNames() {
		if sub != businessDateGroup {
			continue
		}
		start, end := m[2*i], m[2*i+1]
		if start < 0 {
			return "", false
		}
		got := name[start:end]
		if !businessDatePattern.MatchString(got) {
			return "", false
		}
		return got, true
	}
	return "", false
}

var businessDatePattern = regexp.MustCompile(`^\d{8}$`)
