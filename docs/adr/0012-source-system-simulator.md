# ADR-0012: Source system — a deterministic corebank simulator as the legacy-bank stand-in, hard-isolated

- **Status:** Accepted
- **Date:** 2026-08-22
- **Related:** [ADR-0005](./0005-ingestion-platform-control-plane.md), [ADR-0006](./0006-cdc-debezium-on-eks.md), [ADR-0008](./0008-snowflake-dbt-analytics.md), [ADR-0013](./0013-llm-agent-delegation-model.md)

## Context

There is no real legacy bank behind this build, and the platform's hardest capabilities are precisely the ones that only exist in relation to one: CDC from a core-banking database, SFTP/CSV file feeds with control totals, payment webhooks, reconciliation, and **analytical parity against legacy reports** (A§76, A§88). None of ingestion, reconciliation, parity or the end-to-end flows can be built — let alone verified — without a source system that behaves like one.

## Decision

Build `simulator/corebank` as a **real deployable subsystem**, then keep it strictly at arm's length.

- **Legacy-shaped schema** (deliberately quirky, per A§45's standardization targets): `varchar` business keys, `int YYYYMMDD` dates, `char` status codes — `cb_customer`, `cb_account`, `cb_debt`, `cb_payment`, `cb_delq`.
- **Deterministic seeder** — `--customers 30000 --accounts 50000 --seed 42`, ~8% initially delinquent across the D§5 buckets; re-seeding a fresh database yields an identical content fingerprint.
- **Daily drift tick** — `cmd/tick --business-date=…`, catch-up capable, config-driven roll-rate matrix, payment propensity by bucket, cure probability, new accounts and a data-quirk injection rate; deterministic per `(seed, date)` and idempotent on re-run.
- **File-drop generator** writing exactly `HEADER / DATA… / TRAILER` per the D§21 shape and the feed contract's control-total column, `.tmp` upload plus atomic rename, with a **fault-injection matrix**: `bad-control-total`, `bad-record-count`, `malformed-row`, `duplicate-file`, `late`.
- **Webhook simulator** trickling the same day's payments intraday with HMAC signatures and deterministic event ids derived from the payment key — so a replay is a *true* duplicate — plus a `--replay` mode.
- **Legacy report extractor** producing `daily_collections_summary` / `daily_payments_summary` straight from corebank SQL: the independent truth the parity harness joins against.
- **Hard isolation rule:** `simulator/` must never import `ingestion/` or `platform/` code. The file format is re-implemented from the YAML contracts.

## Alternatives considered

- **Static fixture files committed to the repo.** Cheapest, deterministic, no infrastructure. Rejected: it cannot exercise CDC at all (no WAL, no snapshot, no schema drift), cannot produce a late/duplicate/corrupt file on demand, and cannot generate an independent parity truth — the reconciliation and parity gates would be theatre.
- **Sharing the `feedspec` validation library between simulator and ingestion.** The obvious DRY win, and rejected as a correctness fraud: if the writer and the validator share code, a green reconciliation proves only that the code agrees with itself. This is plan risk 6 and a reviewer-enforced hard rule.
- **An anonymized extract of real bank data.** Not available, and it would drag PII and a compliance problem into a dev environment.
- **Generating data inside the ingestion tests.** Couples the source's lifecycle to a test run; the simulator has to run as scheduled workloads (tick 01:00, file drop 02:00, legacy report 02:30) to produce three consecutive green days.
- **A commercial data generator.** No CDC source, no control-total semantics, no legacy report — and another vendor for the least differentiated part of the work.

## Consequences

**Positive**

- Every ingestion state, fault class and reconciliation check can be provoked on demand, which is what makes the Phase-5 fault matrix and the MVP gate runnable rather than aspirational.
- Determinism makes gates repeatable: same seed and date produce the same data, so a failure is a bug and not weather.
- The parity harness gets a genuinely independent source of truth, and CDC gets a database with a real WAL, a real slot and real snapshot behaviour.

**Negative / caveats**

- The simulator is a whole subsystem the real bank would never build: its own maintenance cost, its own bugs, and engineering time not spent on the platform.
- **A simulator is not a real core banking system.** It will not reproduce the quirks that actually break migrations — encoding surprises, fixed-width mainframe oddities, timezone drift, partial-day extracts, mid-file schema changes, duplicated business keys. "Green in dev" therefore *understates* real-world data-quality risk, and A§100 risk 6 stays open.
- The isolation rule forbids the obvious fix for its own cost: file-format logic exists twice, and a contract change means two independent edits kept in step by review.
- Determinism cuts both ways — it hides bugs that only appear under unlucky orderings, volumes or interleavings, so the drift engine's quirk-injection rate is the only source of unpleasant surprises.
- Scale is a default (30k customers / 50k accounts), not a validated target; 10× is possible at more RDS cost and a longer CDC snapshot.
