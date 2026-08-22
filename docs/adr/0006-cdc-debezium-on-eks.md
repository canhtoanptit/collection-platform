# ADR-0006: CDC — Debezium on self-managed Kafka Connect (EKS) + Aiven S3 sink

- **Status:** Accepted
- **Date:** 2026-08-22
- **Related:** [ADR-0003](./0003-postgres-per-service-shared-rds.md), [ADR-0004](./0004-kafka-eventing-envelope-outbox.md), [ADR-0005](./0005-ingestion-platform-control-plane.md), [ADR-0008](./0008-snowflake-dbt-analytics.md), [ADR-0012](./0012-source-system-simulator.md)

## Context

The platform needs near-real-time replication from source databases for both integration and migration (D§17, D§57). A CDC connector must provide snapshot, log position, streaming, checkpoint, restart, schema-change detection, source lag and reconciliation hooks (A§30).

D§18 settles the build question in advance: build the ingestion control plane and abstraction, **but do not implement database transaction-log parsing from scratch**. Our differentiation is the source registry, governance, contracts, checkpointing, reconciliation, canonical model and audit — not WAL parsing.

## Decision

**Debezium on a self-managed Kafka Connect (distributed mode, 1 replica) Deployment on EKS.**

- Debezium Postgres connector against `colx-dev-corebank`: slot `colx_debezium`, `table.include.list = cb_customer, cb_account, cb_debt, cb_payment, cb_delq`, `snapshot.mode=initial`, **`decimal.handling.mode=string`** (money stays exact — [ADR-0016](./0016-data-conventions-money-ids-time.md)), `topic.prefix=cdc.corebank`, JSON without schemas, and **`heartbeat.interval.ms=10000`** as the WAL-growth guard.
- **Aiven S3 sink connector** (Apache-2.0) lands `cdc.corebank.*` and the raw webhook topic in `s3://colx-dev-raw/...` as partitioned `.jsonl.gz` (flush 5 min / 10k records) for the warehouse ([ADR-0008](./0008-snowflake-dbt-analytics.md)).
- A `cdc-checkpointer` CronJob (5 min) writes Connect offsets and `pg_replication_slots` lag/retained-WAL into control-plane checkpoints and Prometheus gauges (`colx_cdc_slot_retained_bytes`, `colx_cdc_lag_seconds`).
- **Raw Debezium topics are internal to `ingestion/`.** A canonicalizer bridges them to canonical snapshot events (`ingestion.{customers,accounts,debts,payments}.v1`) in the A§24 envelope; domain services consume only canonical topics.
- Schema drift is monitored against `contracts/cdc/corebank.v1.yaml`; additive changes flow through as VARIANT with a WARN and a runbook (D§47).

## Alternatives considered

- **MSK Connect.** Managed workers, no JVM to babysit. Rejected for iteration economics: ≈ $88/mo, 10–20 minute config cycles, and opaque logs. At this stage cycle time dominates, and connector configuration is exactly what needs many cycles. **Documented as the production alternative** — the same connector JARs and configuration move across.
- **Writing our own log readers.** Explicitly rejected by D§18 and D§57: Oracle/SQL-Server/Postgres log parsing is a very large maintenance burden and is not the bank's differentiation.
- **Confluent S3 sink.** Functionally equivalent; licensing pushed us to the Apache-2.0 Aiven sink.
- **Polling queries with a watermark column.** No deletes, no before-images, no true log position, and it puts load on the source. Fails A§30's capability list.
- **AWS DMS.** The service D§18's title alludes to. Rejected here because we need the *control plane* semantics ourselves and DMS's task model hides the offsets, schema handling and reconciliation hooks we must own — and it is another managed cost we cannot stop.

## Consequences

**Positive**

- Real Connect logs land in Loki, and a connector change is minutes rather than tens of minutes — the difference between debugging and guessing.
- Standard Debezium semantics (initial snapshot then stream, per-table topics, op codes, LSN ordering) plus a portable exit: the same configuration runs on MSK Connect.
- `decimal.handling.mode=string` keeps `numeric(18,2)` exact all the way to the canonicalizer's minor-unit conversion — no float ever touches money.

**Negative / caveats**

- We operate Kafka Connect: JVM heap, connector restarts, offsets/configs/status topic hygiene, and no managed fallback if it wedges.
- **Single replica means CDC has no HA.** A pod eviction is a gap that must close by resuming from the slot — verified explicitly (kill Connect mid-tick → no gap after restart), but a genuine availability limitation.
- The replication slot is the platform's most dangerous object: while Connect is down or idle, retained WAL grows until it fills db.t4g.micro. Heartbeat plus a 500 MB retained-WAL alert plus a drop/recreate runbook is the mitigation; this is plan risk 3 and the most likely real incident.
- Schema drift is **warned about, not blocked** — a dropped or retyped source column will reach staging as a null/VARIANT surprise before anyone reads the WARN.
- Snapshot mode floods the canonical topics on first run (correct — consumers upsert idempotently) but it makes lag metrics meaningless for the first hour and burns partition throughput.
