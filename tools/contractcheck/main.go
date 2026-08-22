// Command contractcheck is the repo-wide contract gate (CON-7). It reads
// `contracts/` from disk and asserts the cross-artefact invariants that no
// single-file linter can see:
//
//	asyncapi-refs        every $ref in the AsyncAPI topic index resolves
//	catalogue-coverage   every normative event has a schema, an example and a topic
//	reason-codes         the reason-code vocabulary is closed
//	schema-ids           every schema's $id equals its path
//	example-naming       every example file is named *.example.json
//	post-idempotency     every POST command references the Idempotency-Key header
//
// It deliberately does NOT re-do what `contracts/validate_test.go` already does
// (compiling every schema as JSON Schema 2020-12, validating every example
// against its mirrored schema and the A§24 envelope, and the two orphan guards).
// `scripts/ci/contracts-check.sh` runs both, in that order, so a broken schema is
// reported by the harness that can say the most about it.
//
// Usage:
//
//	GOWORK=off go -C tools run ./contractcheck -repo <path to repo root>
//
// Exit code 0 = every check passed; 1 = at least one check failed (or the tool
// could not read what it needs, which is also a failure — a gate that cannot read
// its input must never report success).
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/canhtoanptit/collection-platform/tools/contractcheck/internal/check"
)

func main() {
	repoRoot := flag.String("repo", ".", "path to the repository root (the directory containing contracts/)")
	flag.Parse()

	if err := check.Run(os.Stdout, *repoRoot); err != nil {
		fmt.Fprintf(os.Stderr, "contractcheck: %v\n", err)
		os.Exit(1)
	}
}
