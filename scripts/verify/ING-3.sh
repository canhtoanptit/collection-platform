#!/usr/bin/env bash
#
# scripts/verify/ING-3.sh — verifies the SFTP file-feed contracts and the pure
# feedspec validation library.
#
# What it proves:
#   1. the four feed contracts and the meta-schema exist;
#   2. every contract's filename_regex compiles and captures business_date —
#      checked with python3 (stdlib only), independently of the Go code, because
#      a regex that only one engine accepts is a production surprise;
#   3. the golden fault matrix is complete (>= 6 cases per feed, each a CSV +
#      JSON pair) and its reason codes are documented in SPEC.md;
#   4. the library's tests, vet and coverage floor pass in BOTH module mode
#      (GOWORK=off) and workspace mode, since the module is now a go.work member;
#   5. feedspec stays a leaf: it imports neither the contracts nor the platform
#      module (the caller wires the contracts FS);
#   6. the whole repo still builds and the contract artefacts still validate.
#
# Environment: none (no Docker, no network, no cloud). Go toolchain + python3.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

FEEDS=(loan_accounts payments delinquency_snapshot legacy_daily_summary)
MIN_GOLDEN_CASES=6
MIN_COVERAGE=85

pass=0
fail=0
ok() {
	printf 'ok:   %s\n' "$1"
	pass=$((pass + 1))
}
bad() {
	printf 'FAIL: %s\n' "$1" >&2
	fail=$((fail + 1))
}

# check <description> <command...>        -- command must succeed
check() {
	local desc="$1"
	shift
	if "$@" >/dev/null 2>&1; then ok "$desc"; else bad "$desc"; fi
}

# check_fails <description> <command...>  -- command must FAIL (guard proof)
check_fails() {
	local desc="$1"
	shift
	if "$@" >/dev/null 2>&1; then
		bad "$desc (command unexpectedly succeeded)"
	else
		ok "$desc"
	fi
}

# run <description> <command...>          -- like check, but shows the output on failure
run() {
	local desc="$1"
	shift
	local log="$TMP/run.log"
	if "$@" >"$log" 2>&1; then
		ok "$desc"
	else
		bad "$desc"
		sed 's/^/      | /' "$log" >&2
	fi
}

echo "=== 1. deliverables exist ==="
check "exists: contracts/files/SPEC.md" test -f contracts/files/SPEC.md
for feed in "${FEEDS[@]}"; do
	check "exists: contracts/files/$feed.v1.yaml" test -f "contracts/files/$feed.v1.yaml"
done
for f in \
	ingestion/go.mod \
	ingestion/go.sum \
	ingestion/internal/feedspec/doc.go \
	ingestion/internal/feedspec/feed.go \
	ingestion/internal/feedspec/load.go \
	ingestion/internal/feedspec/rules.go \
	ingestion/internal/feedspec/validate.go \
	ingestion/internal/feedspec/total.go \
	ingestion/internal/feedspec/failure.go \
	ingestion/internal/feedspec/golden_test.go \
	ingestion/internal/feedspec/fuzz_test.go; do
	check "exists: $f" test -f "$f"
done
check "ingestion module path is declared" \
	grep -qxF 'module github.com/canhtoanptit/collection-platform/ingestion' ingestion/go.mod
check "go.work uses ./ingestion" grep -qE '^\s+\./ingestion$' go.work
check "go.work adds exactly one ingestion entry" \
	test "$(grep -cE '^\s+\./ingestion$' go.work)" -eq 1

echo
echo "=== 2. filename_regex, checked with python3 (stdlib only) ==="
# A deliberately independent implementation: it reads the YAML line, not the Go
# struct, so a contract that only the Go loader accepts is caught here.
cat >"$TMP/check_regex.py" <<'PY'
import re
import sys

GROUP = "business_date"


def field(text, key):
    m = re.search(r"(?m)^%s:\s*(.+?)\s*$" % re.escape(key), text)
    if not m:
        sys.exit("%s: missing %s" % (path, key))
    return m.group(1).strip().strip("'\"")


for path in sys.argv[1:]:
    with open(path, encoding="utf-8") as fh:
        text = fh.read()
    feed_id = field(text, "feed_id")
    pattern = field(text, "filename_regex")
    if r"(?P<business_date>\d{8})" not in pattern:
        sys.exit("%s: filename_regex lacks the exact (?P<business_date>\\d{8}) group: %s" % (path, pattern))
    try:
        rx = re.compile(pattern)
    except re.error as err:
        sys.exit("%s: filename_regex does not compile: %s" % (path, err))
    if GROUP not in rx.groupindex:
        sys.exit("%s: filename_regex has no %s group" % (path, GROUP))
    good = "%s_20260822.csv" % feed_id
    m = rx.fullmatch(good)
    if not m or m.group(GROUP) != "20260822":
        sys.exit("%s: filename_regex does not match %s" % (path, good))
    for reject in ("%s_20260822.csv.tmp" % feed_id, "%s_2026082.csv" % feed_id, good.upper()):
        if rx.fullmatch(reject):
            sys.exit("%s: filename_regex must not match %s" % (path, reject))
print("filename_regex ok for %d contract(s)" % (len(sys.argv) - 1))
PY

for feed in "${FEEDS[@]}"; do
	run "filename_regex compiles and captures business_date: $feed" \
		python3 "$TMP/check_regex.py" "contracts/files/$feed.v1.yaml"
done

# Guard proof: the same checker must reject contracts that would otherwise sail
# through unnoticed.
sed "s|filename_regex: .*|filename_regex: '^loan_accounts_[0-9]{8}\\\\.csv\$'|" \
	contracts/files/loan_accounts.v1.yaml >"$TMP/no_group.v1.yaml"
check_fails "python checker rejects a filename_regex without the business_date group" \
	python3 "$TMP/check_regex.py" "$TMP/no_group.v1.yaml"

sed "s|filename_regex: .*|filename_regex: '^loan_accounts_(?P<business_date>\\\\d{8})\\\\.csv[\$'|" \
	contracts/files/loan_accounts.v1.yaml >"$TMP/broken.v1.yaml"
check_fails "python checker rejects a filename_regex that does not compile" \
	python3 "$TMP/check_regex.py" "$TMP/broken.v1.yaml"

echo
echo "=== 3. golden fault matrix ==="
GOLDEN=ingestion/internal/feedspec/testdata/golden
for feed in "${FEEDS[@]}"; do
	csvs=$(find "$GOLDEN/$feed" -name '*.csv' 2>/dev/null | wc -l | tr -d ' ')
	jsons=$(find "$GOLDEN/$feed" -name '*.json' 2>/dev/null | wc -l | tr -d ' ')
	if [ "$csvs" -ge "$MIN_GOLDEN_CASES" ]; then
		ok "golden cases for $feed: $csvs (>= $MIN_GOLDEN_CASES)"
	else
		bad "golden cases for $feed: $csvs (< $MIN_GOLDEN_CASES)"
	fi
	if [ "$csvs" -eq "$jsons" ]; then
		ok "every golden input for $feed has an expectation file ($jsons)"
	else
		bad "golden pairs for $feed: $csvs csv vs $jsons json"
	fi
done

cat >"$TMP/check_golden.py" <<'PY'
import json
import pathlib
import re
import sys

golden = pathlib.Path(sys.argv[1])
spec = pathlib.Path(sys.argv[2]).read_text(encoding="utf-8")
documented = set(re.findall(r"`([A-Z][A-Z0-9_]{3,})`", spec))

problems = []
for feed_dir in sorted(p for p in golden.iterdir() if p.is_dir()):
    verdicts = set()
    for doc_path in sorted(feed_dir.glob("*.json")):
        doc = json.loads(doc_path.read_text(encoding="utf-8"))
        if not doc.get("input", {}).get("business_date"):
            problems.append("%s: input.business_date is required" % doc_path)
        expect = doc["expect"]
        verdicts.add(expect["ok"])
        for failure in expect["failures"]:
            reason = failure["reason"]
            if reason not in documented:
                problems.append("%s: reason %s is not documented in SPEC.md" % (doc_path, reason))
            if failure["severity"] not in ("ERROR", "WARN"):
                problems.append("%s: bad severity %s" % (doc_path, failure["severity"]))
        if expect["ok"] and any(f["severity"] == "ERROR" for f in expect["failures"]):
            problems.append("%s: ok=true with an ERROR failure" % doc_path)
        if not expect["ok"] and not any(f["severity"] == "ERROR" for f in expect["failures"]):
            problems.append("%s: ok=false with no ERROR failure" % doc_path)
    if verdicts != {True, False}:
        problems.append("%s: needs at least one passing and one failing case" % feed_dir)

if problems:
    sys.exit("\n".join(problems))
print("golden expectations consistent")
PY

run "golden expectations are internally consistent and use documented reason codes" \
	python3 "$TMP/check_golden.py" "$GOLDEN" contracts/files/SPEC.md

# The reason codes are contract surface (SPEC.md §11): quarantine rows, reject
# files and dashboards branch on them. Code and document must not drift.
cat >"$TMP/check_reasons.py" <<'PY'
import pathlib
import re
import sys

code = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
spec = pathlib.Path(sys.argv[2]).read_text(encoding="utf-8")

implemented = set(re.findall(r'Reason\s*=\s*"([A-Z][A-Z0-9_]+)"', code))
documented = set(re.findall(r"`([A-Z][A-Z0-9_]{3,})`", spec))

missing = sorted(implemented - documented)
if missing:
    sys.exit("reason codes emitted by feedspec but absent from SPEC.md: %s" % ", ".join(missing))
stale = sorted(r for r in documented - implemented if r.endswith(
    ("_MISSING", "_MALFORMED", "_MISMATCH", "_INVALID", "_UNKNOWN", "_FAILED", "_ERROR",
     "_VIOLATION", "_EXCEEDED", "_COUNT", "_TYPE", "_TRAILER", "_DUPLICATE", "_PRECISION",
     "_UNPARSEABLE")))
if stale:
    sys.exit("reason codes documented in SPEC.md but not emitted: %s" % ", ".join(stale))
print("%d reason codes, code and SPEC.md agree" % len(implemented))
PY

run "reason codes in feedspec and SPEC.md agree" \
	python3 "$TMP/check_reasons.py" ingestion/internal/feedspec/failure.go contracts/files/SPEC.md

echo
echo "=== 4. feedspec library: tests, vet, coverage (both build modes) ==="
run "module mode: GOWORK=off go test ./... -count=1" \
	env GOWORK=off go -C ingestion test ./... -count=1
run "workspace mode: go test ./... -count=1" \
	go -C ingestion test ./... -count=1
run "go vet ./..." go -C ingestion vet ./...
run "golden acceptance: go test -run TestGolden" \
	env GOWORK=off go -C ingestion test ./internal/feedspec/ -run TestGolden -count=1

covline=$(GOWORK=off go -C ingestion test ./internal/feedspec/ -cover -count=1 2>&1 | tail -1)
coverage=$(printf '%s\n' "$covline" | sed -n 's/.*coverage: \([0-9.]*\)% of statements.*/\1/p')
if [ -n "$coverage" ] && awk -v c="$coverage" -v m="$MIN_COVERAGE" 'BEGIN { exit !(c + 0 >= m) }'; then
	ok "coverage of internal/feedspec: $coverage% (>= $MIN_COVERAGE%)"
else
	bad "coverage of internal/feedspec: ${coverage:-unknown}% (< $MIN_COVERAGE%) [$covline]"
fi

echo
echo "=== 5. feedspec stays a leaf library ==="
check_fails "feedspec imports neither the contracts nor the platform module" \
	bash -c "go -C ingestion list -deps ./internal/feedspec | grep -E 'collection-platform/(contracts|platform)'"
check_fails "feedspec does no logging and keeps no globals it can mutate" \
	bash -c "grep -rnE '\"log\"|\"log/slog\"|\bos\.Getenv\b' ingestion/internal/feedspec --include='*.go' | grep -v '_test.go'"
# The module's declared direct dependencies are exactly the three the WP allows:
# cel-go (business rules), shopspring/decimal (control totals, never a float) and
# yaml.v3 (contract parsing). Everything else in go.mod must be indirect.
direct_deps=$(grep -E '^	[^ 	]+ v[^ 	]+$' ingestion/go.mod | awk '{print $1}' | sort | tr '\n' ' ')
if [ "$direct_deps" = "github.com/google/cel-go github.com/shopspring/decimal gopkg.in/yaml.v3 " ]; then
	ok "ingestion declares exactly cel-go, shopspring/decimal and yaml.v3 as direct dependencies"
else
	bad "ingestion direct dependencies are [$direct_deps]"
fi

echo
echo "=== 6. repo-wide build and contract artefacts ==="
run "make build-all" make build-all
run "make contracts-check" make contracts-check

echo
printf 'ING-3: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
