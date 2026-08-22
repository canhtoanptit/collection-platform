package feedspec_test

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/canhtoanptit/collection-platform/ingestion/internal/feedspec"
)

// TestLoadRealContracts pins the four shipped contracts: their identity, the
// header code a source system must send, the physical column order (which is the
// DATA field order and therefore breaking to change), the control-total column
// and the rule set with its severities.
func TestLoadRealContracts(t *testing.T) {
	t.Parallel()

	type ruleWant struct {
		id       string
		severity feedspec.Severity
	}
	tests := []struct {
		feedID       string
		sourceID     string
		feedCode     string
		columns      []string
		required     []string // columns that must be optional are listed in optional
		optional     []string
		controlTotal string
		rules        []ruleWant
		expectedBy   string
		lateBy       string
	}{
		{
			feedID:   "loan_accounts",
			sourceID: "COREBANK_SFTP",
			feedCode: "LOAN_ACCOUNTS",
			columns: []string{
				"acct_no", "cust_no", "prod_cd", "open_dt", "status_cd",
				"curr_bal", "od_amt", "min_due", "last_pay_dt", "oldest_unpaid_dt",
			},
			optional:     []string{"last_pay_dt", "oldest_unpaid_dt"},
			controlTotal: "curr_bal",
			rules: []ruleWant{
				{"od_amt_within_balance", feedspec.SeverityError},
				{"min_due_within_balance", feedspec.SeverityWarn},
			},
			expectedBy: "02:00", lateBy: "03:00",
		},
		{
			feedID:       "payments",
			sourceID:     "COREBANK_SFTP",
			feedCode:     "PAYMENTS",
			columns:      []string{"pay_id", "acct_no", "pay_dt", "amount", "channel_cd", "reversed_flag"},
			controlTotal: "amount",
			rules:        []ruleWant{{"amount_positive", feedspec.SeverityError}},
			expectedBy:   "02:00", lateBy: "03:00",
		},
		{
			feedID:       "delinquency_snapshot",
			sourceID:     "COREBANK_SFTP",
			feedCode:     "DELQ_SNAPSHOT",
			columns:      []string{"acct_no", "as_of_dt", "dpd", "bucket_cd", "od_amt"},
			controlTotal: "od_amt",
			rules:        []ruleWant{{"dpd_non_negative", feedspec.SeverityError}},
			expectedBy:   "02:00", lateBy: "03:00",
		},
		{
			feedID:       "legacy_daily_summary",
			sourceID:     "LEGACY_MI_S3",
			feedCode:     "LEGACY_DAILY_SUMMARY",
			columns:      []string{"report_dt", "bucket_cd", "account_count", "total_overdue", "total_balance"},
			controlTotal: "total_overdue",
			rules:        []ruleWant{{"overdue_within_balance", feedspec.SeverityWarn}},
			expectedBy:   "03:00", lateBy: "04:00",
		},
	}

	for _, tc := range tests {
		t.Run(tc.feedID, func(t *testing.T) {
			t.Parallel()
			f := loadFeed(t, tc.feedID)

			if f.FeedID != tc.feedID {
				t.Errorf("feed_id = %q, want %q", f.FeedID, tc.feedID)
			}
			if f.Version != 1 {
				t.Errorf("version = %d, want 1", f.Version)
			}
			if f.SourceID != tc.sourceID {
				t.Errorf("source_id = %q, want %q", f.SourceID, tc.sourceID)
			}
			if f.FeedCode != tc.feedCode {
				t.Errorf("header.feed_code = %q, want %q", f.FeedCode, tc.feedCode)
			}
			if f.Encoding != "UTF-8" || f.Format != "RFC4180" {
				t.Errorf("encoding/format = %q/%q, want UTF-8/RFC4180", f.Encoding, f.Format)
			}

			var got []string
			for _, c := range f.Columns {
				got = append(got, c.Name)
			}
			if strings.Join(got, ",") != strings.Join(tc.columns, ",") {
				t.Errorf("column order = %v, want %v", got, tc.columns)
			}
			if f.FieldCount() != len(tc.columns)+1 {
				t.Errorf("FieldCount() = %d, want %d", f.FieldCount(), len(tc.columns)+1)
			}

			optional := map[string]bool{}
			for _, n := range tc.optional {
				optional[n] = true
			}
			for _, c := range f.Columns {
				if c.Required == optional[c.Name] {
					t.Errorf("column %s: required = %v, want %v", c.Name, c.Required, !optional[c.Name])
				}
			}

			if f.ControlTotalColumn != tc.controlTotal {
				t.Errorf("control_total_column = %q, want %q", f.ControlTotalColumn, tc.controlTotal)
			}
			ct, ok := f.Column(tc.controlTotal)
			if !ok || ct.Type != feedspec.TypeDecimal || ct.Scale != 2 {
				t.Errorf("control-total column %s must be a decimal of scale 2, got %+v (found=%v)", tc.controlTotal, ct, ok)
			}
			if f.ControlTotalScale() != 2 {
				t.Errorf("ControlTotalScale() = %d, want 2", f.ControlTotalScale())
			}
			if f.Reconciliation.Count != feedspec.CountDeclaredEqualsParsed {
				t.Errorf("reconciliation.count = %q", f.Reconciliation.Count)
			}
			if f.Reconciliation.Amount.Column != tc.controlTotal ||
				f.Reconciliation.Amount.Vs != feedspec.AmountVsTrailerControlTotal {
				t.Errorf("reconciliation.amount = %+v", f.Reconciliation.Amount)
			}

			if len(f.Rules) != len(tc.rules) {
				t.Fatalf("rules = %d, want %d", len(f.Rules), len(tc.rules))
			}
			for i, want := range tc.rules {
				if f.Rules[i].ID != want.id || f.Rules[i].Severity != want.severity {
					t.Errorf("rule[%d] = %s/%s, want %s/%s", i,
						f.Rules[i].ID, f.Rules[i].Severity, want.id, want.severity)
				}
			}

			if f.SLA.ExpectedBy != tc.expectedBy || f.SLA.LateBy != tc.lateBy || f.SLA.Timezone != "UTC" {
				t.Errorf("sla = %+v, want %s/%s/UTC", f.SLA, tc.expectedBy, tc.lateBy)
			}
		})
	}
}

// TestLoadAllRealContracts proves the whole shipped contract set loads together,
// which is what the control plane's bootstrap and the pipeline worker do at
// start-up.
func TestLoadAllRealContracts(t *testing.T) {
	t.Parallel()

	feeds, err := feedspec.LoadAll(contractsFS(t), ".")
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	want := []string{"delinquency_snapshot", "legacy_daily_summary", "loan_accounts", "payments"}
	if len(feeds) != len(want) {
		t.Fatalf("LoadAll returned %d feeds (%v), want %d", len(feeds), feeds, len(want))
	}
	for _, id := range want {
		f, ok := feeds[id]
		if !ok {
			t.Fatalf("LoadAll is missing feed %s", id)
		}
		if f.FeedID != id {
			t.Errorf("feeds[%q].FeedID = %q", id, f.FeedID)
		}
	}
}

func TestLoadAllRejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fsys    fs.FS
		wantErr string
	}{
		{
			name:    "missing directory",
			fsys:    fstest.MapFS{},
			wantErr: "reading contract directory",
		},
		{
			name:    "no contracts",
			fsys:    fstest.MapFS{"files/README.md": &fstest.MapFile{Data: []byte("# files")}},
			wantErr: "no contracts found",
		},
		{
			name: "invalid contract in the set",
			fsys: fstest.MapFS{
				"files/broken.v1.yaml": &fstest.MapFile{Data: []byte("feed_id: broken\n")},
			},
			wantErr: "invalid contract",
		},
		{
			name: "duplicate feed id",
			fsys: fstest.MapFS{
				"files/loan_accounts.v1.yaml": &fstest.MapFile{Data: []byte(baseContract)},
				"files/loan_accounts.v2.yaml": &fstest.MapFile{
					Data: []byte(strings.Replace(baseContract, "version: 1", "version: 2", 1)),
				},
			},
			wantErr: "duplicate feed_id",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := feedspec.LoadAll(tc.fsys, "files")
			if err == nil {
				t.Fatalf("LoadAll succeeded, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("LoadAll error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// baseContract is a minimal valid contract used as the mutation base for the
// meta-schema rejection table. It is deliberately not one of the shipped
// contracts: those are pinned by TestLoadRealContracts.
const baseContract = `feed_id: loan_accounts
version: 1
source_id: COREBANK_SFTP
filename_regex: '^loan_accounts_(?P<business_date>\d{8})\.csv$'
encoding: UTF-8
format: RFC4180
header:
  feed_code: LOAN_ACCOUNTS
  fields: [feed_code, business_date, record_count]
trailer:
  fields: [record_count, control_total]
  control_total_column: curr_bal
columns:
  - name: acct_no
    type: string
    required: true
    pattern: '^[A-Z0-9]{6,12}$'
  - name: prod_cd
    type: enum
    required: true
    enum: [LOAN, CARD]
  - name: open_dt
    type: date_yyyymmdd
    required: false
  - name: dpd
    type: integer
    required: true
    min: 0
    max: 3650
  - name: curr_bal
    type: decimal
    required: true
    scale: 2
  - name: od_amt
    type: decimal
    required: true
    scale: 2
    min: 0.00
business_rules:
  - id: od_amt_within_balance
    expr: od_amt <= curr_bal
    severity: ERROR
sla:
  expected_by: "02:00"
  late_by: "03:00"
  timezone: UTC
reconciliation:
  count: declared_equals_parsed
  amount:
    column: curr_bal
    vs: trailer_control_total
`

// TestLoadAcceptsBaseContract guards the mutation base itself: every rejection
// case below is only meaningful if the unmutated contract loads.
func TestLoadAcceptsBaseContract(t *testing.T) {
	t.Parallel()

	f, err := feedspec.Load(contractFS(baseContract), "loan_accounts.v1.yaml")
	if err != nil {
		t.Fatalf("Load(base contract): %v", err)
	}
	if got := len(f.Columns); got != 6 {
		t.Errorf("columns = %d, want 6", got)
	}
	if f.Path != "loan_accounts.v1.yaml" {
		t.Errorf("Path = %q", f.Path)
	}
}

func contractFS(body string) fs.FS {
	return inlineFS("loan_accounts.v1.yaml", body)
}

func inlineFS(name, body string) fs.FS {
	return fstest.MapFS{name: &fstest.MapFile{Data: []byte(body)}}
}

// TestLoadRejectsInvalidContracts is the meta-schema enforcement table: every
// MUST in contracts/files/SPEC.md fails at Load, with a message that names the
// offending field.
func TestLoadRejectsInvalidContracts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		edits   []edit
		name2   string // optional: load under a different file name
		wantErr string
	}{
		{
			name:    "unknown key",
			edits:   []edit{{"encoding: UTF-8", "encoding: UTF-8\nfilename_regexp: nope"}},
			wantErr: "field filename_regexp not found",
		},
		{
			name:    "feed_id pattern",
			edits:   []edit{{"feed_id: loan_accounts", "feed_id: LoanAccounts"}},
			wantErr: "feed_id \"LoanAccounts\" must match",
		},
		{
			name:    "version below one",
			edits:   []edit{{"version: 1", "version: 0"}},
			wantErr: "version must be >= 1",
		},
		{
			name:    "source_id pattern",
			edits:   []edit{{"source_id: COREBANK_SFTP", "source_id: corebank"}},
			wantErr: "source_id \"corebank\" must match",
		},
		{
			name:    "feed_id disagrees with the file name",
			edits:   []edit{{"feed_id: loan_accounts", "feed_id: payments"}},
			wantErr: "does not match file name stem",
		},
		{
			name:    "version disagrees with the file name",
			edits:   []edit{{"version: 1", "version: 2"}},
			wantErr: "does not match file name version",
		},
		{
			name:    "file name is not <feed_id>.v<N>.yaml",
			name2:   "loan_accounts.yaml",
			wantErr: "must be <feed_id>.v<N>.yaml",
		},
		{
			name:    "filename_regex without the business_date group",
			edits:   []edit{{`'^loan_accounts_(?P<business_date>\d{8})\.csv$'`, `'^loan_accounts_\d{8}\.csv$'`}},
			wantErr: "must contain the named group",
		},
		{
			name:    "filename_regex with a differently spelled date group",
			edits:   []edit{{`(?P<business_date>\d{8})`, `(?P<business_date>[0-9]{8})`}},
			wantErr: "must contain the named group",
		},
		{
			name:    "filename_regex without a closing group",
			edits:   []edit{{`'^loan_accounts_(?P<business_date>\d{8})\.csv$'`, `'^loan_accounts_(?P<business_date>\d{8}\.csv$'`}},
			wantErr: "must contain the named group",
		},
		{
			name:    "filename_regex does not compile",
			edits:   []edit{{`\.csv$'`, `\.csv[$'`}},
			wantErr: "filename_regex does not compile",
		},
		{
			name:    "encoding",
			edits:   []edit{{"encoding: UTF-8", "encoding: ISO-8859-1"}},
			wantErr: `encoding must be "UTF-8"`,
		},
		{
			name:    "format",
			edits:   []edit{{"format: RFC4180", "format: PIPE_DELIMITED"}},
			wantErr: `format must be "RFC4180"`,
		},
		{
			name:    "header feed_code pattern",
			edits:   []edit{{"feed_code: LOAN_ACCOUNTS", "feed_code: loan"}},
			wantErr: "header.feed_code \"loan\" must match",
		},
		{
			name:    "header fields",
			edits:   []edit{{"fields: [feed_code, business_date, record_count]", "fields: [feed_code, record_count]"}},
			wantErr: "header.fields must be exactly",
		},
		{
			name:    "trailer fields",
			edits:   []edit{{"fields: [record_count, control_total]", "fields: [control_total, record_count]"}},
			wantErr: "trailer.fields must be exactly",
		},
		{
			name:    "control_total_column is not a column",
			edits:   []edit{{"control_total_column: curr_bal", "control_total_column: balance"}},
			wantErr: "is not a declared column",
		},
		{
			name: "control_total_column is not a decimal",
			edits: []edit{
				{"control_total_column: curr_bal", "control_total_column: acct_no"},
				{"column: curr_bal", "column: acct_no"},
			},
			wantErr: "must be a decimal column",
		},
		{
			name:    "no columns",
			edits:   []edit{{"columns:", "columns: []\nunused:"}},
			wantErr: "field unused not found",
		},
		{
			name:    "column name pattern",
			edits:   []edit{{"name: acct_no", "name: AcctNo"}},
			wantErr: "columns[0] (AcctNo): name must match",
		},
		{
			name:    "column named after a CEL keyword",
			edits:   []edit{{"name: acct_no", "name: in"}},
			wantErr: "CEL reserved word",
		},
		{
			name:    "duplicate column name",
			edits:   []edit{{"name: prod_cd", "name: acct_no"}},
			wantErr: "duplicate column name",
		},
		{
			name:    "required not stated",
			edits:   []edit{{"    type: string\n    required: true\n", "    type: string\n"}},
			wantErr: "required must be stated explicitly",
		},
		{
			name:    "unknown type",
			edits:   []edit{{"type: string", "type: money"}},
			wantErr: "type \"money\" must be one of",
		},
		{
			name:    "decimal without scale",
			edits:   []edit{{"    type: decimal\n    required: true\n    scale: 2\n", "    type: decimal\n    required: true\n"}},
			wantErr: "scale is required for a decimal column",
		},
		{
			name:    "scale out of range",
			edits:   []edit{{"scale: 2", "scale: 12"}},
			wantErr: "scale must be between 0 and 9",
		},
		{
			name:    "scale on a non-decimal column",
			edits:   []edit{{"    type: string\n    required: true\n", "    type: string\n    required: true\n    scale: 2\n"}},
			wantErr: "scale is only valid for type decimal",
		},
		{
			name:    "pattern on a non-string column",
			edits:   []edit{{"    type: integer\n    required: true\n", "    type: integer\n    required: true\n    pattern: '^\\d+$'\n"}},
			wantErr: "pattern is only valid for type string",
		},
		{
			name:    "pattern does not compile",
			edits:   []edit{{`pattern: '^[A-Z0-9]{6,12}$'`, `pattern: '^[A-Z0-9{6,12}$'`}},
			wantErr: "pattern does not compile",
		},
		{
			name:    "enum without values",
			edits:   []edit{{"    enum: [LOAN, CARD]\n", ""}},
			wantErr: "enum must declare at least one value",
		},
		{
			name:    "duplicate enum value",
			edits:   []edit{{"enum: [LOAN, CARD]", "enum: [LOAN, LOAN]"}},
			wantErr: "duplicate enum value",
		},
		{
			name:    "empty enum value",
			edits:   []edit{{"enum: [LOAN, CARD]", `enum: [LOAN, ""]`}},
			wantErr: "enum values must be non-empty",
		},
		{
			name:    "enum on a non-enum column",
			edits:   []edit{{"    type: string\n    required: true\n", "    type: string\n    required: true\n    enum: [A]\n"}},
			wantErr: "enum is only valid for type enum",
		},
		{
			name:    "max below min",
			edits:   []edit{{"    min: 0\n    max: 3650\n", "    min: 10\n    max: 5\n"}},
			wantErr: "must be >= min",
		},
		{
			name:    "fractional bound on an integer column",
			edits:   []edit{{"    min: 0\n    max: 3650\n", "    min: 0.5\n    max: 3650\n"}},
			wantErr: "must be a whole number for an integer column",
		},
		{
			name:    "bound finer than the column scale",
			edits:   []edit{{"    min: 0.00\n", "    min: 0.001\n"}},
			wantErr: "has more decimal places than scale 2",
		},
		{
			name:    "bound on a string column",
			edits:   []edit{{"    type: string\n    required: true\n", "    type: string\n    required: true\n    min: 1\n"}},
			wantErr: "min/max are only valid for integer and decimal columns",
		},
		{
			name:    "bound is not a number",
			edits:   []edit{{"    min: 0\n", "    min: none\n"}},
			wantErr: `"none" is not a decimal number`,
		},
		{
			name:    "bound is not a scalar",
			edits:   []edit{{"    min: 0\n", "    min: [0]\n"}},
			wantErr: "expected a number, got sequence",
		},
		{
			name:    "rule id pattern",
			edits:   []edit{{"id: od_amt_within_balance", "id: OdAmt"}},
			wantErr: "business_rules[0].id \"OdAmt\" must match",
		},
		{
			name: "duplicate rule id",
			edits: []edit{{
				"  - id: od_amt_within_balance\n    expr: od_amt <= curr_bal\n    severity: ERROR\n",
				"  - id: od_amt_within_balance\n    expr: od_amt <= curr_bal\n    severity: ERROR\n" +
					"  - id: od_amt_within_balance\n    expr: od_amt >= 0\n    severity: WARN\n",
			}},
			wantErr: "duplicate rule id",
		},
		{
			name:    "empty rule expression",
			edits:   []edit{{"expr: od_amt <= curr_bal", `expr: "  "`}},
			wantErr: "expr is required",
		},
		{
			name:    "unknown rule severity",
			edits:   []edit{{"severity: ERROR", "severity: FATAL"}},
			wantErr: "severity must be ERROR or WARN",
		},
		{
			name:    "rule references an unknown column",
			edits:   []edit{{"expr: od_amt <= curr_bal", "expr: od_amt <= balance"}},
			wantErr: "expr does not compile",
		},
		{
			name:    "rule is not boolean",
			edits:   []edit{{"expr: od_amt <= curr_bal", "expr: curr_bal - od_amt"}},
			wantErr: "expr must evaluate to bool",
		},
		{
			name:    "sla expected_by is not HH:MM",
			edits:   []edit{{`expected_by: "02:00"`, `expected_by: "2am"`}},
			wantErr: "sla.expected_by \"2am\" must be HH:MM",
		},
		{
			name:    "sla late_by is not HH:MM",
			edits:   []edit{{`late_by: "03:00"`, `late_by: "25:00"`}},
			wantErr: "sla.late_by \"25:00\" must be HH:MM",
		},
		{
			name:    "sla late_by is not after expected_by",
			edits:   []edit{{`late_by: "03:00"`, `late_by: "01:00"`}},
			wantErr: "must be after sla.expected_by",
		},
		{
			name:    "sla timezone",
			edits:   []edit{{"timezone: UTC", "timezone: Europe/Dublin"}},
			wantErr: `sla.timezone must be "UTC"`,
		},
		{
			name:    "reconciliation count rule",
			edits:   []edit{{"count: declared_equals_parsed", "count: best_effort"}},
			wantErr: "reconciliation.count must be",
		},
		{
			name:    "reconciliation amount source",
			edits:   []edit{{"vs: trailer_control_total", "vs: header_total"}},
			wantErr: "reconciliation.amount.vs must be",
		},
		{
			name:    "reconciliation amount column disagrees with the control total",
			edits:   []edit{{"    column: curr_bal", "    column: od_amt"}},
			wantErr: "must be the trailer control_total_column",
		},
		{
			name:    "not YAML at all",
			edits:   []edit{{"feed_id: loan_accounts", "feed_id: [unclosed"}},
			wantErr: "parsing contract",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := mutate(t, baseContract, tc.edits...)
			name := "loan_accounts.v1.yaml"
			if tc.name2 != "" {
				name = tc.name2
			}
			_, err := feedspec.Load(fstest.MapFS{name: &fstest.MapFile{Data: []byte(body)}}, name)
			if err == nil {
				t.Fatalf("Load succeeded, want an error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Load error =\n%v\nwant it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	t.Parallel()

	_, err := feedspec.Load(fstest.MapFS{}, "nope.v1.yaml")
	if err == nil {
		t.Fatal("Load of a missing contract succeeded")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Load error = %v, want it to wrap fs.ErrNotExist", err)
	}
}

// TestMatchFilename covers the only supported way to derive a business date from
// a file name (ING-4 step 1).
func TestMatchFilename(t *testing.T) {
	t.Parallel()

	tests := []struct {
		feedID string
		name   string
		want   string
	}{
		{"loan_accounts", "loan_accounts_20260822.csv", "20260822"},
		{"loan_accounts", "loan_accounts_20260822.csv.tmp", ""},
		{"loan_accounts", "loan_accounts_2026082.csv", ""},
		{"loan_accounts", "loan_accounts_202608222.csv", ""},
		{"loan_accounts", "LOAN_ACCOUNTS_20260822.CSV", ""},
		{"loan_accounts", "outbound/loan_accounts/loan_accounts_20260822.csv", ""},
		{"loan_accounts", "payments_20260822.csv", ""},
		{"payments", "payments_20260822.csv", "20260822"},
		{"delinquency_snapshot", "delinquency_snapshot_20260101.csv", "20260101"},
		{"legacy_daily_summary", "legacy_daily_summary_20261231.csv", "20261231"},
	}
	for _, tc := range tests {
		t.Run(tc.feedID+"/"+tc.name, func(t *testing.T) {
			t.Parallel()
			f := loadFeed(t, tc.feedID)
			got, ok := f.MatchFilename(tc.name)
			if tc.want == "" {
				if ok {
					t.Fatalf("MatchFilename(%q) matched with %q, want no match", tc.name, got)
				}
				return
			}
			if !ok || got != tc.want {
				t.Fatalf("MatchFilename(%q) = %q, %v; want %q, true", tc.name, got, ok, tc.want)
			}
		})
	}
}

func TestMatchFilenameOnUnloadedFeed(t *testing.T) {
	t.Parallel()

	var f feedspec.Feed
	if _, ok := f.MatchFilename("loan_accounts_20260822.csv"); ok {
		t.Fatal("a zero Feed matched a file name")
	}
}
