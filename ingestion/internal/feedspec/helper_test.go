package feedspec_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/canhtoanptit/collection-platform/ingestion/internal/feedspec"
)

// contractsFS returns the repository's real file-feed contracts.
//
// This is a *file* read of another module's directory, deliberately not an
// import of the contracts Go module: feedspec is a leaf library that takes an
// fs.FS from its caller, and the tests must validate the contracts that actually
// ship rather than copies that can drift. If the directory is missing, that is a
// failure, never a skip — a silently skipped contract test is how a broken
// contract reaches production.
func contractsFS(t testing.TB) fs.FS {
	t.Helper()
	dir := filepath.Join("..", "..", "..", "contracts", "files")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("contracts/files not readable from the ingestion module: %v", err)
	}
	return os.DirFS(dir)
}

// loadFeed loads one real contract by feed id.
func loadFeed(t testing.TB, feedID string) *feedspec.Feed {
	t.Helper()
	f, err := feedspec.Load(contractsFS(t), feedID+".v1.yaml")
	if err != nil {
		t.Fatalf("Load(%s): %v", feedID, err)
	}
	return f
}

// reasons flattens failures to "REASON[/column][#rule]" strings, which is what
// the tests assert on: the stable codes and their location, never the prose of a
// detail message.
func reasons(fs []feedspec.Failure) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		s := string(f.Reason)
		if f.Column != "" {
			s += "/" + f.Column
		}
		if f.RuleID != "" {
			s += "#" + f.RuleID
		}
		out = append(out, s)
	}
	return out
}

func equalReasons(got []feedspec.Failure, want []string) bool {
	g := reasons(got)
	if len(g) != len(want) {
		return false
	}
	for i := range g {
		if g[i] != want[i] {
			return false
		}
	}
	return true
}

// edit is a single textual substitution applied to a base contract.
type edit struct{ old, new string }

// mutate applies edits to a base contract, failing the test when an edit target
// is absent — so a table entry cannot silently stop testing anything.
func mutate(t testing.TB, base string, edits ...edit) string {
	t.Helper()
	for _, e := range edits {
		if !strings.Contains(base, e.old) {
			t.Fatalf("edit target %q is not present in the base contract", e.old)
		}
		base = strings.Replace(base, e.old, e.new, 1)
	}
	return base
}
