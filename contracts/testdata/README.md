# testdata

Cross-service test vectors: a shared, committed answer sheet so two
implementations of the same rule cannot quietly disagree. Released files are
immutable — changes ship as new vN files.

| File | Contents | Consumed by |
|---|---|---|
| `allocation-golden-vectors.json` | 74 champion/challenger allocation vectors | `platform/allocation` (LIB-9), DEC-11 |
| `allocation-golden-vectors.gen.py` | The generator that produced them (provenance) | `scripts/verify/DEC-1A.sh` |

## `allocation-golden-vectors.json`

```json
{ "vectorSetVersion": 1, "algorithm": "…", "generator": "…", "vectorCount": 74,
  "vectors": [ { "accountId": "A123", "salt": "EARLY_COLLECTION_V17",
                 "arms": [ { "name": "CHAMPION", "percentBps": 9000 },
                           { "name": "CHALLENGER", "percentBps": 1000 } ],
                 "expectedBucket": 2972, "expectedArm": "CHAMPION" } ] }
```

`Allocate(accountId, salt, arms)` is correct if and only if it reproduces every
row. The `algorithm` field states the rule in full; in short: bucket = first 8
bytes of `SHA-256(accountId + ":" + salt)` as a big-endian `uint64`, mod 10000;
the arm is the first whose cumulative `percentBps` is **strictly greater** than
the bucket. Allocation is a pure function — no counters, no stored assignment —
so an account keeps its arm across online, batch and replay.

Coverage is deliberate, not incidental: 90/10, 50/50, 100/0, a zero-width first
arm, three-arm even and uneven splits, a single-arm experiment, the same account
under four salts, and the **boundary buckets** (8999 vs 9000 on 90/10, 4999 vs
5000 on 50/50, 3399/3400 and 6699/6700 on 34/33/33) — the off-by-one a `>=`
would get wrong and that would otherwise surface only as a 90.01/9.99 skew
nobody notices.

```bash
python3 contracts/testdata/allocation-golden-vectors.gen.py           # rewrite in place
python3 contracts/testdata/allocation-golden-vectors.gen.py --check   # exit 1 if stale
bash scripts/verify/DEC-1A.sh                                         # + independent verifier
```

The generator is stdlib-only and deterministic (the boundary accounts come from
a documented ascending scan, never from sampling), and `DEC-1A.sh` re-runs it
and diffs the result. That is what stops a failing vector from being "fixed" to
match a broken implementation: the expectations can only change by changing the
generator, in a reviewable diff.
