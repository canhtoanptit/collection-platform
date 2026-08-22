#!/usr/bin/env python3
"""Regenerate contracts/testdata/allocation-golden-vectors.json.

The vectors are the normative specification of champion/challenger allocation
(plan §7 LIB-9 / DEC-11, D§41): `platform/allocation.Allocate(accountId, salt,
arms)` is correct if and only if it reproduces every row. This generator is
committed as provenance — it says exactly how the expected values were produced,
so a reviewer never has to trust a table of magic numbers — and it is
deterministic: re-running it must rewrite the committed file byte for byte
(`scripts/verify/DEC-1A.sh` asserts that, which is what stops a "fixed" vector
from being quietly edited to match a broken implementation).

Python 3 standard library only (hashlib, struct, json, argparse, pathlib). No
randomness anywhere: the boundary-bucket accounts are found by a documented
ascending scan, not by sampling.

Usage:
    python3 contracts/testdata/allocation-golden-vectors.gen.py            # rewrite in place
    python3 contracts/testdata/allocation-golden-vectors.gen.py --out /tmp/x.json
    python3 contracts/testdata/allocation-golden-vectors.gen.py --check    # exit 1 if stale
"""

from __future__ import annotations

import argparse
import hashlib
import json
import pathlib
import struct
import sys

BUCKETS = 10000
"""Number of allocation buckets: basis points, so a 0.01% arm is expressible."""

ALGORITHM = (
    "bucket = the first 8 bytes of SHA-256(accountId + \":\" + salt) read as a "
    "big-endian uint64, modulo 10000; the arm is then found by walking arms[] in "
    "declaration order accumulating percentBps and taking the first arm whose "
    "cumulative total is STRICTLY GREATER than the bucket. Arms therefore "
    "partition the buckets [0, 9999] into contiguous ranges of width percentBps: "
    "a 90/10 split puts buckets 0-8999 in the champion and 9000-9999 in the "
    "challenger, and a zero-width (0 bps) arm can never be selected. Sigma "
    "percentBps must equal 10000 - JSON Schema cannot sum, so strategy-service "
    "validates it (plan §7 DEC-2) - which guarantees exactly one arm matches "
    "every bucket. Allocation is a pure function of accountId and salt with no "
    "counters and no stored assignment, so the same account lands in the same "
    "arm on every decision, in batch exactly as online, and on a replay years "
    "later; changing the salt re-randomises every assignment, which is why a "
    "salt is fixed for the life of an experiment. accountId is the allocation "
    "key (never customerId): experiments are run on accounts, and an account "
    "must not switch arm because a customer relationship changed."
)

GENERATOR = "contracts/testdata/allocation-golden-vectors.gen.py"
OUT_NAME = "allocation-golden-vectors.json"

# --- arm sets -----------------------------------------------------------------
# Shape per the WP brief: [{name, percentBps}] in allocation order.

NINETY_TEN = [
    {"name": "CHAMPION", "percentBps": 9000},
    {"name": "CHALLENGER", "percentBps": 1000},
]
FIFTY_FIFTY = [
    {"name": "ARM_A", "percentBps": 5000},
    {"name": "ARM_B", "percentBps": 5000},
]
HUNDRED_ZERO = [
    {"name": "CHAMPION", "percentBps": 10000},
    {"name": "CHALLENGER", "percentBps": 0},
]
ZERO_HUNDRED = [
    {"name": "CHAMPION", "percentBps": 0},
    {"name": "CHALLENGER", "percentBps": 10000},
]
THREE_EVEN = [
    {"name": "ARM_A", "percentBps": 3400},
    {"name": "ARM_B", "percentBps": 3300},
    {"name": "ARM_C", "percentBps": 3300},
]
THREE_UNEVEN = [
    {"name": "CHAMPION", "percentBps": 5000},
    {"name": "CHALLENGER_A", "percentBps": 3000},
    {"name": "CHALLENGER_B", "percentBps": 2000},
]
SINGLE_ARM = [
    {"name": "CHAMPION", "percentBps": 10000},
]

# --- salts --------------------------------------------------------------------
# Real-shaped salts matching strategy-document.v1.json's `experiment.salt`
# pattern; EARLY_COLLECTION_V17 is the salt in the strategy-document example, so
# a vector here is directly comparable with that document's allocations.

SALT_EARLY = "EARLY_COLLECTION_V17"
SALT_LATE = "LATE_STAGE_2026Q3"
SALT_AB_A = "AB_TEST_SALT_A"
SALT_AB_B = "AB_TEST_SALT_B"
SALT_PILOT = "GUARDRAIL_PILOT_01"

# Account identifiers: the D§8 fixtures (A123/A456), platform-shaped ULIDs, a
# legacy numeric account number, and source keys with awkward shapes — the mix a
# migration population actually contains.
ACCOUNTS = [
    "A123",
    "A456",
    "A789",
    "01K3D0FZ8YQ2W7XN4M6VJ5RTHB",
    "01M0MEKD8027CSPJ9G6GTMH6MK",
    "000123456789",
    "ACCT-0001",
    "acct-0001",
    "9",
    "Z",
    "CARD_STD_00042",
    "LOAN_PERS_00007",
]

# Buckets whose boundary behaviour must be pinned, per arm set. The interesting
# ones are the last bucket of an arm and the first bucket of the next: 8999 vs
# 9000 for 90/10 is exactly the off-by-one that a `>=` instead of a `>` would
# get wrong, and it would only ever show up as a 90.01/9.99 skew nobody notices.
BOUNDARY_TARGETS = {
    SALT_EARLY: [0, 1, 4999, 5000, 8998, 8999, 9000, 9001, 9998, 9999],
    SALT_LATE: [3399, 3400, 6699, 6700],
}
HUNT_PREFIX = "HUNT"
HUNT_LIMIT = 400000


def bucket_of(account_id: str, salt: str) -> int:
    """The allocation bucket for one (accountId, salt) pair — see ALGORITHM."""
    digest = hashlib.sha256((account_id + ":" + salt).encode("utf-8")).digest()
    return struct.unpack(">Q", digest[:8])[0] % BUCKETS


def arm_for(bucket: int, arms: list[dict]) -> str:
    """The arm a bucket falls in: first cumulative percentBps strictly above it."""
    cumulative = 0
    for arm in arms:
        cumulative += arm["percentBps"]
        if cumulative > bucket:
            return arm["name"]
    raise ValueError(
        f"arms do not cover bucket {bucket}: sum(percentBps) = {cumulative}, want {BUCKETS}"
    )


def vector(account_id: str, salt: str, arms: list[dict]) -> dict:
    """One golden vector. Field order is the file's reading order."""
    bucket = bucket_of(account_id, salt)
    return {
        "accountId": account_id,
        "salt": salt,
        "arms": [dict(arm) for arm in arms],
        "expectedBucket": bucket,
        "expectedArm": arm_for(bucket, arms),
    }


def hunt(salt: str, targets: list[int]) -> dict[int, str]:
    """Find an accountId for each target bucket under `salt`.

    Deterministic by construction: candidates are HUNT0000000, HUNT0000001, …
    in ascending order and the FIRST match for a target wins, so the result
    depends only on the salt and the target list.
    """
    wanted = set(targets)
    found: dict[int, str] = {}
    for i in range(HUNT_LIMIT):
        if not wanted:
            break
        candidate = f"{HUNT_PREFIX}{i:07d}"
        bucket = bucket_of(candidate, salt)
        if bucket in wanted:
            found[bucket] = candidate
            wanted.discard(bucket)
    if wanted:
        raise SystemExit(
            f"no accountId found for buckets {sorted(wanted)} under salt {salt!r} "
            f"within {HUNT_LIMIT} candidates — raise HUNT_LIMIT"
        )
    return found


def build() -> dict:
    vectors: list[dict] = []

    # 1. 90/10 champion/challenger over the account mix — the D§41 default split.
    for account in ACCOUNTS:
        vectors.append(vector(account, SALT_EARLY, NINETY_TEN))

    # 2. 50/50 A/B.
    for account in ACCOUNTS[:8]:
        vectors.append(vector(account, SALT_AB_A, FIFTY_FIFTY))

    # 3. 100/0 — the champion always wins and the zero-width challenger is
    #    unreachable (the shape of a challenger defined but not yet ramped up).
    for account in ACCOUNTS[:6]:
        vectors.append(vector(account, SALT_EARLY, HUNDRED_ZERO))

    # 4. 0/100 — a zero-width FIRST arm must never be selected, which is the
    #    case a `>=` comparison gets wrong for bucket 0.
    for account in ACCOUNTS[:6]:
        vectors.append(vector(account, SALT_EARLY, ZERO_HUNDRED))

    # 5. Three-arm split, near-even (34/33/33).
    for account in ACCOUNTS[:8]:
        vectors.append(vector(account, SALT_LATE, THREE_EVEN))

    # 6. Three-arm split, uneven (50/30/20).
    for account in ACCOUNTS[:6]:
        vectors.append(vector(account, SALT_PILOT, THREE_UNEVEN))

    # 7. Single-arm experiment: every bucket resolves to the only arm.
    for account in ACCOUNTS[:4]:
        vectors.append(vector(account, SALT_PILOT, SINGLE_ARM))

    # 8. Boundary buckets — the arm edges, pinned exactly.
    for salt, targets in BOUNDARY_TARGETS.items():
        found = hunt(salt, targets)
        arms = NINETY_TEN if salt == SALT_EARLY else THREE_EVEN
        for target in targets:
            vectors.append(vector(found[target], salt, arms))
        if salt == SALT_EARLY:
            # The same 50/50 boundary accounts under a two-way even split, so
            # bucket 4999 vs 5000 is pinned as well.
            for target in (4999, 5000):
                vectors.append(vector(found[target], salt, FIFTY_FIFTY))

    # 9. Same account, different salt: the assignment must move, which is the
    #    mechanical reason a salt is never changed mid-experiment.
    for account in ("A123", "01K3D0FZ8YQ2W7XN4M6VJ5RTHB"):
        for salt in (SALT_EARLY, SALT_LATE, SALT_AB_A, SALT_AB_B):
            vectors.append(vector(account, salt, NINETY_TEN))

    return {
        "vectorSetVersion": 1,
        "algorithm": ALGORITHM,
        "generator": GENERATOR,
        "vectorCount": len(vectors),
        "vectors": vectors,
    }


def render(doc: dict) -> str:
    """Serialize exactly as the committed file is stored: 2-space indent, trailing newline."""
    return json.dumps(doc, indent=2, ensure_ascii=True) + "\n"


def main() -> int:
    default_out = pathlib.Path(__file__).resolve().parent / OUT_NAME
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", type=pathlib.Path, default=default_out,
                        help=f"output path (default: {OUT_NAME} next to this script)")
    parser.add_argument("--check", action="store_true",
                        help="do not write; exit non-zero if --out differs from the generated content")
    args = parser.parse_args()

    content = render(build())
    if args.check:
        current = args.out.read_text(encoding="utf-8") if args.out.exists() else ""
        if current != content:
            print(f"{args.out} is stale — re-run {GENERATOR}", file=sys.stderr)
            return 1
        print(f"{args.out} is up to date")
        return 0

    args.out.write_text(content, encoding="utf-8")
    print(f"wrote {args.out} ({json.loads(content)['vectorCount']} vectors)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
