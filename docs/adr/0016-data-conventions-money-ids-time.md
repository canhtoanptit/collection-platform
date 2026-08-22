# ADR-0016: Data conventions — int64 minor units, prefixed ULIDs, UTC + RFC3339

- **Status:** Accepted
- **Date:** 2026-08-22
- **Related:** [ADR-0002](./0002-go-hexagonal-monorepo.md), [ADR-0004](./0004-kafka-eventing-envelope-outbox.md), [ADR-0008](./0008-snowflake-dbt-analytics.md), [ADR-0009](./0009-decisioning-layered-pipeline.md)

## Context

Money, identifiers and time cross every boundary in this platform: HTTP APIs, Kafka payloads, Postgres, JSONL batch files, Snowflake, dbt models and a rule DSL that business users read. Reconciliation identities are exact-equality assertions on money (D§37–38), audit rows must be reproducible years later (D§3.6), and idempotent re-runs are keyed by business date. Getting these three conventions wrong is not a style problem — it is a class of silent financial bug. They are decided once, here, and hold everywhere.

## Decision

**Money — `int64` minor units plus an ISO-4217 currency, everywhere.**

- `{amountMinor, currency}` in APIs, events, service databases and in-memory domain types. **No floats, ever. No implicit currency.**
- Split and remainder arithmetic (payment allocation across installments, agency commission in bps) is integer arithmetic with the remainder assigned by an explicit documented rule (e.g. remainder to the last installment), covered by golden arithmetic tests and adversarial review ([ADR-0013](./0013-llm-agent-delegation-model.md)).
- The ingestion canonicalizer converts source `NUMERIC` strings (carried as strings out of CDC — [ADR-0006](./0006-cdc-debezium-on-eks.md)) into minor units at the boundary. Analytics uses `NUMBER(18,2)` ([ADR-0008](./0008-snowflake-dbt-analytics.md)).
- **One deliberate exception:** decision **context documents** carry decimal strings in *major* units, because a business rule must read `"500"`, not `50000` ([ADR-0009](./0009-decisioning-layered-pipeline.md)). The context builder performs the conversion, and the context field catalogue documents the unit for every field.

**Identifiers — ULIDs in `TEXT`, with prefixes on operational entities.**

- ULIDs (`oklog/ulid/v2`) stored in `TEXT` columns: lexically sortable, time-ordered, and matching the `01J…` examples in the design documents.
- Operational entities carry a self-describing prefix (D§20, A§97): **`FIL_`** file registry entry, **`JOB_`** ingestion job/run, **`REC_`** reconciliation run, **`COR_`** correlation id. Format `<PREFIX><ULID>`.
- Domain aggregate ids (`caseId`, `accountId`, `decisionId`, …) are unprefixed ULIDs. Source-system keys (`acct_no`, `cust_no`) keep their legacy shape and are **never** used as platform ids.

**Time — UTC everywhere, RFC3339 on the wire.**

- `timestamptz` storage, RFC3339 with `Z` in APIs and events, UTC cron and Airflow schedules, UTC in Snowflake and dbt. No naive timestamps, no local-time arithmetic.
- **`business_date`** is a UTC calendar date naming the business day the data *belongs to*, taken from the source (the named capture group in a file name, the file header, the tick's `--business-date`, the DAG's logical date) and preserved across late arrival and reprocessing. Reconciliation, S3 partitions, incremental dbt models and batch decisioning runs are all keyed by it, which is what makes re-runs idempotent instead of duplicating.
- Source `int YYYYMMDD` columns convert to real dates at the canonicalization/staging boundary, never carried inward as integers.
- Scheduled work is `server tick <task> --as-of=<date>` invoked by Kubernetes CronJobs; time-dependent logic reads `--as-of` and `platform/clock`. One code path, and no clock mocking in production.

## Alternatives considered

- **Float or JSON-number decimal money in APIs/events.** JSON numbers are IEEE-754 doubles in most parsers, so a one-cent rounding difference silently breaks a control-total identity that is asserted at zero tolerance. Rejected outright.
- **Decimal strings everywhere, including APIs and databases.** Exact and readable, but it pushes a decimal library into every consumer and every SQL comparison. The minor-unit integer is the cheapest exact representation and behaves identically in Go, Postgres and Snowflake. (The decision-context exception exists precisely because *readability* wins in that one place.)
- **Money as a database `NUMERIC` with a decimal wire format.** The same decision one layer down — it still requires a wire convention, and it invites float conversion in clients.
- **UUIDv4 identifiers.** No ordering, poor index locality, and they do not match the design documents' examples. Rejected.
- **Auto-increment business keys.** Leak volume, collide across environments, and cannot be minted before insert — which the outbox pattern requires. (`bigserial` remains fine for internal outbox rows.)
- **Prefixes on every id, including aggregates.** Rejected: prefixes earn their keep where an id appears in an alert or a log line, and cost readability on domain ids.
- **Local-time business dates / local-time SLAs.** Plan §9's default is UTC as the simplest verifiable option. A local-time change is possible later but must be explicit, not assumed.

## Consequences

**Positive**

- One conversion chain with exactly one documented boundary; reconciliation identities are exact integer arithmetic that either balances or does not.
- Ids sort by creation time, which makes keyset pagination, log reading and partition keys trivial, and a prefixed id in an alert says what it is without a lookup.
- UTC plus `--as-of` makes every scheduled job re-runnable for any date and every time-dependent test deterministic.

**Negative / caveats**

- The major-unit exception in context documents is a real seam: a bug there means a decision made on the wrong number. It is covered by byte-for-byte golden context fixtures and the online-vs-batch context parity test, and it will still be the first place to look when a rule misfires.
- `int64` minor units cannot express fractional-cent accrual (interest calculation at sub-cent precision). Out of scope for v1; it would need a scaled integer or a decimal type and a new convention.
- ULIDs in `TEXT` cost more bytes than a `bigint` or a native `uuid`, and are only monotonic per process — concurrent producers interleave within a millisecond.
- Two id shapes (prefixed and bare) mean two parse/validate paths, and a prefix pasted into the wrong field fails late.
- UTC business dates will not line up with a local-time regulatory reporting window if one is later required — and by then every consumer, partition and mart assumes UTC.
