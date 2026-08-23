package ids_test

import (
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/canhtoanptit/collection-platform/platform/ids"
)

func TestNewULIDIsCanonical(t *testing.T) {
	t.Parallel()

	for range 100 {
		id := ids.NewULID()
		if len(id) != 26 {
			t.Fatalf("NewULID() = %q, want 26 characters, got %d", id, len(id))
		}
		if !ids.IsULID(id) {
			t.Fatalf("NewULID() = %q, which IsULID rejects", id)
		}
		if strings.ToUpper(id) != id {
			t.Fatalf("NewULID() = %q, want uppercase only", id)
		}
	}
}

func TestNewULIDIsMonotonicWithinProcess(t *testing.T) {
	t.Parallel()

	// 5000 ids minted back to back land in the same millisecond many times
	// over, which is exactly the case a non-monotonic generator gets wrong.
	const n = 5000
	got := make([]string, n)
	for i := range got {
		got[i] = ids.NewULID()
	}
	for i := 1; i < n; i++ {
		if got[i] <= got[i-1] {
			t.Fatalf("ids are not strictly increasing at %d: %q <= %q", i, got[i], got[i-1])
		}
	}
}

func TestNewULIDIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()

	const goroutines, per = 16, 200

	var (
		mu  sync.Mutex
		all = make([]string, 0, goroutines*per)
		wg  sync.WaitGroup
	)
	for g := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()

			local := make([]string, per)
			for i := range local {
				local[i] = ids.NewULID()
				// Each goroutine observes its own calls in program order, so
				// its subsequence must be strictly increasing — this is the
				// guarantee a generator that samples the clock outside its
				// lock silently loses.
				if i > 0 && local[i] <= local[i-1] {
					t.Errorf("goroutine %d id %d is not increasing: %q <= %q", g, i, local[i], local[i-1])
					return
				}
			}
			mu.Lock()
			all = append(all, local...)
			mu.Unlock()
		}()
	}
	wg.Wait()

	sort.Strings(all)
	for i := 1; i < len(all); i++ {
		if all[i] == all[i-1] {
			t.Fatalf("duplicate id minted concurrently: %q", all[i])
		}
	}
	if len(all) != goroutines*per {
		t.Fatalf("collected %d ids, want %d", len(all), goroutines*per)
	}
}

func TestNewULIDAtEncodesTheTimestamp(t *testing.T) {
	t.Parallel()

	// ULIDs are ordered by their timestamp prefix, so an id minted for an
	// earlier instant must sort before one minted for a later instant even
	// when the later call happens first.
	later := ids.NewULIDAt(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	earlier := ids.NewULIDAt(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))

	if earlier >= later {
		t.Fatalf("timestamp ordering lost: earlier=%q later=%q", earlier, later)
	}
	if !ids.IsULID(earlier) || !ids.IsULID(later) {
		t.Fatalf("NewULIDAt produced a non-canonical id: %q / %q", earlier, later)
	}
	// A non-UTC input must produce the same timestamp prefix as its UTC
	// equivalent: the same instant is the same instant.
	loc := time.FixedZone("UTC+7", 7*3600)
	utc := ids.NewULIDAt(time.Date(2026, 8, 22, 3, 0, 0, 0, time.UTC))
	off := ids.NewULIDAt(time.Date(2026, 8, 22, 10, 0, 0, 0, loc))
	if utc[:10] != off[:10] {
		t.Fatalf("timezone normalisation lost: %q vs %q", utc[:10], off[:10])
	}
}

func TestNewULIDAtClampsInstantsOutsideTheULIDRange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		at   time.Time
		want string // expected 10-character timestamp prefix
	}{
		{"before the Unix epoch clamps to zero", time.Date(1969, 7, 20, 20, 17, 0, 0, time.UTC), "0000000000"},
		{"the epoch itself", time.Unix(0, 0), "0000000000"},
		{"beyond the 48-bit range clamps to the maximum", time.Date(99999, 1, 1, 0, 0, 0, 0, time.UTC), "7ZZZZZZZZZ"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := ids.NewULIDAt(tc.at)
			if !ids.IsULID(got) {
				t.Fatalf("NewULIDAt(%v) = %q, which IsULID rejects", tc.at, got)
			}
			if got[:10] != tc.want {
				t.Errorf("NewULIDAt(%v) timestamp prefix = %q, want %q", tc.at, got[:10], tc.want)
			}
		})
	}
}

func TestIsULID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"golden ULID from the contracts examples", "01M0KK5FG0RG0GVH3G6KY89V93", true},
		{"all zeroes", "00000000000000000000000000", true},
		{"max legal value", "7ZZZZZZZZZZZZZZZZZZZZZZZZZ", true},
		{"empty", "", false},
		{"too short", "01M0KK5FG0RG0GVH3G6KY89V9", false},
		{"too long", "01M0KK5FG0RG0GVH3G6KY89V933", false},
		{"lowercase", "01m0kk5fg0rg0gvh3g6ky89v93", false},
		{"excluded letter I", "01M0MEKD80M9S346Q3D25VT4FI", false},
		{"excluded letter L", "01M0MEKD80M9S346Q3D25VT4FL", false},
		{"excluded letter O", "01M0MEKD80M9S346Q3D25VT4FO", false},
		{"excluded letter U", "01M0MEKD80M9S346Q3D25VT4FU", false},
		{"timestamp overflow (first char above 7)", "8ZZZZZZZZZZZZZZZZZZZZZZZZZ", false},
		{"hyphen", "01M0KK5FG0RG0GVH3G6KY89V-3", false},
		{"UUID", "3f2504e0-4f89-11d3-9a0c-0305e82c3301", false},
		{"prefixed id is not a bare ULID", "FIL_01M0KK53S0DQ9PSTK0206PRD7M", false},
		{"envelope example correlationId", "01M0KK4G8042Z1PTCGJA3G3KMK", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ids.IsULID(tc.in); got != tc.want {
				t.Errorf("IsULID(%q) = %t, want %t", tc.in, got, tc.want)
			}
		})
	}
}

func TestPrefixedIdentifiers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mint    func() string
		valid   func(string) bool
		prefix  string
		example string
	}{
		{"file", ids.NewFileID, ids.IsFileID, ids.FilePrefix, "FIL_01M0KK53S0DQ9PSTK0206PRD7M"},
		{"job", ids.NewJobID, ids.IsJobID, ids.JobPrefix, "JOB_01M0KK53S0DQ9PSTK0206PRD7M"},
		{"reconciliation", ids.NewReconciliationID, ids.IsReconciliationID, ids.ReconciliationPrefix, "REC_01M0KK53S0DQ9PSTK0206PRD7M"},
		{"correlation", ids.NewCorrelationID, ids.IsCorrelationID, ids.CorrelationPrefix, "COR_01M0KK53S0DQ9PSTK0206PRD7M"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := tc.mint()
			if !strings.HasPrefix(got, tc.prefix) {
				t.Errorf("minted %q, want prefix %q", got, tc.prefix)
			}
			if !tc.valid(got) {
				t.Errorf("minted %q, which its own validator rejects", got)
			}
			if !tc.valid(tc.example) {
				t.Errorf("validator rejects the documented example %q", tc.example)
			}
			if bare := strings.TrimPrefix(got, tc.prefix); !ids.IsULID(bare) {
				t.Errorf("minted %q whose ULID part %q is not canonical", got, bare)
			}
			// A prefixed id is never a legal envelope identifier.
			if ids.IsULID(got) {
				t.Errorf("IsULID accepted the prefixed id %q", got)
			}
			// Cross-prefix confusion must not validate.
			if tc.valid("XXX_01M0KK53S0DQ9PSTK0206PRD7M") {
				t.Errorf("%s validator accepted a foreign prefix", tc.name)
			}
			if tc.valid(tc.prefix + "not-a-ulid") {
				t.Errorf("%s validator accepted a bad ULID body", tc.name)
			}
			if tc.valid(strings.TrimPrefix(tc.example, tc.prefix)) {
				t.Errorf("%s validator accepted an unprefixed ULID", tc.name)
			}
		})
	}
}

func TestIsPrefixedULID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		prefix string
		in     string
		want   bool
	}{
		{"exact match", ids.FilePrefix, "FIL_01M0KK53S0DQ9PSTK0206PRD7M", true},
		{"empty prefix accepts a bare ULID", "", "01M0KK53S0DQ9PSTK0206PRD7M", true},
		{"wrong prefix", ids.JobPrefix, "FIL_01M0KK53S0DQ9PSTK0206PRD7M", false},
		{"prefix only", ids.FilePrefix, "FIL_", false},
		{"double prefix", ids.FilePrefix, "FIL_FIL_01M0KK53S0DQ9PSTK0206PRD7M", false},
		{"trailing space", ids.FilePrefix, "FIL_01M0KK53S0DQ9PSTK0206PRD7M ", false},
		{"empty", ids.FilePrefix, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ids.IsPrefixedULID(tc.prefix, tc.in); got != tc.want {
				t.Errorf("IsPrefixedULID(%q, %q) = %t, want %t", tc.prefix, tc.in, got, tc.want)
			}
		})
	}
}

func TestStrip(t *testing.T) {
	t.Parallel()

	const bare = "01M0KK53S0DQ9PSTK0206PRD7M"

	tests := []struct {
		name         string
		in           string
		want         string
		wantPrefixed bool
	}{
		{"file id", ids.FilePrefix + bare, bare, true},
		{"job id", ids.JobPrefix + bare, bare, true},
		{"reconciliation id", ids.ReconciliationPrefix + bare, bare, true},
		{"correlation id", ids.CorrelationPrefix + bare, bare, true},
		{"bare ULID is unchanged", bare, bare, false},
		{"unknown prefix is unchanged", "ACC_" + bare, "ACC_" + bare, false},
		{"empty", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, prefixed := ids.Strip(tc.in)
			if got != tc.want || prefixed != tc.wantPrefixed {
				t.Errorf("Strip(%q) = (%q, %t), want (%q, %t)", tc.in, got, prefixed, tc.want, tc.wantPrefixed)
			}
		})
	}
}
