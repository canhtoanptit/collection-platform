#!/usr/bin/env bash
# scripts/verify/CON-4.sh — proves the wave-B/C OpenAPI contracts
# (arrangement, payment, contact, recovery, agency, legal) lint clean, generate
# compilable strict-server code, obey the contracts/README.md conventions, and
# have not lost any endpoint. Environment: none (no compose stack, no cluster).
#
# Needs the Go module cache to already hold github.com/oapi-codegen/runtime
# (populated by any earlier codegen run, or by one network fetch).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

TMP="$(mktemp -d)"
# /tmp/smoke_*.go are the paths the CON-4 acceptance criteria name, so codegen
# writes exactly there — and they are removed again on exit, so the script is
# re-runnable back to back with the same result.
cleanup() {
  rm -rf "$TMP"
  rm -f /tmp/smoke_common.go
  for s in arrangement payment contact recovery agency legal; do
    rm -f "/tmp/smoke_$s.go"
  done
}
trap cleanup EXIT

TOOL=(env GOWORK=off go -C "$REPO_ROOT/tools" tool)
OPENAPI="$REPO_ROOT/contracts/openapi"
COMMON="$OPENAPI/common.v1.yaml"

# CON-4's six specs, and the number of operations each must expose. The counts
# are the regression guard: dropping an endpoint is the failure mode a lint
# cannot see. A function rather than an associative array — /bin/bash on macOS
# is 3.2 and has none.
SPECS=(arrangement payment contact recovery agency legal)
want_ops() {
  case "$1" in
    arrangement) echo 11 ;;  # A§17 six + GET /v1/arrangements + four promise operations
    payment)     echo 5  ;;  # intake, get, allocations get+post, account payment history
    contact)     echo 5  ;;  # create, get, outcome, by customer, by case
    recovery)    echo 4  ;;  # create, get, by account, recovery-metrics
    agency)      echo 8  ;;  # A§18 four + agencies create/list/get + placement fees
    legal)       echo 6  ;;  # referrals create/get, cases create/get, status-changes, case legal view
    *) echo "want_ops: unknown spec $1" >&2; return 1 ;;
  esac
}

pass=0
fail=0
ok()  { printf 'ok:   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL: %s\n' "$1" >&2; fail=$((fail + 1)); }

check()       { if eval "$2" >/dev/null 2>&1; then ok "$1"; else bad "$1"; fi; }
check_fails() { if eval "$2" >/dev/null 2>&1; then bad "$1 (command unexpectedly succeeded)"; else ok "$1"; fi; }

# ---------------------------------------------------------------------------
# The convention checker. contracts/README.md is normative; these are the
# rules a reviewer would otherwise have to read 3000 lines of YAML to confirm.
# Line-oriented on purpose: python3 stdlib has no YAML parser, and the specs
# are written in a fixed two-space style, so the checks stay exact without
# adding a dependency (scripts/verify/README.md rule 10).
# ---------------------------------------------------------------------------
cat >"$TMP/conventions.py" <<'PY'
import re
import sys

ULID_RE = re.compile(r'^[0-9A-HJKMNP-TV-Z]{26}$')
ULID_PATTERN = "'^[0-9A-HJKMNP-TV-Z]{26}$'"
CURRENCY_PATTERN = "'^[A-Z]{3}$'"
ERROR_CODES = ('400', '401', '403', '404', '409', '412', '422', '500')

problems = []


def report(path, lineno, msg):
    problems.append(f'{path}:{lineno}: {msg}')


for path in sys.argv[1:]:
    with open(path, encoding='utf-8') as fh:
        lines = fh.read().splitlines()

    # -- no operation may restate the shared error contract -----------------
    for i, line in enumerate(lines, 1):
        if re.match(r'^    (Error|ErrorDetail):\s*$', line):
            report(path, i, 'redeclares the shared Error/ErrorDetail schema; '
                            "$ref './common.v1.yaml#/components/schemas/Error' instead")

    # -- every object schema closes itself ---------------------------------
    if 'additionalProperties: false' not in '\n'.join(lines):
        report(path, 0, 'no `additionalProperties: false` anywhere — object schemas must be closed')
    for i, line in enumerate(lines, 1):
        if 'additionalProperties: true' in line or re.search(r'additionalProperties:\s*\{', line):
            report(path, i, 'open object schema: `additionalProperties` must be false')

    # -- every error status code delegates to common.v1.yaml ---------------
    for i, line in enumerate(lines, 1):
        m = re.match(r"^\s+'(\d{3})':", line)
        if not m or m.group(1) not in ERROR_CODES:
            continue
        window = ' '.join(lines[i - 1:i + 2])
        if 'common.v1.yaml#/components/responses/' not in window:
            report(path, i, f'{m.group(1)} response does not $ref '
                            './common.v1.yaml#/components/responses/*')

    # -- operationIds: present, unique, camelCase --------------------------
    seen = {}
    for i, line in enumerate(lines, 1):
        m = re.match(r'^      operationId: (\S+)\s*$', line)
        if not m:
            continue
        op = m.group(1)
        if not re.match(r'^[a-z][a-zA-Z0-9]*$', op):
            report(path, i, f'operationId {op!r} is not camelCase')
        if op in seen:
            report(path, i, f'duplicate operationId {op!r} (first seen at line {seen[op]})')
        seen[op] = i
    if not seen:
        report(path, 0, 'no operationIds found')

    # -- every POST command takes the shared Idempotency-Key (A§21) --------
    posts = [i for i, line in enumerate(lines, 1) if re.match(r'^    post:\s*$', line)]
    keys = [i for i, line in enumerate(lines, 1)
            if "common.v1.yaml#/components/parameters/IdempotencyKey" in line]
    if len(posts) != len(keys):
        report(path, 0, f'{len(posts)} POST operation(s) but {len(keys)} '
                        'Idempotency-Key reference(s): every POST command needs one (A§21)')

    # -- money is int64 minor units with a sibling ISO-4217 currency -------
    for i, line in enumerate(lines, 1):
        m = re.match(r'^(\s+)(\w*[Mm]inor):\s*$', line)
        if m:
            window = ' '.join(lines[i:i + 3])
            if 'type: integer' not in window or 'format: int64' not in window:
                report(path, i, f'{m.group(2)} must be `type: integer` + `format: int64` '
                                '(money is int64 minor units)')
        m = re.match(r'^(\s+)currency:\s*$', line)
        if m:
            window = ' '.join(lines[i:i + 12])
            if CURRENCY_PATTERN not in window:
                report(path, i, f'currency must carry pattern {CURRENCY_PATTERN}')

    # -- platform-generated ids use the ULID pattern -----------------------
    if ULID_PATTERN not in '\n'.join(lines):
        report(path, 0, f'no ULID pattern {ULID_PATTERN} found; platform-generated '
                        'ids are ULIDs (contracts/README.md §6)')

    # -- every ULID-shaped example really is a valid ULID ------------------
    for i, line in enumerate(lines, 1):
        for cand in re.findall(r'\b[0-9A-Z]{26}\b', line):
            if not ULID_RE.match(cand):
                report(path, i, f'{cand!r} looks like a ULID example but violates '
                                'the Crockford base32 alphabet (no I, L, O, U)')

    # -- list responses are {items, nextCursor} ----------------------------
    for i, line in enumerate(lines, 1):
        m = re.match(r'^    (\w+Page):\s*$', line)
        if not m:
            continue
        block = '\n'.join(lines[i:i + 60])
        if not re.search(r'^\s+- items$', block, re.M) or \
           not re.search(r'^\s+- nextCursor$', block, re.M):
            report(path, i, f'{m.group(1)} must require both `items` and `nextCursor` '
                            '(contracts/README.md §10)')
        if 'nullable: true' not in block:
            report(path, i, f'{m.group(1)}.nextCursor must be nullable (null on the last page)')

    # -- bearerAuth is declared and applied --------------------------------
    text = '\n'.join(lines)
    if 'bearerAuth:' not in text or 'scheme: bearer' not in text:
        report(path, 0, 'no bearerAuth security scheme declared')
    if not re.search(r'^security:\n  - bearerAuth: \[\]$', text, re.M):
        report(path, 0, 'bearerAuth is not applied as the document security requirement')

for p in problems:
    print(p)
sys.exit(1 if problems else 0)
PY

# ---------------------------------------------------------------------------
# Endpoint-count / Idempotency-Key checker over the GENERATED code — this is
# what a service actually compiles against, so asserting here rather than on
# the YAML proves the contract survives codegen.
# ---------------------------------------------------------------------------
cat >"$TMP/generated.py" <<'PY'
import re
import sys

path, want_ops = sys.argv[1], int(sys.argv[2])
src = open(path, encoding='utf-8').read()

iface = re.search(r'type StrictServerInterface interface \{(.*?)\n\}', src, re.S)
if not iface:
    print(f'{path}: no StrictServerInterface generated')
    sys.exit(1)

ops = re.findall(r'^\t// \((?P<method>[A-Z]+) (?P<route>\S+)\)\n\t(?P<name>\w+)\(ctx ',
                 iface.group(1), re.M)
if len(ops) != want_ops:
    print(f'{path}: {len(ops)} operations generated, want {want_ops}: '
          + ', '.join(f'{m} {r}' for m, r, _ in ops))
    sys.exit(1)

bad = []
for method, route, name in ops:
    if method != 'POST':
        continue
    block = re.search(r'type %sParams struct \{(.*?)\n\}' % name, src, re.S)
    if not block or 'IdempotencyKey' not in block.group(1):
        bad.append(f'{name} ({method} {route})')
if bad:
    print(f'{path}: POST operations without an Idempotency-Key param: ' + ', '.join(bad))
    sys.exit(1)

sys.exit(0)
PY

# ---------------------------------------------------------------------------
# 1. The files exist and lint clean, individually and through the repo target.
# ---------------------------------------------------------------------------
for name in "${SPECS[@]}"; do
  check "contracts/openapi/$name.v1.yaml exists" "test -f '$OPENAPI/$name.v1.yaml'"
done

for name in "${SPECS[@]}"; do
  check "vacuum lint reports no errors in $name.v1.yaml" \
    "${TOOL[*]} vacuum lint -x -e '$OPENAPI/$name.v1.yaml'"
done

check "make contracts-check is green" "make -C '$REPO_ROOT' contracts-check"

# ---------------------------------------------------------------------------
# 2. contracts/README.md conventions hold across all six specs.
# ---------------------------------------------------------------------------
spec_paths=()
for name in "${SPECS[@]}"; do spec_paths+=("$OPENAPI/$name.v1.yaml"); done

if python3 "$TMP/conventions.py" "${spec_paths[@]}" >"$TMP/conventions.out" 2>&1; then
  ok "all six specs satisfy the contracts/README.md conventions"
else
  bad "convention violations found:"
  sed 's/^/      /' "$TMP/conventions.out" >&2
fi

# Expected fail: the checker must actually reject a violation, not just pass
# well-formed input.
cp "$OPENAPI/arrangement.v1.yaml" "$TMP/open-schema.yaml"
python3 - "$TMP/open-schema.yaml" <<'PY'
import sys
p = sys.argv[1]
s = open(p, encoding='utf-8').read().replace(
    'additionalProperties: false', 'additionalProperties: true', 1)
open(p, 'w', encoding='utf-8').write(s)
PY
check_fails "convention checker rejects an open object schema" \
  "python3 '$TMP/conventions.py' '$TMP/open-schema.yaml'"

cp "$OPENAPI/legal.v1.yaml" "$TMP/no-idempotency.yaml"
python3 - "$TMP/no-idempotency.yaml" <<'PY'
import sys
p = sys.argv[1]
lines = open(p, encoding='utf-8').read().splitlines(keepends=True)
out = [ln for ln in lines
       if 'common.v1.yaml#/components/parameters/IdempotencyKey' not in ln]
open(p, 'w', encoding='utf-8').writelines(out)
PY
check_fails "convention checker rejects a POST command with no Idempotency-Key" \
  "python3 '$TMP/conventions.py' '$TMP/no-idempotency.yaml'"

# Expected fail: vacuum must reject a duplicated operationId.
cp "$OPENAPI/recovery.v1.yaml" "$TMP/dup-op.yaml"
python3 - "$TMP/dup-op.yaml" <<'PY'
import sys
p = sys.argv[1]
s = open(p, encoding='utf-8').read().replace(
    'operationId: getRecovery\n', 'operationId: createRecovery\n', 1)
open(p, 'w', encoding='utf-8').write(s)
PY
check_fails "vacuum rejects a duplicated operationId" \
  "${TOOL[*]} vacuum lint -x -e '$TMP/dup-op.yaml'"

# ---------------------------------------------------------------------------
# 3. Codegen smoke: strict-server types + server compile and vet.
#
# WORKAROUND (reported in the CON-4 hand-off): oapi-codegen v2.8.0 cannot
# resolve a cross-file $ref on its own — `./common.v1.yaml#/...` is an
# "unrecognized external reference" and codegen exits non-zero. The specs stay
# as they are; codegen needs two extra pieces:
#
#   1. `-import-mapping='./common.v1.yaml:-'` on each service spec — the `-`
#      target means "these components are in the package being generated", so
#      the generated code refers to `IdempotencyKey`, `BadRequestJSONResponse`
#      and friends as local identifiers.
#   2. A companion generation of common.v1.yaml into the SAME package with
#      `-generate types,strict-server,skip-prune`, which is what actually
#      defines those identifiers. `skip-prune` is required because common
#      declares `paths: {}`, so every component looks unused and is otherwise
#      pruned to an empty file; `strict-server` is required because the
#      `*JSONResponse` wrappers the service code embeds are emitted by the
#      strict-server template, not the types template.
#
# The service Makefiles must do the same two generations.
# ---------------------------------------------------------------------------
SCRATCH="$TMP/smoke"
mkdir -p "$SCRATCH"
(cd "$SCRATCH" && GOWORK=off go mod init smoke >/dev/null 2>&1)

check "common.v1.yaml generates shared component types and response wrappers" \
  "${TOOL[*]} oapi-codegen -generate types,strict-server,skip-prune \
     -package smoke -o /tmp/smoke_common.go '$COMMON'"

for name in "${SPECS[@]}"; do
  check "oapi-codegen generates strict-server code for $name.v1.yaml" \
    "${TOOL[*]} oapi-codegen -generate types,std-http-server,strict-server \
       -package smoke -import-mapping='./common.v1.yaml:-' \
       -o /tmp/smoke_$name.go '$OPENAPI/$name.v1.yaml'"
done

# Expected fail: without the import mapping the cross-file $refs are fatal.
# This is what makes the workaround above load-bearing rather than decorative.
check_fails "codegen without -import-mapping rejects the cross-file \$refs" \
  "${TOOL[*]} oapi-codegen -generate types,std-http-server,strict-server \
     -package smoke -o '$TMP/no-mapping.go' '$OPENAPI/arrangement.v1.yaml'"

for name in "${SPECS[@]}"; do
  if [ -f "/tmp/smoke_$name.go" ] && [ -f /tmp/smoke_common.go ]; then
    mkdir -p "$SCRATCH/$name"
    cp "/tmp/smoke_$name.go" /tmp/smoke_common.go "$SCRATCH/$name/"
  fi
done

if (cd "$SCRATCH" && GOWORK=off go mod tidy) >"$TMP/tidy.out" 2>&1; then
  ok "scratch module resolves the oapi-codegen runtime dependency"
  for name in "${SPECS[@]}"; do
    check "generated $name server vets in a scratch module" \
      "(cd '$SCRATCH' && GOWORK=off go vet ./$name/...)"
  done
else
  bad "scratch module could not resolve github.com/oapi-codegen/runtime \
(needs the Go module cache or one network fetch); see $TMP/tidy.out"
  sed 's/^/      /' "$TMP/tidy.out" >&2
fi

# ---------------------------------------------------------------------------
# 4. Endpoint counts and Idempotency-Key survive codegen.
# ---------------------------------------------------------------------------
for name in "${SPECS[@]}"; do
  if [ ! -f "/tmp/smoke_$name.go" ]; then
    bad "$name: no generated code to inspect"
    continue
  fi
  n="$(want_ops "$name")"
  if python3 "$TMP/generated.py" "/tmp/smoke_$name.go" "$n" \
      >"$TMP/gen-$name.out" 2>&1; then
    ok "$name exposes $n operations, every POST taking Idempotency-Key"
  else
    bad "$name generated-code check failed:"
    sed 's/^/      /' "$TMP/gen-$name.out" >&2
  fi
done

# ---------------------------------------------------------------------------
# 5. The contracts module still validates everything it owns.
# ---------------------------------------------------------------------------
check "go -C contracts test ./... is green" "go -C '$REPO_ROOT/contracts' test ./..."

printf '\nCON-4: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
