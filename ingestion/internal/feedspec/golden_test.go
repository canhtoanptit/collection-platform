package feedspec_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/canhtoanptit/collection-platform/ingestion/internal/feedspec"
)

// goldenDir holds one directory per feed, and within it one pair per fault
// class: <case>.csv is the input file, <case>.json is the expected outcome.
//
// A new fault class is a new pair of files and no code change — which is the
// whole point: the fault matrix is data, so ING-4, SIM-4 and this library argue
// about files rather than about assertions buried in Go.
const goldenDir = "testdata/golden"

// minGoldenCasesPerFeed is the floor from the ING-3 acceptance criteria. It is
// asserted here as well as in scripts/verify/ING-3.sh so that deleting cases
// fails the unit tests, not only the verify script.
const minGoldenCasesPerFeed = 6

// goldenFile is the expected-outcome document. Decimal totals are strings: a
// JSON number would reintroduce the float this library exists to avoid.
type goldenFile struct {
	Input struct {
		BusinessDate string `json:"business_date"`
	} `json:"input"`
	Expect struct {
		OK                   bool               `json:"ok"`
		HeaderCount          int                `json:"header_count"`
		DeclaredCount        int                `json:"declared_count"`
		ParsedCount          int                `json:"parsed_count"`
		RejectedCount        int                `json:"rejected_count"`
		WarnCount            int                `json:"warn_count"`
		DeclaredControlTotal string             `json:"declared_control_total"`
		ComputedControlTotal string             `json:"computed_control_total"`
		Failures             []feedspec.Failure `json:"failures"`
	} `json:"expect"`
}

func TestGolden(t *testing.T) {
	t.Parallel()

	feedDirs, err := os.ReadDir(goldenDir)
	if err != nil {
		t.Fatalf("reading %s: %v", goldenDir, err)
	}
	if len(feedDirs) == 0 {
		t.Fatalf("no golden feeds under %s", goldenDir)
	}

	for _, fd := range feedDirs {
		if !fd.IsDir() {
			continue
		}
		feedID := fd.Name()
		t.Run(feedID, func(t *testing.T) {
			t.Parallel()

			feed := loadFeed(t, feedID)
			inputs, err := filepath.Glob(filepath.Join(goldenDir, feedID, "*.csv"))
			if err != nil {
				t.Fatalf("globbing golden inputs: %v", err)
			}
			sort.Strings(inputs)
			if len(inputs) < minGoldenCasesPerFeed {
				t.Fatalf("feed %s has %d golden cases, want at least %d",
					feedID, len(inputs), minGoldenCasesPerFeed)
			}

			for _, csvPath := range inputs {
				name := strings.TrimSuffix(filepath.Base(csvPath), ".csv")
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					runGoldenCase(t, feed, csvPath)
				})
			}
		})
	}
}

func runGoldenCase(t *testing.T, feed *feedspec.Feed, csvPath string) {
	t.Helper()

	jsonPath := strings.TrimSuffix(csvPath, ".csv") + ".json"
	raw, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("every golden input needs an expectation file: %v", err)
	}
	var want goldenFile
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&want); err != nil {
		t.Fatalf("decoding %s: %v", jsonPath, err)
	}
	if want.Input.BusinessDate == "" {
		t.Fatalf("%s: input.business_date is required", jsonPath)
	}

	in, err := os.Open(csvPath)
	if err != nil {
		t.Fatalf("opening %s: %v", csvPath, err)
	}
	defer func() { _ = in.Close() }()

	got, err := feed.ValidateFile(in, want.Input.BusinessDate)
	if err != nil {
		t.Fatalf("ValidateFile returned a transport error: %v", err)
	}

	scale := feed.ControlTotalScale()
	checks := []struct {
		field     string
		got, want any
	}{
		{"ok", got.OK(), want.Expect.OK},
		{"header_count", got.HeaderCount, want.Expect.HeaderCount},
		{"declared_count", got.DeclaredCount, want.Expect.DeclaredCount},
		{"parsed_count", got.ParsedCount, want.Expect.ParsedCount},
		{"rejected_count", got.RejectedCount, want.Expect.RejectedCount},
		{"warn_count", got.WarnCount, want.Expect.WarnCount},
		{"declared_control_total", got.DeclaredControlTotal.StringFixed(scale), want.Expect.DeclaredControlTotal},
		{"computed_control_total", got.ComputedControlTotal.StringFixed(scale), want.Expect.ComputedControlTotal},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.field, c.got, c.want)
		}
	}

	// Failures are compared on the stable fields only — reason, severity,
	// location — never on Detail, which is prose for a human reading a reject
	// record and is deliberately not contract.
	gotKeys := failureKeys(got.Failures)
	wantKeys := failureKeys(want.Expect.Failures)
	if strings.Join(gotKeys, "\n") != strings.Join(wantKeys, "\n") {
		t.Errorf("failures =\n  %s\nwant\n  %s", strings.Join(gotKeys, "\n  "), strings.Join(wantKeys, "\n  "))
		for _, f := range got.Failures {
			t.Logf("detail: %s", f)
		}
	}

	if feed.FeedID != got.FeedID {
		t.Errorf("FeedID = %q, want %q", got.FeedID, feed.FeedID)
	}
	if got.BusinessDate != want.Input.BusinessDate {
		t.Errorf("BusinessDate = %q, want %q", got.BusinessDate, want.Input.BusinessDate)
	}
	if len(got.Errors())+len(got.Warnings()) != len(got.Failures) {
		t.Errorf("Errors()+Warnings() = %d+%d, want %d failures in total",
			len(got.Errors()), len(got.Warnings()), len(got.Failures))
	}
}

func failureKeys(fs []feedspec.Failure) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		key := "row=" + itoa(f.RowNumber) + " " + string(f.Severity) + " " + string(f.Reason)
		if f.Column != "" {
			key += " column=" + f.Column
		}
		if f.RuleID != "" {
			key += " rule=" + f.RuleID
		}
		out = append(out, key)
	}
	return out
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
