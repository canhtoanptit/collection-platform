#!/usr/bin/env bash
#
# scripts/ci/contracts-check.sh — the contract gate (CON-7). `make contracts-check`
# runs this; so does .github/workflows/contracts-ci.yml on every PR that touches
# contracts/, scripts/ci/ or tools/contractcheck/.
#
# Six stages, cheapest and most specific first, so the first thing you read is the
# thing that is wrong:
#
#   1. JSON syntax        every *.json under contracts/ parses
#   2. contracts harness  go -C contracts test ./...  — schemas compile as JSON
#                         Schema 2020-12, every example validates against its
#                         mirrored schema and (for events) the A§24 envelope, and
#                         both orphan guards hold. This also proves the embed FS.
#   3. contractcheck      cross-artefact invariants no single-file linter sees:
#                         AsyncAPI refs resolve, the event catalogue is covered,
#                         the reason-code vocabulary is closed, $id == path,
#                         examples are named *.example.json, and every POST
#                         command requires Idempotency-Key (A§21).
#   4. vacuum lint        every OpenAPI spec, including the components-only
#                         common.v1.yaml, against contracts/vacuum-ruleset.yaml
#                         (recommended + operationId + 4xx + 500).
#   5. immutability       no released file under contracts/ has been edited
#                         (skips with a notice until contracts-v1.0 exists).
#   6. oasdiff breaking   per spec, worktree vs the freeze tag (same skip).
#
# Every stage runs even when an earlier one fails: a contributor fixing contracts
# wants the whole list, not one error at a time. Exit 0 = all green.
#
# Environment: none — no Docker, no network, no cloud. Needs go, git, python3
# (stdlib only) and the pinned tools from tools/go.mod (vacuum, oasdiff), which
# are invoked as `GOWORK=off go -C tools tool <name>` because tools/ is
# deliberately outside the go.work workspace.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

CONTRACTS_DIR="contracts"
RULESET="$CONTRACTS_DIR/vacuum-ruleset.yaml"
TAG_GLOB="contracts-v*"

fail=0

section() { printf '\n=== %s\n' "$*"; }
note() { printf '    %s\n' "$*"; }
bad() {
	printf 'FAIL: %s\n' "$*" >&2
	fail=1
}

# tool runs a pinned build tool from tools/go.mod. GOWORK=off is required or the
# tool directives are invisible; -C tools means every *path* argument is relative
# to tools/, hence the ../ prefixes below.
tool() { GOWORK=off go -C tools tool "$@"; }

# ----------------------------------------------------------------- 1. JSON ----

section "1/6 JSON syntax — every *.json under $CONTRACTS_DIR/"
json_report="$(
	python3 - "$CONTRACTS_DIR" <<'PY'
import json, pathlib, sys

root = pathlib.Path(sys.argv[1])
bad, n = [], 0
for p in sorted(root.rglob("*.json")):
    n += 1
    try:
        json.loads(p.read_text(encoding="utf-8"))
    except Exception as exc:                                        # noqa: BLE001
        bad.append(f"invalid JSON: {p}: {exc}")
print(f"{n} JSON artefacts parsed, {len(bad)} invalid")
for b in bad:
    print(b)
sys.exit(1 if bad else 0)
PY
)" || bad "invalid JSON under $CONTRACTS_DIR/"
printf '%s\n' "$json_report" | while IFS= read -r line; do note "$line"; done

# ------------------------------------------------------- 2. contracts test ----

section "2/6 contracts harness — go -C $CONTRACTS_DIR test ./..."
if ! go -C "$CONTRACTS_DIR" test -count=1 ./...; then
	bad "the contracts self-validation harness failed (schemas, examples, envelope wrapping, orphan guards)"
fi

# -------------------------------------------------------- 3. contractcheck ----

section "3/6 contractcheck — cross-artefact invariants"
if ! GOWORK=off go -C tools run ./contractcheck -repo "$REPO_ROOT"; then
	bad "tools/contractcheck reported problems (see the FAIL lines above)"
fi

# --------------------------------------------------------- 4. vacuum lint -----

section "4/6 vacuum lint — every spec against $RULESET"
if [ ! -f "$RULESET" ]; then
	bad "$RULESET is missing — the lint gate would silently degrade to vacuum's defaults"
else
	specs=$(find "$CONTRACTS_DIR/openapi" -name '*.yaml' | sort)
	if [ -z "$specs" ]; then
		bad "no OpenAPI specs found under $CONTRACTS_DIR/openapi"
	else
		note "$(echo "$specs" | wc -l | tr -d ' ') specs (including the components-only common.v1.yaml):"
		echo "$specs" | sed 's/^/      /'
		# Quiet first (one process for all specs, ~1s), verbose only on failure.
		# --min-score 0: severity is the gate, not vacuum's quality score, so a
		# document never fails for warnings alone (see the ruleset header).
		if ! tool vacuum lint -b -x -n error --min-score 0 -r "../$RULESET" \
			--globbed-files "../$CONTRACTS_DIR/openapi/*.yaml"; then
			tool vacuum lint -b -d -e -a --no-clip -n error --min-score 0 -r "../$RULESET" \
				--globbed-files "../$CONTRACTS_DIR/openapi/*.yaml" || true
			bad "vacuum reported error-severity violations"
		else
			note "no error-severity violations"
		fi
	fi
fi

# --------------------------------------------------------- 5. immutability ----

section "5/6 released-file immutability"
if ! bash scripts/ci/check-contract-immutability.sh; then
	bad "a released contract file was modified — see the rule above"
fi

# ------------------------------------------------------ 6. oasdiff breaking ----

section "6/6 oasdiff breaking — worktree vs the freeze tag"
baseline="$(git tag -l "$TAG_GLOB" | sort -V | tail -1)"
if [ -z "$baseline" ]; then
	note "SKIP — no $TAG_GLOB tag yet, so there is no published API to break."
	note "Activates automatically once the lead tags contracts-v1.0 (CI needs fetch-depth: 0)."
else
	note "baseline $baseline"
	for spec in $(find "$CONTRACTS_DIR/openapi" -name '*.yaml' | sort); do
		if ! git cat-file -e "$baseline:$spec" 2>/dev/null; then
			note "$spec: new since $baseline, nothing to compare"
			continue
		fi
		# --fail-on ERR is mandatory: `oasdiff breaking` prints breaking changes
		# but exits 0 without it, which would make this stage decorative.
		if ! tool oasdiff breaking --fail-on ERR "$baseline:$spec" "../$spec"; then
			bad "$spec has breaking changes against $baseline — ship a new vN spec instead (contracts/README.md §12)"
		fi
	done
fi

# ------------------------------------------------------------------ result ----

echo
if [ "$fail" -ne 0 ]; then
	echo "contracts-check: FAILED"
	exit 1
fi
echo "contracts-check: OK"
