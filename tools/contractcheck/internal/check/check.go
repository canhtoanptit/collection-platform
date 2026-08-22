// Package check holds the contract checks run by `tools/contractcheck` (CON-7).
// Each check is a function from the repository on disk to a result: notes that
// prove it looked at something, and problems that fail the build.
package check

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// contractsDir is the only directory these checks read, relative to the repo root.
const contractsDir = "contracts"

// schemaBaseURL is the naming authority every schema $id starts with. Nothing is
// ever fetched over the network (contracts/README.md §2).
const schemaBaseURL = "https://contracts.collections.internal/"

// Run executes every check against repoRoot, writing a sectioned report to w.
// It returns an error if any check failed, or if the contracts directory could
// not be read at all.
func Run(w io.Writer, repoRoot string) error {
	root, err := findRepoRoot(repoRoot)
	if err != nil {
		return err
	}
	r := &repo{root: root}

	fmt.Fprintf(w, "contractcheck: %s\n", r.path(contractsDir))

	// Every check always runs: one failure must not hide the rest, because a
	// contributor fixing contracts wants the whole list in one pass.
	checks := []struct {
		name string
		run  func(*repo) result
	}{
		{"asyncapi-refs", checkAsyncAPIRefs},
		{"catalogue-coverage", checkCatalogueCoverage},
		{"reason-codes", checkReasonCodes},
		{"schema-ids", checkSchemaIDs},
		{"example-naming", checkExampleNaming},
		{"post-idempotency", checkPostIdempotency},
	}

	failed, problems := 0, 0
	for _, c := range checks {
		res := c.run(r)
		res.print(w, c.name)
		if len(res.problems) > 0 {
			failed++
			problems += len(res.problems)
		}
	}

	fmt.Fprintln(w)
	if failed > 0 {
		fmt.Fprintf(w, "contractcheck: FAIL — %d/%d checks failed, %d problems\n", failed, len(checks), problems)
		return fmt.Errorf("%d of %d checks failed (%d problems)", failed, len(checks), problems)
	}
	fmt.Fprintf(w, "contractcheck: OK — %d/%d checks passed\n", len(checks), len(checks))
	return nil
}

// findRepoRoot resolves the -repo flag to a directory that contains contracts/.
// The nearest ancestor is accepted too, so the tool works from any working
// directory — notably from `tools/`, which is where `go -C tools run ./...` puts
// it. It never invents a root: if no ancestor has a contracts/ directory that is
// an error, because a gate that silently checks nothing is worse than no gate.
func findRepoRoot(start string) (string, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("resolving -repo %q: %w", start, err)
	}
	for dir := abs; ; {
		if fi, err := os.Stat(filepath.Join(dir, contractsDir)); err == nil && fi.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no %s/ directory at %s or in any of its parents", contractsDir, abs)
		}
		dir = parent
	}
}

// result is one check's outcome.
type result struct {
	notes    []string
	problems []string
}

func (res *result) notef(format string, a ...any) {
	res.notes = append(res.notes, fmt.Sprintf(format, a...))
}

func (res *result) problemf(format string, a ...any) {
	res.problems = append(res.problems, fmt.Sprintf(format, a...))
}

func (res *result) print(w io.Writer, name string) {
	fmt.Fprintf(w, "\n-- %s\n", name)
	for _, n := range res.notes {
		fmt.Fprintf(w, "   note: %s\n", n)
	}
	sort.Strings(res.problems)
	for _, p := range res.problems {
		fmt.Fprintf(w, "   FAIL: %s\n", p)
	}
	if len(res.problems) == 0 {
		fmt.Fprintf(w, "   PASS: %s\n", name)
	}
}

// repo is read-only access to the contract artefacts on disk.
type repo struct {
	root string
}

// path joins a repo-relative slash path onto the repo root.
func (r *repo) path(rel string) string {
	return filepath.Join(r.root, filepath.FromSlash(rel))
}

// exists reports whether a repo-relative path is a regular file.
func (r *repo) exists(rel string) bool {
	fi, err := os.Stat(r.path(rel))
	return err == nil && fi.Mode().IsRegular()
}

// readJSON decodes a repo-relative JSON file.
func (r *repo) readJSON(rel string) (any, error) {
	b, err := os.ReadFile(r.path(rel))
	if err != nil {
		return nil, err
	}
	var doc any
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", rel, err)
	}
	return doc, nil
}

// walkFiles lists, in lexical order, every file under a repo-relative root whose
// slash path ends in suffix. A missing root yields no files and no error: it is
// the caller's job to decide whether that is a problem.
func (r *repo) walkFiles(root, suffix string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(r.path(root), func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir():
			return nil
		}
		rel, relErr := filepath.Rel(r.root, p)
		if relErr != nil {
			return relErr
		}
		slashed := filepath.ToSlash(rel)
		if strings.HasSuffix(slashed, suffix) {
			out = append(out, slashed)
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("walking %s: %w", root, err)
	}
	sort.Strings(out)
	return out, nil
}

// glob returns the matches of a repo-relative slash glob, as slash paths.
func (r *repo) glob(pattern string) ([]string, error) {
	matches, err := filepath.Glob(r.path(pattern))
	if err != nil {
		return nil, fmt.Errorf("bad glob %q: %w", pattern, err)
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		rel, err := filepath.Rel(r.root, m)
		if err != nil {
			return nil, err
		}
		out = append(out, filepath.ToSlash(rel))
	}
	sort.Strings(out)
	return out, nil
}
