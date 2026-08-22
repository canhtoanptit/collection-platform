#!/usr/bin/env bash
#
# scripts/verify/CON-6.sh — verifies the canonical ingestion snapshot contracts.
#
# Proves that the five schemas under contracts/schemas/ingestion/ and their five
# golden examples exist, are addressable ($id == path, so cross-file $refs and
# the runtime loader resolve), are closed (additionalProperties:false + explicit
# required), and actually constrain the things the platform depends on:
#
#   - AccountSnapshot.oldestUnpaidDueDate is a REQUIRED key with a NULLABLE
#     value  (delinquency-service: dpd = asOf - oldestUnpaidDueDate, D§5);
#   - money is int64 minor units with an ISO-4217 sibling currency, never a
#     float and never major units (contracts/README §4);
#   - timestamps are RFC3339 UTC with a Z suffix (contracts/README §5);
#   - PaymentNotification names (sourceSystem, externalPaymentRef) as the
#     natural dedup key that absorbs the webhook/batch overlap (D§47);
#   - PaymentWebhook $refs the envelope ULID def and PaymentNotification rather
#     than restating either.
#
# Includes two expected-fail assertions, because a guard that never rejects
# anything is not a guard: the $id check must reject a doctored $id, and the
# contracts harness must reject an AccountSnapshot example whose
# oldestUnpaidDueDate has been removed.
#
# Environment: none (no Docker, no network, no cloud). bash + python3 (stdlib
# only) + the pinned Go toolchain.
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

SCHEMA_DIR="contracts/schemas/ingestion"
EXAMPLE_DIR="contracts/examples/ingestion"
NAMES=(CustomerSnapshot AccountSnapshot DebtSnapshot PaymentNotification PaymentWebhook)

command -v python3 >/dev/null 2>&1 || {
	echo "python3 is required (stdlib only)" >&2
	exit 1
}
command -v go >/dev/null 2>&1 || {
	echo "the Go toolchain is required to run the contracts harness" >&2
	exit 1
}

# ---------------------------------------------------------------------------
# The assertion helper. One focused check per invocation so that a reviewer
# sees WHICH assertion failed without reading this script.
# ---------------------------------------------------------------------------
cat >"$TMP/con6.py" <<'PYEOF'
"""CON-6 contract assertions. Usage: con6.py <check> [name]. Exit 0 = pass.

The contracts module is read from $CON6_ROOT (default: the repo root), which is
what lets the caller point the same checks at a doctored copy under /tmp.
"""
import json
import os
import sys
from pathlib import Path

BASE = "https://contracts.collections.internal/"
DIALECT = "https://json-schema.org/draft/2020-12/schema"
UTC = r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?Z$"
INT64_MAX = 9223372036854775807
INT64_MIN = -9223372036854775808
MONEY_CARRYING = ("AccountSnapshot", "DebtSnapshot", "PaymentNotification")
NAMES = ("CustomerSnapshot", "AccountSnapshot", "DebtSnapshot",
         "PaymentNotification", "PaymentWebhook")
ROOT = Path(os.environ.get("CON6_ROOT", "."))


def die(msg):
    print("assertion failed: " + msg, file=sys.stderr)
    sys.exit(1)


def need(cond, msg):
    if not cond:
        die(msg)


def rel_schema(name):
    return "schemas/ingestion/%s.v1.json" % name


def schema(name):
    return json.loads((ROOT / "contracts" / rel_schema(name)).read_text())


def example(name):
    p = ROOT / ("contracts/examples/ingestion/%s.v1.example.json" % name)
    return json.loads(p.read_text())


def prop(doc, field):
    """Property subschema with a local #/$defs/... $ref resolved."""
    p = doc["properties"][field]
    ref = p.get("$ref", "")
    if ref.startswith("#/$defs/"):
        merged = dict(doc["$defs"][ref[len("#/$defs/"):]])
        merged.update({k: v for k, v in p.items() if k != "$ref"})
        return merged
    return p


def walk(node):
    """Every dict in a JSON document, depth first."""
    if isinstance(node, dict):
        yield node
        for v in node.values():
            yield from walk(v)
    elif isinstance(node, list):
        for v in node:
            yield from walk(v)


def nullable(p, fmt=None):
    return isinstance(p.get("type"), list) and set(p["type"]) == {"string", "null"} \
        and (fmt is None or p.get("format") == fmt)


def int64(p, minimum=None, exclusive_minimum=None):
    return p.get("type") == "integer" and p.get("maximum") == INT64_MAX \
        and (minimum is None or p.get("minimum") == minimum) \
        and (exclusive_minimum is None or p.get("exclusiveMinimum") == exclusive_minimum)


# --------------------------------------------------------------- checks ----

def check_id(name):
    doc = schema(name)
    want = BASE + rel_schema(name)
    need(doc.get("$id") == want, "%s: $id is %r, want %r" % (name, doc.get("$id"), want))
    need(doc.get("$schema") == DIALECT, "%s: $schema is not draft 2020-12" % name)
    need(doc.get("title") == name + ".v1", "%s: title should be %s.v1" % (name, name))


def check_closed(name):
    doc = schema(name)
    need(doc.get("type") == "object", "%s: top level must be type object" % name)
    need(doc.get("additionalProperties") is False,
         "%s: additionalProperties must be false" % name)
    need(isinstance(doc.get("required"), list) and doc["required"],
         "%s: an explicit non-empty required list is mandatory" % name)
    for sub in walk(doc.get("properties", {})):
        if sub.get("type") == "object":
            need(sub.get("additionalProperties") is False,
                 "%s: nested object without additionalProperties:false" % name)


def check_all_required(name):
    doc = schema(name)
    props, req = set(doc["properties"]), doc["required"]
    need(len(req) == len(set(req)), "%s: duplicate entry in required" % name)
    need(props == set(req),
         "%s: required must cover every property (a canonical snapshot carries a "
         "nullable VALUE, never an absent KEY); missing=%s extra=%s"
         % (name, sorted(props - set(req)), sorted(set(req) - props)))


def check_described(name):
    doc = schema(name)
    need(len(doc.get("description", "")) > 200,
         "%s: the schema needs a description an auditor can use" % name)
    for field, p in doc["properties"].items():
        need(len(p.get("description", "")) > 30,
             "%s.%s: every field carries a description that says what it means "
             "and its unit (contracts/README §2)" % (name, field))


def check_utc(name):
    doc = schema(name)
    for field, p in doc["properties"].items():
        if p.get("format") == "date-time":
            need(p.get("pattern") == UTC,
                 "%s.%s: date-time must be pinned UTC-only with %r" % (name, field, UTC))


def check_example_keys(name):
    doc, ex = schema(name), example(name)
    need(set(ex) == set(doc["required"]),
         "%s: example keys must be exactly the required set; missing=%s extra=%s"
         % (name, sorted(set(doc["required"]) - set(ex)), sorted(set(ex) - set(doc["required"]))))


def check_example_money(name):
    for node in walk(example(name)):
        for k, v in node.items():
            if k.endswith("Minor"):
                need(isinstance(v, int) and not isinstance(v, bool),
                     "%s.%s = %r: minor units are JSON integers, never floats or "
                     "strings (contracts/README §4)" % (name, k, v))
            if k == "currency":
                need(v == "EUR", "%s.%s = %r: the golden examples are one EUR "
                                 "portfolio" % (name, k, v))


def check_currency():
    for name in MONEY_CARRYING:
        p = schema(name)["properties"]["currency"]
        need(p.get("pattern") == "^[A-Z]{3}$",
             "%s.currency must be pinned to ISO-4217 alpha-3" % name)


def check_closed_vocabularies():
    cases = {"CustomerSnapshot": ("status", {"ACTIVE", "DECEASED"}),
             "AccountSnapshot": ("status", {"ACTIVE", "CLOSED", "WRITTEN_OFF"}),
             "PaymentNotification": ("channel", {"BANK_TRANSFER", "DIRECT_DEBIT"})}
    for name, (field, expected) in cases.items():
        values = schema(name)["properties"][field].get("enum")
        need(isinstance(values, list) and values, "%s.%s must be a closed enum" % (name, field))
        need(expected <= set(values),
             "%s.%s enum is missing %s" % (name, field, sorted(expected - set(values))))
        leak = {"UNKNOWN", "OTHER", "N_A", "MISC"} & set(values)
        need(not leak, "%s.%s must not carry an escape-hatch member %s: an unmapped "
                       "source code is dead-lettered, not defaulted" % (name, field, sorted(leak)))


def check_source_system():
    for name in NAMES[:4]:
        p = schema(name)["properties"]["sourceSystem"]
        need(p.get("pattern") == "^[A-Z][A-Z0-9_]{1,31}$",
             "%s.sourceSystem must use the platform source-id shape "
             "(SCREAMING_SNAKE, as contracts/files/*.yaml and the domain events do)" % name)


def check_account_dpd():
    doc = schema("AccountSnapshot")
    need("oldestUnpaidDueDate" in doc["required"],
         "oldestUnpaidDueDate must be a REQUIRED key: an absent key is "
         "indistinguishable from a truncated message")
    p = doc["properties"]["oldestUnpaidDueDate"]
    need(nullable(p, "date"),
         "oldestUnpaidDueDate must be a nullable ISO date: null means no unpaid "
         "amount, i.e. DPD undefined rather than zero")
    text = p["description"] + doc["properties"]["asOf"]["description"]
    for token in ("dpd", "asOf"):
        need(token.lower() in text.lower(),
             "the DPD calculation (dpd = asOf - oldestUnpaidDueDate) must be "
             "documented on the fields that carry it; missing %r" % token)


def check_account_money():
    doc = schema("AccountSnapshot")
    need(int64(prop(doc, "currentBalanceMinor"), minimum=INT64_MIN),
         "currentBalanceMinor must be a signed int64: a credit balance is legitimate")
    for field in ("overdueAmountMinor", "minimumDueMinor"):
        need(int64(prop(doc, field), minimum=0), "%s must be a non-negative int64" % field)
    for field in ("closedAt", "lastPaymentAt"):
        need(isinstance(doc["properties"][field].get("type"), list),
             "%s must be nullable" % field)
    need(doc["properties"]["openedAt"].get("format") == "date",
         "openedAt must be an ISO date, never the source's int YYYYMMDD")


def check_debt_money():
    doc = schema("DebtSnapshot")
    for field in ("principalMinor", "interestMinor", "feesMinor", "penaltiesMinor"):
        need(int64(prop(doc, field), minimum=0), "%s must be a non-negative int64" % field)
    need("recoverableAmountMinor" not in doc["properties"],
         "DebtSnapshot carries components only: the total is derived by debt-service "
         "so the parts and the total cannot disagree on the wire")


def check_payment_dedup():
    doc = schema("PaymentNotification")
    need(int64(prop(doc, "amountMinor"), exclusive_minimum=0),
         "amountMinor must be a strictly positive int64: a reversal is the same "
         "natural key with reversed=true, never a negative amount")
    need(doc["properties"]["reversed"].get("type") == "boolean"
         and doc["properties"]["reversed"].get("default") is False,
         "reversed must be a boolean with a documented default of false")
    text = doc["description"] + doc["properties"]["externalPaymentRef"]["description"]
    for token in ("sourceSystem", "externalPaymentRef", "dedup"):
        need(token.lower() in text.lower(),
             "the natural dedup key must be documented; missing %r" % token)


def check_webhook():
    doc = schema("PaymentWebhook")
    want_ulid = BASE + "schemas/envelope/EventEnvelope.v1.json#/$defs/ulid"
    want_payment = BASE + "schemas/ingestion/PaymentNotification.v1.json"
    need(doc["properties"]["eventId"].get("$ref") == want_ulid,
         "PaymentWebhook.eventId must $ref the envelope ULID def, not restate the pattern")
    need(doc["properties"]["payment"].get("$ref") == want_payment,
         "PaymentWebhook.payment must $ref PaymentNotification.v1 so the provider "
         "contract and the canonical topic cannot drift apart")
    for token in ("X-Signature", "HMAC", "JWT", "/v1/webhooks/payments"):
        need(token in doc["description"],
             "PaymentWebhook must document %r (transport-level auth is not in the body)" % token)


def check_customer_pii():
    doc = schema("CustomerSnapshot")
    for field in ("fullName", "dob", "phone", "email"):
        need(field in doc["required"], "%s must be a required key" % field)
        need("null" in doc["properties"][field]["type"],
             "%s must be nullable: PII minimisation means a consumer that does not "
             "need it must not require a value" % field)
        need("PII" in doc["properties"][field]["description"],
             "%s must be marked as PII for the audit reader" % field)
    for token in ("A§69", "customer-service", "MASKING POLICY"):
        need(token in doc["description"],
             "CustomerSnapshot must document PII flow and analytics masking; missing %r" % token)


def check_topics():
    topics = {"CustomerSnapshot": "ingestion.customers.v1",
              "AccountSnapshot": "ingestion.accounts.v1",
              "DebtSnapshot": "ingestion.debts.v1",
              "PaymentNotification": "ingestion.payments.v1"}
    for name, topic in topics.items():
        doc = schema(name)
        need(topic in doc["description"], "%s must name its canonical topic %s" % (name, topic))
        need("EventEnvelope" in doc["description"],
             "%s must state that it travels as the A§24 envelope payload" % name)


CHECKS = {
    "id": check_id, "closed": check_closed, "all-required": check_all_required,
    "described": check_described, "utc": check_utc,
    "example-keys": check_example_keys, "example-money": check_example_money,
    "currency": check_currency, "vocabularies": check_closed_vocabularies,
    "source-system": check_source_system, "account-dpd": check_account_dpd,
    "account-money": check_account_money, "debt-money": check_debt_money,
    "payment-dedup": check_payment_dedup, "webhook": check_webhook,
    "customer-pii": check_customer_pii, "topics": check_topics,
}

if __name__ == "__main__":
    if len(sys.argv) < 2 or sys.argv[1] not in CHECKS:
        print("usage: con6.py <%s> [name]" % "|".join(sorted(CHECKS)), file=sys.stderr)
        sys.exit(2)
    fn = CHECKS[sys.argv[1]]
    fn(*sys.argv[2:])
PYEOF

py() { python3 "$TMP/con6.py" "$@"; }

echo "=== 1. deliverables exist ==="
for n in "${NAMES[@]}"; do
	check "exists: $SCHEMA_DIR/$n.v1.json" test -f "$SCHEMA_DIR/$n.v1.json"
	check "exists: $EXAMPLE_DIR/$n.v1.example.json" test -f "$EXAMPLE_DIR/$n.v1.example.json"
done
check "exists: scripts/verify/CON-6.sh" test -f scripts/verify/CON-6.sh
check "no stray files under $SCHEMA_DIR" \
	test "$(find "$SCHEMA_DIR" -type f ! -name '*.v1.json' | wc -l | tr -d ' ')" = 0
check "no stray files under $EXAMPLE_DIR" \
	test "$(find "$EXAMPLE_DIR" -type f ! -name '*.v1.example.json' | wc -l | tr -d ' ')" = 0

echo
echo "=== 2. valid JSON, \$id == path, draft 2020-12 ==="
for n in "${NAMES[@]}"; do
	check "valid JSON: $n.v1.json" python3 -m json.tool "$SCHEMA_DIR/$n.v1.json"
	check "valid JSON: $n.v1.example.json" python3 -m json.tool "$EXAMPLE_DIR/$n.v1.example.json"
	check "\$id == path + \$schema == 2020-12: $n" py id "$n"
done

echo
echo "=== 3. the \$id check is a real guard (expected-fail) ==="
mkdir -p "$TMP/doctored/contracts/schemas/ingestion" "$TMP/doctored/contracts/examples/ingestion"
cp "$SCHEMA_DIR"/*.json "$TMP/doctored/contracts/schemas/ingestion/"
cp "$EXAMPLE_DIR"/*.json "$TMP/doctored/contracts/examples/ingestion/"
python3 - "$TMP/doctored/contracts/schemas/ingestion/AccountSnapshot.v1.json" <<'PYEOF'
import json
import sys
p = sys.argv[1]
doc = json.load(open(p))
doc["$id"] = "https://contracts.collections.internal/schemas/ingestion/Account.v1.json"
json.dump(doc, open(p, "w"))
PYEOF
check "the copied tree itself is fine (CustomerSnapshot under the copy passes)" \
	env CON6_ROOT="$TMP/doctored" python3 "$TMP/con6.py" id CustomerSnapshot
check_fails "a doctored \$id is rejected (the check is not vacuous)" \
	env CON6_ROOT="$TMP/doctored" python3 "$TMP/con6.py" id AccountSnapshot

echo
echo "=== 4. every schema is closed, fully required and documented ==="
for n in "${NAMES[@]}"; do
	check "additionalProperties:false + explicit required: $n" py closed "$n"
	check "required covers every property (nullable value, never absent key): $n" py all-required "$n"
	check "every field carries a description: $n" py described "$n"
	check "date-time fields are pinned UTC-only: $n" py utc "$n"
done

echo
echo "=== 5. golden examples mirror their schema ==="
for n in "${NAMES[@]}"; do
	check "example keys == required keys: $n" py example-keys "$n"
	check "minor units are integers and the portfolio is EUR: $n" py example-money "$n"
done

echo
echo "=== 6. platform conventions (money, identifiers, vocabularies) ==="
check "currency is ISO-4217 alpha-3 wherever money travels" py currency
check "sourceSystem uses the platform source-id shape" py source-system
check "lifecycle vocabularies are closed enums with no escape hatch" py vocabularies
check "canonical topic + envelope documented on every snapshot" py topics

echo
echo "=== 7. load-bearing contract details ==="
check "AccountSnapshot: oldestUnpaidDueDate is a required key with a nullable date" py account-dpd
check "AccountSnapshot: int64 minor units, signed balance, ISO dates" py account-money
check "DebtSnapshot: non-negative int64 components, no derived total" py debt-money
check "PaymentNotification: positive int64 amount + documented natural dedup key" py payment-dedup
check "PaymentWebhook: \$refs the envelope ULID and PaymentNotification" py webhook
check "CustomerSnapshot: PII nullable, marked, and masking documented" py customer-pii

echo
echo "=== 8. the contracts harness ==="
check "go -C contracts test ./... (schemas compile, examples validate, no orphans)" \
	go -C "$REPO_ROOT/contracts" test ./...

# Expected-fail: the harness must reject an example that violates the schema.
# Run against a throwaway copy of the module so the working tree is never
# mutated, trimmed to the envelope plus this WP's contracts so the assertion
# depends on CON-6 alone. The same filtered tests must pass on the pristine
# copy first, so a failure can only come from the mutation.
echo
echo "=== 9. the harness rejects a bad snapshot (expected-fail) ==="
cp -R "$REPO_ROOT/contracts" "$TMP/module"
# Allowlist, not denylist: keep the envelope (which this WP $refs) and the
# ingestion contracts, drop every other schema/example family so no other work
# package's in-flight file can decide this assertion either way.
find "$TMP/module/schemas" "$TMP/module/examples" -mindepth 1 -maxdepth 1 \
	-type d ! -name envelope ! -name ingestion -exec rm -rf {} +
find "$TMP/module/schemas" "$TMP/module/examples" -mindepth 1 -maxdepth 1 \
	-type f -name '*.json' -delete
HARNESS_TESTS='TestSchemasCompile|TestExamplesValidateAgainstSchemas'
check "pristine copy (envelope + ingestion contracts only) validates" \
	go -C "$TMP/module" test ./... -run "$HARNESS_TESTS"
python3 - "$TMP/module/examples/ingestion/AccountSnapshot.v1.example.json" <<'PYEOF'
import json
import sys
p = sys.argv[1]
doc = json.load(open(p))
del doc["oldestUnpaidDueDate"]          # the DPD input: a required key
doc["overdueAmountMinor"] = 312.40      # major units as a float
json.dump(doc, open(p, "w"), indent=2)
PYEOF
check_fails "harness rejects an AccountSnapshot example with no oldestUnpaidDueDate and a float amount" \
	go -C "$TMP/module" test ./... -run "$HARNESS_TESTS"
check "the rejection names the offending example" \
	grep -q "AccountSnapshot.v1.example.json" \
	<(go -C "$TMP/module" test ./... -run "$HARNESS_TESTS" 2>&1 || true)

echo
printf 'CON-6: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
