#!/usr/bin/env bash
#
# scripts/verify/CON-2.sh — verifies the CON-2 event catalogue: the 20 payload schemas
# it owns, their 20 envelope-wrapped golden examples, and the AsyncAPI topic index that
# maps every topic to its partition key and payload schema.
#
# It asserts observable outcomes only: files exist, $id equals the file path, the Go
# contract harness passes, the AsyncAPI document parses and its refs resolve. It ends
# with eight expected-FAIL assertions that inject deliberately broken contracts and prove
# the harness rejects them — a guard that never rejects anything is not a guard.
#
# Environment: none (no Docker, no network, no cloud). bash + coreutils + go + python3
# (stdlib only; pyyaml is used when present and structurally emulated when not).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

TMP="$(mktemp -d)"

# Probe artefacts injected by section 9. Removed on every exit path so the script is
# re-runnable back to back and never leaves a file behind for ownership-check to find.
PROBE_SCHEMA="contracts/schemas/events/case/ZzzVerifyProbe.v1.json"
PROBE_EXAMPLE="contracts/examples/events/case/ZzzVerifyProbe.v1.example.json"
PROBE_ORPHAN="contracts/examples/events/case/ZzzVerifyOrphan.v1.example.json"
cleanup() { rm -rf "$TMP"; rm -f "$PROBE_SCHEMA" "$PROBE_EXAMPLE" "$PROBE_ORPHAN"; }
trap cleanup EXIT

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

# The 20 (context/Event) pairs CON-2 owns. The decisioning contexts
# (strategy, decision, treatment) and the canonical ingestion snapshots belong to
# DEC-1 and CON-6; they are referenced by the AsyncAPI index but not asserted here.
EVENTS="customer/CustomerUpdated
account/AccountUpdated
debt/DebtUpdated
delinquency/DelinquencyChanged
case/CaseCreated
case/CaseAssigned
case/CaseResolved
contact/ContactAttempted
contact/ContactCompleted
arrangement/PromiseCreated
arrangement/PromiseBroken
arrangement/ArrangementCreated
arrangement/ArrangementBroken
payment/PaymentReceived
payment/PaymentAllocated
recovery/RecoveryRecorded
agency/DebtPlaced
agency/DebtRecalled
legal/LegalStatusChanged
ingestion/FileStatusChanged"

CONTEXTS="customer account debt delinquency case contact arrangement payment recovery agency legal ingestion"

ASYNCAPI="contracts/asyncapi/collections.v1.yaml"

# The 19 channels of the normative topic index, each with the partition-key field the
# whole ordering guarantee depends on (A§23 + A§25 + A§26, plan §7 CON-2 table).
# Two channels legitimately carry more than one key, declared per message.
CHANNELS="collections.customer|customerId
collections.account|accountId
collections.debt|debtId
collections.delinquency|accountId
collections.case|caseId
collections.strategy|strategyId | ruleSetId | configId
collections.decision|decisionId
collections.treatment|caseId
collections.contact|contactId
collections.arrangement|promiseId | arrangementId
collections.payment|paymentId
collections.recovery|recoveryId
collections.agency|placementId
collections.legal|legalCaseId
ingestion.customers.v1|customerId
ingestion.accounts.v1|accountId
ingestion.debts.v1|debtId
ingestion.payments.v1|accountId
ingestion.file.lifecycle.v1|fileId"

echo "=== 1. the 20 payload schemas exist ==="
while IFS= read -r e; do
	[ -n "$e" ] || continue
	check "exists: contracts/schemas/events/$e.v1.json" test -f "contracts/schemas/events/$e.v1.json"
done <<<"$EVENTS"

schema_count=0
for c in $CONTEXTS; do
	n="$(find "contracts/schemas/events/$c" -name '*.v1.json' -type f 2>/dev/null | wc -l | tr -d ' ')"
	schema_count=$((schema_count + n))
done
check "exactly 20 schemas across the 12 CON-2 contexts (found $schema_count)" test "$schema_count" -eq 20

echo
echo "=== 2. the 20 golden examples exist and mirror their schema ==="
while IFS= read -r e; do
	[ -n "$e" ] || continue
	check "exists: contracts/examples/events/$e.v1.example.json" test -f "contracts/examples/events/$e.v1.example.json"
done <<<"$EVENTS"

example_count=0
for c in $CONTEXTS; do
	n="$(find "contracts/examples/events/$c" -name '*.v1.example.json' -type f 2>/dev/null | wc -l | tr -d ' ')"
	example_count=$((example_count + n))
done
check "exactly 20 examples across the 12 CON-2 contexts (found $example_count)" test "$example_count" -eq 20
check "no stray *.json under the CON-2 example dirs that is not *.example.json" \
	test -z "$(find contracts/examples/events -name '*.json' -not -name '*.example.json' -type f 2>/dev/null)"

echo
echo "=== 3. every schemas/events schema is valid JSON with a path-equal \$id ==="
cat >"$TMP/check_ids.py" <<'PY'
import json, pathlib, sys

BASE = "https://contracts.collections.internal/"
DRAFT = "https://json-schema.org/draft/2020-12/schema"
root = pathlib.Path("contracts")
bad = []
n = 0


def no_duplicate_keys(pairs):
    """json.loads silently keeps the last of duplicate keys, which can hide a whole
    property behind a typo (e.g. a second `type` overriding the first). Reject them."""
    seen = {}
    for k, v in pairs:
        if k in seen:
            raise ValueError(f"duplicate key {k!r}")
        seen[k] = v
    return seen


for p in sorted(root.glob("schemas/events/**/*.json")):
    rel = p.relative_to(root).as_posix()
    n += 1
    try:
        d = json.loads(p.read_text(), object_pairs_hook=no_duplicate_keys)
    except Exception as exc:                                  # noqa: BLE001
        bad.append(f"{rel}: not valid JSON: {exc}")
        continue
    if d.get("$id") != BASE + rel:
        bad.append(f"{rel}: $id is {d.get('$id')!r}, want {BASE + rel!r}")
    if d.get("$schema") != DRAFT:
        bad.append(f"{rel}: $schema is {d.get('$schema')!r}, want {DRAFT!r}")
    if d.get("additionalProperties") is not False:
        bad.append(f"{rel}: top-level additionalProperties must be false")
    if not d.get("required"):
        bad.append(f"{rel}: top-level required list is missing or empty")
    if not d.get("description"):
        bad.append(f"{rel}: no top-level description")
for b in bad:
    print("  " + b, file=sys.stderr)
print(f"  checked {n} schemas under schemas/events/", file=sys.stderr)
sys.exit(1 if bad else 0)
PY
check "\$id == https://contracts.collections.internal/<path>, draft 2020-12, additionalProperties:false, required, described, no duplicate keys" \
	python3 "$TMP/check_ids.py"
python3 "$TMP/check_ids.py" 2>&1 >/dev/null | tail -1 || true

echo
echo "=== 4. ULID discipline: reuse the envelope definition, never re-declare the pattern ==="
ULID_REF='EventEnvelope.v1.json#/$defs/ulid'
for c in $CONTEXTS; do
	[ "$c" = "ingestion" ] && continue # FileStatusChanged carries only the FIL_-prefixed id
	while IFS= read -r f; do
		[ -n "$f" ] || continue
		check "references the envelope ULID definition: ${f#contracts/schemas/events/}" \
			grep -qF "$ULID_REF" "$f"
	done <<<"$(find "contracts/schemas/events/$c" -name '*.v1.json' -type f 2>/dev/null)"
done
check_fails "no CON-2 schema re-declares the bare ULID pattern (must \$ref the envelope instead)" \
	grep -rlF '^[0-9A-HJKMNP-TV-Z]{26}$' \
	contracts/schemas/events/customer contracts/schemas/events/account \
	contracts/schemas/events/debt contracts/schemas/events/delinquency \
	contracts/schemas/events/case contracts/schemas/events/contact \
	contracts/schemas/events/arrangement contracts/schemas/events/payment \
	contracts/schemas/events/recovery contracts/schemas/events/agency \
	contracts/schemas/events/legal contracts/schemas/events/ingestion
check "FileStatusChanged declares the FIL_-prefixed ULID pattern (docs/conventions.md §1)" \
	grep -qF 'FIL_[0-9A-HJKMNP-TV-Z]{26}' contracts/schemas/events/ingestion/FileStatusChanged.v1.json

echo
echo "=== 5. money and time conventions in the CON-2 payloads ==="
cat >"$TMP/check_conventions.py" <<'PY'
import json, pathlib, sys

CONTEXTS = "customer account debt delinquency case contact arrangement payment recovery agency legal ingestion".split()
UTC = r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?Z$"
bad = []
money_fields = timestamp_fields = date_fields = 0


def walk(rel, node, path=""):
    global money_fields, timestamp_fields, date_fields
    if not isinstance(node, dict):
        return
    for name, spec in (node.get("properties") or {}).items():
        if not isinstance(spec, dict):
            continue
        here = f"{path}.{name}"
        types = spec.get("type")
        types = [types] if isinstance(types, str) else (types or [])
        if name.endswith("Minor"):
            money_fields += 1
            if "integer" not in types:
                bad.append(f"{rel}{here}: *Minor must be an int64 integer, got {types}")
        if name == "currency" and spec.get("pattern") != "^[A-Z]{3}$":
            bad.append(f"{rel}{here}: currency must be ISO-4217 ^[A-Z]{{3}}$")
        if spec.get("format") == "date-time":
            timestamp_fields += 1
            if spec.get("pattern") != UTC:
                bad.append(f"{rel}{here}: date-time must carry the UTC-only pattern (no local offsets)")
        if spec.get("format") == "date":
            date_fields += 1
        if not spec.get("description") and "$ref" not in spec and "anyOf" not in spec:
            bad.append(f"{rel}{here}: every field carries a description")
        if "anyOf" in spec and not spec.get("description"):
            bad.append(f"{rel}{here}: every field carries a description")
        walk(rel, spec, here)
        walk(rel, (spec.get("items") or {}), here + "[]")
    for defname, spec in (node.get("$defs") or {}).items():
        walk(rel, spec, f"{path}#{defname}")


root = pathlib.Path("contracts/schemas/events")
files = [p for c in CONTEXTS for p in sorted((root / c).glob("*.v1.json"))]
for p in files:
    d = json.loads(p.read_text())
    rel = p.name
    walk(rel, d)
    props = d.get("properties", {})
    has_money = any(k.endswith("Minor") for k in props) or any(
        k.endswith("Minor") for s in (d.get("$defs") or {}).values() for k in (s.get("properties") or {})
    )
    if has_money and "currency" not in props:
        bad.append(f"{rel}: carries *Minor amounts but no sibling `currency`")
for b in bad:
    print("  " + b, file=sys.stderr)
print(
    f"  {len(files)} payloads: {money_fields} minor-unit amounts, {timestamp_fields} UTC timestamps, {date_fields} calendar dates",
    file=sys.stderr,
)
sys.exit(1 if bad else 0)
PY
check "every *Minor is an integer with a sibling ISO-4217 currency; every date-time is UTC-only; every field described" \
	python3 "$TMP/check_conventions.py"
python3 "$TMP/check_conventions.py" 2>&1 >/dev/null | tail -1 || true

echo
echo "=== 6. AsyncAPI topic index ==="
check "exists: $ASYNCAPI" test -f "$ASYNCAPI"
check "declares AsyncAPI 3.0.0" grep -qE '^asyncapi: 3\.0\.0$' "$ASYNCAPI"
check "documents that every message rides the A§24 envelope" grep -qF 'EventEnvelope.v1' "$ASYNCAPI"
check "documents that ordering is per aggregate via the partition key (A§26)" grep -qF 'A§26' "$ASYNCAPI"
check "documents the DLQ convention collections.dlq.<service>" grep -qF 'collections.dlq.{service}' "$ASYNCAPI"
check "documents the ingestion DLQ dlq.ingestion.v1" grep -qF 'dlq.ingestion.v1' "$ASYNCAPI"

while IFS= read -r row; do
	[ -n "$row" ] || continue
	ch="${row%%|*}"
	key="${row#*|}"
	check "channel present: $ch" grep -qE "^  ${ch//./\\.}:$" "$ASYNCAPI"
	check "channel $ch declares partition key: $key" grep -qF "    x-partition-key: $key" "$ASYNCAPI"
done <<<"$CHANNELS"

cat >"$TMP/check_asyncapi.py" <<'PY'
"""Parse the AsyncAPI index and assert its internal and external refs resolve.

Uses PyYAML when it is installed; otherwise falls back to a line-oriented structural
reader that is sufficient for this document's fixed 2-space-indented shape. Either way
the assertions are the same, so the result does not depend on the environment.
"""
import pathlib, re, sys

DOC = pathlib.Path("contracts/asyncapi/collections.v1.yaml")
HERE = DOC.parent
text = DOC.read_text()
problems = []
mode = "pyyaml"

try:
    import yaml  # type: ignore

    d = yaml.safe_load(text)
    channels = list((d.get("channels") or {}).keys())
    messages = list(((d.get("components") or {}).get("messages") or {}).keys())
    msg_refs, ext_refs = [], []
    for ch, body in (d.get("channels") or {}).items():
        for _, m in (body.get("messages") or {}).items():
            msg_refs.append(m.get("$ref", ""))
        if not body.get("address"):
            problems.append(f"channel {ch}: no address")
        if not body.get("x-partition-key"):
            problems.append(f"channel {ch}: no x-partition-key")
    for name, m in (((d.get("components") or {}).get("messages")) or {}).items():
        if not m.get("x-partition-key"):
            problems.append(f"message {name}: no x-partition-key")
        if not m.get("x-producer"):
            problems.append(f"message {name}: no x-producer")
        ref = ((m.get("payload") or {}).get("schema") or {}).get("$ref")
        if not ref:
            problems.append(f"message {name}: payload.schema.$ref missing")
        else:
            ext_refs.append(ref)
        if not m.get("traits"):
            problems.append(f"message {name}: does not carry the envelope message trait")
    if d.get("asyncapi") != "3.0.0":
        problems.append(f"asyncapi is {d.get('asyncapi')!r}, want '3.0.0'")
except ModuleNotFoundError:
    # Indentation-based reader for this document's fixed 2-space shape: top-level keys at
    # column 0, channels and components members at 2, message names at 4, their fields at 6.
    mode = "structural (pyyaml absent)"
    channels, messages = [], []
    bodies, section, in_msgs, cur = {}, None, False, None
    for line in text.splitlines():
        if line and not line[0].isspace():
            section = line[:-1] if line.endswith(":") else None
            in_msgs, cur = False, None
            continue
        if section == "channels":
            m = re.match(r"^  ([A-Za-z][\w.\-]*):$", line)
            if m:
                channels.append(m.group(1))
                cur = None
            elif channels:
                bodies.setdefault("channel:" + channels[-1], []).append(line)
        elif section == "components":
            if line == "  messages:":
                in_msgs, cur = True, None
                continue
            if re.match(r"^  \S", line):
                in_msgs, cur = False, None
                continue
            if in_msgs:
                m = re.match(r"^    ([A-Za-z]\w*):$", line)
                if m:
                    cur = m.group(1)
                    messages.append(cur)
                    bodies[cur] = []
                elif cur is not None:
                    bodies[cur].append(line)
    msg_refs = re.findall(r"\$ref: '(#/components/messages/[^']+)'", text)
    ext_refs = re.findall(r"\$ref: (\.\./[^\s'\"]+)", text)
    if not re.search(r"^asyncapi: 3\.0\.0$", text, re.M):
        problems.append("asyncapi: 3.0.0 not declared")
    if "\t" in text:
        problems.append("file contains a tab character (YAML indentation must be spaces)")
    for ch in channels:
        body = "\n".join(bodies.get("channel:" + ch, []))
        for req in ("address:", "x-partition-key:", "messages:"):
            if req not in body:
                problems.append(f"channel {ch}: missing {req}")
    for name in messages:
        body = "\n".join(bodies.get(name, []))
        if not body:
            problems.append(f"message {name}: empty definition")
        for req in ("x-partition-key:", "x-producer:", "traits:", "payload:"):
            if req not in body:
                problems.append(f"message {name}: missing {req}")

# --- assertions, identical in both modes -----------------------------------------
for ref in msg_refs:
    name = ref.rsplit("/", 1)[-1]
    if name not in messages:
        problems.append(f"channel message $ref -> {ref} has no definition under components.messages")
for ref in sorted(set(ext_refs)):
    target = (HERE / ref).resolve()
    if not target.is_file():
        problems.append(f"payload $ref does not resolve: {ref} (expected {target})")
if len(channels) != 19:
    problems.append(f"{len(channels)} channels declared, want exactly 19: {channels}")
unused = sorted(set(messages) - {r.rsplit('/', 1)[-1] for r in msg_refs})
if unused:
    problems.append(f"messages defined but wired to no channel: {unused}")

for p in problems:
    print("  " + p, file=sys.stderr)
print(
    f"  parsed with {mode}: {len(channels)} channels, {len(messages)} messages, "
    f"{len(set(ext_refs))} payload schema refs, all resolved={not problems}",
    file=sys.stderr,
)
sys.exit(1 if problems else 0)
PY
check "AsyncAPI parses; 19 channels; every message ref defined; every payload \$ref resolves on disk" \
	python3 "$TMP/check_asyncapi.py"
python3 "$TMP/check_asyncapi.py" 2>&1 >/dev/null | tail -1 || true

echo
echo "=== 7. cross-artefact consistency: schema <-> example <-> topic index ==="
cat >"$TMP/check_keys.py" <<'PY'
"""For every CON-2 event: the partition key declared in the AsyncAPI index must be a
required field of the payload schema, and the golden example's envelope `aggregateId`
must equal that payload field. This is what makes the topic index normative rather than
decorative — a key that names a field nobody publishes cannot order anything.
"""
import json, pathlib, re, sys

pairs = [line.split("/") for line in sys.argv[1].splitlines() if line.strip()]
doc = pathlib.Path("contracts/asyncapi/collections.v1.yaml").read_text()

# Message names sit at indent 4 and their x-partition-key at indent 6 (channel-level keys
# are at indent 4, so they cannot be confused with message-level ones).
keys, cur = {}, None
for line in doc.splitlines():
    m = re.match(r"^    ([A-Za-z]\w*):$", line)
    if m:
        cur = m.group(1)
        continue
    m = re.match(r"^      x-partition-key: (\S.*)$", line)
    if m and cur:
        keys[cur] = m.group(1).strip()
problems = []
for ctx, event in pairs:
    schema = json.loads(pathlib.Path(f"contracts/schemas/events/{ctx}/{event}.v1.json").read_text())
    example = json.loads(pathlib.Path(f"contracts/examples/events/{ctx}/{event}.v1.example.json").read_text())
    key = keys.get(event)
    if key is None:
        problems.append(f"{event}: no x-partition-key in the AsyncAPI index")
        continue
    if key not in (schema.get("required") or []):
        problems.append(f"{event}: partition key `{key}` is not a required field of the payload schema")
    if key not in (schema.get("properties") or {}):
        problems.append(f"{event}: partition key `{key}` is not a property of the payload schema")
    if example.get("aggregateId") != (example.get("payload") or {}).get(key):
        problems.append(
            f"{event}: example aggregateId {example.get('aggregateId')!r} != payload.{key} "
            f"{(example.get('payload') or {}).get(key)!r}"
        )
    if example.get("eventType") != event:
        problems.append(f"{event}: example eventType is {example.get('eventType')!r}")
for p in problems:
    print("  " + p, file=sys.stderr)
print(f"  {len(pairs)} events: partition key required in schema and equal to the example aggregateId", file=sys.stderr)
sys.exit(1 if problems else 0)
PY
check "every partition key is a required payload field and equals the example's envelope aggregateId" \
	python3 "$TMP/check_keys.py" "$EVENTS"
python3 "$TMP/check_keys.py" "$EVENTS" 2>&1 >/dev/null | tail -1 || true

echo
echo "=== 8. the contract harness passes (mirror rule + envelope wrapping + orphan guards) ==="
check "go -C contracts test ./... (schemas compile, examples validate against schema AND envelope)" \
	go -C contracts test -count=1 ./...
check "every CON-2 example is syntactically valid JSON" \
	bash -c 'find contracts/examples/events -name "*.example.json" -type f -print0 | xargs -0 -n1 python3 -m json.tool >/dev/null'

echo
echo "=== 9. expected-FAIL: the harness rejects broken contracts ==="
# A valid probe schema + example pair. Section 9 mutates one thing at a time and
# requires the harness to reject each mutation, which is what proves the guards bite.
write_probe_schema() {
	local id="$1"
	cat >"$PROBE_SCHEMA" <<JSON
{
  "\$schema": "https://json-schema.org/draft/2020-12/schema",
  "\$id": "$id",
  "title": "ZzzVerifyProbe.v1",
  "description": "Throwaway probe written by scripts/verify/CON-2.sh and deleted before it exits. Never commit this file.",
  "type": "object",
  "additionalProperties": false,
  "required": ["caseId", "probe"],
  "properties": {
    "caseId": {
      "\$ref": "https://contracts.collections.internal/schemas/envelope/EventEnvelope.v1.json#/\$defs/ulid",
      "description": "Probe case id."
    },
    "probe": { "type": "string", "description": "Probe marker." }
  }
}
JSON
}

write_probe_example() {
	local event_type="$1"
	local payload="$2"
	cat >"$PROBE_EXAMPLE" <<JSON
{
  "eventId": "01M0KK6HNRPTPN8AJ6Y0E5NDQ7",
  "eventType": "$event_type",
  "eventVersion": 1,
  "occurredAt": "2026-09-01T00:00:00Z",
  "producer": "case-service",
  "aggregateType": "Case",
  "aggregateId": "01M0KK4P3G0MQSQ3A1X2PMA6VX",
  "correlationId": "01M0KK4G8042Z1PTCGJA3G3KMK",
  "payload": $payload
}
JSON
}

GOOD_PAYLOAD='{ "caseId": "01M0KK4P3G0MQSQ3A1X2PMA6VX", "probe": "ok" }'
GOOD_ID='https://contracts.collections.internal/schemas/events/case/ZzzVerifyProbe.v1.json'

write_probe_schema "$GOOD_ID"
write_probe_example "ZzzVerifyProbe" "$GOOD_PAYLOAD"
check "positive control: a well-formed probe schema + example pair passes the harness" \
	go -C contracts test -count=1 ./...

write_probe_schema 'https://contracts.collections.internal/schemas/events/case/WrongName.v1.json'
check_fails "rejects a schema whose \$id does not equal its path" \
	go -C contracts test -count=1 ./...

write_probe_schema "$GOOD_ID"
write_probe_example "ZzzVerifyProbe" '{ "caseId": "01M0KK4P3G0MQSQ3A1X2PMA6VX" }'
check_fails "rejects an example whose payload violates its own schema (missing required field)" \
	go -C contracts test -count=1 ./...

write_probe_example "ZzzVerifyProbe" '{ "caseId": "not-a-ulid", "probe": "ok" }'
check_fails "rejects an example whose ULID field is not a ULID (envelope \$defs/ulid is enforced)" \
	go -C contracts test -count=1 ./...

write_probe_example "SomeOtherEvent" "$GOOD_PAYLOAD"
check_fails "rejects an example whose eventType disagrees with its file name" \
	go -C contracts test -count=1 ./...

write_probe_example "ZzzVerifyProbe" "$GOOD_PAYLOAD"
sed -i.bak 's/"occurredAt": "2026-09-01T00:00:00Z"/"occurredAt": "2026-09-01T00:00:00+01:00"/' "$PROBE_EXAMPLE"
rm -f "$PROBE_EXAMPLE.bak"
check_fails "rejects an envelope occurredAt with a local offset (UTC Z only)" \
	go -C contracts test -count=1 ./...

write_probe_example "ZzzVerifyProbe" "$GOOD_PAYLOAD"
rm -f "$PROBE_EXAMPLE"
check_fails "rejects an event payload schema that ships no example" \
	go -C contracts test -count=1 ./...

rm -f "$PROBE_SCHEMA"
cp "contracts/examples/events/case/CaseCreated.v1.example.json" "$PROBE_ORPHAN"
check_fails "rejects an orphan example with no mirrored schema" \
	go -C contracts test -count=1 ./...
rm -f "$PROBE_ORPHAN"

check "tree restored: the harness passes again after every probe is removed" \
	go -C contracts test -count=1 ./...

echo
echo "=== 10. shell hygiene ==="
check "bash -n clean: scripts/verify/CON-2.sh" bash -n scripts/verify/CON-2.sh
check "has 'set -euo pipefail': scripts/verify/CON-2.sh" grep -qF 'set -euo pipefail' scripts/verify/CON-2.sh
check "has bash shebang: scripts/verify/CON-2.sh" grep -qF '#!/usr/bin/env bash' scripts/verify/CON-2.sh

echo
printf 'CON-2: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
