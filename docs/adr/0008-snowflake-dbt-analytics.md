# ADR-0008: Analytics — Snowflake Enterprise + dbt, loaded by Airflow-triggered `COPY INTO`

- **Status:** Accepted
- **Date:** 2026-08-22
- **Related:** [ADR-0005](./0005-ingestion-platform-control-plane.md), [ADR-0006](./0006-cdc-debezium-on-eks.md), [ADR-0007](./0007-airflow-self-hosted-batch-only.md), [ADR-0015](./0015-observability-otel-grafana-stack.md), [ADR-0016](./0016-data-conventions-money-ids-time.md)

## Context

The platform needs an analytical layer for MI, migration parity, feature data and strategy measurement (D§30–36, A§43–51). Four properties are load-bearing: layered modelling RAW → STAGING → INTERMEDIATE → MARTS (A§43), **analytical parity against legacy reports** as a migration gate (A§76, A§88), PII protection that can be *proved* by a query (A§69), and loads that reconcile against the ingestion file registry ([ADR-0005](./0005-ingestion-platform-control-plane.md)).

## Decision

**Snowflake (Enterprise edition) as the analytical platform, dbt as the only transformation tool.**

- **Layers exactly A§43/§49:** `RAW_*` databases preserving source fidelity (VARIANT for CDC/webhooks/events; all-VARCHAR plus `_file_id,_row_number,_business_date` for file feeds), then `stg_<source>_<entity>` → `int_<concept>` → `fct_*`/`dim_*`, with SCD2 dimensions where history matters (A§48), enforced **dbt model contracts on marts**, and the A§50 test set (unique/not_null/relationships/accepted_values plus business singular tests). Transformation logic lives in dbt, never in Airflow (D§55).
- **Cost shape:** XS warehouses `WH_{INGEST,TRANSFORM,ANALYTICS}` with 60 s auto-suspend (idle ≈ $0), a resource monitor hard-capping **50 credits/mo** and suspending at 100%.
- **Security:** RBAC `COLX_LOADER / TRANSFORMER / REPORTER / PII_READER` (the last granted to nobody by default), key-pair auth for every service user, a storage integration to `colx-dev-raw`, and native `MASKING POLICY` objects (Terraform-owned) applied by dbt post-hooks (so masking survives a rebuild). REPORTER has no grants on RAW at all (D§45).
- **Loading:** **Airflow-triggered `COPY INTO` with `FORCE=FALSE`**, `ON_ERROR=ABORT_STATEMENT`, and a `QUERY_TAG` carrying the correlation id. `COPY_HISTORY.rows_loaded` is written back to the file registry and posted as a reconciliation check (`loaded == parsed`) that flips the file to `RECONCILED`.
- **Parity models** full-outer-join the legacy report against the new marts at identical grain, with `abs_diff <= 0.01` as a test.

## Alternatives considered

- **Snowpipe / auto-ingest.** Continuous, cheap to operate, less orchestration. Rejected: a load must be an explicit, correlated, reconciled event tied to a `file_id`. Snowpipe's asynchronous ingestion and implicit dedup window make the count identity awkward to assert and hide the load from the registry — the opposite of D§38.
- **Databricks/lakehouse, BigQuery, Redshift, open table formats (D§54).** Rejected in favour of the design's named choice: analytical scale, compute/storage separation, enterprise governance, dbt ecosystem.
- **PostgreSQL as the warehouse.** Cheapest by far and adequate at simulator volumes. Rejected: D§54 names Snowflake, and the specific controls this platform leans on — native dynamic masking, `COPY_HISTORY`, Time Travel, per-warehouse credit monitors — would all become hand-rolled.
- **Snowflake Standard edition.** Cheaper per credit. Rejected: no native dynamic masking, so PII protection becomes secure views plus a hand-maintained role matrix — much harder to verify mechanically than A§69's one-policy-per-column. Plan §9 keeps "drop to Standard + secure views if credits exceed ~40/mo" as an explicit escape hatch.
- **dbt Cloud.** Rejected: dbt Core in our own image, invoked by Airflow, keeps CI local and the artefacts (manifest, run_results) in S3 where we can use them for slim CI.
- **Stored procedures / Spark / Airflow Python transformations (D§55).** Rejected: no testing, lineage or documentation story comparable to dbt.

## Consequences

**Positive**

- Every load is explicit, correlated (`QUERY_TAG` → `COPY_HISTORY`) and reconciled against the registry; a missing file fails a check rather than quietly shrinking a mart.
- Masking is one assertion per column (`REPORTER` sees `***MASKED***`, `PII_READER` sees the value), which an agent can verify in a script.
- Parity turns migration correctness into a test suite, and slim CI keeps PR builds to the modified models.
- Idle cost ≈ $0, and the credit cap makes a runaway backfill fail a DAG rather than an invoice.

**Negative / caveats**

- **Enterprise costs more than Standard**, and the 30-day trial has to be spent deliberately — the Snowflake Terraform stack is written in Phase 2 but applied at Phase 6 kickoff so the trial covers the phase that needs it.
- Snowflake is the deepest single-vendor dependency in the stack (A§100 risk 3). Mitigations are real but partial: RAW stays in S3, models are dbt SQL, no business logic lives in warehouse procedures — a migration would still be a project, not a config change.
- `FORCE=FALSE` deduplicates on load history *per file name*: a re-loaded file that changed name will double-load. The file registry, not Snowflake, is the authority on "already loaded".
- A 50-credit hard cap is a deliberate availability-for-cost trade: hitting it suspends the warehouse and fails the day's DAG.
- Parity at `abs_diff <= 0.01` against a *simulated* legacy report proves the pipeline, not the real bank's data quirks ([ADR-0012](./0012-source-system-simulator.md)).
