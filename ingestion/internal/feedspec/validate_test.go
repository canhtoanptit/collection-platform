package feedspec_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/canhtoanptit/collection-platform/ingestion/internal/feedspec"
)

const businessDate = "20260822"

// failuresOf extracts the structured failures from a validation error.
func failuresOf(t *testing.T, err error) []feedspec.Failure {
	t.Helper()
	if err == nil {
		return nil
	}
	var verr *feedspec.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("error %v is not a *feedspec.ValidationError", err)
	}
	if verr.Error() == "" {
		t.Error("ValidationError.Error() is empty")
	}
	return verr.Failures
}

func TestValidateHeader(t *testing.T) {
	t.Parallel()
	f := loadFeed(t, "loan_accounts")

	tests := []struct {
		name         string
		line         string
		businessDate string
		want         []string
	}{
		{
			name: "valid", line: "HEADER,LOAN_ACCOUNTS,20260822,3", businessDate: businessDate,
		},
		{
			name: "quoted fields are RFC4180 and valid",
			line: `HEADER,"LOAN_ACCOUNTS","20260822","3"`, businessDate: businessDate,
		},
		{
			name: "zero rows is a valid header", line: "HEADER,LOAN_ACCOUNTS,20260822,0", businessDate: businessDate,
		},
		{
			name: "any date when the caller has none", line: "HEADER,LOAN_ACCOUNTS,20250101,3", businessDate: "",
		},
		{
			name: "wrong record type", line: "DATA,LOAN_ACCOUNTS,20260822,3", businessDate: businessDate,
			want: []string{"HEADER_RECORD_TYPE"},
		},
		{
			name: "too few fields", line: "HEADER,LOAN_ACCOUNTS,20260822", businessDate: businessDate,
			want: []string{"HEADER_MALFORMED"},
		},
		{
			name: "too many fields", line: "HEADER,LOAN_ACCOUNTS,20260822,3,extra", businessDate: businessDate,
			want: []string{"HEADER_MALFORMED"},
		},
		{
			name: "feed code mismatch", line: "HEADER,PAYMENTS,20260822,3", businessDate: businessDate,
			want: []string{"HEADER_FEED_CODE_MISMATCH"},
		},
		{
			name: "business date is not a calendar date", line: "HEADER,LOAN_ACCOUNTS,20260231,3", businessDate: businessDate,
			want: []string{"HEADER_BUSINESS_DATE_INVALID"},
		},
		{
			name: "business date is not eight digits", line: "HEADER,LOAN_ACCOUNTS,2026-08-22,3", businessDate: businessDate,
			want: []string{"HEADER_BUSINESS_DATE_INVALID"},
		},
		{
			name: "business date disagrees with the file name", line: "HEADER,LOAN_ACCOUNTS,20260821,3", businessDate: businessDate,
			want: []string{"HEADER_BUSINESS_DATE_MISMATCH"},
		},
		{
			name: "record count is signed", line: "HEADER,LOAN_ACCOUNTS,20260822,-1", businessDate: businessDate,
			want: []string{"HEADER_RECORD_COUNT_INVALID"},
		},
		{
			name: "record count is decimal", line: "HEADER,LOAN_ACCOUNTS,20260822,3.0", businessDate: businessDate,
			want: []string{"HEADER_RECORD_COUNT_INVALID"},
		},
		{
			name: "record count is thousands-separated", line: `HEADER,LOAN_ACCOUNTS,20260822,"1,245,231"`, businessDate: businessDate,
			want: []string{"HEADER_RECORD_COUNT_INVALID"},
		},
		{
			name: "record count overflows an int", line: "HEADER,LOAN_ACCOUNTS,20260822,99999999999999999999", businessDate: businessDate,
			want: []string{"HEADER_RECORD_COUNT_INVALID"},
		},
		{
			name: "several problems at once", line: "HEADER,LOAN,20260231,x", businessDate: businessDate,
			want: []string{"HEADER_FEED_CODE_MISMATCH", "HEADER_BUSINESS_DATE_INVALID", "HEADER_RECORD_COUNT_INVALID"},
		},
		{
			name: "line is not parseable CSV", line: `HEADER,"unclosed,20260822,3`, businessDate: businessDate,
			want: []string{"HEADER_MALFORMED"},
		},
		{
			name: "empty line", line: "", businessDate: businessDate,
			want: []string{"HEADER_MALFORMED"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := f.ValidateHeader(tc.line, tc.businessDate)
			got := failuresOf(t, err)
			if !equalReasons(got, tc.want) {
				t.Fatalf("ValidateHeader(%q) failures = %v, want %v", tc.line, reasons(got), tc.want)
			}
		})
	}
}

func TestParseHeaderReturnsDeclaredValues(t *testing.T) {
	t.Parallel()
	f := loadFeed(t, "loan_accounts")

	h, err := f.ParseHeader("HEADER,LOAN_ACCOUNTS,20260822,1245231", businessDate)
	if err != nil {
		t.Fatalf("ParseHeader: %v", err)
	}
	if h.FeedCode != "LOAN_ACCOUNTS" || h.BusinessDate != businessDate || h.RecordCount != 1245231 {
		t.Fatalf("ParseHeader = %+v", h)
	}

	if _, err := f.ParseHeader(`HEADER,"unclosed`, businessDate); err == nil {
		t.Fatal("ParseHeader accepted an unparseable line")
	}
}

func TestParseTrailer(t *testing.T) {
	t.Parallel()
	f := loadFeed(t, "loan_accounts")

	tests := []struct {
		name  string
		line  string
		want  []string
		count int
		total string
	}{
		{name: "valid", line: "TRAILER,3,1801.00", count: 3, total: "1801.00"},
		{name: "zero rows", line: "TRAILER,0,0.00", count: 0, total: "0.00"},
		{name: "negative total is structurally fine", line: "TRAILER,3,-1801.00", count: 3, total: "-1801.00"},
		{
			name: "control total must carry the column scale", line: "TRAILER,3,1801",
			want: []string{"TRAILER_CONTROL_TOTAL_INVALID/curr_bal"}, count: 3,
		},
		{
			name: "control total with too many places", line: "TRAILER,3,1801.000",
			want: []string{"TRAILER_CONTROL_TOTAL_INVALID/curr_bal"}, count: 3,
		},
		{
			name: "control total in exponent notation", line: "TRAILER,3,1.80100e3",
			want: []string{"TRAILER_CONTROL_TOTAL_INVALID/curr_bal"}, count: 3,
		},
		{
			// An unparseable count is reported as -1 so a caller can never
			// mistake it for a declared zero.
			name: "record count is not a count", line: "TRAILER,three,1801.00",
			want: []string{"TRAILER_RECORD_COUNT_INVALID"}, count: -1, total: "1801.00",
		},
		{
			name: "both fields wrong", line: "TRAILER,-3,none",
			want:  []string{"TRAILER_RECORD_COUNT_INVALID", "TRAILER_CONTROL_TOTAL_INVALID/curr_bal"},
			count: -1,
		},
		{name: "wrong field count", line: "TRAILER,3", want: []string{"TRAILER_MALFORMED"}},
		{name: "wrong record type", line: "FOOTER,3,1801.00", want: []string{"TRAILER_RECORD_TYPE"}},
		{name: "unparseable line", line: `TRAILER,"3,1801.00`, want: []string{"TRAILER_MALFORMED"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := f.ParseTrailer(tc.line)
			fails := failuresOf(t, err)
			if !equalReasons(fails, tc.want) {
				t.Fatalf("ParseTrailer(%q) failures = %v, want %v", tc.line, reasons(fails), tc.want)
			}
			if got.RecordCount != tc.count {
				t.Errorf("record count = %d, want %d", got.RecordCount, tc.count)
			}
			wantTotal := tc.total
			if wantTotal == "" {
				wantTotal = "0.00"
			}
			if got.ControlTotal.StringFixed(2) != wantTotal {
				t.Errorf("control total = %s, want %s", got.ControlTotal.StringFixed(2), wantTotal)
			}
		})
	}
}

func TestValidateRowLoanAccounts(t *testing.T) {
	t.Parallel()
	f := loadFeed(t, "loan_accounts")

	// The valid row, as fields, so cases can vary exactly one thing.
	base := []string{
		"DATA", "ACC0000002", "CUS0000002", "CARD", "20210320", "DQ",
		"600.50", "100.00", "25.00", "20260715", "20260620",
	}
	with := func(index int, value string) []string {
		out := append([]string(nil), base...)
		out[index] = value
		return out
	}

	tests := []struct {
		name           string
		record         []string
		want           []string
		rulesEvaluated bool
	}{
		{name: "valid", record: base, rulesEvaluated: true},
		{
			name:           "optional dates absent",
			record:         with(10, ""),
			rulesEvaluated: true,
		},
		{
			name:   "wrong record type",
			record: with(0, "TRAILER"),
			want:   []string{"RECORD_TYPE_UNKNOWN"},
		},
		{
			name:   "empty record",
			record: []string{},
			want:   []string{"RECORD_TYPE_UNKNOWN"},
		},
		{
			name:   "field missing",
			record: base[:len(base)-1],
			want:   []string{"ROW_FIELD_COUNT"},
		},
		{
			name:   "field added",
			record: append(append([]string(nil), base...), "extra"),
			want:   []string{"ROW_FIELD_COUNT"},
		},
		{
			name:   "required field empty",
			record: with(2, ""),
			want:   []string{"REQUIRED_FIELD_MISSING/cust_no"},
		},
		{
			name:   "pattern mismatch",
			record: with(1, "acc-2"),
			want:   []string{"PATTERN_MISMATCH/acct_no"},
			// A pattern violation still leaves a usable value, so the rules
			// are evaluated: the row is wrong, not unreadable.
			rulesEvaluated: true,
		},
		{
			name:   "enum invalid",
			record: with(3, "card"),
			want:   []string{"ENUM_INVALID/prod_cd"}, rulesEvaluated: true,
		},
		{
			name:   "date is not a calendar date",
			record: with(4, "20210230"),
			want:   []string{"INVALID_DATE/open_dt"},
		},
		{
			name:   "optional date invalid",
			record: with(9, "0"),
			want:   []string{"INVALID_DATE/last_pay_dt"},
		},
		{
			name:   "decimal with a thousands separator",
			record: with(6, "1,200.50"),
			want:   []string{"INVALID_DECIMAL/curr_bal"},
		},
		{
			name:   "decimal scale exceeded",
			record: with(6, "600.505"),
			want:   []string{"DECIMAL_SCALE_EXCEEDED/curr_bal"}, rulesEvaluated: true,
		},
		{
			name:   "business rule ERROR",
			record: with(7, "700.00"),
			want:   []string{"BUSINESS_RULE_FAILED#od_amt_within_balance"}, rulesEvaluated: true,
		},
		{
			name:   "business rule WARN",
			record: with(8, "700.00"),
			want:   []string{"BUSINESS_RULE_FAILED#min_due_within_balance"}, rulesEvaluated: true,
		},
		{
			name:   "both rules fail",
			record: with(6, "10.00"),
			want: []string{
				"BUSINESS_RULE_FAILED#od_amt_within_balance",
				"BUSINESS_RULE_FAILED#min_due_within_balance",
			},
			rulesEvaluated: true,
		},
		{
			// The rules must NOT run on a row with an unparseable cell:
			// od_amt (100.00) would otherwise be compared against a
			// meaningless curr_bal and produce a second, misleading failure.
			name:   "rules are skipped when a cell has no usable value",
			record: with(6, "not-a-number"),
			want:   []string{"INVALID_DECIMAL/curr_bal"},
		},
		{
			name:   "several cells wrong",
			record: with(1, "x"),
			want:   []string{"PATTERN_MISMATCH/acct_no"}, rulesEvaluated: true,
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
			if res.RulesEvaluated != tc.rulesEvaluated {
				t.Errorf("RulesEvaluated = %v, want %v", res.RulesEvaluated, tc.rulesEvaluated)
			}
			wantOK := len(tc.want) == 0 ||
				(len(tc.want) == 1 && strings.HasSuffix(tc.want[0], "#min_due_within_balance"))
			if res.OK() != wantOK {
				t.Errorf("OK() = %v, want %v", res.OK(), wantOK)
			}
		})
	}
}

func TestValidateRowTypedValues(t *testing.T) {
	t.Parallel()
	f := loadFeed(t, "loan_accounts")

	res, err := f.ValidateRow([]string{
		"DATA", "ACC0000002", "CUS0000002", "CARD", "20210320", "DQ",
		"600.50", "100.00", "25.00", "", "20260620",
	})
	if err != nil {
		t.Fatalf("ValidateRow: %v", err)
	}
	if len(res.Failures) != 0 {
		t.Fatalf("unexpected failures: %v", reasons(res.Failures))
	}
	if len(res.Values) != len(f.Columns) {
		t.Fatalf("Values = %d, want one per column (%d)", len(res.Values), len(f.Columns))
	}

	bal, ok := res.Value("curr_bal")
	if !ok || !bal.Present || bal.Dec.String() != "600.5" || bal.Raw != "600.50" {
		t.Errorf("curr_bal = %+v", bal)
	}
	open, ok := res.Value("open_dt")
	if !ok || open.Date.Format("2006-01-02") != "2021-03-20" || open.Date.Location().String() != "UTC" {
		t.Errorf("open_dt = %+v", open)
	}
	last, ok := res.Value("last_pay_dt")
	if !ok || last.Present {
		t.Errorf("absent optional value should be Present=false, got %+v", last)
	}
	if _, ok := res.Value("nope"); ok {
		t.Error("Value returned a column that does not exist")
	}

	res.SetRowNumber(7)
	if res.RowNumber != 7 {
		t.Errorf("SetRowNumber did not stamp the row: %+v", res)
	}
}

func TestValidateRowIntegerAndBounds(t *testing.T) {
	t.Parallel()
	f := loadFeed(t, "payments")

	tests := []struct {
		name   string
		record []string
		want   []string
	}{
		{name: "valid", record: []string{"DATA", "1001", "ACC0000001", "20260822", "150.00", "DD", "N"}},
		{
			name:   "integer with a separator",
			record: []string{"DATA", "1,001", "ACC0000001", "20260822", "150.00", "DD", "N"},
			want:   []string{"INVALID_INTEGER/pay_id"},
		},
		{
			name:   "integer overflows int64",
			record: []string{"DATA", "99999999999999999999", "ACC0000001", "20260822", "150.00", "DD", "N"},
			want:   []string{"INVALID_INTEGER/pay_id"},
		},
		{
			name:   "integer below the inclusive minimum",
			record: []string{"DATA", "0", "ACC0000001", "20260822", "150.00", "DD", "N"},
			want:   []string{"MIN_VIOLATION/pay_id"},
		},
		{
			name:   "amount below the inclusive minimum also breaks the rule",
			record: []string{"DATA", "1001", "ACC0000001", "20260822", "0.00", "DD", "N"},
			want:   []string{"MIN_VIOLATION/amount", "BUSINESS_RULE_FAILED#amount_positive"},
		},
		{
			name:   "signed amount",
			record: []string{"DATA", "1001", "ACC0000001", "20260822", "-0.01", "DD", "N"},
			want:   []string{"MIN_VIOLATION/amount", "BUSINESS_RULE_FAILED#amount_positive"},
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
		})
	}
}

func TestValidateRowMaxBound(t *testing.T) {
	t.Parallel()

	f, err := feedspec.Load(contractFS(baseContract), "loan_accounts.v1.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// columns: acct_no, prod_cd, open_dt, dpd(min 0 max 3650), curr_bal, od_amt(min 0)
	res, err := f.ValidateRow([]string{"DATA", "ACC001", "LOAN", "20260101", "4000", "10.00", "-1.00"})
	if err != nil {
		t.Fatalf("ValidateRow: %v", err)
	}
	want := []string{"MAX_VIOLATION/dpd", "MIN_VIOLATION/od_amt"}
	if !equalReasons(res.Failures, want) {
		t.Fatalf("failures = %v, want %v", reasons(res.Failures), want)
	}
}

func TestValidatorSequence(t *testing.T) {
	t.Parallel()
	f := loadFeed(t, "loan_accounts")

	v := f.NewValidator(businessDate)
	if err := v.ValidateHeader("HEADER,LOAN_ACCOUNTS,20260822,2"); err != nil {
		t.Fatalf("ValidateHeader: %v", err)
	}
	rows := [][]string{
		{"DATA", "ACC0000001", "CUS0000001", "LOAN", "20200115", "AC", "1200.50", "0.00", "50.00", "20260801", ""},
		// min_due > curr_bal: a WARN, so the file still passes.
		{"DATA", "ACC0000002", "CUS0000002", "CARD", "20210320", "DQ", "600.50", "100.00", "700.00", "", ""},
	}
	for _, r := range rows {
		res, err := v.ValidateRow(r)
		if err != nil {
			t.Fatalf("ValidateRow: %v", err)
		}
		if !res.OK() {
			t.Fatalf("row unexpectedly rejected: %v", reasons(res.Failures))
		}
	}
	if err := v.ValidateTrailer("TRAILER,2,1801.00"); err != nil {
		t.Fatalf("ValidateTrailer: %v", err)
	}

	got := v.Result()
	if !got.OK() {
		t.Errorf("file should pass with only a WARN: %v", reasons(got.Failures))
	}
	if got.ParsedCount != 2 || got.RejectedCount != 0 || got.WarnCount != 1 {
		t.Errorf("counts = parsed %d, rejected %d, warn %d; want 2/0/1",
			got.ParsedCount, got.RejectedCount, got.WarnCount)
	}
	if got.HeaderCount != 2 || got.DeclaredCount != 2 {
		t.Errorf("declared counts = header %d, trailer %d; want 2/2", got.HeaderCount, got.DeclaredCount)
	}
	if got.ComputedControlTotal.StringFixed(2) != "1801.00" {
		t.Errorf("computed control total = %s", got.ComputedControlTotal)
	}
	if len(got.Warnings()) != 1 || len(got.Errors()) != 0 {
		t.Errorf("warnings/errors = %d/%d, want 1/0", len(got.Warnings()), len(got.Errors()))
	}

	// Result must be idempotent: the terminal checks are computed, not appended.
	again := v.Result()
	if len(again.Failures) != len(got.Failures) {
		t.Errorf("Result is not idempotent: %d then %d failures", len(got.Failures), len(again.Failures))
	}
}

func TestValidatorStructuralFaults(t *testing.T) {
	t.Parallel()
	f := loadFeed(t, "loan_accounts")

	row := []string{"DATA", "ACC0000001", "CUS0000001", "LOAN", "20200115", "AC", "1200.50", "0.00", "50.00", "", ""}

	t.Run("duplicate header", func(t *testing.T) {
		t.Parallel()
		v := f.NewValidator(businessDate)
		if err := v.ValidateHeader("HEADER,LOAN_ACCOUNTS,20260822,0"); err != nil {
			t.Fatalf("first header: %v", err)
		}
		err := v.ValidateHeader("HEADER,LOAN_ACCOUNTS,20260822,0")
		if !equalReasons(failuresOf(t, err), []string{"HEADER_DUPLICATE"}) {
			t.Fatalf("second header failures = %v", reasons(failuresOf(t, err)))
		}
	})

	t.Run("header after a row is a missing header", func(t *testing.T) {
		t.Parallel()
		v := f.NewValidator(businessDate)
		if _, err := v.ValidateRow(row); err != nil {
			t.Fatalf("ValidateRow: %v", err)
		}
		err := v.ValidateHeader("HEADER,LOAN_ACCOUNTS,20260822,1")
		if !equalReasons(failuresOf(t, err), []string{"HEADER_MISSING"}) {
			t.Fatalf("failures = %v", reasons(failuresOf(t, err)))
		}
		// ... and it is reported exactly once.
		res := v.Result()
		if !equalReasons(res.Failures, []string{"HEADER_MISSING", "TRAILER_MISSING"}) {
			t.Fatalf("result failures = %v", reasons(res.Failures))
		}
	})

	t.Run("row after the trailer", func(t *testing.T) {
		t.Parallel()
		v := f.NewValidator(businessDate)
		if err := v.ValidateHeader("HEADER,LOAN_ACCOUNTS,20260822,1"); err != nil {
			t.Fatalf("header: %v", err)
		}
		if _, err := v.ValidateRow(row); err != nil {
			t.Fatalf("row: %v", err)
		}
		if err := v.ValidateTrailer("TRAILER,1,1200.50"); err != nil {
			t.Fatalf("trailer: %v", err)
		}
		res, err := v.ValidateRow(row)
		if err != nil {
			t.Fatalf("late row: %v", err)
		}
		if !equalReasons(res.Failures, []string{"ROW_AFTER_TRAILER"}) {
			t.Fatalf("late row failures = %v", reasons(res.Failures))
		}
	})

	t.Run("duplicate trailer", func(t *testing.T) {
		t.Parallel()
		v := f.NewValidator(businessDate)
		if err := v.ValidateHeader("HEADER,LOAN_ACCOUNTS,20260822,0"); err != nil {
			t.Fatalf("header: %v", err)
		}
		if err := v.ValidateTrailer("TRAILER,0,0.00"); err != nil {
			t.Fatalf("first trailer: %v", err)
		}
		err := v.ValidateTrailer("TRAILER,0,0.00")
		if !equalReasons(failuresOf(t, err), []string{"TRAILER_MALFORMED"}) {
			t.Fatalf("second trailer failures = %v", reasons(failuresOf(t, err)))
		}
	})

	t.Run("unparseable header and trailer lines", func(t *testing.T) {
		t.Parallel()
		v := f.NewValidator(businessDate)
		err := v.ValidateHeader(`HEADER,"unclosed`)
		if !equalReasons(failuresOf(t, err), []string{"HEADER_MALFORMED"}) {
			t.Fatalf("header failures = %v", reasons(failuresOf(t, err)))
		}
		err = v.ValidateTrailer(`TRAILER,"unclosed`)
		if !equalReasons(failuresOf(t, err), []string{"TRAILER_MALFORMED"}) {
			t.Fatalf("trailer failures = %v", reasons(failuresOf(t, err)))
		}
		// The trailer never parsed, so nothing was declared.
		if res := v.Result(); res.DeclaredCount != -1 || res.OK() {
			t.Errorf("result = %+v", res)
		}
	})

	t.Run("empty file", func(t *testing.T) {
		t.Parallel()
		res, err := f.ValidateFile(strings.NewReader(""), businessDate)
		if err != nil {
			t.Fatalf("ValidateFile: %v", err)
		}
		if !equalReasons(res.Failures, []string{"HEADER_MISSING", "TRAILER_MISSING"}) {
			t.Fatalf("failures = %v", reasons(res.Failures))
		}
		if res.ParsedCount != 0 || res.HeaderCount != -1 || res.DeclaredCount != -1 {
			t.Errorf("counts = %+v", res)
		}
	})

	t.Run("zero-row file is valid", func(t *testing.T) {
		t.Parallel()
		res, err := f.ValidateFile(strings.NewReader("HEADER,LOAN_ACCOUNTS,20260822,0\nTRAILER,0,0.00\n"), businessDate)
		if err != nil {
			t.Fatalf("ValidateFile: %v", err)
		}
		if !res.OK() {
			t.Fatalf("a day with no rows must be valid, got %v", reasons(res.Failures))
		}
	})
}

func TestValidateFileTransportFaults(t *testing.T) {
	t.Parallel()
	f := loadFeed(t, "loan_accounts")

	t.Run("unknown record type", func(t *testing.T) {
		t.Parallel()
		in := "HEADER,LOAN_ACCOUNTS,20260822,0\nFOOTER,0\nTRAILER,0,0.00\n"
		res, err := f.ValidateFile(strings.NewReader(in), businessDate)
		if err != nil {
			t.Fatalf("ValidateFile: %v", err)
		}
		if !equalReasons(res.Failures, []string{"RECORD_TYPE_UNKNOWN"}) {
			t.Fatalf("failures = %v", reasons(res.Failures))
		}
		if res.Failures[0].RowNumber != 2 {
			t.Errorf("row number = %d, want 2", res.Failures[0].RowNumber)
		}
	})

	t.Run("unparseable CSV stops the file", func(t *testing.T) {
		t.Parallel()
		in := "HEADER,LOAN_ACCOUNTS,20260822,1\nDATA,\"unclosed,CUS1,LOAN\nTRAILER,1,0.00\n"
		res, err := f.ValidateFile(strings.NewReader(in), businessDate)
		if err != nil {
			t.Fatalf("ValidateFile: %v", err)
		}
		want := []string{"ROW_UNPARSEABLE", "TRAILER_MISSING"}
		if !equalReasons(res.Failures, want) {
			t.Fatalf("failures = %v, want %v", reasons(res.Failures), want)
		}
	})

	t.Run("reader error is an error, not a verdict", func(t *testing.T) {
		t.Parallel()
		_, err := f.ValidateFile(errReader{}, businessDate)
		if err == nil {
			t.Fatal("ValidateFile hid a reader failure")
		}
		if !strings.Contains(err.Error(), "reading feed loan_accounts") {
			t.Errorf("error = %v", err)
		}
	})
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("disk on fire") }

func TestControlTotalAccumulator(t *testing.T) {
	t.Parallel()
	f := loadFeed(t, "payments")

	acc := f.NewControlTotalAccumulator()
	if acc.Column() != "amount" {
		t.Fatalf("Column() = %q, want amount", acc.Column())
	}

	// The classic float trap: 0.10 + 0.20 == 0.30 must hold exactly.
	for _, s := range []string{"0.10", "0.20"} {
		d, err := decimal.NewFromString(s)
		if err != nil {
			t.Fatalf("NewFromString(%s): %v", s, err)
		}
		acc.AddDecimal(d)
	}
	if got := acc.Result().String(); got != "0.3" {
		t.Fatalf("0.10 + 0.20 = %s, want 0.3", got)
	}
	if !acc.Matches(decimal.RequireFromString("0.30")) {
		t.Error("0.30 must match the computed 0.3 at scale 2")
	}
	if acc.Matches(decimal.RequireFromString("0.31")) {
		t.Error("0.31 must not match")
	}
	if acc.Rows() != 2 {
		t.Errorf("Rows() = %d, want 2", acc.Rows())
	}

	// A row whose control column did not parse contributes nothing, loudly.
	res, err := f.ValidateRow([]string{"DATA", "1001", "ACC0000001", "20260822", "nope", "DD", "N"})
	if err != nil {
		t.Fatalf("ValidateRow: %v", err)
	}
	if err := acc.Add(res); !errors.Is(err, feedspec.ErrNoControlValue) {
		t.Fatalf("Add of an unparsed control value = %v, want ErrNoControlValue", err)
	}
	if acc.Rows() != 2 {
		t.Errorf("a rejected row must not be counted: Rows() = %d", acc.Rows())
	}

	good, err := f.ValidateRow([]string{"DATA", "1001", "ACC0000001", "20260822", "0.70", "DD", "N"})
	if err != nil {
		t.Fatalf("ValidateRow: %v", err)
	}
	if err := acc.Add(good); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got := acc.Result().StringFixed(2); got != "1.00" {
		t.Fatalf("total = %s, want 1.00", got)
	}
}

// TestUnloadedFeedIsInert proves a hand-built Feed cannot silently accept data:
// its rules were never compiled, so every entry point refuses.
func TestUnloadedFeedIsInert(t *testing.T) {
	t.Parallel()

	f := &feedspec.Feed{FeedID: "hand_made", Columns: []feedspec.Column{{Name: "a", Type: feedspec.TypeString}}}

	if _, ok := f.Column("a"); ok {
		t.Error("a hand-built Feed must not resolve columns: the name index is built by Load")
	}

	if err := f.ValidateHeader("HEADER,X,20260822,0", businessDate); !errors.Is(err, feedspec.ErrNotLoaded) {
		t.Errorf("ValidateHeader = %v, want ErrNotLoaded", err)
	}
	if _, err := f.ParseHeader("HEADER,X,20260822,0", businessDate); !errors.Is(err, feedspec.ErrNotLoaded) {
		t.Errorf("ParseHeader = %v, want ErrNotLoaded", err)
	}
	if _, err := f.ParseTrailer("TRAILER,0,0.00"); !errors.Is(err, feedspec.ErrNotLoaded) {
		t.Errorf("ParseTrailer = %v, want ErrNotLoaded", err)
	}
	if _, err := f.ValidateRow([]string{"DATA", "x"}); !errors.Is(err, feedspec.ErrNotLoaded) {
		t.Errorf("ValidateRow = %v, want ErrNotLoaded", err)
	}
	if _, err := f.ValidateFile(strings.NewReader(""), businessDate); !errors.Is(err, feedspec.ErrNotLoaded) {
		t.Errorf("ValidateFile = %v, want ErrNotLoaded", err)
	}
	v := f.NewValidator(businessDate)
	if err := v.ValidateHeaderRecord([]string{"HEADER"}); !errors.Is(err, feedspec.ErrNotLoaded) {
		t.Errorf("Validator.ValidateHeaderRecord = %v, want ErrNotLoaded", err)
	}
	if err := v.ValidateTrailerRecord([]string{"TRAILER"}); !errors.Is(err, feedspec.ErrNotLoaded) {
		t.Errorf("Validator.ValidateTrailerRecord = %v, want ErrNotLoaded", err)
	}
	if _, err := v.ValidateRow([]string{"DATA", "x"}); !errors.Is(err, feedspec.ErrNotLoaded) {
		t.Errorf("Validator.ValidateRow = %v, want ErrNotLoaded", err)
	}
}

func TestFailureString(t *testing.T) {
	t.Parallel()

	f := feedspec.Failure{
		RowNumber: 3, Column: "curr_bal", RuleID: "od_amt_within_balance",
		Reason: feedspec.ReasonBusinessRuleFailed, Severity: feedspec.SeverityError,
		Detail: "od_amt <= curr_bal",
	}
	got := f.String()
	for _, want := range []string{"ERROR", "BUSINESS_RULE_FAILED", "row=3", "column=curr_bal", "rule=od_amt_within_balance", "od_amt <= curr_bal"} {
		if !strings.Contains(got, want) {
			t.Errorf("Failure.String() = %q, want it to contain %q", got, want)
		}
	}
}
