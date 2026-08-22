#!/usr/bin/env bash
#
# scripts/verify/CON-3.sh — verifies the five wave-A OpenAPI specs
# (customer, account, debt, delinquency, case).
#
# Asserts that each spec exists, pins OpenAPI 3.0.3 / info.version 1.0.0,
# declares exactly the paths the contracts/README.md ownership matrix assigns to
# it, carries the expected operationIds, sources every shared component from
# common.v1.yaml instead of forking it, honours the Idempotency-Key / If-Match /
# pagination / error-response conventions, lints clean under vacuum with zero
# errors, and — the point of the whole work package — feeds oapi-codegen
# successfully so that EXE-1 can generate a strict server from it.
#
# Includes expected-fail assertions, because a check that only ever accepts
# good input proves nothing.
#
# Environment: none (no Docker, no cloud). Needs bash, coreutils, python3
# (stdlib only), and the repo's pinned tools via `go -C tools tool <name>`.
# The codegen smoke test compiles generated code that imports
# github.com/oapi-codegen/runtime, so the FIRST run needs either network or a
# module cache that already holds it; later runs are offline.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

OPENAPI_DIR="contracts/openapi"
SPECS="customer account debt delinquency case"
TOOL="GOWORK=off go -C $REPO_ROOT/tools tool"

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

# check_eq <description> <expected> <actual>
check_eq() {
	if [ "$2" = "$3" ]; then
		ok "$1 ($3)"
	else
		bad "$1: expected '$2', got '$3'"
	fi
}

# count <pattern> <file>  -- match count, 0 when absent (grep -c exits 1 then)
count() {
	grep -c "$1" "$2" 2>/dev/null || true
}

# ---------------------------------------------------------------------------
# Expected shape of each spec. Paths come from the contracts/README.md URL
# path-ownership matrix; the service that owns the data owns the path, which is
# why account/debt/delinquency declare paths under /v1/accounts and /v1/customers
# (A§7.3).
# ---------------------------------------------------------------------------
spec_paths() {
	case "$1" in
	customer)
		printf '%s\n' \
			'/v1/customers/{customerId}'
		;;
	account)
		printf '%s\n' \
			'/v1/accounts/{accountId}' \
			'/v1/accounts/{accountId}/balance' \
			'/v1/accounts/{accountId}/history' \
			'/v1/customers/{customerId}/accounts'
		;;
	debt)
		printf '%s\n' \
			'/v1/accounts/{accountId}/debt' \
			'/v1/debts/{debtId}'
		;;
	delinquency)
		printf '%s\n' \
			'/v1/accounts/{accountId}/delinquency' \
			'/v1/accounts/{accountId}/delinquency/history' \
			'/v1/delinquency/bucket-configs'
		;;
	case)
		printf '%s\n' \
			'/v1/cases' \
			'/v1/cases/{caseId}' \
			'/v1/cases/{caseId}/activities' \
			'/v1/cases/{caseId}/assign' \
			'/v1/cases/{caseId}/close' \
			'/v1/cases/{caseId}/reopen' \
			'/v1/cases/{caseId}/resume' \
			'/v1/cases/{caseId}/suspend' \
			'/v1/customers/{customerId}/cases'
		;;
	esac
}

spec_operation_ids() {
	case "$1" in
	customer) printf '%s\n' getCustomer ;;
	account) printf '%s\n' getAccount getAccountBalance listAccountHistory listCustomerAccounts ;;
	debt) printf '%s\n' getAccountDebt getDebt ;;
	delinquency) printf '%s\n' getAccountDelinquency getBucketConfig listAccountDelinquencyHistory replaceBucketConfig ;;
	case) printf '%s\n' assignCase closeCase createCase getCase listCaseActivities listCases listCustomerCases reopenCase resumeCase suspendCase updateCase ;;
	esac
}

# operations, POST commands, PATCH operations, paginated lists
spec_counts() {
	case "$1" in
	customer) echo "1 0 0 0" ;;
	account) echo "4 0 0 2" ;;
	debt) echo "2 0 0 0" ;;
	delinquency) echo "4 0 0 1" ;;
	case) echo "11 6 1 3" ;;
	esac
}

echo "=== 1. specs exist and pin the contract-wide versions ==="
for s in $SPECS; do
	f="$OPENAPI_DIR/$s.v1.yaml"
	check "$s.v1.yaml exists" test -f "$f"
	check "$s.v1.yaml declares openapi 3.0.3 (not 3.1 — oapi-codegen)" \
		grep -qxF 'openapi: 3.0.3' "$f"
	check "$s.v1.yaml declares info.version 1.0.0" \
		grep -qxF '  version: 1.0.0' "$f"
	check "$s.v1.yaml declares the bearerAuth JWT security scheme" \
		grep -q 'bearerFormat: JWT' "$f"
	check "$s.v1.yaml applies bearerAuth at the document root" \
		grep -qxF '  - bearerAuth: []' "$f"
	check "$s.v1.yaml declares at least one server (vacuum oas3-api-servers)" \
		grep -qxF 'servers:' "$f"
done

echo
echo "=== 2. path ownership matches the contracts/README.md matrix ==="
for s in $SPECS; do
	f="$OPENAPI_DIR/$s.v1.yaml"
	spec_paths "$s" | sort >"$TMP/$s.paths.want"
	grep -oE '^  /v1/[^:]*' "$f" | sed 's/^  //' | sort >"$TMP/$s.paths.got"
	if diff -u "$TMP/$s.paths.want" "$TMP/$s.paths.got" >"$TMP/$s.paths.diff" 2>&1; then
		ok "$s.v1.yaml declares exactly its owned paths ($(wc -l <"$TMP/$s.paths.got" | tr -d ' '))"
	else
		bad "$s.v1.yaml path set differs from the ownership matrix:"
		cat "$TMP/$s.paths.diff" >&2
	fi
done
# A path may legitimately appear in several specs (routing is by full path), but
# never twice in the same spec, and never twice for the same method.
cat "$TMP"/*.paths.got | sort >"$TMP/all.paths"
check_eq "no duplicate path within any single spec" \
	"$(sort -u "$TMP/all.paths" | wc -l | tr -d ' ')" \
	"$(wc -l <"$TMP/all.paths" | tr -d ' ')"

echo
echo "=== 3. operationIds are the expected camelCase set, unique repo-wide ==="
: >"$TMP/all.ops"
for s in $SPECS; do
	f="$OPENAPI_DIR/$s.v1.yaml"
	spec_operation_ids "$s" | sort >"$TMP/$s.ops.want"
	grep -E '^      operationId: ' "$f" | awk '{print $2}' | sort >"$TMP/$s.ops.got"
	if diff -u "$TMP/$s.ops.want" "$TMP/$s.ops.got" >"$TMP/$s.ops.diff" 2>&1; then
		ok "$s.v1.yaml operationIds as expected ($(wc -l <"$TMP/$s.ops.got" | tr -d ' '))"
	else
		bad "$s.v1.yaml operationId set unexpected:"
		cat "$TMP/$s.ops.diff" >&2
	fi
	cat "$TMP/$s.ops.got" >>"$TMP/all.ops"
done
check_eq "22 operations across the five wave-A specs" 22 "$(wc -l <"$TMP/all.ops" | tr -d ' ')"
check_eq "every operationId is unique across the five specs" \
	"$(sort -u "$TMP/all.ops" | wc -l | tr -d ' ')" \
	"$(wc -l <"$TMP/all.ops" | tr -d ' ')"
check_fails "no operationId is PascalCase or snake_case" \
	grep -qE '^([A-Z]|[a-z0-9]+_)' "$TMP/all.ops"

echo
echo "=== 4. shared components come from common.v1.yaml, never forked ==="
COMMON="$OPENAPI_DIR/common.v1.yaml"
for s in $SPECS; do
	f="$OPENAPI_DIR/$s.v1.yaml"
	# No spec may declare its own Error contract (contracts/README.md §7).
	check_fails "$s.v1.yaml does not redeclare Error/ErrorDetail locally" \
		grep -qE '^    (Error|ErrorDetail):' "$f"
	# Every cross-file component reference must resolve in common.v1.yaml.
	missing=0
	for name in $(grep -oE "\./common\.v1\.yaml#/components/[a-z]+/[A-Za-z]+" "$f" |
		sed 's|.*/||' | sort -u); do
		grep -qE "^    $name:" "$COMMON" || {
			echo "  unresolved: $name" >&2
			missing=1
		}
	done
	check_eq "$s.v1.yaml common.v1.yaml refs all resolve" 0 "$missing"
done
check "every spec references the shared canned error responses" \
	sh -c 'for s in customer account debt delinquency case; do
		grep -q "common.v1.yaml#/components/responses/InternalError" contracts/openapi/$s.v1.yaml || exit 1
	done'

echo
echo "=== 5. HTTP conventions: idempotency, concurrency, pagination, errors ==="
for s in $SPECS; do
	f="$OPENAPI_DIR/$s.v1.yaml"
	set -- $(spec_counts "$s")
	want_ops="$1"
	want_post="$2"
	want_patch="$3"
	want_page="$4"

	check_eq "$s.v1.yaml POST commands" "$want_post" "$(count '^    post:$' "$f")"
	# A§21: every POST that creates a side effect requires Idempotency-Key.
	check_eq "$s.v1.yaml Idempotency-Key refs == POST commands" \
		"$want_post" "$(count 'parameters/IdempotencyKey' "$f")"
	# contracts/README.md §8/§9: If-Match is for PATCH only, never PUT/POST.
	check_eq "$s.v1.yaml PATCH operations" "$want_patch" "$(count '^    patch:$' "$f")"
	check_eq "$s.v1.yaml If-Match refs == PATCH operations" \
		"$want_patch" "$(count 'parameters/IfMatch' "$f")"
	# §10: limit + opaque cursor always travel together.
	check_eq "$s.v1.yaml limit refs == paginated lists" \
		"$want_page" "$(count 'parameters/Limit' "$f")"
	check_eq "$s.v1.yaml cursor refs == limit refs" \
		"$want_page" "$(count 'parameters/Cursor' "$f")"
	# §7: the full set of shared error responses on every operation.
	for r in BadRequest Unauthorized Forbidden InternalError; do
		check_eq "$s.v1.yaml $r on every operation" \
			"$want_ops" "$(count "responses/$r" "$f")"
	done
	# Paginated responses are {items, nextCursor}: one nextCursor per *List
	# schema (CaseList is shared by two operations, so this counts schemas).
	check_eq "$s.v1.yaml nextCursor properties == list schemas" \
		"$(count '^    [A-Za-z]*List:$' "$f")" \
		"$(count '^        nextCursor:$' "$f")"
done
check "case.v1.yaml documents 409 CASE_CLOSED and CASE_TRANSITION_INVALID" \
	sh -c 'grep -q CASE_CLOSED contracts/openapi/case.v1.yaml &&
		grep -q CASE_TRANSITION_INVALID contracts/openapi/case.v1.yaml'
check "case.v1.yaml documents 412 on PATCH via PreconditionFailed" \
	grep -q 'responses/PreconditionFailed' "$OPENAPI_DIR/case.v1.yaml"
check "case.v1.yaml sort is the closed 4-value enum" \
	sh -c 'python3 - <<PY
import re,sys
t=open("contracts/openapi/case.v1.yaml").read()
m=re.search(r"name: sort.*?enum:\n((?:[ \t]+- [^\n]*\n)+)", t, re.S)
want=["nextActionAt","-nextActionAt","priority","-priority"]
got=[l.strip()[2:] for l in m.group(1).splitlines()] if m else []
sys.exit(0 if sorted(got)==sorted(want) else 1)
PY'
check "case.v1.yaml requires cases:admin for close and reopen" \
	sh -c 'python3 - <<PY
import re,sys
t=open("contracts/openapi/case.v1.yaml").read()
ops=dict(re.findall(r"operationId: (\w+)\n(.*?)\n      tags:", t, re.S))
sys.exit(0 if all("cases:admin" in ops.get(o,"") for o in ("closeCase","reopenCase")) else 1)
PY'
check "delinquency.v1.yaml PUT bucket-configs requires delinquency:admin" \
	sh -c 'python3 - <<PY
import re,sys
t=open("contracts/openapi/delinquency.v1.yaml").read()
ops=dict(re.findall(r"operationId: (\w+)\n(.*?)\n      tags:", t, re.S))
d=ops.get("replaceBucketConfig","")
sys.exit(0 if "delinquency:admin" in d and "BUCKET_CONFIG_INVALID" in d
	and "contiguous" in d else 1)
PY'
check_fails "no PUT or POST carries If-Match (PATCH only)" \
	sh -c 'python3 - <<PY
import re,sys
bad=0
for s in "customer account debt delinquency case".split():
    t=open("contracts/openapi/%s.v1.yaml"%s).read()
    for m in re.finditer(r"^    (get|put|post|delete):$(.*?)(?=^    [a-z]+:$|^  /|\Z)",
                         t, re.S|re.M):
        if "parameters/IfMatch" in m.group(2):
            bad=1
sys.exit(0 if bad else 1)
PY'

echo
echo "=== 6. schema hygiene ==="
for s in $SPECS; do
	f="$OPENAPI_DIR/$s.v1.yaml"
	# Every object schema states additionalProperties explicitly (false, or
	# true for the one deliberately open member, CaseActivity.detail).
	check_eq "$s.v1.yaml additionalProperties on every object schema" \
		"$(count '^      type: object$' "$f")" \
		"$(count '^      additionalProperties: ' "$f")"
done
check "only case.v1.yaml CaseActivity.detail is an open object" \
	sh -c 'test "$(grep -h "additionalProperties: true" contracts/openapi/{customer,account,debt,delinquency,case}.v1.yaml | wc -l | tr -d " ")" = 1'
check "every ULID example matches the Crockford base32 ULID pattern" \
	sh -c 'python3 - <<PY
import re,sys
pat=re.compile(r"\b[0-9A-Z]{26}\b"); ok=re.compile(r"^[0-9A-HJKMNP-TV-Z]{26}$")
bad=[]
for s in "customer account debt delinquency case".split():
    p="contracts/openapi/%s.v1.yaml"%s
    for i,line in enumerate(open(p),1):
        bad += ["%s:%d: %s"%(p,i,m) for m in pat.findall(line) if not ok.match(m)]
print("\n".join(bad))
sys.exit(1 if bad else 0)
PY'
check "no spec writes files under contracts/examples/openapi (mirror rule)" \
	test '!' -d contracts/examples/openapi

echo
echo "=== 7. vacuum lint: zero errors per spec ==="
for s in $SPECS; do
	if eval "$TOOL vacuum lint -d -e ../$OPENAPI_DIR/$s.v1.yaml" >"$TMP/$s.vacuum" 2>&1; then
		ok "vacuum lint $s.v1.yaml: no errors"
	else
		bad "vacuum lint $s.v1.yaml reported errors:"
		cat "$TMP/$s.vacuum" >&2
	fi
done

echo
echo "=== 8. codegen smoke: oapi-codegen strict-server + go vet ==="
# oapi-codegen cannot follow a relative external $ref on its own; the documented
# workaround is `-import-mapping=./common.v1.yaml:-`, which inlines the shared
# components into the same Go package. common.v1.yaml has no paths, so its
# components are generated with `skip-prune`. This is exactly the two-step
# generation EXE-1 will wire into `make -C services/<name> generate`.
MOD="$TMP/smoke"
mkdir -p "$MOD"
printf 'module smoke\n\ngo 1.24\n' >"$MOD/go.mod"
codegen_ok=1
for s in $SPECS; do
	# Package name is always `smoke`, never the spec name: `case` is a Go
	# keyword and gofmt would reject `package case`.
	mkdir -p "$MOD/$s"
	if eval "$TOOL oapi-codegen -generate types,strict-server,skip-prune \
			-package smoke -o $MOD/$s/common_gen.go \
			$REPO_ROOT/$OPENAPI_DIR/common.v1.yaml" >>"$TMP/codegen.log" 2>&1 &&
		eval "$TOOL oapi-codegen -generate types,std-http-server,strict-server \
			-import-mapping='./common.v1.yaml:-' \
			-package smoke -o $MOD/$s/smoke_$s.go \
			$REPO_ROOT/$OPENAPI_DIR/$s.v1.yaml" >>"$TMP/codegen.log" 2>&1; then
		ok "oapi-codegen types,std-http-server,strict-server: $s.v1.yaml"
	else
		bad "oapi-codegen failed for $s.v1.yaml:"
		tail -20 "$TMP/codegen.log" >&2
		codegen_ok=0
	fi
done

if [ "$codegen_ok" -eq 1 ]; then
	if (cd "$MOD" && GOWORK=off GOFLAGS=-mod=mod go mod tidy) >"$TMP/tidy.log" 2>&1; then
		ok "scratch module resolves github.com/oapi-codegen/runtime"
		for s in $SPECS; do
			if (cd "$MOD" && GOWORK=off go vet "./$s") >"$TMP/vet.$s.log" 2>&1; then
				ok "go vet on generated code: $s"
			else
				bad "go vet failed on generated code for $s:"
				cat "$TMP/vet.$s.log" >&2
			fi
		done
	else
		bad "scratch module could not resolve github.com/oapi-codegen/runtime (needs network or a warm module cache):"
		tail -10 "$TMP/tidy.log" >&2
	fi
else
	bad "skipping go vet: codegen failed above"
fi

echo
echo "=== 9. the contracts module and the repo contract gate stay green ==="
if go -C contracts test -count=1 ./... >"$TMP/contracts-test.log" 2>&1; then
	ok "go -C contracts test ./... (no schemas added, nothing broken)"
else
	bad "go -C contracts test ./... failed:"
	tail -30 "$TMP/contracts-test.log" >&2
fi
if make contracts-check >"$TMP/contracts-check.log" 2>&1; then
	ok "make contracts-check"
else
	bad "make contracts-check failed:"
	grep -vE '^ *$' "$TMP/contracts-check.log" | tail -40 >&2
fi

echo
echo "=== 10. guards actually reject bad input (expected-fail) ==="
cp "$COMMON" "$TMP/common.v1.yaml"
# 10a. A dangling internal $ref must be a vacuum ERROR, not a warning.
sed 's|#/components/schemas/Customer|#/components/schemas/NoSuchSchema|' \
	"$OPENAPI_DIR/customer.v1.yaml" >"$TMP/dangling.v1.yaml"
check_fails "vacuum rejects a spec with an unresolvable \$ref" \
	eval "$TOOL vacuum lint -d -e $TMP/dangling.v1.yaml"
# 10b. Cross-file $refs genuinely need -import-mapping: prove the plain
#      invocation fails, so the workaround in step 8 is documented, not folklore.
check_fails "oapi-codegen without -import-mapping cannot follow ./common.v1.yaml" \
	eval "$TOOL oapi-codegen -generate types,std-http-server,strict-server \
		-package smoke -o $TMP/nomapping.go \
		$REPO_ROOT/$OPENAPI_DIR/customer.v1.yaml"
# 10c. A spec that pins OpenAPI 3.1 would break oapi-codegen: prove the version
#      assertion in step 1 is a real test and not a tautology.
sed 's|^openapi: 3.0.3$|openapi: 3.1.0|' "$OPENAPI_DIR/debt.v1.yaml" >"$TMP/v31.v1.yaml"
check_fails "the 3.0.3 assertion rejects a 3.1 document" \
	grep -qxF 'openapi: 3.0.3' "$TMP/v31.v1.yaml"

printf '\nCON-3: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
