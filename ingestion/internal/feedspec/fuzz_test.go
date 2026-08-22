package feedspec_test

import (
	"strings"
	"testing"

	"github.com/canhtoanptit/collection-platform/ingestion/internal/feedspec"
)

// FuzzValidateRow feeds arbitrary records to the row validator. Bank feeds
// contain surprises — truncated lines, mojibake, huge numbers — and a validator
// that panics on one of them takes the whole ingestion worker down, turning a
// data-quality problem into an outage.
//
// The invariants asserted are the ones ING-4 relies on:
//   - no panic, whatever the record;
//   - either the record could not be mapped to the columns (no Values) or there
//     is exactly one Value per declared column;
//   - every failure carries a reason and a known severity, so the quarantine
//     record is always writable;
//   - validation is deterministic — the same record produces the same failures,
//     which map iteration order in the rule activation could otherwise break.
func FuzzValidateRow(f *testing.F) {
	feed, err := feedspec.Load(contractsFS(f), "loan_accounts.v1.yaml")
	if err != nil {
		f.Fatalf("Load: %v", err)
	}

	seeds := []string{
		"DATA,ACC0000001,CUS0000001,LOAN,20200115,AC,1200.50,0.00,50.00,20260801,",
		"DATA,ACC0000001,CUS0000001,LOAN,20200115,AC,1200.50,0.00,50.00,20260801,20260101",
		"DATA,,,,,,,,,,",
		"DATA",
		"",
		",",
		"HEADER,LOAN_ACCOUNTS,20260822,3",
		"TRAILER,3,1801.00",
		"DATA,ACC0000001,CUS0000001,LOAN,20200115,AC,1e309,-0,50.00,00000000,99999999",
		"DATA,ACC0000001,CUS0000001,MORT,20260231,AC,0.001,999999999999999999999.99,0,,",
		"DATA,\xff\xfe,CUS0000001,CARD,20200115,AC,1200.50,0.00,50.00,,",
		"DATA,ACC0000001,CUS0000001,CARD,20200115,AC,-45035996273704.97,0.00,0.00,,",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, line string) {
		record := strings.Split(line, ",")

		res, err := feed.ValidateRow(record)
		if err != nil {
			t.Fatalf("ValidateRow returned an error for a loaded feed: %v", err)
		}
		if n := len(res.Values); n != 0 && n != len(feed.Columns) {
			t.Fatalf("Values = %d, want 0 or %d", n, len(feed.Columns))
		}
		for _, fl := range res.Failures {
			if fl.Reason == "" {
				t.Fatalf("failure without a reason: %+v", fl)
			}
			if fl.Severity != feedspec.SeverityError && fl.Severity != feedspec.SeverityWarn {
				t.Fatalf("failure with an unknown severity: %+v", fl)
			}
		}

		again, err := feed.ValidateRow(record)
		if err != nil {
			t.Fatalf("second ValidateRow: %v", err)
		}
		if got, want := strings.Join(reasons(again.Failures), "|"), strings.Join(reasons(res.Failures), "|"); got != want {
			t.Fatalf("validation is not deterministic: %q then %q", want, got)
		}
	})
}

// FuzzValidateFile fuzzes the whole-file path: the record dispatcher, the CSV
// tokeniser and the trailer cross-checks.
func FuzzValidateFile(f *testing.F) {
	feed, err := feedspec.Load(contractsFS(f), "payments.v1.yaml")
	if err != nil {
		f.Fatalf("Load: %v", err)
	}

	seeds := []string{
		"HEADER,PAYMENTS,20260822,1\nDATA,1001,ACC0000001,20260822,150.00,DD,N\nTRAILER,1,150.00\n",
		"HEADER,PAYMENTS,20260822,0\nTRAILER,0,0.00\n",
		"",
		"\n\n\n",
		"TRAILER,0,0.00\nHEADER,PAYMENTS,20260822,0\n",
		"HEADER,PAYMENTS,20260822,1\nDATA,\"unclosed\nTRAILER,1,0.00\n",
		"HEADER,PAYMENTS,20260822,1\nHEADER,PAYMENTS,20260822,1\nTRAILER,1,0.00\n",
		"FOOTER\n",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, body string) {
		res, err := feed.ValidateFile(strings.NewReader(body), "20260822")
		if err != nil {
			t.Fatalf("ValidateFile returned an error for an in-memory reader: %v", err)
		}
		if res.ParsedCount < 0 || res.RejectedCount < 0 || res.WarnCount < 0 {
			t.Fatalf("negative counts: %+v", res)
		}
		if res.OK() != (len(res.Errors()) == 0) {
			t.Fatalf("OK() disagrees with Errors(): %+v", res)
		}
		if exp := res.ComputedControlTotal.Exponent(); exp < -2 {
			t.Fatalf("computed control total %s carries more than the column scale", res.ComputedControlTotal)
		}
	})
}
