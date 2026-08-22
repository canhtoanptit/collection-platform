#!/usr/bin/env bash
#
# scripts/verify/CON-7.sh — verifies the contracts CI gate: the orchestrator
# (scripts/ci/contracts-check.sh), the cross-artefact checker
# (tools/contractcheck), the vacuum ruleset, the released-file immutability gate,
# the oasdiff breaking-change gate, and the two workflows that run them.
#
# A gate is only worth what its negative tests prove, so most of this script is
# expected-FAIL assertions: each guard is shown rejecting exactly the mistake it
# exists to catch, and every mutation is checked to have actually changed
# something (a no-op mutation would turn an expected-FAIL into a lie).
#
# Every mutation happens on a throwaway copy of contracts/ or inside a scratch
# `git clone` under $(mktemp -d). The real working tree is never mutated and no
# tag is ever created in the real repository — section 8 asserts both.
#
# Environment: none (no Docker, no network, no cloud). Needs bash, coreutils, go,
# git and python3 (stdlib only).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

THROWAWAY_TAG="contracts-v0.0.0-test"
RULESET="contracts/vacuum-ruleset.yaml"
IMMUTABILITY="scripts/ci/check-contract-immutability.sh"
CONTRACTS_CHECK="scripts/ci/contracts-check.sh"

# Recorded now, compared in section 8: this script must not leave a tag behind.
git tag -l | sort >"$TMP/tags-before"

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

# sub <file> <old> <new> — one textual substitution that fails loudly when the old
# text is absent, so a probe can never silently mutate nothing.
cat >"$TMP/sub.py" <<'PY'
import pathlib, sys

path, old, new = pathlib.Path(sys.argv[1]), sys.argv[2], sys.argv[3]
text = path.read_text(encoding="utf-8")
if old not in text:
    print(f"probe setup failed: {old!r} is not in {path}", file=sys.stderr)
    sys.exit(1)
path.write_text(text.replace(old, new, 1), encoding="utf-8")
PY
sub() { python3 "$TMP/sub.py" "$@"; }

echo "=== 1. deliverables exist and are well formed ==="
for f in \
	"$RULESET" \
	"$CONTRACTS_CHECK" \
	"$IMMUTABILITY" \
	scripts/verify/CON-7.sh \
	scripts/verify/FND-0.sh \
	.github/workflows/contracts-ci.yml \
	.github/workflows/charts-ci.yml \
	tools/contractcheck/main.go; do
	check "exists: $f" test -f "$f"
done
for s in "$CONTRACTS_CHECK" "$IMMUTABILITY" scripts/verify/CON-7.sh scripts/verify/FND-0.sh; do
	check "bash -n clean: $s" bash -n "$s"
	check "has 'set -euo pipefail': $s" grep -qF 'set -euo pipefail' "$s"
done

# YAML structural sanity without PyYAML: parse with the yaml.v3 already pinned in
# the tools module (this python3 has no yaml module, and a workflow that does not
# parse is a workflow that never runs).
cat >"$TMP/yamlcheck.go" <<'GO'
package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Parses each file named on the command line and, for GitHub workflows, asserts
// the structure Actions requires: on + jobs, every job with runs-on and steps.
func main() {
	bad := 0
	for _, f := range os.Args[1:] {
		b, err := os.ReadFile(f)
		if err != nil {
			fmt.Printf("%s: unreadable: %v\n", f, err)
			bad++
			continue
		}
		var doc map[string]any
		if err := yaml.Unmarshal(b, &doc); err != nil {
			fmt.Printf("%s: not valid YAML: %v\n", f, err)
			bad++
			continue
		}
		if _, isWorkflow := doc["jobs"]; !isWorkflow {
			fmt.Printf("%s: parsed, %d top-level keys\n", f, len(doc))
			continue
		}
		if _, ok := doc["on"]; !ok {
			fmt.Printf("%s: workflow has no `on:` trigger\n", f)
			bad++
		}
		jobs, ok := doc["jobs"].(map[string]any)
		if !ok || len(jobs) == 0 {
			fmt.Printf("%s: workflow has no jobs\n", f)
			bad++
			continue
		}
		for name, raw := range jobs {
			job, ok := raw.(map[string]any)
			if !ok {
				fmt.Printf("%s: job %s is not a mapping\n", f, name)
				bad++
				continue
			}
			if _, ok := job["runs-on"]; !ok {
				fmt.Printf("%s: job %s has no runs-on\n", f, name)
				bad++
			}
			if steps, ok := job["steps"].([]any); !ok || len(steps) == 0 {
				fmt.Printf("%s: job %s has no steps\n", f, name)
				bad++
			}
		}
		fmt.Printf("%s: workflow parsed, %d job(s)\n", f, len(jobs))
	}
	os.Exit(bad)
}
GO
yamlcheck() {
	GOWORK=off go -C tools run "$TMP/yamlcheck.go" "$@"
}
check "YAML parses: workflows have on/jobs/runs-on/steps, ruleset and topic index parse" \
	yamlcheck ../.github/workflows/contracts-ci.yml ../.github/workflows/charts-ci.yml \
	"../$RULESET" ../contracts/asyncapi/collections.v1.yaml
check_fails "the YAML checker is not a rubber stamp (it rejects a workflow with no jobs)" \
	bash -c 'printf "name: broken\njobs: []\n" > "$TMP/broken.yml"; GOWORK=off go -C tools run "$TMP/yamlcheck.go" "$TMP/broken.yml"'

check "contracts-ci.yml checks out with fetch-depth: 0 (without tags the gates skip silently)" \
	grep -qE 'fetch-depth:[[:space:]]*0' .github/workflows/contracts-ci.yml
check "contracts-ci.yml runs make contracts-check" \
	grep -qF 'make contracts-check' .github/workflows/contracts-ci.yml
check "contracts-ci.yml triggers on tools/contractcheck/**" \
	grep -qF 'tools/contractcheck/**' .github/workflows/contracts-ci.yml
check "contracts-ci.yml triggers on scripts/ci/**" \
	grep -qF 'scripts/ci/**' .github/workflows/contracts-ci.yml
check "charts-ci.yml builds chart dependencies before linting" \
	grep -qF 'helm dependency build deployment/charts/render-test' .github/workflows/charts-ci.yml
check "charts-ci.yml lints both charts with --strict" \
	grep -qF 'helm lint --strict deployment/charts/collections-service deployment/charts/render-test' \
	.github/workflows/charts-ci.yml
check "charts-ci.yml asserts the render is non-empty and contains a Deployment" \
	grep -qF 'grep -q '"'"'^kind: Deployment'"'"'' .github/workflows/charts-ci.yml

echo
echo "=== 2. the whole gate is green on this tree ==="
check "bash $CONTRACTS_CHECK (JSON, harness, contractcheck, vacuum, immutability, oasdiff)" \
	bash "$CONTRACTS_CHECK"
check "make contracts-check" make contracts-check

echo
echo "=== 3. contractcheck rejects broken contracts (probe copy, never the real tree) ==="
CC="$TMP/contractcheck"
check "tools/contractcheck builds" env GOWORK=off go -C tools build -o "$CC" ./contractcheck

PROBE="$TMP/probe"
reset_probe() {
	rm -rf "$PROBE"
	mkdir -p "$PROBE"
	cp -R contracts "$PROBE/contracts"
}

# cc_must_fail <description> <expected message fragment>
# contractcheck must exit non-zero AND fail on the specific problem under test,
# not on collateral damage from a clumsy probe.
cc_must_fail() {
	local desc="$1" want="$2" out
	if out="$("$CC" -repo "$PROBE" 2>&1)"; then
		bad "$desc (contractcheck unexpectedly passed)"
		return
	fi
	if grep -qF "$want" <<<"$out"; then
		ok "$desc"
	else
		bad "$desc (failed, but not on '$want')"
		grep -F 'FAIL:' <<<"$out" | head -3 >&2
	fi
}

reset_probe
check "positive control: an untouched copy of contracts/ passes contractcheck" "$CC" -repo "$PROBE"

reset_probe
sub "$PROBE/contracts/asyncapi/collections.v1.yaml" \
	'../schemas/events/case/CaseCreated.v1.json' \
	'../schemas/events/case/ZzzProbeMissing.v1.json'
cc_must_fail "rejects an AsyncAPI payload \$ref pointing at a file that does not exist" \
	'does not resolve — no file at'

reset_probe
sub "$PROBE/contracts/asyncapi/collections.v1.yaml" \
	"\$ref: '#/components/messages/CaseCreated'" \
	"\$ref: '#/components/messages/ZzzProbeMissing'"
cc_must_fail "rejects an internal AsyncAPI \$ref with no definition" \
	'internal $ref "#/components/messages/ZzzProbeMissing" does not resolve'

reset_probe
rm -f "$PROBE/contracts/examples/events/treatment/TreatmentExecuted.v1.example.json"
cc_must_fail "rejects a catalogue event whose schema ships no golden example" \
	'ships no example'

reset_probe
rm -f "$PROBE/contracts/schemas/events/strategy/StrategyRetired.v1.json"
cc_must_fail "rejects a catalogue event with no payload schema at all" \
	'no schema — expected'

reset_probe
sub "$PROBE/contracts/examples/events/decision/DecisionMade.v1.example.json" \
	'STRATEGY_ELIGIBLE' 'ZZZ_PROBE_UNKNOWN_CODE'
cc_must_fail "rejects a reason code that is not in the registry (the vocabulary is closed)" \
	'unknown reason code ZZZ_PROBE_UNKNOWN_CODE'

reset_probe
sub "$PROBE/contracts/schemas/events/case/CaseCreated.v1.json" \
	'https://contracts.collections.internal/schemas/events/case/CaseCreated.v1.json' \
	'https://contracts.collections.internal/schemas/events/case/WrongName.v1.json'
cc_must_fail "rejects a schema whose \$id does not equal its path" \
	"must equal the file's path"

reset_probe
cp "$PROBE/contracts/examples/events/case/CaseCreated.v1.example.json" \
	"$PROBE/contracts/examples/events/case/zzz-probe-stray.json"
cc_must_fail "rejects a *.json under examples/ that is not named *.example.json" \
	'must be named <Name>.v<N>.example.json'

reset_probe
sub "$PROBE/contracts/openapi/case.v1.yaml" \
	"- \$ref: './common.v1.yaml#/components/parameters/IdempotencyKey'" \
	"- \$ref: './common.v1.yaml#/components/parameters/CorrelationId'"
cc_must_fail "rejects a POST command that does not require Idempotency-Key (A§21)" \
	'does not reference the Idempotency-Key parameter'

reset_probe
sub "$PROBE/contracts/openapi/model.v1.yaml" \
	'      operationId: scoreModelVersion' \
	"      operationId: scoreModelVersion
      parameters:
        - \$ref: './common.v1.yaml#/components/parameters/IdempotencyKey'"
cc_must_fail "rejects a stale idempotency exemption (an exempt POST that now carries the header)" \
	'but now references Idempotency-Key'

reset_probe
rm -rf "$PROBE/contracts"
check_fails "refuses to report success when there is no contracts/ directory to check" \
	"$CC" -repo "$PROBE"

echo
echo "=== 4. the vacuum ruleset rejects specs that break the house rules ==="
SPECS="$TMP/specs"
reset_specs() {
	rm -rf "$SPECS"
	mkdir -p "$SPECS"
	cp contracts/openapi/common.v1.yaml contracts/openapi/case.v1.yaml "$SPECS/"
}

vacuum_lint() {
	GOWORK=off go -C tools tool vacuum lint -b -d -e -a --no-clip -n error --min-score 0 \
		-r "../$RULESET" "$1"
}

# vacuum_must_flag <description> <rule-id> <spec>
vacuum_must_flag() {
	local desc="$1" rule="$2" spec="$3" out
	if out="$(vacuum_lint "$spec" 2>&1)"; then
		bad "$desc (vacuum unexpectedly passed)"
		return
	fi
	if grep -qF "rule: $rule" <<<"$out"; then
		ok "$desc"
	else
		bad "$desc (failed, but not on rule $rule)"
	fi
}

# insert_probe_op <operationId line> <responses block>
# Inserts one throwaway GET operation at the top of the paths object, so each
# negative test differs from a passing spec by exactly one intended defect.
insert_probe_op() {
	sub "$SPECS/case.v1.yaml" 'paths:
' "paths:
  /v1/zzz-probe:
    get:
$1
      summary: Probe operation inserted by scripts/verify/CON-7.sh
      description: Throwaway operation used to prove a lint rule fires. Never shipped.
      tags:
        - cases
      responses:
$2
"
}

RESP_200="        '200':
          description: Probe response.
          content:
            application/json:
              schema:
                type: string"
RESP_400="        '400':
          \$ref: './common.v1.yaml#/components/responses/BadRequest'"
RESP_500="        '500':
          \$ref: './common.v1.yaml#/components/responses/InternalError'"
OPID="      operationId: zzzProbeOperation"

reset_specs
check "positive control: the unmutated spec copy passes the ruleset" vacuum_lint "$SPECS/case.v1.yaml"

reset_specs
insert_probe_op "$OPID" "$RESP_200
$RESP_500"
vacuum_must_flag "flags an operation with no 4xx response (A§20)" \
	operation-4xx-response "$SPECS/case.v1.yaml"

reset_specs
insert_probe_op "$OPID" "$RESP_200
$RESP_400"
vacuum_must_flag "flags an operation with no 500 response (A§20)" \
	operation-5xx-response "$SPECS/case.v1.yaml"

reset_specs
insert_probe_op "      x-not-an-operation-id: zzzProbe" "$RESP_200
$RESP_400
$RESP_500"
vacuum_must_flag "flags an operation with no operationId (oapi-codegen needs it)" \
	operation-operationId "$SPECS/case.v1.yaml"

echo
echo "=== 5. the immutability gate (scratch clone + throwaway tag, never the real repo) ==="
CLONE="$TMP/clone"
check "git clone --no-tags (so the test's tag state is exactly what it creates)" \
	git clone --quiet --no-hardlinks --no-tags "$REPO_ROOT" "$CLONE"
# The gate under test is the working-tree version, not whatever is committed.
cp "$IMMUTABILITY" "$CONTRACTS_CHECK" "$CLONE/scripts/ci/"
cp "$RULESET" "$CLONE/contracts/"
cp tools/go.mod "$CLONE/tools/go.mod"
rm -rf "$CLONE/tools/contractcheck"
cp -R tools/contractcheck "$CLONE/tools/contractcheck"

clone_immutability() { bash "$CLONE/scripts/ci/check-contract-immutability.sh" "$@"; }
clone_says_skip() { clone_immutability 2>&1 | grep -q 'SKIP'; }

check "graceful skip: no contracts-v* tag => exit 0" clone_immutability
check "the skip is explicit (a silent skip is indistinguishable from a pass)" clone_says_skip
check_fails "the skip path is not a blanket pass: a bad --baseline is still an error" \
	clone_immutability --baseline no-such-ref-zzz

git -C "$CLONE" tag "$THROWAWAY_TAG"
check "clean tree at the tag => pass (baseline auto-discovered from the contracts-v* glob)" \
	clone_immutability
check "clean tree at the tag => pass (explicit --baseline)" \
	clone_immutability --baseline "$THROWAWAY_TAG"
check_fails "with a tag present the gate no longer skips" clone_says_skip

sub "$CLONE/contracts/schemas/events/case/CaseCreated.v1.json" '"title"' '"title-probe"'
check_fails "a modified released schema fails the gate" clone_immutability
check_fails "and fails with an explicit --baseline too" \
	clone_immutability --baseline "$THROWAWAY_TAG"
git -C "$CLONE" checkout --quiet -- contracts
check "restoring the file makes the gate pass again" clone_immutability

rm -f "$CLONE/contracts/schemas/events/case/CaseCreated.v1.json"
check_fails "a deleted released schema fails the gate (deletion is breaking too)" clone_immutability
git -C "$CLONE" checkout --quiet -- contracts

printf '\nprobe line appended by scripts/verify/CON-7.sh\n' >>"$CLONE/contracts/README.md"
check "contracts/README.md is exempt (documentation is not the contract)" clone_immutability
sub "$CLONE/contracts/schemas/README.md" '# ' '# probe '
check "contracts/**/README.md is exempt as well" clone_immutability
git -C "$CLONE" checkout --quiet -- contracts

echo
echo "=== 6. the oasdiff breaking-change gate ==="
BREAK="$TMP/break"
mkdir -p "$BREAK"
cp contracts/openapi/common.v1.yaml contracts/openapi/case.v1.yaml "$BREAK/"

oasdiff_breaking() {
	GOWORK=off go -C tools tool oasdiff breaking --fail-on ERR "$1" "$2"
}

check "an unchanged spec has no breaking changes against its committed version" \
	oasdiff_breaking 'HEAD:contracts/openapi/case.v1.yaml' ../contracts/openapi/case.v1.yaml
sub "$BREAK/case.v1.yaml" '  /v1/cases/{caseId}/reopen:' '  /v1/cases/{caseId}/reopen-renamed:'
check_fails "a removed API path is breaking (and --fail-on ERR makes it exit non-zero)" \
	oasdiff_breaking 'HEAD:contracts/openapi/case.v1.yaml' "$BREAK/case.v1.yaml"

echo
echo "=== 7. post-freeze dry run: the whole gate on the non-skip path ==="
# Proves nothing needs re-running once the lead tags contracts-v1.0: with a tag
# present, stages 5 and 6 take their real path instead of printing a skip notice.
tagged_gate() { (cd "$CLONE" && bash scripts/ci/contracts-check.sh); }
if tagged_gate >"$TMP/tagged.log" 2>&1; then
	ok "contracts-check is green in the tagged clone (immutability + oasdiff both active)"
else
	bad "contracts-check failed in the tagged clone (see below)"
	tail -20 "$TMP/tagged.log" >&2
fi
check_fails "the tagged run skipped nothing" grep -q 'SKIP' "$TMP/tagged.log"
check "the tagged run really compared against the tag" \
	grep -qF "baseline $THROWAWAY_TAG" "$TMP/tagged.log"

sub "$CLONE/contracts/openapi/case.v1.yaml" \
	'  /v1/cases/{caseId}/reopen:' '  /v1/cases/{caseId}/reopen-renamed:'
check_fails "contracts-check fails in the tagged clone when a released spec is changed" tagged_gate
git -C "$CLONE" checkout --quiet -- contracts

echo
echo "=== 8. the real repository was not touched ==="
git tag -l | sort >"$TMP/tags-after"
check "no tag was created, deleted or renamed in the real repo" \
	diff -q "$TMP/tags-before" "$TMP/tags-after"
check_fails "the throwaway tag does not exist in the real repo" \
	git rev-parse --verify --quiet "refs/tags/$THROWAWAY_TAG"
untracked_probes() { git status --porcelain contracts | grep -vF 'vacuum-ruleset.yaml'; }
check_fails "no probe artefact was left under contracts/" untracked_probes
check "the gate is still green after every probe" bash "$CONTRACTS_CHECK"

echo
printf 'CON-7: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
