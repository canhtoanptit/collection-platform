# ADR-0005: Ingestion — a dedicated platform: control plane, file registry, explicit reconciliation

- **Status:** Accepted
- **Date:** 2026-08-22
- **Related:** [ADR-0006](./0006-cdc-debezium-on-eks.md), [ADR-0007](./0007-airflow-self-hosted-batch-only.md), [ADR-0008](./0008-snowflake-dbt-analytics.md), [ADR-0012](./0012-source-system-simulator.md), [ADR-0004](./0004-kafka-eventing-envelope-outbox.md)

## Context

Collections data arrives four ways and always will: CDC from source databases, SFTP/CSV from legacy banking systems and partners, APIs/webhooks, and event streams (D§14–22, A§28–37). SFTP is not going away because APIs are nicer — legacy systems and external parties will require file exchange for years (D§56). For financial data, **reconciliation must be explicit rather than inferred from pipeline success** (D§38), and invalid data must never silently enter production (D§22).

The alternative to a platform is a pipeline per feed, which is how banks lose files quietly.

## Decision

Build ingestion as a **platform with a control plane** (A§28.1, D§105), not a set of pipelines.

- **Control plane** owns the source registry, feed registry, connector configuration, credential *references*, schedules and SLAs, quality rules and reconciliation rules; it exposes the D§79 + A§19 APIs. Workers hold **no direct database access** — all state goes through the control-plane API, which keeps that API honest.
- **File registry** with the exact A§36 state set — `DISCOVERED RECEIVED VALIDATING VALIDATED PROCESSING PROCESSED RECONCILING RECONCILED FAILED QUARANTINED ARCHIVED DUPLICATE` — a table-driven transition map (illegal transitions error), and an **append-only `file_state_transition` audit** of every transition with actor and reason (D§3.6). `UNIQUE(source_id, checksum_sha256)` and `UNIQUE(feed_id, file_name)` implement A§33 dedup: a checksum repeat is `DUPLICATE` (terminal, audited), a name reuse with different content is `QUARANTINED FILENAME_REUSED`.
- **Validation in D§21 order** — header → schema → rows → business rules → trailer → control total — driven by the feed contract YAML (`contracts/files/*.v1.yaml`). Any ERROR row **quarantines the whole file** with the original plus `rejects.jsonl`; WARNs are recorded and processing continues.
- **Checkpoints per source type** (A§35) and **explicit reconciliation runs/checks** (D§37–38, A§37): COUNT identity `declared == rejected + loaded`, AMOUNT identity `control_total_declared == control_total_computed` at zero tolerance, plus cross-source checks. `PROCESSED → RECONCILING → RECONCILED → ARCHIVED` only on PASS.
- **SFTP transport is containerized `atmoz/sftp` on EKS** (ClusterIP; host key and user keys from Secrets Manager via ESO), so the A§31 connector flow is genuinely exercised.

## Alternatives considered

- **AWS Transfer Family.** Managed, hardened, no host keys to own. Rejected twice over: $216/mo, **and** it writes straight to S3, bypassing the A§31 connect → verify-host-key → authenticate → discover → download → checksum → register → validate flow that is the capability being built. A managed transport would leave our connector untested.
- **Ad-hoc pipelines per feed** (a Lambda or a DAG per file). Cheapest start, and the shape D§105 exists to reject: no registry, no dedup, no state history, no reconciliation identity, no place to answer "did yesterday's file arrive and did it balance?".
- **Land in S3 and let the warehouse validate** (Snowpipe + dbt tests). Loses the pre-load quarantine gate — bad data is already in RAW, and the control total has no place to fail.
- **Row-level rejection instead of whole-file quarantine.** Tempting for availability. Rejected: a control total is meaningless against a partial load, so a partially-loaded file cannot be reconciled — and reconciliation is the point.
- **Buy an ETL/MFT product** (Informatica, Boomi, GoAnywhere). Rejected per [ADR-0001](./0001-build-vendor-neutral-platform.md): the ingestion control plane is on the build side of D§63.

## Consequences

**Positive**

- Every file has an id, a state history, a checksum, a control-total verdict and a correlation id — the operational questions have answers that are rows, not log greps.
- One control plane serves CDC, SFTP, API and event sources, so a new feed is configuration plus a contract, not a new pipeline.
- The SFTP failure modes that matter (host-key change, late file, duplicate, corrupt trailer) are all reachable on demand via the simulator's fault matrix ([ADR-0012](./0012-source-system-simulator.md)).

**Negative / caveats**

- **Whole-file quarantine stalls a day's load for that feed** until someone reprocesses a corrected file. That is the intended trade, and it means quarantine alerting and the reprocess path are load-bearing, not nice-to-have.
- The control plane is a synchronous dependency of every worker: its availability is now an ingestion availability problem, and its API is on the critical path of every file.
- `atmoz/sftp` on EKS means we own the host key, a PVC and user provisioning; it is a dev-scale ClusterIP service, not a hardened internet-facing SFTP endpoint. Production would use a managed transfer service *behind the same connector interface*.
- The registry is a superset of D§20 with a dozen states; every state must stay reachable and audited (a Phase-5 gate criterion) or the model degrades into `PROCESSED`/`FAILED` with extra columns.
