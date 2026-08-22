package feedspec_test

import (
	"strings"
	"testing"

	"github.com/canhtoanptit/collection-platform/ingestion/internal/feedspec"
)

// bigContract exercises the decimal → CEL double boundary: two 2dp money
// columns compared by a rule, with the sum reconciled exactly.
const bigContract = `feed_id: big_amounts
version: 1
source_id: TEST_SOURCE
filename_regex: '^big_amounts_(?P<business_date>\d{8})\.csv$'
encoding: UTF-8
format: RFC4180
header:
  feed_code: BIG_AMOUNTS
  fields: [feed_code, business_date, record_count]
trailer:
  fields: [record_count, control_total]
  control_total_column: total
columns:
  - name: total
    type: decimal
    required: true
    scale: 2
  - name: part
    type: decimal
    required: true
    scale: 2
business_rules:
  - id: part_within_total
    expr: part <= total
    severity: ERROR
sla:
  expected_by: "02:00"
  late_by: "03:00"
  timezone: UTC
reconciliation:
  count: declared_equals_parsed
  amount:
    column: total
    vs: trailer_control_total
`

func loadInline(t testing.TB, name, body string) *feedspec.Feed {
	t.Helper()
	f, err := feedspec.Load(inlineFS(name, body), name)
	if err != nil {
		t.Fatalf("Load(%s): %v", name, err)
	}
	return f
}

// TestRuleDecimalPrecisionBoundary is the enforcement of the documented CEL
// decimal decision: comparisons stay faithful up to |value x 10^scale| = 2^52,
// and beyond it the row is rejected rather than compared at reduced precision.
func TestRuleDecimalPrecisionBoundary(t *testing.T) {
	t.Parallel()
	f := loadInline(t, "big_amounts.v1.yaml", bigContract)

	tests := []struct {
		name        string
		total, part string
		want        []string
	}{
		{
			// 2^52 / 100 exactly: the largest 2dp value the rules accept.
			name: "at the boundary", total: "45035996273704.96", part: "45035996273704.96",
		},
		{
			name: "one cent past the boundary", total: "45035996273704.97", part: "0.00",
			want: []string{"DECIMAL_EXCEEDS_RULE_PRECISION/total"},
		},
		{
			name: "negative, past the boundary", total: "0.00", part: "-45035996273704.97",
			want: []string{"DECIMAL_EXCEEDS_RULE_PRECISION/part"},
		},
		{
			// Ordering must survive the conversion: one cent apart at 40
			// trillion is still one cent apart as a float64.
			name: "one cent apart at forty trillion", total: "40000000000000.00", part: "40000000000000.01",
			want: []string{"BUSINESS_RULE_FAILED#part_within_total"},
		},
		{
			name: "equal at forty trillion", total: "40000000000000.01", part: "40000000000000.01",
		},
		{
			// The classic float trap, as a rule: 0.1 + 0.2 style values must
			// not make a <= comparison lie.
			name: "cents compare exactly", total: "0.30", part: "0.30",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := f.ValidateRow([]string{"DATA", tc.total, tc.part})
			if err != nil {
				t.Fatalf("ValidateRow: %v", err)
			}
			if !equalReasons(res.Failures, tc.want) {
				t.Fatalf("failures = %v, want %v", reasons(res.Failures), tc.want)
			}
		})
	}
}

// TestControlTotalIsExactBeyondTheRuleBoundary proves the two paths are
// independent: a value too large for a rule comparison is still summed exactly
// into the control total, because the total never goes through a float.
func TestControlTotalIsExactBeyondTheRuleBoundary(t *testing.T) {
	t.Parallel()
	f := loadInline(t, "big_amounts.v1.yaml", bigContract)

	in := strings.Join([]string{
		"HEADER,BIG_AMOUNTS,20260822,2",
		"DATA,90071992547409.91,0.00",
		"DATA,0.09,0.00",
		"TRAILER,2,90071992547410.00",
	}, "\n") + "\n"

	res, err := f.ValidateFile(strings.NewReader(in), businessDate)
	if err != nil {
		t.Fatalf("ValidateFile: %v", err)
	}
	if got := res.ComputedControlTotal.StringFixed(2); got != "90071992547410.00" {
		t.Fatalf("computed control total = %s, want 90071992547410.00 exactly", got)
	}
	// The row is rejected for the rule comparison, and the total still
	// reconciles: the control total is never the reason to trust a float.
	want := []string{"DECIMAL_EXCEEDS_RULE_PRECISION/total"}
	if !equalReasons(res.Failures, want) {
		t.Fatalf("failures = %v, want %v", reasons(res.Failures), want)
	}
}

// optionalContract has one optional column and two rules over it: one that
// null-checks and one that does not.
const optionalContract = `feed_id: optional_columns
version: 1
source_id: TEST_SOURCE
filename_regex: '^optional_columns_(?P<business_date>\d{8})\.csv$'
encoding: UTF-8
format: RFC4180
header:
  feed_code: OPTIONAL_COLUMNS
  fields: [feed_code, business_date, record_count]
trailer:
  fields: [record_count, control_total]
  control_total_column: amount
columns:
  - name: amount
    type: decimal
    required: true
    scale: 2
  - name: prev_dpd
    type: integer
    required: false
  - name: last_pay_dt
    type: date_yyyymmdd
    required: false
business_rules:
  - id: prev_dpd_guarded
    expr: prev_dpd == null || prev_dpd >= 0
    severity: ERROR
  - id: prev_dpd_unguarded
    expr: prev_dpd >= 0
    severity: WARN
  - id: last_pay_dt_guarded
    expr: last_pay_dt == null || last_pay_dt <= "20260822"
    severity: WARN
sla:
  expected_by: "02:00"
  late_by: "03:00"
  timezone: UTC
reconciliation:
  count: declared_equals_parsed
  amount:
    column: amount
    vs: trailer_control_total
`

// TestRuleOptionalColumnsAreNull documents the contract for optional columns: a
// rule must null-check, and one that does not fails closed as an ERROR instead
// of quietly passing on a zero value.
func TestRuleOptionalColumnsAreNull(t *testing.T) {
	t.Parallel()
	f := loadInline(t, "optional_columns.v1.yaml", optionalContract)

	tests := []struct {
		name   string
		record []string
		want   []string
	}{
		{
			name:   "absent optional makes an unguarded rule fail closed",
			record: []string{"DATA", "10.00", "", ""},
			want:   []string{"RULE_EVALUATION_ERROR#prev_dpd_unguarded"},
		},
		{
			name:   "present optional satisfies both rules",
			record: []string{"DATA", "10.00", "5", "20260101"},
		},
		{
			name:   "present optional violating both rules keeps each severity",
			record: []string{"DATA", "10.00", "-5", "20260101"},
			want: []string{
				"BUSINESS_RULE_FAILED#prev_dpd_guarded",
				"BUSINESS_RULE_FAILED#prev_dpd_unguarded",
			},
		},
		{
			name:   "date comparison is lexical and therefore chronological",
			record: []string{"DATA", "10.00", "0", "20260823"},
			want:   []string{"BUSINESS_RULE_FAILED#last_pay_dt_guarded"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := f.ValidateRow(tc.record)
			if err != nil {
				t.Fatalf("ValidateRow: %v", err)
			}
			if !equalReasons(res.Failures, tc.want) {
				t.Fatalf("failures = %v, want %v", reasons(res.Failures), tc.want)
			}
			// A rule evaluation error is an ERROR whatever the rule declares.
			for _, fl := range res.Failures {
				if fl.Reason == feedspec.ReasonRuleEvaluationError && fl.Severity != feedspec.SeverityError {
					t.Errorf("rule evaluation error has severity %s, want ERROR", fl.Severity)
				}
			}
		})
	}
}

// TestRuleSeverityDrivesTheFileVerdict is the ING-4 policy in one assertion: an
// ERROR row means the file cannot be validated, a WARN row does not.
func TestRuleSeverityDrivesTheFileVerdict(t *testing.T) {
	t.Parallel()
	f := loadFeed(t, "loan_accounts")

	warnFile := strings.Join([]string{
		"HEADER,LOAN_ACCOUNTS,20260822,1",
		"DATA,ACC0000001,CUS0000001,LOAN,20200115,AC,100.00,0.00,900.00,,",
		"TRAILER,1,100.00",
	}, "\n") + "\n"
	errFile := strings.Join([]string{
		"HEADER,LOAN_ACCOUNTS,20260822,1",
		"DATA,ACC0000001,CUS0000001,LOAN,20200115,AC,100.00,900.00,10.00,,",
		"TRAILER,1,100.00",
	}, "\n") + "\n"

	warn, err := f.ValidateFile(strings.NewReader(warnFile), businessDate)
	if err != nil {
		t.Fatalf("ValidateFile(warn): %v", err)
	}
	if !warn.OK() || warn.WarnCount != 1 || warn.RejectedCount != 0 {
		t.Errorf("WARN file: ok=%v warn=%d rejected=%d, want true/1/0", warn.OK(), warn.WarnCount, warn.RejectedCount)
	}

	bad, err := f.ValidateFile(strings.NewReader(errFile), businessDate)
	if err != nil {
		t.Fatalf("ValidateFile(error): %v", err)
	}
	if bad.OK() || bad.RejectedCount != 1 {
		t.Errorf("ERROR file: ok=%v rejected=%d, want false/1", bad.OK(), bad.RejectedCount)
	}
}
