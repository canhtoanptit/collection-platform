# ADR-0007: Orchestration — Airflow 2.11 self-hosted on EKS, for data/batch only

- **Status:** Accepted
- **Date:** 2026-08-22
- **Related:** [ADR-0005](./0005-ingestion-platform-control-plane.md), [ADR-0008](./0008-snowflake-dbt-analytics.md), [ADR-0009](./0009-decisioning-layered-pipeline.md), [ADR-0015](./0015-observability-otel-grafana-stack.md)

## Context

The platform needs dependency-ordered batch orchestration: file processing, loads, dbt runs, reconciliation, batch decisioning, backfills, SLA monitoring (D§23, A§38–42). It also needs a hard boundary. D§25 makes the separation **mandatory**: Airflow orchestrates `daily data → ingest → transform → score → reconcile`; the collections workflow (`case created → strategy → treatment → contact → promise → payment → resolve`) belongs to the domain services. A§100 risk 4 names the failure mode — Airflow silently becoming the business workflow engine.

## Decision

**Airflow 2.11, self-hosted on EKS** via the official chart, and scoped to data/batch orchestration only.

- KubernetesExecutor; image `colx/airflow` = `apache/airflow:2.11-python3.12` plus the cncf-kubernetes, snowflake, amazon and statsd providers; pgbouncer; **git-sync of `airflow/dags`** (60 s); metadata on the platform RDS ([ADR-0003](./0003-postgres-per-service-shared-rds.md)); remote logs to `s3://colx-dev-ops/airflow-logs`; statsd → Prometheus; all connections and variables via ExternalSecrets (`AIRFLOW_CONN_*`).
- **DAG rules (binding, A§42):** short idempotent tasks; a task calls an API, polls a status, triggers a load or asserts a reconciliation, and nothing else. No business state in XCom. No business rules in DAG code. No secrets in DAGs. Dataset/event-aware scheduling between ingestion → load → dbt. Every DAG passes `correlation_id` in `dag_run.conf` and forwards it to every API call, `COPY INTO` and dbt invocation ([ADR-0015](./0015-observability-otel-grafana-stack.md)).
- **Scope boundary:** batch decisioning is invoked by a DAG but *executed* by decision-service via its API ([ADR-0009](./0009-decisioning-layered-pipeline.md)); the DAG only invokes, polls, loads outcomes and asserts the control-total identity. Domain state never lives in Airflow.
- A DAG-integrity pytest in CI imports every DAG and treats deprecation warnings as errors.

## Alternatives considered

- **MWAA.** Managed, no scheduler to run. Rejected: $90–350/mo **and it cannot be stopped**, which puts a permanent floor under an environment that is idle most of the day; plus less control over the image and provider versions.
- **Dagster / Prefect.** Asset-centric models that fit the dbt half of the work more naturally, with better local ergonomics. Rejected: D§53 names Airflow, the operator ecosystem we need (KPO, Snowflake, AWS) is there, and for a fleet of agent sessions the value of a boring, exhaustively-documented tool outweighs ergonomics.
- **Argo Workflows or plain Kubernetes CronJobs.** Kubernetes-native and cheap. Rejected: no backfill semantics, no dataset-aware scheduling, no SLA/lateness model, no operational UI for a data workflow — we would rebuild Airflow badly. (Service-level scheduled work *does* use CronJobs — `server tick <task> --as-of=<date>` — because that is domain logic, not orchestration.)
- **Cloud-native workflow services (Step Functions).** Fine for integration flows; no data-workflow ergonomics, and state machines in JSON are a poor place for a daily analytics cycle.
- **Using Airflow as the domain workflow engine.** The one alternative that is forbidden rather than merely rejected (D§25, A§100 risk 4).

## Consequences

**Positive**

- One place to see the daily cycle, with retries, backfills, lateness and SLA visibility for free; Dataset triggers keep ingestion → load → dbt honest instead of time-coupled.
- Fully declarative (chart + git-synced DAGs + ExternalSecrets), so teardown and rebuild cost nothing but time.
- DAG-integrity tests catch import breakage in CI, before the scheduler discovers it at 02:05.

**Negative / caveats**

- We own a scheduler, a webserver, pgbouncer and an Airflow metadata database — the largest piece of dev-time operational surface after EKS itself, for work that is a handful of daily DAGs.
- KubernetesExecutor pays pod-startup latency per task. Correct for daily batch, wrong for anything interactive — which reinforces, rather than relieves, the "no business workflow" rule.
- **"No business logic in DAGs" is a convention, not a compiler.** Review has to enforce it, and the pressure to sneak one rule into a DAG (it is so much faster) is constant. A§100 risk 4 stays open for the life of the platform.
- Airflow's metadata DB shares the platform instance, so a runaway DAG competes with services for the same database.
- 2.11 is a pinned 2.x line: the 3.x migration is deferred work, and provider pins will age.
