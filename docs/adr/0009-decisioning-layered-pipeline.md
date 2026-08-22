# ADR-0009: Decisioning — layered pipeline, declarative versioned strategies, non-Turing-complete rule DSL, immutable audit

- **Status:** Accepted
- **Date:** 2026-08-22
- **Related:** [ADR-0001](./0001-build-vendor-neutral-platform.md), [ADR-0004](./0004-kafka-eventing-envelope-outbox.md), [ADR-0008](./0008-snowflake-dbt-analytics.md), [ADR-0013](./0013-llm-agent-delegation-model.md), [ADR-0016](./0016-data-conventions-money-ids-time.md)

## Context

Decisioning is the capability the platform exists for (D§7–8, D§39–43, A§52–60). It must be **explainable** (every decision returns strategy, rules, models, reason codes, constraints — A§58), **reproducible** (an audit row plus an input snapshot is enough to re-derive it — D§3.6, D§39), **governed** (approval lifecycle, no code deploy for a policy change), **simulatable before activation** (D§40, A§59) — and policy must never be overridden by optimization (A§54, D§43). A§100 risk 5 names the failure mode: decisioning becomes ungoverned code.

## Decision

1. **Fixed-order layered pipeline** (A§52, A§104): `Policy → Eligibility → Segmentation → Rules → Models → Optimization → TreatmentSelection`, then explanation and audit. Constraint types expose **narrowing only** (`Suppress`, `ForbidChannel`), so optimization structurally cannot widen what policy allowed; a **post-selection guard** re-validates the chosen treatment against the policy snapshot and yields `NO_ACTION` + `POLICY_VIOLATION_GUARD` on violation. Precedence is enforced by construction *and* at runtime, and asserted by a property test over ≥1000 cases.
2. **Strategies are declarative versioned documents** (`strategy-document.v1.json`, A§53 expanded): eligibility, ordered first-match segments, a `ruleSetRef`, `modelRefs` with timeout/on-error behaviour, treatments, and an optional experiment block. Selection is eligibility + `priority` (highest wins, ties by lexicographic id). A version is immutable after activation (A§11.3); rule sets, policy sets and guardrail configs are separately versioned and immutable once `PUBLISHED`. Policy sets are **global, never pinned per strategy** (A§54).
3. **The rule DSL is deliberately not Turing-complete.** Condition groups `{all|any}` nest ≤5; leaves are `{field, op, value}` over a fixed operator set; `field` must exist in the versioned **context field catalogue** (path + type + unit); `FORBID_CHANNEL` exists only in POLICY sets; first match wins by `(priority desc, ruleId asc)`; a missing field yields a false leaf plus a `FIELD_MISSING` trace entry, never an error. A rule set is *data* — loadable, diffable, fuzzable, golden-vector testable.
4. **Audit is immutable and append-only** (D§39, D§3.6): `decision_audit` is INSERT + SELECT only (trigger plus role grants; `UPDATE`/`DELETE` provably rejected), recording strategy/policy/rule/model versions, experiment arm and bucket, reason codes, decision, treatment, and an **S3 input snapshot** reference with a content hash. The trace document goes to S3; `GET /v1/decisions/{id}/explanation` renders it.
5. **Simulation gates activation** (A§60): `TEST → SIMULATED` requires a completed simulation for the current content hash; editing a definition invalidates it by hash mismatch. Approvals require distinct identities (author ≠ business approver ≠ risk approver), and activation atomically retires the prior ACTIVE version. Shadow runs compare against a baseline or `LEGACY`, categorizing every diff with the A§88 taxonomy.
6. **Champion/challenger by deterministic hash** (D§41): `arm = first 8 bytes of SHA-256("accountId:salt") mod 10000` over cumulative bps. Same account, same arm, forever, with no stored assignment; arm and bucket land in the audit row.

## Alternatives considered

- **An expression/scripting language for rules (CEL, Lua, Starlark, embedded JS).** Far more expressive, familiar to engineers. Rejected: it turns "explain this decision" into an interpreter-trace problem, defeats rule-parity testing against legacy logic (A§88), and lets an author write a rule nobody can review or simulate. Explainability and parity are worth more than expressiveness here.
- **A vendor rules/decisioning engine (Drools, FICO Blaze, Camunda DMN).** Mature authoring UIs and governance. Rejected per [ADR-0001](./0001-build-vendor-neutral-platform.md) — the decision contract, versioning and audit model are exactly what the bank must own — and because the audit trail would become the vendor's format.
- **Rules as Go code.** Fast, typed, trivially testable — and every policy change becomes a deployment, which D§91 names as the thing configurable strategies exist to avoid.
- **ML end-to-end treatment selection.** No explanation, no governance, and no way to prove policy compliance (A§100 risk 5). Models are inputs to the pipeline, behind a model contract (D§42), never the pipeline.
- **Random champion/challenger assignment persisted per account.** Needs a table, breaks reproducibility when a decision is re-derived, and makes simulation non-deterministic.
- **Optimization before policy** (i.e. "score everything, then filter"). Rejected structurally: A§54's worked example — policy forbids contact, optimization prefers SMS, result is *no SMS*.

## Consequences

**Positive**

- A decision can be replayed from its audit row plus its input snapshot, months later, with the exact versions it used.
- The DSL's small surface makes it fuzzable and golden-vector-driven, so an adversarial agent can attack it from the specification alone without reading the implementation.
- Policy precedence is a property test rather than a promise; strategy changes are configuration with an approval trail rather than deployments.

**Negative / caveats**

- A non-Turing-complete DSL will eventually fail to express something a business user wants. The answer is a new operator — a contract version plus an evaluator change plus new golden vectors — which is markedly slower than "just write code". That slowness is the feature, and it will be resented.
- The context field catalogue becomes a hard dependency for every rule author; adding a field is a versioned contract change (e.g. model scores arriving in Phase 13 require a new catalogue version, not a mutation).
- Simulation is only as trustworthy as the claim that batch population lines *are* the same context documents the online path builds. Plan risk 4; guarded by the context-parity test in the MVP gate, not by hope.
- An append-only audit grows without bound; range partitioning by `decided_at` is the only retention lever in v1, and archival policy is unresolved.
- "Optimization" in v1 is a constraint filter plus treatment-priority ranking. Expected-value optimization under capacity constraints arrives later (Phase 13) behind the same stage interface — until then the stage name overpromises.
