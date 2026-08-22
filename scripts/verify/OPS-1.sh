#!/usr/bin/env bash
#
# scripts/verify/OPS-1.sh — verifies the conventions & delegation pack itself.
#
# Asserts that CLAUDE.md, docs/wp-template.md, docs/ownership.yaml,
# docs/review-policy.md, docs/conventions.md, docs/gates/README.md and the two
# tools (check-ownership.sh, lint-runbook.sh) exist, are complete, and actually
# work — including expected-fail assertions, because a guard that never rejects
# anything is not a guard.
#
# Environment: none (no Docker, no network, no cloud). python3 + git only.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

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

echo "=== 1. deliverables exist ==="
for f in \
	CLAUDE.md \
	docs/wp-template.md \
	docs/ownership.yaml \
	docs/review-policy.md \
	docs/conventions.md \
	docs/gates/README.md \
	tools/check-ownership.sh \
	tools/lint-runbook.sh \
	scripts/verify/README.md \
	scripts/verify/OPS-1.sh; do
	check "exists: $f" test -f "$f"
done

echo
echo "=== 2. CLAUDE.md section markers ==="
while IFS= read -r marker; do
	[ -n "$marker" ] || continue
	check "CLAUDE.md has section: $marker" grep -qF "$marker" CLAUDE.md
done <<'EOF'
## 0. How to use this file
## 1. Repo map
## 2. Build and test commands
## 3. Go standards
## 4. OpenAPI-first
## 5. Events, outbox, inbox
## 6. HTTP APIs
## 7. What NOT to touch
## 8. Verification and definition of done
## 9. Commits
## 10. Hard rules (never negotiable)
EOF

echo
echo "=== 3. CLAUDE.md binding conventions ==="
while IFS= read -r marker; do
	[ -n "$marker" ] || continue
	check "CLAUDE.md states: $marker" grep -qF "$marker" CLAUDE.md
done <<'EOF'
docs/ownership.yaml
make verify WP=
make ownership-check WP=
makefiles/service.mk
go -C tools tool
int64
ISO-4217
oklog/ulid/v2
exhaustive
%w
strict-server
platform/outbox.Enqueue
platform/inbox.Dedupe
platform/events
platform/apierror
platform/httpkit
Idempotency-Key
collections.<context>
collections.dlq.<service>
A§20
A§21
A§24
A§25
Conventional Commits
Never `git push`
scripts/verify/<WP-ID>.sh
domain >=90%, module >=80%
simulator/
A§54
Airflow
EOF

echo
echo "=== 4. docs/wp-template.md template fields ==="
while IFS= read -r field; do
	[ -n "$field" ] || continue
	check "wp-template.md has field: $field" grep -qF "$field" docs/wp-template.md
done <<'EOF'
## WP
## Size
## Context
## Consumes
## Provides
## Deliverable paths
## Implementation requirements
## Acceptance criteria
## Out of scope
## Verification script
EOF

echo
echo "=== 5. docs/review-policy.md mandatory adversarial list ==="
for wp in LIB-7 SVC-5 SVC-6 ING-8 DEC-5 LIB-9 DEC-9 DEC-10 DEC-11 DEC-16 UI-4; do
	check "review-policy.md lists $wp for adversarial verification" \
		grep -qE "\*\*$wp" docs/review-policy.md
done
for marker in "acceptance met" "Contract adherence" "Idempotency" "Error contract A§20" \
	"Tests assert behaviour" "must not read the implementation"; do
	check "review-policy.md covers: $marker" grep -qiF "$marker" docs/review-policy.md
done

echo
echo "=== 6. docs/conventions.md operational conventions ==="
while IFS= read -r marker; do
	[ -n "$marker" ] || continue
	check "conventions.md states: $marker" grep -qF "$marker" docs/conventions.md
done <<'EOF'
FIL_
JOB_
REC_
COR_
UTC everywhere
business_date
dag_run.conf
QUERY_TAG
scripts/verify/<WP-ID>.sh
make verify WP=
Opus-class
strongest available model
EOF

echo
echo "=== 7. docs/gates/README.md phase-gate procedure ==="
while IFS= read -r marker; do
	[ -n "$marker" ] || continue
	check "gates/README.md states: $marker" grep -qF "$marker" docs/gates/README.md
done <<'EOF'
docs/gates/gate-<n>.md
docs/gates/evidence/
Every line is runnable
did not implement
No phase starts on a red gate
EOF

echo
echo "=== 8. ownership.yaml parses and enforces ==="
check "ownership.yaml parses (OPS-1 owns CLAUDE.md)" \
	bash tools/check-ownership.sh OPS-1 --files "CLAUDE.md"
check "OPS-1 owns its whole deliverable set" \
	bash tools/check-ownership.sh OPS-1 --files "CLAUDE.md
docs/wp-template.md
docs/ownership.yaml
docs/review-policy.md
docs/conventions.md
docs/gates/README.md
docs/gates/evidence/0/summary.md
tools/check-ownership.sh
tools/lint-runbook.sh
scripts/verify/README.md
scripts/verify/OPS-1.sh"
check_fails "OPS-1 may NOT touch services/case/main.go" \
	bash tools/check-ownership.sh OPS-1 --files "services/case/main.go"
check_fails "OPS-1 may NOT touch another WP's verify script" \
	bash tools/check-ownership.sh OPS-1 --files "scripts/verify/LIB-7.sh"
check "FND-0 owns Makefile" \
	bash tools/check-ownership.sh FND-0 --files "Makefile"
check_fails "FND-0 may NOT touch CLAUDE.md (OPS-1 owns it)" \
	bash tools/check-ownership.sh FND-0 --files "CLAUDE.md"
check "glob '**' spans directories (FND-0 / deployment chart)" \
	bash tools/check-ownership.sh FND-0 --files "deployment/charts/collections-service/templates/deployment.yaml"
check "Phase-1 entries resolve (CON-2 event schema)" \
	bash tools/check-ownership.sh CON-2 --files "contracts/schemas/events/case/CaseCreated.v1.json"
check_fails "CON-2 may NOT author decisioning event schemas (DEC-1 owns them)" \
	bash tools/check-ownership.sh CON-2 --files "contracts/schemas/events/decision/DecisionMade.v1.json"
check_fails "unknown WP id is rejected" \
	bash tools/check-ownership.sh NOPE-9 --files "CLAUDE.md"
check_fails "_default is not a usable WP id" \
	bash tools/check-ownership.sh _default --files "CLAUDE.md"
check "empty file list passes trivially" \
	bash tools/check-ownership.sh OPS-1 --files ""
check_fails "missing WP argument is a usage error" \
	bash tools/check-ownership.sh --files "CLAUDE.md"

printf 'BAD-WP:\n  paths: [a, b]\n' >"$TMP/bad-ownership.yaml"
check_fails "restricted-subset parser rejects nested/flow YAML" \
	bash tools/check-ownership.sh BAD-WP --ownership "$TMP/bad-ownership.yaml" --files "a"
printf 'GOOD-WP:\n  - "a/**"\n' >"$TMP/good-ownership.yaml"
check "restricted-subset parser accepts the documented form" \
	bash tools/check-ownership.sh GOOD-WP --ownership "$TMP/good-ownership.yaml" --files "a/b/c.txt"

echo
echo "=== 9. lint-runbook.sh enforces the D§82 heading set ==="
headings="$(bash tools/lint-runbook.sh --list)"
heading_count="$(printf '%s\n' "$headings" | grep -c . || true)"
check "lint-runbook --list prints 9 canonical headings" test "$heading_count" -eq 9
for h in Owner "Support group" SLA "Expected schedule" "Alert policy" "Retry policy" \
	Escalation Reconciliation "Runbook steps"; do
	check "canonical heading present: $h" grep -qxF "$h" <<<"$headings"
done

: >"$TMP/complete-runbook.md"
{
	echo "# Runbook: example pipeline"
	echo
	while IFS= read -r h; do
		[ -n "$h" ] || continue
		printf '## %s\n\nTBD\n\n' "$h"
	done <<<"$headings"
} >"$TMP/complete-runbook.md"

{
	echo "# Runbook: incomplete pipeline"
	echo
	echo "## Owner"
	echo "team-collections-ops"
	echo
	echo "## SLA"
	echo "02:00 UTC"
} >"$TMP/incomplete-runbook.md"

check "complete fixture passes lint-runbook" \
	bash tools/lint-runbook.sh "$TMP/complete-runbook.md"
check_fails "heading-missing fixture fails lint-runbook" \
	bash tools/lint-runbook.sh "$TMP/incomplete-runbook.md"
check "lint-runbook names each missing heading" \
	grep -q "missing required D§82 heading: 'Escalation'" \
	<(bash tools/lint-runbook.sh "$TMP/incomplete-runbook.md" 2>&1 || true)
check_fails "lint-runbook rejects a non-existent file" \
	bash tools/lint-runbook.sh "$TMP/does-not-exist.md"
check_fails "lint-runbook with no arguments is a usage error" \
	bash tools/lint-runbook.sh

echo
echo "=== 10. shell hygiene of OPS-1 scripts ==="
for s in tools/check-ownership.sh tools/lint-runbook.sh scripts/verify/OPS-1.sh; do
	check "bash -n clean: $s" bash -n "$s"
	check "has 'set -euo pipefail': $s" grep -qF 'set -euo pipefail' "$s"
	check "has bash shebang: $s" grep -qF '#!/usr/bin/env bash' "$s"
done

echo
echo "=== 11. root Makefile wiring ==="
check "Makefile has a 'verify' target running scripts/verify/\$(WP).sh" \
	grep -qF 'bash scripts/verify/$(WP).sh' Makefile
check "Makefile has an 'ownership-check' target running tools/check-ownership.sh" \
	grep -qF 'bash tools/check-ownership.sh $(WP)' Makefile

echo
printf 'OPS-1: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
