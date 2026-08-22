# Review policy

Two review mechanisms exist. **Code review is mandatory for every WP.** **Adversarial verification is
mandatory for the WPs listed in §3** and optional (encouraged) elsewhere.

Neither review is performed by the agent that implemented the WP.

---

## 1. Code review — every WP

A review agent reads the brief, then the diff, and works the checklist below. The review verdict is
`APPROVED` or `CHANGES REQUESTED` with numbered findings, each pointing at a file and line and saying
what to do. "Looks good" is not a review.

### Checklist

1. **Acceptance met.** Every acceptance command in the brief was run and passed — including the
   expected-fail cases. Re-run them; do not take the implementer's word. Any skipped or `-short` test
   is a finding.
2. **Contract adherence.** Handlers implement the OpenAPI operations as generated (strict-server), no
   hand-edited generated code (`make generate && git diff --exit-code`), no undocumented fields, no
   released contract file modified. Event payloads match the schema in `contracts/schemas/**`; envelope
   built via `platform/events` only.
3. **Idempotency.** Every POST command path takes `Idempotency-Key` and replays correctly (same key +
   hash → stored response, different hash → 422, in-flight → 409). Every consumer calls
   `inbox.Dedupe(consumer, eventId)` in the same transaction as its side effects. Every producer
   publishes only via `outbox.Enqueue` in the state-change transaction. Ask: what happens if this
   message arrives twice, out of order, or after a crash between DB commit and broker ack?
4. **Error contract A§20.** Errors go through `platform/apierror`; `{code, message, correlationId,
   details[]}` exactly; stable SCREAMING_SNAKE codes; no internal detail or stack trace leaked; correct
   status mapping; `details[].field` names match the contract's field names.
5. **Tests assert behaviour, not implementation.** Table-driven; names describe the rule; no assertions
   on private call sequences or mock invocation counts standing in for behaviour. State machines have an
   exhaustive `state × command` table. Coverage floors met (domain ≥90%, module ≥80%) — and the covered
   lines are the interesting ones, not just constructors.

### Also check, briefly

- Layering (domain imports no adapters/pgx/kgo), `context` plumbed, errors wrapped with `%w`, no panic
  in request paths, `exhaustive` switches.
- Money is `int64` minor units + currency (major-unit decimal strings only inside decision context
  documents); no float arithmetic on money; split remainders assigned by a documented rule.
- UTC everywhere; time from `platform/clock`; `tick --as-of=` for scheduled work.
- Audit tables append-only; migrations appended, never rewritten.
- Ownership: `make ownership-check WP=<id>` clean; diff matches the brief's deliverable paths.
- Runbook updated and `bash tools/lint-runbook.sh <file>` green if operational behaviour changed.
- No secrets, no long-lived credentials, no new dependencies outside the brief.

---

## 2. Adversarial verification

A second agent tries to break the implementation. The procedure is what makes it worth anything:

1. **Inputs: the brief and the contracts only.** The adversarial agent reads the WP brief, the design
   sections it cites, and `contracts/**`. It **must not read the implementation** — not the source, not
   the existing tests, not the PR diff. If it has already read them, use a different agent.
2. **It writes independent tests** from the specification: golden vectors, property tests, fuzz
   harnesses, integration scenarios. Tests land in the WP's own test paths (or the agreed golden-vector
   file) so they run in CI forever after.
3. **It attempts to violate the invariants**, deliberately and specifically: duplicate and out-of-order
   delivery, crash between commit and ack, concurrent writers on the same aggregate, boundary and
   off-by-one dates, timezone edges, zero/negative/huge amounts, rounding remainders, missing context
   fields, unknown enum values, expired and wrong-issuer tokens, replay of a whole topic slice, policy
   that forbids what optimization prefers.
4. **A failing adversarial test is a defect, not a bad test** — until the implementer proves the test
   contradicts the brief. If the brief is ambiguous, the brief is fixed first, then the code.
5. **Output**: the new tests (committed), plus a short report listing attempted violations, what held,
   what broke. Committed under `docs/gates/evidence/<phase>/adversarial-<WP-ID>.md`.
6. The phase gate does not pass while an adversarial finding is open.

Use the strongest available model for adversarial verification (see `docs/conventions.md`).

---

## 3. Mandatory adversarial verification

These WPs handle money, delivery guarantees, immutability, or safety constraints. Each one **must** have
an adversarial pass before its phase gate:

| WP | Area | What the adversary attacks |
|---|---|---|
| **LIB-7** | Outbox relay | Rolled-back tx publishing; duplicate publication; per-key order loss; leader killed mid-batch; invalid payload reaching the broker. |
| **SVC-5** | Arrangement schedules | Σ installments ≠ total; rounding remainder; non-ascending or past-dated first installment; grace-period edges; breach tick across day/DST boundaries; promise/arrangement state machine gaps. |
| **SVC-6** | Payment allocation | Allocation sums ≠ payment amount; minor-unit rounding drift; over-allocation past installment/debt balances; duplicate natural key (`source_system, external_payment_ref`) across webhook and file/CDC arrival; reversal handling. |
| **ING-8** | Reconciliation engine | Green run on tampered counts; `declared == rejected + loaded` identity holes; control-total tolerance abuse; recon inferring success from pipeline success; premature ARCHIVED. |
| **DEC-5** / **LIB-9** | Rule DSL (`platform/ruledsl`) | ≥10 hostile golden vectors: deep nesting, priority/ruleId tie-breaks, `BETWEEN` inclusivity, unit and type edges, missing fields (must be leaf-false + `FIELD_MISSING`, never an error), operator matrix; fuzz for panics/non-determinism. |
| **DEC-9** | Decision-audit immutability | Any UPDATE/DELETE path reaching `decision_audit` (SQL, app role, trigger bypass, partition swap); snapshot hash mismatch; explanation diverging from the stored trace. |
| **DEC-10** | Batch control totals | `populationRows == decided + suppressed + ineligible + errored` broken by malformed lines, retries, partial failures, re-POSTed run ids; duplicate audit rows on replay. |
| **DEC-11** | Champion/challenger allocation | Arm instability for the same account; bucket boundaries (0, 9999, cumulative bps); arms not summing to 10000; unapproved challenger executing. |
| **DEC-16** | Treatment guardrails | Dispatch outside a contact window or above a frequency cap under any clock (property test); policy re-check failing open instead of closed; doNotContact flipped between decision and execution; scheduled_for poller re-dispatching. |
| **UI-4** | PTP command | Double-submit producing two promises; missing/reused `Idempotency-Key`; client validation diverging from the contract; `details[]` not surfaced; success not invalidating the timeline. |

---

## 4. Sequence per WP

```text
implement -> self-check (definition of done, CLAUDE.md §8)
          -> code review agent (§1)                      -> findings closed
          -> adversarial agent (§2) if listed in §3       -> findings closed
          -> phase gate (docs/gates/README.md)
```

A WP is not "done" because its author says so. It is done when the review agent's re-run of the
acceptance commands is green and no finding is open.
