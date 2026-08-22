#!/usr/bin/env bash
# scripts/verify/DEC-1B.sh — proves the four decisioning OpenAPI specs
# (strategy, decision, treatment, model) are valid, lint-clean, self-contained,
# convention-compliant, and generate a Go strict-server that compiles and vets.
#
# Environment: none beyond the repo's pinned toolchain. `go`, `bash`, coreutils,
# and the tools module (`go -C tools tool vacuum|oapi-codegen`). The codegen
# smoke builds a throwaway Go module that requires
# `github.com/oapi-codegen/runtime`, so the Go module cache must be warm or the
# module proxy reachable; the script fails with a clear message if it is not.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

SPEC_DIR="$REPO_ROOT/contracts/openapi"
SPECS=(strategy decision treatment model)
# Pinned repo tooling. GOWORK=off is required: tools/ is not a go.work member,
# so its tool directives are invisible in workspace mode (Makefile TOOL).
tool() { env GOWORK=off go -C "$REPO_ROOT/tools" tool "$@"; }
# The generated runtime helper package used by oapi-codegen output. Pinned here
# rather than resolved by `go mod tidy` so the smoke module is reproducible.
RUNTIME_MOD='github.com/oapi-codegen/runtime@v1.7.0'

pass=0
fail=0
ok()  { printf 'ok:   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL: %s\n' "$1" >&2; fail=$((fail + 1)); }

check()       { if eval "$2" >/dev/null 2>&1; then ok "$1"; else bad "$1"; fi; }
check_fails() { if eval "$2" >/dev/null 2>&1; then bad "$1 (command unexpectedly succeeded)"; else ok "$1"; fi; }
# assert_eq <label> <expected> <actual>
assert_eq()   { if [ "$2" = "$3" ]; then ok "$1 ($3)"; else bad "$1: expected '$2', got '$3'"; fi; }

printf '=== DEC-1B: decisioning OpenAPI specs ===\n\n'

# ---------------------------------------------------------------- 1. presence
printf -- '--- files and document version\n'
for s in "${SPECS[@]}"; do
  check "$s.v1.yaml exists"                 "test -f '$SPEC_DIR/$s.v1.yaml'"
  check "$s.v1.yaml declares openapi 3.0.3" "grep -qx 'openapi: 3.0.3' '$SPEC_DIR/$s.v1.yaml'"
  check "$s.v1.yaml declares bearerAuth"    "grep -q 'bearerAuth:' '$SPEC_DIR/$s.v1.yaml'"
done

# DEC-1B owns exactly these four specs plus this script; nothing else.
check "no files under contracts/examples/openapi (mirror rule keeps it empty)" \
      "test -z \"\$(find '$REPO_ROOT/contracts/examples' -path '*openapi*' -type f 2>/dev/null)\""

# ------------------------------------------------------------- 2. vacuum lint
printf -- '\n--- vacuum lint (0 errors required per spec)\n'
for s in "${SPECS[@]}"; do
  # -e errors only; default --fail-severity=error means warnings do not fail.
  check "vacuum lint clean: $s.v1.yaml" \
        "tool vacuum lint -d -e -b '$SPEC_DIR/$s.v1.yaml'"
done
check "make contracts-check green" "make -C '$REPO_ROOT' contracts-check"

# Every inline `example:` and named `examples:` entry must validate against its
# own schema — otherwise the specs document values the servers would reject.
# vacuum reports that as `oas3-valid-schema-example`; assert zero violations,
# then prove the assertion bites by mutating one example.
for s in "${SPECS[@]}"; do
  check_fails "no invalid inline examples in $s.v1.yaml (oas3-valid-schema-example)" \
              "tool vacuum lint -d --all-results -b --no-clip '$SPEC_DIR/$s.v1.yaml' | grep -q oas3-valid-schema-example"
done

# Mutation fixtures live beside a copy of common.v1.yaml so the relative $refs
# still resolve — otherwise an expected-fail could pass for the wrong reason.
mkdir -p "$TMP/mut"
cp "$SPEC_DIR/common.v1.yaml" "$TMP/mut/common.v1.yaml"
grep -v '^      operationId: ' "$SPEC_DIR/model.v1.yaml" > "$TMP/mut/no-operation-id.yaml"
sed 's/^                  mode: ONLINE$/                  mode: BOGUS_MODE/' \
    "$SPEC_DIR/decision.v1.yaml" > "$TMP/mut/bad-example.yaml"
check       "mutation fixture actually mutated the example" \
            "grep -q 'mode: BOGUS_MODE' '$TMP/mut/bad-example.yaml'"
check_fails "vacuum rejects a spec with a missing operationId" \
            "tool vacuum lint -d -e -b '$TMP/mut/no-operation-id.yaml'"
check       "vacuum flags an example that violates its schema" \
            "tool vacuum lint -d --all-results -b --no-clip '$TMP/mut/bad-example.yaml' | grep -q oas3-valid-schema-example"

# ----------------------------------------------------- 3. repo conventions
printf -- '\n--- contract conventions (contracts/README.md)\n'

# 3a. operationIds: unique and camelCase.
for s in "${SPECS[@]}"; do
  ids="$(sed -n 's/^      operationId: \(.*\)$/\1/p' "$SPEC_DIR/$s.v1.yaml")"
  dupes="$(printf '%s\n' "$ids" | sort | uniq -d | tr '\n' ' ')"
  assert_eq "operationIds unique in $s.v1.yaml" "" "$(printf '%s' "$dupes" | tr -d ' ')"
  badcase="$(printf '%s\n' "$ids" | grep -vE '^[a-z][A-Za-z0-9]*$' | tr '\n' ' ' || true)"
  assert_eq "operationIds camelCase in $s.v1.yaml" "" "$(printf '%s' "$badcase" | tr -d ' ')"
done

# 3b. Every 4xx/5xx response $refs a canned response. The single documented
#     exception is model.v1.yaml's 503 (common.v1.yaml is released and has no
#     503); its body still $refs the shared A§20 Error schema.
nonstd=0
for s in "${SPECS[@]}"; do
  n="$(awk "/^ {8}'[45][0-9][0-9]': *\$/{getline nxt; if (nxt !~ /^ {10}\\\$ref: '\\.\\/common\\.v1\\.yaml#\\/components\\/responses\\//) c++} END{print c+0}" "$SPEC_DIR/$s.v1.yaml")"
  nonstd=$((nonstd + n))
done
assert_eq "every error response \$refs common.v1.yaml (no local exceptions)" "0" "$nonstd"
check "model 503 \$refs the shared common.v1.yaml ServiceUnavailable response" \
      "grep -q \"common.v1.yaml#/components/responses/ServiceUnavailable\" '$SPEC_DIR/model.v1.yaml'"
check "model.v1.yaml declares no local components.responses" \
      "! grep -qE '^  responses:' '$SPEC_DIR/model.v1.yaml'"
check "model.v1.yaml still documents both 503 codes in the operation description" \
      "grep -q 'MODEL_TIMEOUT' '$SPEC_DIR/model.v1.yaml' && grep -q 'MODEL_UNAVAILABLE' '$SPEC_DIR/model.v1.yaml'"
check "no spec defines its own error schema" \
      "! grep -qE '^ +(Error|ErrorDetail|ApiError|Problem):' $(printf \"'%s' \" \"$SPEC_DIR\"/{strategy,decision,treatment,model}.v1.yaml)"

# 3c. Idempotency-Key on every POST command. Documented exemptions:
#     treatment's provider webhook (external caller, HMAC-signed, idempotent on
#     (providerRef,status)) and model's score (pure function, no side effect).
for pair in "strategy:7:7" "decision:4:4" "treatment:2:1" "model:1:0"; do
  s="${pair%%:*}"; rest="${pair#*:}"; want_post="${rest%%:*}"; want_idem="${rest##*:}"
  got_post="$(grep -c '^    post:$' "$SPEC_DIR/$s.v1.yaml" || true)"
  got_idem="$(grep -c 'components/parameters/IdempotencyKey' "$SPEC_DIR/$s.v1.yaml" || true)"
  assert_eq "POST operations in $s.v1.yaml"        "$want_post" "$got_post"
  assert_eq "Idempotency-Key \$refs in $s.v1.yaml" "$want_idem" "$got_idem"
done
check "treatment provider webhook is the HMAC-authenticated exemption" \
      "grep -q 'security: \[\]' '$SPEC_DIR/treatment.v1.yaml' && grep -q 'X-Signature' '$SPEC_DIR/treatment.v1.yaml'"

# 3d. Shared parameters come from common.v1.yaml, never restated.
for s in "${SPECS[@]}"; do
  check "$s.v1.yaml uses common CorrelationId parameter" \
        "grep -q \"common.v1.yaml#/components/parameters/CorrelationId\" '$SPEC_DIR/$s.v1.yaml'"
done
for s in strategy decision treatment; do
  check "$s.v1.yaml paginates with common Limit + Cursor" \
        "grep -q \"common.v1.yaml#/components/parameters/Limit\" '$SPEC_DIR/$s.v1.yaml' && grep -q \"common.v1.yaml#/components/parameters/Cursor\" '$SPEC_DIR/$s.v1.yaml'"
done
check "decision PATCH takes the common If-Match parameter" \
      "grep -q \"common.v1.yaml#/components/parameters/IfMatch\" '$SPEC_DIR/decision.v1.yaml'"

# 3e. Every `type: object` declares additionalProperties. Exactly two are typed
#     string maps (strategy treatment params, treatment params); the rest are
#     `false`.
for s in "${SPECS[@]}"; do
  o="$(grep -c '^ *type: object$' "$SPEC_DIR/$s.v1.yaml" || true)"
  a="$(grep -c '^ *additionalProperties:' "$SPEC_DIR/$s.v1.yaml" || true)"
  assert_eq "every object declares additionalProperties in $s.v1.yaml" "$o" "$a"
done
maps=0
for s in "${SPECS[@]}"; do
  o="$(grep -c '^ *additionalProperties:' "$SPEC_DIR/$s.v1.yaml" || true)"
  f="$(grep -c '^ *additionalProperties: false$' "$SPEC_DIR/$s.v1.yaml" || true)"
  maps=$((maps + o - f))
done
# Four typed string/int maps, all deliberate and documented: treatment params
# in strategy.v1.yaml and treatment.v1.yaml, and contactHistory.byChannel7d in
# decision.v1.yaml and model.v1.yaml (open key set owned by contact-service,
# every value a non-negative integer).
assert_eq "typed-map exceptions to additionalProperties:false" "4" "$maps"

# 3f. Self-contained: no $ref out of the OpenAPI world into the JSON Schemas.
for s in "${SPECS[@]}"; do
  check "$s.v1.yaml has no \$ref into contracts/schemas" \
        "! grep -E \"^ *\\\$ref:\" '$SPEC_DIR/$s.v1.yaml' | grep -q 'schemas/'"
  check "$s.v1.yaml cites the normative JSON Schema paths in prose" \
        "grep -q 'schemas/decisioning/' '$SPEC_DIR/$s.v1.yaml'"
done

# 3g. Platform ids are ULIDs; source-system ids keep their own format.
for s in "${SPECS[@]}"; do
  check "$s.v1.yaml uses the ULID pattern for platform ids" \
        "grep -q '0-9A-HJKMNP-TV-Z' '$SPEC_DIR/$s.v1.yaml'"
done

# 3h. Money: decimal strings in major units, only inside context-document
#     shapes, and documented.
check "decision.v1.yaml documents the major-unit decimal-string money rule" \
      "grep -q 'DecimalAmount' '$SPEC_DIR/decision.v1.yaml' && grep -q 'major' '$SPEC_DIR/decision.v1.yaml'"
check "model.v1.yaml documents the major-unit decimal-string money rule" \
      "grep -q 'DecimalAmount' '$SPEC_DIR/model.v1.yaml' && grep -q 'major' '$SPEC_DIR/model.v1.yaml'"
check "no amountMinor / int64 minor-unit money leaked into the decisioning specs" \
      "! grep -qi 'amountMinor' $(printf \"'%s' \" \"$SPEC_DIR\"/{strategy,decision,treatment,model}.v1.yaml)"

# 3i. Scope names are documented in operation descriptions.
for scope in strategy-author business-approver risk-approver; do
  check "strategy.v1.yaml documents scope $scope" \
        "grep -q '$scope' '$SPEC_DIR/strategy.v1.yaml'"
done
check "decision.v1.yaml documents decisions:read and decisions:write scopes" \
      "grep -q 'decisions:read' '$SPEC_DIR/decision.v1.yaml' && grep -q 'decisions:write' '$SPEC_DIR/decision.v1.yaml'"
check "treatment.v1.yaml documents treatments:read and treatments:write scopes" \
      "grep -q 'treatments:read' '$SPEC_DIR/treatment.v1.yaml' && grep -q 'treatments:write' '$SPEC_DIR/treatment.v1.yaml'"
check "model.v1.yaml documents the scope its scoring call requires" \
      "grep -q 'decisions:write' '$SPEC_DIR/model.v1.yaml'"

# 3j. Alignment with DEC-1A's normative decisioning JSON Schemas. The inline
#     OpenAPI copies are self-contained by necessity, so drift is the standing
#     risk; these assertions are the mechanical guard.
printf -- '\n--- alignment with contracts/schemas/decisioning (DEC-1A, normative)\n'
CTXDOC="$REPO_ROOT/contracts/schemas/decisioning/context-document.v1.json"
check "DEC-1A context-document.v1.json is present to align against" "test -f '$CTXDOC'"

# contactability is exactly HIGH|MEDIUM|LOW|UNCONTACTABLE (a reachability band;
# permission lives in doNotContact and the CustomerUpdated constraint tri-state).
for f in decision model; do
  got="$(awk '/^        contactability:$/{f=1} f && /^          enum:$/{e=1;next} e && /^            - /{v=$0; sub(/^ *- /,"",v); printf "%s ", v; next} e{exit}' "$SPEC_DIR/$f.v1.yaml")"
  assert_eq "contactability enum in $f.v1.yaml" "HIGH MEDIUM LOW UNCONTACTABLE " "$got"
done
check "no stale UNKNOWN contactability member survives in any spec" \
      "! grep -qw UNKNOWN $(printf \"'%s' \" \"$SPEC_DIR\"/{strategy,decision,treatment,model}.v1.yaml)"
check "DEC-1A schema agrees: UNCONTACTABLE, not UNKNOWN" \
      "grep -q 'UNCONTACTABLE' '$CTXDOC' && ! grep -q '\"UNKNOWN\"' '$CTXDOC'"

# byChannel7d is an open-but-typed map keyed by SCREAMING_SNAKE channel codes.
# OpenAPI 3.0.3 has no propertyNames, so the key rule is documented here and
# enforced by the JSON Schema; the value type is enforced in both.
for f in decision model; do
  check "byChannel7d is an open typed map in $f.v1.yaml" \
        "awk '/^    ContextChannelCounts:\$/{b=1} b && /^      additionalProperties:\$/{a=1} b && a && /^        type: integer\$/{ok=1} b && /^      example:\$/{exit} END{exit !ok}' '$SPEC_DIR/$f.v1.yaml'"
  check "byChannel7d declares no closed lowerCamelCase channel properties in $f.v1.yaml" \
        "! awk '/^    ContextChannelCounts:\$/{b=1} b && /^        - (sms|email|letter|digital)\$/{print} b && /^    ContextContactHistory:\$/{exit}' '$SPEC_DIR/$f.v1.yaml' | grep -q ."
  check "byChannel7d examples use SCREAMING_SNAKE channel keys in $f.v1.yaml" \
        "grep -qE '^ +SMS: [0-9]+\$' '$SPEC_DIR/$f.v1.yaml' && ! grep -qE '^ +sms: [0-9]+\$' '$SPEC_DIR/$f.v1.yaml'"
done
check "DEC-1A schema agrees: byChannel7d keys are SCREAMING_SNAKE, values typed" \
      "grep -q 'propertyNames' '$CTXDOC'"

# A rule must be able to address a map entry, so a field path may carry an
# uppercase or underscored segment after the first: contactHistory.byChannel7d.SMS.
check "rule field paths can address a byChannel7d channel entry" \
      "printf 'contactHistory.byChannel7d.SMS' | grep -qE '^[a-z][A-Za-z0-9]*(\.[A-Za-z0-9][A-Za-z0-9_]*)+\$'"
check "strategy.v1.yaml ConditionLeaf.field uses the catalogue path pattern" \
      "grep -qF -- '[A-Za-z0-9][A-Za-z0-9_]*)+' '$SPEC_DIR/strategy.v1.yaml'"
for f in decision model; do
  check "$f.v1.yaml provenance path permits a map-entry segment" \
        "grep -qF -- '[A-Za-z0-9][A-Za-z0-9_]*){0,4}' '$SPEC_DIR/$f.v1.yaml'"
done

# The two inline context documents must stay structurally identical: the model
# scores exactly the document the decision was made on.
ctx_sig() {
  awk '
    /^    (DecisionSubject|ContextDelinquency|ContextAccount|ContextCustomer|ContextArrangement|ContextChannelCounts|ContextContactHistory|ContextProvenance|ContextDocument):$/ { inblk=1; print "## " $0; next }
    /^    [A-Za-z]+:$/ { inblk=0 }
    inblk && /^ *(type|format|pattern|enum|minimum|maximum|minItems|maxItems|minLength|maxLength|additionalProperties|required|properties|\$ref):/ { print }
    inblk && /^ *- [A-Za-z0-9_]+$/ { print }
  ' "$1"
}
ctx_sig "$SPEC_DIR/decision.v1.yaml" > "$TMP/ctx-decision.txt"
ctx_sig "$SPEC_DIR/model.v1.yaml"    > "$TMP/ctx-model.txt"
check "inline ContextDocument is structurally identical in decision and model specs" \
      "test -s '$TMP/ctx-decision.txt' && diff -q '$TMP/ctx-decision.txt' '$TMP/ctx-model.txt'"

# ---------------------------------------------- 4. A§16 catalogue and example
printf -- '\n--- A§16 decision API catalogue and worked example\n'
for p in \
  "post:/v1/decisions" \
  "post:/v1/decisions/batch" \
  "get:/v1/decisions/{decisionId}" \
  "get:/v1/decisions/{decisionId}/explanation" \
  "post:/v1/decisions/simulations" \
  "get:/v1/reference/reason-codes" ; do
  path="${p#*:}"
  check "A§16 path present: $path" "grep -qF '  $path:' '$SPEC_DIR/decision.v1.yaml'"
done
for v in "id: EARLY_COLLECTION" "version: 17" "type: CONTACT" "channel: SMS" \
         "- DPD_31_60" "- CHANNEL_ELIGIBLE" "model: PAYMENT_PROPENSITY" \
         "version: '8'" "customerId: C123" "accountId: A123" ; do
  check "A§16 worked-example value mirrored: $v" "grep -qF -- \"$v\" '$SPEC_DIR/decision.v1.yaml'"
done
check "A§60 lifecycle states all present in strategy.v1.yaml" \
      "for st in DRAFT TEST SIMULATED BUSINESS_APPROVED RISK_APPROVED REJECTED SCHEDULED ACTIVE RETIRED; do grep -q \"        - \$st\$\" '$SPEC_DIR/strategy.v1.yaml' || exit 1; done"
check "A§88 diff categories all present in decision.v1.yaml" \
      "for c in UNCATEGORIZED EXPECTED DATA_DIFFERENCE RULE_TRANSLATION_ERROR MISSING_RULE LEGACY_BUG NEW_BUG; do grep -q \"        - \$c\$\" '$SPEC_DIR/decision.v1.yaml' || exit 1; done"
check "documented business error codes present" \
      "grep -q 'STRATEGY_NOT_EDITABLE' '$SPEC_DIR/strategy.v1.yaml' \
       && grep -q 'STRATEGY_TRANSITION_INVALID' '$SPEC_DIR/strategy.v1.yaml' \
       && grep -q 'RULESET_PUBLISHED' '$SPEC_DIR/strategy.v1.yaml' \
       && grep -q 'CONTEXT_UNAVAILABLE' '$SPEC_DIR/decision.v1.yaml' \
       && grep -q 'MODEL_TIMEOUT' '$SPEC_DIR/model.v1.yaml'"

# ---------------------------------------- 5. model.v1.yaml custom-verb decision
printf -- '\n--- model scoring path: custom verb resolution\n'
check "model score path is the sub-resource form .../versions/{version}/score" \
      "grep -qF '  /v1/models/{modelId}/versions/{version}/score:' '$SPEC_DIR/model.v1.yaml'"
check "the ':score' colon custom verb is absent" \
      "! grep -qF '{version}:score:' '$SPEC_DIR/model.v1.yaml'"
check "model.v1.yaml documents why the colon form was rejected" \
      "grep -q 'ServeMux' '$SPEC_DIR/model.v1.yaml'"

# Expected-fail: the reason the colon form is unusable. net/http.ServeMux
# requires a wildcard to span a whole path segment, so `{version}:score`
# panics at registration — after codegen has happily emitted it.
mkdir -p "$TMP/muxprobe"
cat > "$TMP/muxprobe/go.mod" <<'GOMOD'
module muxprobe

go 1.24
GOMOD
cat > "$TMP/muxprobe/main.go" <<'GOPROG'
// Registers every pattern from the file named in os.Args[1] on a real
// net/http.ServeMux. Exits non-zero on the first pattern ServeMux rejects,
// naming it, so a routing conflict is a build-time fact rather than a
// start-up surprise.
package main

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
)

func main() {
	f, err := os.Open(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer f.Close()

	mux := http.NewServeMux()
	rc := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		pat := sc.Text()
		if pat == "" {
			continue
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr, "REJECTED %q: %v\n", pat, r)
					rc = 1
				}
			}()
			mux.HandleFunc(pat, func(http.ResponseWriter, *http.Request) {})
		}()
	}
	os.Exit(rc)
}
GOPROG
printf 'POST /v1/models/{modelId}/versions/{version}:score\n' > "$TMP/colon.txt"
printf 'POST /v1/models/{modelId}/versions/{version}/score\n' > "$TMP/slash.txt"
check_fails "net/http.ServeMux rejects '{version}:score' (why the path diverges from the plan sketch)" \
            "env GOWORK=off go -C '$TMP/muxprobe' run . '$TMP/colon.txt'"
check       "net/http.ServeMux accepts '{version}/score'" \
            "env GOWORK=off go -C '$TMP/muxprobe' run . '$TMP/slash.txt'"

# ------------------------------------------------------- 6. codegen smoke test
printf -- '\n--- oapi-codegen smoke: types + std-http-server + strict-server\n'
SMOKE="$TMP/smoke"
mkdir -p "$SMOKE"
cat > "$SMOKE/go.mod" <<'GOMOD'
module smoke

go 1.24
GOMOD
if ! (cd "$SMOKE" && env GOWORK=off go get "$RUNTIME_MOD" >/dev/null 2>&1); then
  printf 'FAIL: cannot resolve %s — warm the Go module cache or make the module proxy reachable\n' "$RUNTIME_MOD" >&2
  exit 1
fi

for s in "${SPECS[@]}"; do
  d="$SMOKE/$s"
  mkdir -p "$d"
  # common.v1.yaml is a components-only document with `paths: {}`; skip-prune
  # keeps its canned responses so the strict-server references resolve. Real
  # services do the same thing through api/oapi-codegen.yaml.
  check "codegen (common components) for $s" \
        "tool oapi-codegen -generate types,strict-server,skip-prune -package smoke -o '$d/common_gen.go' '$SPEC_DIR/common.v1.yaml'"
  # -import-mapping is required, not optional: every operation $refs
  # ./common.v1.yaml for its error responses, and oapi-codegen refuses an
  # unmapped external reference. `-` means "the types are in this package".
  check "codegen (types,std-http-server,strict-server) for $s.v1.yaml" \
        "tool oapi-codegen -generate types,std-http-server,strict-server -package smoke -import-mapping='./common.v1.yaml:-' -o '$d/smoke_$s.go' '$SPEC_DIR/$s.v1.yaml'"
done

check "generated code vets (all four specs, one package each)" \
      "(cd '$SMOKE' && env GOWORK=off go vet ./...)"

printf -- '\n--- generated operation counts\n'
for pair in "strategy:23" "decision:14" "treatment:4" "model:1"; do
  s="${pair%%:*}"; want="${pair##*:}"
  got="$(grep -c 'func (siw \*ServerInterfaceWrapper)' "$SMOKE/$s/smoke_$s.go" || true)"
  assert_eq "operations generated from $s.v1.yaml" "$want" "$got"
done

# --------------------------------------------- 7. generated routes are routable
printf -- '\n--- generated route patterns register on a real router\n'
for s in "${SPECS[@]}"; do
  grep -oE 'http\.Method[A-Za-z]+\+" "\+options\.BaseURL\+"[^"]+"' "$SMOKE/$s/smoke_$s.go" \
    | sed -E 's/http\.Method([A-Za-z]+)\+" "\+options\.BaseURL\+"(.*)"/\1 \2/' \
    | awk '{ print toupper($1) " " $2 }' > "$TMP/routes-$s.txt"
  check "$s.v1.yaml yielded route patterns" "test -s '$TMP/routes-$s.txt'"
done
for s in strategy treatment model; do
  check "net/http.ServeMux routes every $s.v1.yaml pattern" \
        "env GOWORK=off go -C '$TMP/muxprobe' run . '$TMP/routes-$s.txt'"
done
# Expected-fail, and documented in decision.v1.yaml's header: the A§16 path set
# is not expressible in net/http.ServeMux. `GET /v1/decisions/{decisionId}/…`
# overlaps `GET /v1/decisions/batch/{batchId}` and
# `GET /v1/decisions/simulations/{simulationId}` on paths like
# `/v1/decisions/batch/explanation`, and ServeMux considers neither more
# specific. decision-service must therefore be generated with `chi-server`.
# This assertion exists so nobody "fixes" it by renaming an A§16 path.
check_fails "net/http.ServeMux cannot route decision.v1.yaml (A§16 overlap — chi-server required)" \
            "env GOWORK=off go -C '$TMP/muxprobe' run . '$TMP/routes-decision.txt'"
check "decision.v1.yaml documents the chi-server router requirement" \
      "grep -q 'chi-server' '$SPEC_DIR/decision.v1.yaml' && grep -q 'ROUTER REQUIREMENT' '$SPEC_DIR/decision.v1.yaml'"
check "decision.v1.yaml generates cleanly with chi-server as well" \
      "tool oapi-codegen -generate types,chi-server,strict-server -package smoke -import-mapping='./common.v1.yaml:-' -o '$TMP/chi_decision.go' '$SPEC_DIR/decision.v1.yaml'"

# ------------------------------------------------ 8. contracts module still ok
printf -- '\n--- contracts module\n'
check "go -C contracts test ./... green" "go -C '$REPO_ROOT/contracts' test -count=1 ./..."
check "contracts module builds (embed FS intact)" "go -C '$REPO_ROOT/contracts' build ./..."

printf '\nDEC-1B: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
