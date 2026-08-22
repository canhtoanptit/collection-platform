# registries

Closed vocabularies. Released files are immutable — changes ship as new vN files.

| File | Contents | Authored by |
|---|---|---|
| `reason-codes.v1.json` | Every reason code a decision, treatment or guardrail may emit | DEC-1 |

## `reason-codes.v1.json`

```json
{ "registryVersion": 1,
  "codes": [ { "code": "DPD_31_60", "category": "RULE", "description": "…" } ] }
```

`code` is `^[A-Z][A-Z0-9_]{2,63}$` and unique. `category` names the pipeline
stage that emits the code, which is what lets `GET /v1/decisions/{id}/explanation`
group an explanation the way A§58 presents one:

| Category | Emitted by |
|---|---|
| `POLICY` | the policy stage — customer-level constraints, highest precedence (A§54) |
| `ELIGIBILITY` | strategy selection and channel/treatment eligibility |
| `SEGMENT` | segmentation (first-match) |
| `RULE` | a matched rule in a `RULES` rule set |
| `MODEL` | model scores, bands and score-unavailable substitutions |
| `CONSTRAINT` | narrowing constraints and the post-selection policy guard |
| `EXECUTION` | pre-dispatch guardrails and fail-closed operational outcomes |

**The vocabulary is closed.** Every `reasonCode` in a rule set, a decision, an
event or an example must exist here:

- publish time — DEC-4 rejects a rule set naming an unknown code with a 400;
- decision time — reason codes are rendered from this registry, so an unknown
  code has no description to show;
- CI — `tools/contractcheck` (CON-7) and `scripts/verify/DEC-1A.sh` cross-check
  every code used in `contracts/**` against this file.

The one documented exception is `population-line.v1.json`'s
`legacyDecision.reasonCodes`: legacy codes are carried verbatim for parity
comparison (A§88), because dropping an unmapped code would hide exactly the
differences a parity run exists to find.

Adding a code is additive and cheap. Removing or re-defining one is breaking —
audit rows already carry it — so it ships as `reason-codes.v2.json`.
