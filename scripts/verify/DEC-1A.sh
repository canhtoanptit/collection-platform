#!/usr/bin/env bash
#
# scripts/verify/DEC-1A.sh — decisioning JSON Schemas, decisioning event schemas,
# the reason-code registry and the champion/challenger allocation golden vectors.
#
# What this proves, beyond "the files exist":
#   * the harness accepts every artefact  (go -C contracts test ./...)
#   * every $id equals the artefact's path (the loader and every cross-file $ref
#     resolve through it, so a wrong $id silently breaks every consumer)
#   * the reason-code vocabulary is CLOSED: every reasonCode used in any of this
#     WP's schemas or examples exists in registries/reason-codes.v1.json
#   * the context field catalogue and the context document agree leaf for leaf —
#     a rule can only address a path the catalogue declares, so a drift between
#     them would make the DSL's type promise a lie
#   * the allocation vectors are reproducible from their committed generator,
#     byte for byte, and are internally consistent with an independent
#     re-implementation of the documented algorithm
#   * the guards actually reject: FORBID_CHANNEL outside a POLICY set, a sixth
#     level of condition nesting, a three-element BETWEEN, a defaultScore on an
#     onError:FAIL model, a treatment type on the wrong channel, a wrong $id, an
#     orphan example, an unknown reason code, and a tampered vector file
#
# Environment: none. Needs bash, python3 (stdlib only), go, and the repo.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

TMP="$(mktemp -d)"

SCHEMA_DIR="contracts/schemas/decisioning"
EXAMPLE_DIR="contracts/examples/decisioning"
RULE_SET_EXAMPLE="$EXAMPLE_DIR/rule-set.v1.example.json"
STRATEGY_EXAMPLE="$EXAMPLE_DIR/strategy-document.v1.example.json"
REGISTRY="contracts/registries/reason-codes.v1.json"
VECTORS="contracts/testdata/allocation-golden-vectors.json"
GENERATOR="contracts/testdata/allocation-golden-vectors.gen.py"
PROBE_SCHEMA="$SCHEMA_DIR/zz-negative-probe.v1.json"
PROBE_EXAMPLE="$EXAMPLE_DIR/zz-negative-probe.v1.example.json"

# Mutation probes edit tracked example files in place, so restoration is
# unconditional and byte-exact — never left to the happy path.
cleanup() {
	rm -f "$PROBE_SCHEMA" "$PROBE_EXAMPLE"
	[ -f "$TMP/rule-set.orig" ] && cp "$TMP/rule-set.orig" "$RULE_SET_EXAMPLE"
	[ -f "$TMP/strategy.orig" ] && cp "$TMP/strategy.orig" "$STRATEGY_EXAMPLE"
	rm -rf "$TMP"
}
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

# check <description> <command...>       -- command must succeed
check() {
	local desc="$1"
	shift
	if "$@" >/dev/null 2>&1; then ok "$desc"; else bad "$desc"; fi
}

# check_fails <description> <command...> -- command must FAIL (guard proof)
check_fails() {
	local desc="$1"
	shift
	if "$@" >/dev/null 2>&1; then
		bad "$desc (command unexpectedly succeeded)"
	else
		ok "$desc"
	fi
}

# count_files <label> <expected> <dir...>
count_files() {
	local label="$1" want="$2" got
	shift 2
	got="$(find "$@" -type f -name '*.json' | wc -l | tr -d ' ')"
	if [ "$got" = "$want" ]; then
		ok "$label: $want JSON artefacts"
	else
		bad "$label: $got JSON artefacts, want $want"
	fi
}

harness() { go -C contracts test -count=1 "$@" ./...; }

# --------------------------------------------------------------------------
echo "=== 1. artefact inventory ==="
count_files "decisioning schemas" 7 "$SCHEMA_DIR"
count_files "decisioning event schemas (strategy 5 + decision 1 + treatment 3)" 9 \
	contracts/schemas/events/strategy contracts/schemas/events/decision contracts/schemas/events/treatment
count_files "decisioning event examples (envelope-wrapped)" 9 \
	contracts/examples/events/strategy contracts/examples/events/decision contracts/examples/events/treatment
count_files "strategy event schemas" 5 contracts/schemas/events/strategy
count_files "decision event schemas" 1 contracts/schemas/events/decision
count_files "treatment event schemas" 3 contracts/schemas/events/treatment
count_files "strategy event examples" 5 contracts/examples/events/strategy
count_files "decision event examples" 1 contracts/examples/events/decision
count_files "treatment event examples" 3 contracts/examples/events/treatment

for f in context-field-catalogue context-document rule-set strategy-document \
	guardrail-config population-line decision-outcome-line; do
	check "schema exists: $f.v1.json" test -f "$SCHEMA_DIR/$f.v1.json"
	check "example exists: $f.v1.example.json" test -f "$EXAMPLE_DIR/$f.v1.example.json"
done

decisioning_examples="$(find "$EXAMPLE_DIR" -type f -name '*.example.json' | wc -l | tr -d ' ')"
if [ "$decisioning_examples" -ge 7 ]; then
	ok "$EXAMPLE_DIR ships $decisioning_examples direct examples (>= 7)"
else
	bad "$EXAMPLE_DIR ships $decisioning_examples direct examples, want >= 7"
fi

check "reason-code registry exists" test -f "$REGISTRY"
check "allocation vectors exist" test -f "$VECTORS"
check "allocation vector generator exists" test -f "$GENERATOR"

# --------------------------------------------------------------------------
echo
echo "=== 2. \$id equals path, dialect is 2020-12 ==="
cat >"$TMP/idcheck.py" <<'PY'
"""Assert $id == https://contracts.collections.internal/<path in the contracts module>."""
import json, pathlib, sys

BASE = "https://contracts.collections.internal/"
DRAFT = "https://json-schema.org/draft/2020-12/schema"
bad = 0
for root in sys.argv[1:]:
    for p in sorted(pathlib.Path(root).rglob("*.json")):
        doc = json.loads(p.read_text(encoding="utf-8"))
        rel = p.relative_to("contracts").as_posix()
        want = BASE + rel
        if doc.get("$id") != want:
            print(f"{p}: $id = {doc.get('$id')!r}, want {want!r}")
            bad += 1
        if doc.get("$schema") != DRAFT:
            print(f"{p}: $schema = {doc.get('$schema')!r}, want {DRAFT!r}")
            bad += 1
        if doc.get("additionalProperties") is not False and "oneOf" not in doc:
            print(f"{p}: top-level object must be additionalProperties:false")
            bad += 1
        if "required" not in doc:
            print(f"{p}: top-level object must declare an explicit required list")
            bad += 1
sys.exit(1 if bad else 0)
PY
check "every DEC-1A schema has the right \$id, dialect, additionalProperties:false and required" \
	python3 "$TMP/idcheck.py" "$SCHEMA_DIR" \
	contracts/schemas/events/strategy contracts/schemas/events/decision contracts/schemas/events/treatment

# --------------------------------------------------------------------------
echo
echo "=== 3. reason-code closure ==="
cat >"$TMP/reasoncodes.py" <<'PY'
"""Every reason code used anywhere in DEC-1A's artefacts must exist in the registry.

Collected from: string values of `reasonCode`, string items of `reasonCodes` and
`suppressionReasons`, and — inside schema files, where those keys hold a
subschema rather than a value — the `examples` of those subschemas. Deliberately
strict over our own artefacts: population-line's `legacyDecision.reasonCodes`
allows unmapped legacy codes by contract, but every code WE ship is in the
registry, so a typo in an example is a red build and not a lie in the docs.
"""
import json, pathlib, re, sys

CODE_KEYS = {"reasonCode", "reasonCodes", "suppressionReasons"}
CODE_RE = re.compile(r"^[A-Z][A-Z0-9_]{2,63}$")

registry = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
known = {c["code"] for c in registry["codes"]}

used: dict[str, set[str]] = {}


def harvest(node, sink):
    if isinstance(node, str):
        if CODE_RE.match(node):
            sink.add(node)
    elif isinstance(node, list):
        for item in node:
            harvest(item, sink)
    elif isinstance(node, dict):
        harvest(node.get("examples", []), sink)


def walk(node, sink):
    if isinstance(node, dict):
        for key, value in node.items():
            if key in CODE_KEYS:
                harvest(value, sink)
            walk(value, sink)
    elif isinstance(node, list):
        for item in node:
            walk(item, sink)


for root in sys.argv[2:]:
    for p in sorted(pathlib.Path(root).rglob("*.json")):
        sink: set[str] = set()
        walk(json.loads(p.read_text(encoding="utf-8")), sink)
        for code in sink:
            used.setdefault(code, set()).add(p.as_posix())

if not used:
    print("no reason codes found at all — the collector is broken, not the artefacts")
    sys.exit(1)

unknown = {c: f for c, f in used.items() if c not in known}
for code, files in sorted(unknown.items()):
    print(f"unknown reason code {code} used in: {', '.join(sorted(files))}")
print(f"checked {len(used)} distinct reason codes against {len(known)} registry codes")
sys.exit(1 if unknown else 0)
PY
check "every reasonCode used in DEC-1A artefacts exists in the registry" \
	python3 "$TMP/reasoncodes.py" "$REGISTRY" "$SCHEMA_DIR" "$EXAMPLE_DIR" \
	contracts/schemas/events/strategy contracts/schemas/events/decision contracts/schemas/events/treatment \
	contracts/examples/events/strategy contracts/examples/events/decision contracts/examples/events/treatment

mkdir -p "$TMP/bogus"
printf '{"reasonCodes": ["DPD_31_60", "BOGUS_CODE_XYZ"]}\n' >"$TMP/bogus/unknown.json"
check_fails "the closure checker rejects an unknown reason code" \
	python3 "$TMP/reasoncodes.py" "$REGISTRY" "$TMP/bogus"

cat >"$TMP/registry.py" <<'PY'
"""Registry hygiene: >= 25 codes, unique, SCREAMING_SNAKE, closed category set, described."""
import json, pathlib, re, sys

CATEGORIES = {"POLICY", "ELIGIBILITY", "SEGMENT", "RULE", "MODEL", "CONSTRAINT", "EXECUTION"}
CODE_RE = re.compile(r"^[A-Z][A-Z0-9_]{2,63}$")

doc = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
bad = 0
if doc.get("registryVersion") != 1:
    print(f"registryVersion = {doc.get('registryVersion')!r}, want 1")
    bad += 1
if set(doc) != {"registryVersion", "codes"}:
    print(f"unexpected top-level keys: {sorted(set(doc) - {'registryVersion', 'codes'})}")
    bad += 1
codes = doc["codes"]
if len(codes) < 25:
    print(f"{len(codes)} codes, want >= 25")
    bad += 1
seen = set()
for entry in codes:
    if set(entry) != {"code", "category", "description"}:
        print(f"{entry.get('code')}: keys {sorted(entry)}, want code/category/description")
        bad += 1
    code = entry.get("code", "")
    if not CODE_RE.match(code):
        print(f"{code!r} is not SCREAMING_SNAKE_CASE")
        bad += 1
    if code in seen:
        print(f"{code} is listed twice")
        bad += 1
    seen.add(code)
    if entry.get("category") not in CATEGORIES:
        print(f"{code}: category {entry.get('category')!r} is outside {sorted(CATEGORIES)}")
        bad += 1
    if len(entry.get("description", "")) < 20:
        print(f"{code}: description is missing or too short to be useful")
        bad += 1
print(f"{len(codes)} reason codes across {len({e['category'] for e in codes})} categories")
sys.exit(1 if bad else 0)
PY
check "reason-code registry is well formed (>= 25 unique codes, closed categories)" \
	python3 "$TMP/registry.py" "$REGISTRY"

# --------------------------------------------------------------------------
echo
echo "=== 4. field catalogue agrees with the context document ==="
cat >"$TMP/catalogue.py" <<'PY'
"""The catalogue must declare exactly the addressable leaves of the context document.

Leaves are derived from context-document.v1.json by walking `properties` and
resolving local `$ref`s. Arrays (provenance) are not addressable — the DSL has no
quantifiers — and an object with `additionalProperties` as a subschema
(contactHistory.byChannel7d) is an open map, so the catalogue may list any number
of concrete keys beneath it but must list at least one.
"""
import json, pathlib, sys

schema = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
catalogue = json.loads(pathlib.Path(sys.argv[2]).read_text(encoding="utf-8"))
defs = schema.get("$defs", {})

leaves: set[str] = set()
open_prefixes: set[str] = set()


def resolve(node):
    ref = node.get("$ref")
    if ref and ref.startswith("#/$defs/"):
        return defs[ref.split("/")[-1]]
    return node


def walk(node, prefix):
    node = resolve(node)
    props = node.get("properties")
    if props:
        for name, child in props.items():
            walk(child, f"{prefix}.{name}" if prefix else name)
        return
    types = node.get("type")
    types = types if isinstance(types, list) else [types]
    if "array" in types:
        return  # not addressable
    if "object" in types and isinstance(node.get("additionalProperties"), dict):
        open_prefixes.add(prefix)
        return
    if prefix:
        leaves.add(prefix)


walk(schema, "")

paths = [f["path"] for f in catalogue["fields"]]
bad = 0
if len(paths) != len(set(paths)):
    dupes = sorted({p for p in paths if paths.count(p) > 1})
    print(f"duplicate catalogue paths: {dupes}")
    bad += 1

declared = set(paths)
for missing in sorted(leaves - declared):
    print(f"context document leaf not in the catalogue: {missing}")
    bad += 1
for extra in sorted(declared - leaves):
    if not any(extra.startswith(prefix + ".") for prefix in open_prefixes):
        print(f"catalogue path is not a context document leaf: {extra}")
        bad += 1
for prefix in sorted(open_prefixes):
    if not any(p.startswith(prefix + ".") for p in declared):
        print(f"open map {prefix} has no catalogued keys")
        bad += 1

by_type = {f["path"]: f["type"] for f in catalogue["fields"]}
for money in ("delinquency.amountOverdue", "account.currentBalance"):
    if by_type.get(money) != "number":
        print(f"{money} must be catalogued as a number (decimal string on the wire)")
        bad += 1
    unit = next(f.get("unit") for f in catalogue["fields"] if f["path"] == money)
    if unit != "majorUnits":
        print(f"{money} must be catalogued unit majorUnits, got {unit!r}")
        bad += 1

print(f"{len(leaves)} context leaves, {len(paths)} catalogue entries, "
      f"{len(open_prefixes)} open map(s)")
sys.exit(1 if bad else 0)
PY
check "catalogue covers every context-document leaf and nothing else" \
	python3 "$TMP/catalogue.py" "$SCHEMA_DIR/context-document.v1.json" \
	"$EXAMPLE_DIR/context-field-catalogue.v1.example.json"

# --------------------------------------------------------------------------
echo
echo "=== 5. allocation golden vectors ==="
cat >"$TMP/vectors.py" <<'PY'
"""Independent re-implementation of the documented allocation algorithm.

Written from the `algorithm` paragraph in the vector file rather than from the
generator, so it is a second opinion: if the committed expectations were edited
to match a broken implementation, this disagrees.
"""
import hashlib, json, pathlib, struct, sys

doc = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
vectors = doc["vectors"]
bad = 0

for field in ("vectorSetVersion", "algorithm", "generator", "vectorCount", "vectors"):
    if field not in doc:
        print(f"vector file is missing the {field} header field")
        bad += 1
if len(doc.get("algorithm", "")) < 200:
    print("the algorithm header must document the algorithm, not name it")
    bad += 1
if doc.get("vectorCount") != len(vectors):
    print(f"vectorCount {doc.get('vectorCount')} != {len(vectors)} vectors")
    bad += 1
if len(vectors) < 50:
    print(f"{len(vectors)} vectors, want >= 50")
    bad += 1

for i, v in enumerate(vectors):
    if set(v) != {"accountId", "salt", "arms", "expectedBucket", "expectedArm"}:
        print(f"vector {i}: keys {sorted(v)}")
        bad += 1
        continue
    digest = hashlib.sha256((v["accountId"] + ":" + v["salt"]).encode("utf-8")).digest()
    bucket = struct.unpack(">Q", digest[:8])[0] % 10000
    if bucket != v["expectedBucket"]:
        print(f"vector {i} ({v['accountId']}/{v['salt']}): bucket {bucket} != {v['expectedBucket']}")
        bad += 1
    total = 0
    arm = None
    for a in v["arms"]:
        total += a["percentBps"]
        if arm is None and total > bucket:
            arm = a["name"]
    if total != 10000:
        print(f"vector {i}: arms total {total} bps, want 10000")
        bad += 1
    if arm != v["expectedArm"]:
        print(f"vector {i} ({v['accountId']}/{v['salt']}): arm {arm} != {v['expectedArm']}")
        bad += 1

def has(pred):
    return any(pred(v) for v in vectors)

def bps(v):
    return [a["percentBps"] for a in v["arms"]]

coverage = {
    "90/10 split": has(lambda v: bps(v) == [9000, 1000]),
    "50/50 split": has(lambda v: bps(v) == [5000, 5000]),
    "100/0 split": has(lambda v: bps(v) == [10000, 0]),
    "zero-width first arm": has(lambda v: v["arms"][0]["percentBps"] == 0),
    "three-arm split": has(lambda v: len(v["arms"]) == 3),
    "boundary bucket 8999 on 90/10": has(
        lambda v: bps(v) == [9000, 1000] and v["expectedBucket"] == 8999 and v["expectedArm"] == "CHAMPION"),
    "boundary bucket 9000 on 90/10": has(
        lambda v: bps(v) == [9000, 1000] and v["expectedBucket"] == 9000 and v["expectedArm"] == "CHALLENGER"),
    "boundary bucket 4999 on 50/50": has(
        lambda v: bps(v) == [5000, 5000] and v["expectedBucket"] == 4999),
    "boundary bucket 5000 on 50/50": has(
        lambda v: bps(v) == [5000, 5000] and v["expectedBucket"] == 5000),
}
salts_per_account: dict[str, set[str]] = {}
for v in vectors:
    salts_per_account.setdefault(v["accountId"], set()).add(v["salt"])
coverage["same account under >= 2 salts"] = any(len(s) >= 2 for s in salts_per_account.values())
coverage["a salt change moves at least one account"] = any(
    len({vv["expectedBucket"] for vv in vectors if vv["accountId"] == acct}) > 1
    for acct, salts in salts_per_account.items() if len(salts) >= 2)

for name, present in coverage.items():
    if not present:
        print(f"vector coverage gap: {name}")
        bad += 1

print(f"{len(vectors)} vectors verified against an independent implementation; "
      f"{len(coverage)} coverage cases present")
sys.exit(1 if bad else 0)
PY
check "vectors agree with an independent implementation and cover the boundaries" \
	python3 "$TMP/vectors.py" "$VECTORS"

check "the committed vectors are exactly what the generator produces (--check)" \
	python3 "$GENERATOR" --check --out "$VECTORS"

python3 "$GENERATOR" --out "$TMP/regenerated.json" >/dev/null
check "regenerating the vectors is byte-identical (determinism)" \
	cmp -s "$VECTORS" "$TMP/regenerated.json"

python3 - "$VECTORS" "$TMP/tampered.json" <<'PY'
import json, sys
doc = json.loads(open(sys.argv[1], encoding="utf-8").read())
doc["vectors"][0]["expectedArm"] = "CHALLENGER" if doc["vectors"][0]["expectedArm"] == "CHAMPION" else "CHAMPION"
open(sys.argv[2], "w", encoding="utf-8").write(json.dumps(doc, indent=2) + "\n")
PY
check_fails "a tampered vector file fails the generator's --check" \
	python3 "$GENERATOR" --check --out "$TMP/tampered.json"
check_fails "a tampered vector file fails the independent verifier" \
	python3 "$TMP/vectors.py" "$TMP/tampered.json"

cat >"$TMP/crossref.py" <<'PY'
"""The decision-outcome-line example's arm and bucket must be the real allocation.

Cross-artefact, on purpose: the outcome example, the strategy example's
experiment salt and the golden vectors have to describe one world, or a reader
learns the wrong thing from whichever they open first.
"""
import hashlib, json, pathlib, struct, sys

outcome = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
strategy = json.loads(pathlib.Path(sys.argv[2]).read_text(encoding="utf-8"))
vectors = json.loads(pathlib.Path(sys.argv[3]).read_text(encoding="utf-8"))["vectors"]

salt = strategy["experiment"]["salt"]
arms = strategy["experiment"]["arms"]
account = outcome["subject"]["accountId"]
digest = hashlib.sha256((account + ":" + salt).encode("utf-8")).digest()
bucket = struct.unpack(">Q", digest[:8])[0] % 10000
total, arm = 0, None
for a in arms:
    total += a["percentBps"]
    if arm is None and total > bucket:
        arm = a["name"]

bad = 0
if total != 10000:
    print(f"strategy example arms total {total} bps, want 10000")
    bad += 1
if outcome["allocationBucket"] != bucket:
    print(f"outcome example allocationBucket {outcome['allocationBucket']} != real bucket {bucket}")
    bad += 1
if outcome["experimentArm"] != arm:
    print(f"outcome example experimentArm {outcome['experimentArm']} != real arm {arm}")
    bad += 1
if not any(v["accountId"] == account and v["salt"] == salt and v["expectedBucket"] == bucket
           for v in vectors):
    print(f"no golden vector covers ({account}, {salt}) -> {bucket}")
    bad += 1
if outcome["treatmentCode"] not in {t["treatmentCode"] for t in strategy["treatments"]}:
    print(f"outcome treatment {outcome['treatmentCode']} is not offered by the strategy example")
    bad += 1
print(f"{account} under salt {salt} -> bucket {bucket}, arm {arm}")
sys.exit(1 if bad else 0)
PY
check "outcome example, strategy salt and golden vectors describe one allocation" \
	python3 "$TMP/crossref.py" "$EXAMPLE_DIR/decision-outcome-line.v1.example.json" \
	"$STRATEGY_EXAMPLE" "$VECTORS"

cat >"$TMP/ruleref.py" <<'PY'
"""Every treatment a rule selects must be offered by the strategy that uses the set."""
import json, pathlib, sys

rules = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
strategy = json.loads(pathlib.Path(sys.argv[2]).read_text(encoding="utf-8"))
catalogue = {f["path"] for f in json.loads(pathlib.Path(sys.argv[3]).read_text(encoding="utf-8"))["fields"]}

offered = {t["treatmentCode"] for t in strategy["treatments"]}
bad = 0
if strategy["ruleSetRef"]["ruleSetId"] != rules["ruleSetId"] or \
        strategy["ruleSetRef"]["version"] != rules["version"]:
    print("the strategy example does not reference the rule-set example")
    bad += 1

fields, ops = set(), set()


def walk(node):
    if isinstance(node, dict):
        if "field" in node and "op" in node:
            fields.add(node["field"])
            ops.add(node["op"])
        for v in node.values():
            walk(v)
    elif isinstance(node, list):
        for v in node:
            walk(v)


for rule in rules["rules"]:
    walk(rule["when"])
    action = rule["then"]
    if action["outcome"] == "SELECT_TREATMENT" and action["treatmentCode"] not in offered:
        print(f"{rule['ruleId']} selects {action['treatmentCode']}, not offered by the strategy")
        bad += 1
walk(strategy["eligibility"])
walk(strategy["segments"])

for f in sorted(fields - catalogue):
    print(f"rule/strategy condition addresses uncatalogued field {f}")
    bad += 1

want_ops = {"EQ", "NE", "GT", "GTE", "LT", "LTE", "BETWEEN", "IN", "NOT_IN", "IS_NULL", "NOT_NULL"}
if want_ops - ops:
    print(f"the rule-set example never exercises: {sorted(want_ops - ops)}")
    bad += 1
print(f"{len(fields)} distinct fields, {len(ops)}/{len(want_ops)} operators exercised")
sys.exit(1 if bad else 0)
PY
echo
echo "=== 6. examples are mutually consistent ==="
check "rule-set example: every field catalogued, every treatment offered, all 11 operators used" \
	python3 "$TMP/ruleref.py" "$RULE_SET_EXAMPLE" "$STRATEGY_EXAMPLE" \
	"$EXAMPLE_DIR/context-field-catalogue.v1.example.json"

cat >"$TMP/d8.py" <<'PY'
"""The D§8 worked example must be reproducible from the committed artefacts.

dpd 35 on account A123 -> the EARLY_SMS_31_60 rule matches first under the
documented (priority DESC, ruleId ASC) order and selects an SMS treatment with
DPD_31_60; the outcome example carries that treatment plus the CHANNEL_ELIGIBLE
class of pipeline codes. This is a hand evaluation of the ordering rule, not a
call into the (not yet written) engine — it exists so the contracts themselves
demonstrate the worked example.
"""
import json, pathlib, sys

ctx = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
rules = json.loads(pathlib.Path(sys.argv[2]).read_text(encoding="utf-8"))
outcome = json.loads(pathlib.Path(sys.argv[3]).read_text(encoding="utf-8"))
strategy = json.loads(pathlib.Path(sys.argv[4]).read_text(encoding="utf-8"))

bad = 0
if ctx["delinquency"]["dpd"] != 35 or ctx["subject"]["accountId"] != "A123":
    print("the context example is no longer the D§8 fixture (A123, dpd 35)")
    bad += 1
if ctx["delinquency"]["amountOverdue"] != "500.00":
    print("the D§8 fixture overdue amount must be the major-unit string 500.00")
    bad += 1

ordered = sorted(rules["rules"], key=lambda r: (-r["priority"], r["ruleId"]))
first_sms = next((r for r in ordered
                  if r["then"].get("treatmentCode") == outcome["treatmentCode"]), None)
if first_sms is None:
    print(f"no rule selects {outcome['treatmentCode']}")
    bad += 1
else:
    if first_sms["then"]["reasonCode"] != "DPD_31_60":
        print(f"{first_sms['ruleId']} explains itself with "
              f"{first_sms['then']['reasonCode']}, want DPD_31_60")
        bad += 1
    dpd_leaf = next((c for c in first_sms["when"]["all"]
                     if c.get("field") == "delinquency.dpd"), None)
    if not dpd_leaf or dpd_leaf["op"] != "BETWEEN" or dpd_leaf["value"] != [31, 60]:
        print("the D§8 rule must gate on dpd BETWEEN [31, 60] (inclusive)")
        bad += 1
    elif not dpd_leaf["value"][0] <= ctx["delinquency"]["dpd"] <= dpd_leaf["value"][1]:
        print("dpd 35 does not fall in the D§8 rule's range")
        bad += 1

treatment = next((t for t in strategy["treatments"]
                  if t["treatmentCode"] == outcome["treatmentCode"]), None)
if treatment is None or treatment["channel"] != "SMS" or outcome["channel"] != "SMS":
    print("the D§8 outcome must be an SMS treatment")
    bad += 1
for code in ("DPD_31_60", "CHANNEL_ELIGIBLE"):
    if code not in outcome["reasonCodes"]:
        print(f"the D§8 outcome must carry {code}")
        bad += 1
print(f"D§8: dpd 35 -> {outcome['treatmentCode']} on {outcome['channel']} "
      f"({', '.join(outcome['reasonCodes'][:3])}, ...)")
sys.exit(1 if bad else 0)
PY
check "D§8 worked example: dpd 35 -> SMS with DPD_31_60 + CHANNEL_ELIGIBLE" \
	python3 "$TMP/d8.py" "$EXAMPLE_DIR/context-document.v1.example.json" \
	"$RULE_SET_EXAMPLE" "$EXAMPLE_DIR/decision-outcome-line.v1.example.json" "$STRATEGY_EXAMPLE"

# --------------------------------------------------------------------------
echo
echo "=== 7. the contracts harness accepts everything ==="
check "go -C contracts test ./... (schemas compile, examples validate, no orphans)" harness

# --------------------------------------------------------------------------
echo
echo "=== 8. expected failures — the guards actually reject ==="
cp "$RULE_SET_EXAMPLE" "$TMP/rule-set.orig"
cp "$STRATEGY_EXAMPLE" "$TMP/strategy.orig"

cat >"$TMP/mutate.py" <<'PY'
"""Apply one named mutation to an example, so a guard can be proven to reject it."""
import json, pathlib, sys

path = pathlib.Path(sys.argv[1])
which = sys.argv[2]
doc = json.loads(path.read_text(encoding="utf-8"))

if which == "forbid-channel-in-rules":
    doc["rules"][0]["then"] = {"outcome": "FORBID_CHANNEL", "channel": "SMS",
                               "reasonCode": "CHANNEL_FORBIDDEN_BY_POLICY"}
elif which == "select-in-policy":
    doc["kind"] = "POLICY"
elif which == "nesting-6":
    group = {"field": "delinquency.dpd", "op": "GT", "value": 1}
    for _ in range(6):
        group = {"all": [group]}
    doc["rules"][0]["when"] = group
elif which == "nesting-5":
    group = {"all": [{"field": "delinquency.dpd", "op": "GT", "value": 1}]}
    for _ in range(4):
        group = {"all": [group]}
    doc["rules"][0]["when"] = group
elif which == "between-three":
    doc["rules"][3]["when"]["all"][0]["value"] = [31, 60, 90]
elif which == "in-empty":
    doc["rules"][3]["when"]["all"][1]["value"] = []
elif which == "is-null-with-value":
    doc["rules"][6]["when"]["all"][0]["value"] = None
elif which == "all-and-any":
    doc["rules"][0]["when"] = {"all": [], "any": [{"field": "delinquency.dpd", "op": "GT", "value": 1}]}
elif which == "suppress-with-treatment":
    doc["rules"][0]["then"]["treatmentCode"] = "SMS_EARLY_REMINDER"
elif which == "unknown-field-shape":
    doc["rules"][0]["when"]["all"][0]["field"] = "Arrangement..hasActiveArrangement"
elif which == "fail-with-default-score":
    doc["modelRefs"][0]["onError"] = "FAIL"
elif which == "default-without-score":
    del doc["modelRefs"][0]["defaultScore"]
elif which == "type-channel-mismatch":
    doc["treatments"][1]["channel"] = "LETTER"
elif which == "schema-version-2":
    doc["schemaVersion"] = 2
elif which == "arm-bps-out-of-range":
    doc["experiment"]["arms"][0]["percentBps"] = 10001
else:
    raise SystemExit(f"unknown mutation {which}")

path.write_text(json.dumps(doc, indent=2) + "\n", encoding="utf-8")
PY

# examples_valid <mutation-target> <mutation> -- true when the harness still accepts the tree
examples_valid() {
	python3 "$TMP/mutate.py" "$1" "$2"
	local rc=0
	harness -run TestExamplesValidateAgainstSchemas >/dev/null 2>&1 || rc=$?
	cp "$TMP/rule-set.orig" "$RULE_SET_EXAMPLE"
	cp "$TMP/strategy.orig" "$STRATEGY_EXAMPLE"
	return $rc
}

check_fails "rule-set: FORBID_CHANNEL is rejected in a RULES set" \
	examples_valid "$RULE_SET_EXAMPLE" forbid-channel-in-rules
check_fails "rule-set: SELECT_TREATMENT is rejected in a POLICY set (narrowing only)" \
	examples_valid "$RULE_SET_EXAMPLE" select-in-policy
check_fails "rule-set: a 6th level of condition nesting is rejected" \
	examples_valid "$RULE_SET_EXAMPLE" nesting-6
check "rule-set: 5 levels of condition nesting are accepted (the cap is exactly 5)" \
	examples_valid "$RULE_SET_EXAMPLE" nesting-5
check_fails "rule-set: BETWEEN with three values is rejected" \
	examples_valid "$RULE_SET_EXAMPLE" between-three
check_fails "rule-set: IN with an empty set is rejected" \
	examples_valid "$RULE_SET_EXAMPLE" in-empty
check_fails "rule-set: IS_NULL carrying a value is rejected" \
	examples_valid "$RULE_SET_EXAMPLE" is-null-with-value
check_fails "rule-set: a group with both all and any is rejected" \
	examples_valid "$RULE_SET_EXAMPLE" all-and-any
check_fails "rule-set: SUPPRESS carrying a treatmentCode is rejected" \
	examples_valid "$RULE_SET_EXAMPLE" suppress-with-treatment
check_fails "rule-set: a malformed field path is rejected" \
	examples_valid "$RULE_SET_EXAMPLE" unknown-field-shape
check_fails "strategy: onError FAIL with a defaultScore is rejected" \
	examples_valid "$STRATEGY_EXAMPLE" fail-with-default-score
check_fails "strategy: onError DEFAULT without a defaultScore is rejected" \
	examples_valid "$STRATEGY_EXAMPLE" default-without-score
check_fails "strategy: treatment type SMS on the LETTER channel is rejected" \
	examples_valid "$STRATEGY_EXAMPLE" type-channel-mismatch
check_fails "strategy: schemaVersion 2 is rejected by a v1 schema" \
	examples_valid "$STRATEGY_EXAMPLE" schema-version-2
check_fails "strategy: percentBps above 10000 is rejected" \
	examples_valid "$STRATEGY_EXAMPLE" arm-bps-out-of-range

check "examples restored byte-for-byte after the mutation probes" \
	cmp -s "$TMP/rule-set.orig" "$RULE_SET_EXAMPLE"
check "strategy example restored byte-for-byte after the mutation probes" \
	cmp -s "$TMP/strategy.orig" "$STRATEGY_EXAMPLE"

cat >"$TMP/write-probe.py" <<'PY'
"""Write a syntactically valid decisioning schema whose $id names the wrong path."""
import json, sys
json.dump({
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "$id": "https://contracts.collections.internal/schemas/decisioning/WRONG-PATH.v1.json",
    "title": "negative probe",
    "type": "object",
    "additionalProperties": False,
    "required": [],
    "properties": {},
}, open(sys.argv[1], "w"), indent=2)
PY

# probe_wrong_id <command...> -- run a checker over a tree containing a bad-$id schema
probe_wrong_id() {
	python3 "$TMP/write-probe.py" "$PROBE_SCHEMA"
	local rc=0
	"$@" >/dev/null 2>&1 || rc=$?
	rm -f "$PROBE_SCHEMA"
	return $rc
}
check_fails "the harness rejects a schema whose \$id does not match its path" \
	probe_wrong_id go -C contracts test -count=1 -run TestSchemasCompile ./...
check_fails "this script's own \$id checker rejects it too" \
	probe_wrong_id python3 "$TMP/idcheck.py" "$SCHEMA_DIR"

probe_orphan_example() {
	printf '{"nothing": true}\n' >"$PROBE_EXAMPLE"
	local rc=0
	harness -run TestExamplesValidateAgainstSchemas >/dev/null 2>&1 || rc=$?
	rm -f "$PROBE_EXAMPLE"
	return $rc
}
check_fails "the harness rejects an example with no mirrored schema (orphan guard)" probe_orphan_example

check "no negative-probe file survived" bash -c '! test -e "'"$PROBE_SCHEMA"'" && ! test -e "'"$PROBE_EXAMPLE"'"'

printf '\nDEC-1A: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
