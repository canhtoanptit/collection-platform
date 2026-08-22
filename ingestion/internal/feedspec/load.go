package feedspec

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
	"gopkg.in/yaml.v3"
)

// businessDateGroup is the named capture group every filename_regex must
// declare, and the only way a file's business date is derived (SPEC.md §2).
const businessDateGroup = "business_date"

// requiredBusinessDateGroup is the exact spelling a contract must use, so that
// every feed's business date is captured identically and the regex stored on the
// feed row (ING-1) is comparable across feeds.
const requiredBusinessDateGroup = `(?P<business_date>\d{8})`

// Accepted values of the format-fixing keys. v1 supports exactly one of each:
// widening them is a contract change, not a configuration change.
const (
	encodingUTF8   = "UTF-8"
	formatRFC4180  = "RFC4180"
	timezoneUTC    = "UTC"
	maxColumnScale = 9
)

var (
	feedIDPattern   = regexp.MustCompile(`^[a-z][a-z0-9_]{2,63}$`)
	sourceIDPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{2,63}$`)
	feedCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,31}$`)
	columnPattern   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	ruleIDPattern   = regexp.MustCompile(`^[a-z][a-z0-9_]{2,63}$`)
	hhmmPattern     = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
	contractPattern = regexp.MustCompile(`^([a-z][a-z0-9_]*)\.v([0-9]+)\.ya?ml$`)
)

// wantHeaderFields and wantTrailerFields are the fixed D§21 line shapes. The
// contract restates them so a reader of the YAML sees the file layout, but they
// are not configurable: header/trailer parsing is code, not data.
var (
	wantHeaderFields  = []string{"feed_code", "business_date", "record_count"}
	wantTrailerFields = []string{"record_count", "control_total"}
)

// celReserved are the CEL keywords a column may not be named, because column
// names become rule variables.
var celReserved = map[string]bool{
	"as": true, "break": true, "const": true, "continue": true, "else": true,
	"false": true, "for": true, "function": true, "if": true, "import": true,
	"in": true, "let": true, "loop": true, "namespace": true, "null": true,
	"package": true, "return": true, "true": true, "var": true, "void": true,
	"while": true,
}

// Load reads and validates one file-feed contract from fsys and compiles its
// business rules. Every meta-schema violation in SPEC.md is an error here, at
// start-up, rather than a surprise on row 900,000: unknown keys, a
// filename_regex without the business_date group, a decimal column without a
// scale, a control-total column that is not a decimal column, a rule that does
// not compile or does not return bool.
//
// fsys is typically the contracts module's embed.FS or an os.DirFS over
// contracts/files. This package never opens a path of its own.
func Load(fsys fs.FS, name string) (*Feed, error) {
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, fmt.Errorf("feedspec: reading contract %s: %w", name, err)
	}

	var y feedYAML
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&y); err != nil {
		return nil, fmt.Errorf("feedspec: parsing contract %s: %w", name, err)
	}

	f, err := y.feed(name)
	if err != nil {
		return nil, fmt.Errorf("feedspec: invalid contract %s: %w", name, err)
	}
	if err := compileRules(f); err != nil {
		return nil, fmt.Errorf("feedspec: invalid contract %s: %w", name, err)
	}
	f.loaded = true
	return f, nil
}

// LoadAll loads every *.v<N>.yaml contract directly under dir, keyed by feed_id.
// It fails on the first invalid contract — a partially loaded contract set would
// let the pipeline start with a feed it cannot validate. Duplicate feed ids
// across files (loan_accounts.v1.yaml and a copy) are an error too.
func LoadAll(fsys fs.FS, dir string) (map[string]*Feed, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("feedspec: reading contract directory %s: %w", dir, err)
	}
	feeds := make(map[string]*Feed)
	for _, e := range entries {
		if e.IsDir() || !contractPattern.MatchString(e.Name()) {
			continue
		}
		f, err := Load(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		if prev, dup := feeds[f.FeedID]; dup {
			return nil, fmt.Errorf("feedspec: duplicate feed_id %q in %s and %s", f.FeedID, prev.Path, f.Path)
		}
		feeds[f.FeedID] = f
	}
	if len(feeds) == 0 {
		return nil, fmt.Errorf("feedspec: no contracts found in %s", dir)
	}
	return feeds, nil
}

// ---------------------------------------------------------------- YAML shapes

type feedYAML struct {
	FeedID        string       `yaml:"feed_id"`
	Version       int          `yaml:"version"`
	SourceID      string       `yaml:"source_id"`
	Description   string       `yaml:"description"`
	FilenameRegex string       `yaml:"filename_regex"`
	Encoding      string       `yaml:"encoding"`
	Format        string       `yaml:"format"`
	Header        headerYAML   `yaml:"header"`
	Trailer       trailerYAML  `yaml:"trailer"`
	Columns       []columnYAML `yaml:"columns"`
	BusinessRules []ruleYAML   `yaml:"business_rules"`
	SLA           slaYAML      `yaml:"sla"`
	Recon         reconYAML    `yaml:"reconciliation"`
}

type headerYAML struct {
	FeedCode string   `yaml:"feed_code"`
	Fields   []string `yaml:"fields"`
}

type trailerYAML struct {
	Fields             []string `yaml:"fields"`
	ControlTotalColumn string   `yaml:"control_total_column"`
}

type columnYAML struct {
	Name        string   `yaml:"name"`
	Type        string   `yaml:"type"`
	Required    *bool    `yaml:"required"`
	Description string   `yaml:"description"`
	Pattern     string   `yaml:"pattern"`
	Enum        []string `yaml:"enum"`
	Scale       *int     `yaml:"scale"`
	Min         *number  `yaml:"min"`
	Max         *number  `yaml:"max"`
}

type ruleYAML struct {
	ID          string `yaml:"id"`
	Expr        string `yaml:"expr"`
	Severity    string `yaml:"severity"`
	Description string `yaml:"description"`
}

type slaYAML struct {
	ExpectedBy string `yaml:"expected_by"`
	LateBy     string `yaml:"late_by"`
	Timezone   string `yaml:"timezone"`
}

type reconYAML struct {
	Count  string          `yaml:"count"`
	Amount amountReconYAML `yaml:"amount"`
}

type amountReconYAML struct {
	Column string `yaml:"column"`
	Vs     string `yaml:"vs"`
}

// number reads a numeric bound from its literal YAML text, so 0.01 is exactly
// one cent and never an IEEE-754 approximation of one cent.
type number struct{ decimal.Decimal }

func (n *number) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("expected a number, got %s", nodeKind(node.Kind))
	}
	d, err := decimal.NewFromString(node.Value)
	if err != nil {
		return fmt.Errorf("%q is not a decimal number", node.Value)
	}
	n.Decimal = d
	return nil
}

func nodeKind(k yaml.Kind) string {
	switch k {
	case yaml.DocumentNode:
		return "document"
	case yaml.SequenceNode:
		return "sequence"
	case yaml.MappingNode:
		return "mapping"
	case yaml.ScalarNode:
		return "scalar"
	case yaml.AliasNode:
		return "alias"
	default:
		return "unknown"
	}
}

// ------------------------------------------------------------ meta validation

// feed validates the decoded YAML against SPEC.md and builds the Feed. Every
// problem is collected (errors.Join) rather than only the first, because a
// contract author fixing one field at a time is a slow feedback loop.
func (y *feedYAML) feed(contractPath string) (*Feed, error) {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	f := &Feed{
		Path:               contractPath,
		FeedID:             y.FeedID,
		Version:            y.Version,
		SourceID:           y.SourceID,
		Description:        y.Description,
		FilenameRegex:      y.FilenameRegex,
		Encoding:           y.Encoding,
		Format:             y.Format,
		FeedCode:           y.Header.FeedCode,
		HeaderFields:       y.Header.Fields,
		TrailerFields:      y.Trailer.Fields,
		ControlTotalColumn: y.Trailer.ControlTotalColumn,
		SLA:                SLA{ExpectedBy: y.SLA.ExpectedBy, LateBy: y.SLA.LateBy, Timezone: y.SLA.Timezone},
		Reconciliation: Reconciliation{
			Count:  CountRule(y.Recon.Count),
			Amount: AmountRecon{Column: y.Recon.Amount.Column, Vs: AmountSource(y.Recon.Amount.Vs)},
		},
		ctIndex: -1,
	}

	// identity
	if !feedIDPattern.MatchString(y.FeedID) {
		bad("feed_id %q must match %s", y.FeedID, feedIDPattern)
	}
	if y.Version < 1 {
		bad("version must be >= 1, got %d", y.Version)
	}
	if !sourceIDPattern.MatchString(y.SourceID) {
		bad("source_id %q must match %s", y.SourceID, sourceIDPattern)
	}
	if m := contractPattern.FindStringSubmatch(path.Base(contractPath)); m != nil {
		if m[1] != y.FeedID {
			bad("feed_id %q does not match file name stem %q", y.FeedID, m[1])
		}
		if v, err := strconv.Atoi(m[2]); err == nil && v != y.Version {
			bad("version %d does not match file name version v%d", y.Version, v)
		}
	} else {
		bad("file name %q must be <feed_id>.v<N>.yaml", path.Base(contractPath))
	}

	// physical format
	switch {
	case y.FilenameRegex == "":
		bad("filename_regex is required")
	case !strings.Contains(y.FilenameRegex, requiredBusinessDateGroup):
		bad("filename_regex must contain the named group %s", requiredBusinessDateGroup)
	default:
		re, err := regexp.Compile(y.FilenameRegex)
		switch {
		case err != nil:
			bad("filename_regex does not compile: %v", err)
		case !hasGroup(re, businessDateGroup):
			bad("filename_regex must declare the named group %q", businessDateGroup)
		default:
			f.filenameRE = re
		}
	}
	if y.Encoding != encodingUTF8 {
		bad("encoding must be %q, got %q", encodingUTF8, y.Encoding)
	}
	if y.Format != formatRFC4180 {
		bad("format must be %q, got %q", formatRFC4180, y.Format)
	}

	// header / trailer
	if !feedCodePattern.MatchString(y.Header.FeedCode) {
		bad("header.feed_code %q must match %s", y.Header.FeedCode, feedCodePattern)
	}
	if !equalStrings(y.Header.Fields, wantHeaderFields) {
		bad("header.fields must be exactly %v, got %v", wantHeaderFields, y.Header.Fields)
	}
	if !equalStrings(y.Trailer.Fields, wantTrailerFields) {
		bad("trailer.fields must be exactly %v, got %v", wantTrailerFields, y.Trailer.Fields)
	}

	// columns
	if len(y.Columns) == 0 {
		bad("columns must declare at least one column")
	}
	f.byName = make(map[string]int, len(y.Columns))
	for i, cy := range y.Columns {
		col, cerrs := cy.column(i)
		errs = append(errs, cerrs...)
		if _, dup := f.byName[col.Name]; dup && col.Name != "" {
			bad("columns[%d]: duplicate column name %q", i, col.Name)
			continue
		}
		f.byName[col.Name] = len(f.Columns)
		f.Columns = append(f.Columns, col)
	}

	if y.Trailer.ControlTotalColumn == "" {
		bad("trailer.control_total_column is required")
	} else if i, ok := f.byName[y.Trailer.ControlTotalColumn]; !ok {
		bad("trailer.control_total_column %q is not a declared column", y.Trailer.ControlTotalColumn)
	} else if f.Columns[i].Type != TypeDecimal {
		bad("trailer.control_total_column %q must be a decimal column, got %s", y.Trailer.ControlTotalColumn, f.Columns[i].Type)
	} else {
		f.ctIndex = i
	}

	// business rules (compiled separately, once the columns are known)
	seenRule := map[string]bool{}
	for i, ry := range y.BusinessRules {
		if !ruleIDPattern.MatchString(ry.ID) {
			bad("business_rules[%d].id %q must match %s", i, ry.ID, ruleIDPattern)
		}
		if seenRule[ry.ID] {
			bad("business_rules[%d]: duplicate rule id %q", i, ry.ID)
		}
		seenRule[ry.ID] = true
		if strings.TrimSpace(ry.Expr) == "" {
			bad("business_rules[%d] (%s): expr is required", i, ry.ID)
		}
		sev := Severity(ry.Severity)
		if sev != SeverityError && sev != SeverityWarn {
			bad("business_rules[%d] (%s): severity must be ERROR or WARN, got %q", i, ry.ID, ry.Severity)
		}
		f.Rules = append(f.Rules, Rule{ID: ry.ID, Expr: ry.Expr, Severity: sev, Description: ry.Description})
	}

	// sla
	if !hhmmPattern.MatchString(y.SLA.ExpectedBy) {
		bad("sla.expected_by %q must be HH:MM", y.SLA.ExpectedBy)
	}
	if !hhmmPattern.MatchString(y.SLA.LateBy) {
		bad("sla.late_by %q must be HH:MM", y.SLA.LateBy)
	}
	if hhmmPattern.MatchString(y.SLA.ExpectedBy) && hhmmPattern.MatchString(y.SLA.LateBy) &&
		y.SLA.LateBy <= y.SLA.ExpectedBy {
		bad("sla.late_by %s must be after sla.expected_by %s", y.SLA.LateBy, y.SLA.ExpectedBy)
	}
	if y.SLA.Timezone != timezoneUTC {
		bad("sla.timezone must be %q, got %q", timezoneUTC, y.SLA.Timezone)
	}

	// reconciliation
	if CountRule(y.Recon.Count) != CountDeclaredEqualsParsed {
		bad("reconciliation.count must be %q, got %q", CountDeclaredEqualsParsed, y.Recon.Count)
	}
	if AmountSource(y.Recon.Amount.Vs) != AmountVsTrailerControlTotal {
		bad("reconciliation.amount.vs must be %q, got %q", AmountVsTrailerControlTotal, y.Recon.Amount.Vs)
	}
	if y.Recon.Amount.Column != y.Trailer.ControlTotalColumn {
		bad("reconciliation.amount.column %q must be the trailer control_total_column %q — a contract that reconciles a different column than it totals is inconsistent",
			y.Recon.Amount.Column, y.Trailer.ControlTotalColumn)
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return f, nil
}

// column validates one columns[] entry.
func (cy columnYAML) column(i int) (Column, []error) {
	var errs []error
	bad := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("columns[%d] (%s): "+format, append([]any{i, cy.Name}, args...)...))
	}

	col := Column{
		Name:        cy.Name,
		Type:        ColumnType(cy.Type),
		Description: cy.Description,
		Pattern:     cy.Pattern,
		Enum:        cy.Enum,
	}
	if !columnPattern.MatchString(cy.Name) {
		bad("name must match %s", columnPattern)
	}
	if celReserved[cy.Name] {
		bad("name is a CEL reserved word and cannot be a rule variable")
	}
	if cy.Required == nil {
		bad("required must be stated explicitly (true or false)")
	} else {
		col.Required = *cy.Required
	}

	switch col.Type {
	case TypeString:
		if len(cy.Enum) > 0 {
			bad("enum is only valid for type enum")
		}
		if cy.Pattern != "" {
			re, err := regexp.Compile(cy.Pattern)
			if err != nil {
				bad("pattern does not compile: %v", err)
			} else {
				col.pattern = re
			}
		}
	case TypeEnum:
		if len(cy.Enum) == 0 {
			bad("enum must declare at least one value")
		}
		seen := map[string]bool{}
		for _, v := range cy.Enum {
			if v == "" {
				bad("enum values must be non-empty")
			}
			if seen[v] {
				bad("duplicate enum value %q", v)
			}
			seen[v] = true
		}
		if cy.Pattern != "" {
			bad("pattern is only valid for type string")
		}
	case TypeInteger, TypeDate:
		if cy.Pattern != "" {
			bad("pattern is only valid for type string")
		}
		if len(cy.Enum) > 0 {
			bad("enum is only valid for type enum")
		}
	case TypeDecimal:
		if cy.Pattern != "" {
			bad("pattern is only valid for type string")
		}
		if len(cy.Enum) > 0 {
			bad("enum is only valid for type enum")
		}
	default:
		bad("type %q must be one of string, integer, decimal, date_yyyymmdd, enum", cy.Type)
	}

	switch col.Type {
	case TypeDecimal:
		switch {
		case cy.Scale == nil:
			bad("scale is required for a decimal column")
		case *cy.Scale < 0 || *cy.Scale > maxColumnScale:
			bad("scale must be between 0 and %d, got %d", maxColumnScale, *cy.Scale)
		default:
			col.Scale = *cy.Scale
		}
	case TypeString, TypeInteger, TypeDate, TypeEnum:
		if cy.Scale != nil {
			bad("scale is only valid for type decimal")
		}
	default:
		// type already reported above.
	}

	switch col.Type {
	case TypeInteger, TypeDecimal:
		if cy.Min != nil {
			m := cy.Min.Decimal
			col.Min = &m
		}
		if cy.Max != nil {
			m := cy.Max.Decimal
			col.Max = &m
		}
		if col.Min != nil && col.Max != nil && col.Max.LessThan(*col.Min) {
			bad("max %s must be >= min %s", col.Max, col.Min)
		}
		for _, bound := range []struct {
			name string
			val  *decimal.Decimal
		}{{"min", col.Min}, {"max", col.Max}} {
			name, b := bound.name, bound.val
			if b == nil {
				continue
			}
			if col.Type == TypeInteger && !b.Equal(b.Truncate(0)) {
				bad("%s %s must be a whole number for an integer column", name, b)
			}
			if col.Type == TypeDecimal && -b.Exponent() > int32(col.Scale) { //nolint:gosec // scale is validated 0..9
				bad("%s %s has more decimal places than scale %d", name, b, col.Scale)
			}
		}
	case TypeString, TypeDate, TypeEnum:
		if cy.Min != nil || cy.Max != nil {
			bad("min/max are only valid for integer and decimal columns")
		}
	default:
		// type already reported above.
	}

	return col, errs
}

func hasGroup(re *regexp.Regexp, name string) bool {
	for _, n := range re.SubexpNames() {
		if n == name {
			return true
		}
	}
	return false
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
