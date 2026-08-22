# ADR-0015: Observability — OpenTelemetry + kube-prometheus-stack + Loki + Tempo + Alloy

- **Status:** Accepted
- **Date:** 2026-08-22
- **Related:** [ADR-0004](./0004-kafka-eventing-envelope-outbox.md), [ADR-0007](./0007-airflow-self-hosted-batch-only.md), [ADR-0008](./0008-snowflake-dbt-analytics.md), [ADR-0010](./0010-terraform-stacks-ci-only-applies.md), [ADR-0016](./0016-data-conventions-money-ids-time.md)

## Context

Every request, event and file must carry a correlation id and a trace id (D§50), the operational dashboard has a defined row set (D§51), and — the requirement that actually shapes the design — **correlation must survive the whole chain**: SFTP file → ingestion job → Snowflake load → dbt run → decision batch → case (A§96, A§97). An incident starts as `correlation_id = COR_…` and must end at a case.

Secondary constraint: the environment is destroyed and rebuilt regularly ([ADR-0010](./0010-terraform-stacks-ci-only-applies.md)), so anything that is not declarative is a liability.

## Decision

**OpenTelemetry as the instrumentation contract, a self-hosted Grafana stack in-cluster.**

- **Instrumentation:** `platform/otelkit` — `Init(ctx, ServiceInfo)`, `Logger(ctx)` emitting slog lines with `trace_id` and `correlation_id`, HTTP middleware, and `KafkaHeaders(ctx)` / `ContextFromHeaders` carrying `traceparent` plus correlation/causation ids (A§24, A§97).
- **In-cluster:** kube-prometheus-stack (Prometheus 7 d / 10 GB, Grafana with a dashboard sidecar, Alertmanager → **SNS email**), **Loki** and **Tempo** single-binary with `colx-dev-ops` S3 backends, **Grafana Alloy** daemonset for logs, an **OTel Collector** for traces (OTLP → Tempo), MSK open monitoring scraped, base alert rules (node NotReady, PV > 80%, CrashLoop, deadman).
- **Correlation chain, asserted not assumed:** `COR_` minted at flow start → `file_registry.correlation_id` → Kafka envelope field *and* message header → Airflow `dag_run.conf` → Snowflake `QUERY_TAG` (visible in `QUERY_HISTORY`/`COPY_HISTORY`) → `decision_audit.correlation_id` → `case_activities.correlation_id`. E2E-1 asserts one id across ingestion → delinquency → case; the MVP gate samples the full file → case chain.
- **Every alert rule carries a `runbook_url` annotation** that must resolve to a runbook containing the D§82 control set; a CI check enforces both.
- Dashboards and alerts are files in `deployment/observability/`, so the whole stack rebuilds with the cluster.

## Alternatives considered

- **Amazon Managed Prometheus + Managed Grafana.** No components to run. Rejected: per-sample ingestion charges plus per-seat Grafana pricing for a single developer, and the dashboards/alerts would live outside the declarative helmfile — teardown and rebuild stop being free, which is the property this environment is built around.
- **DataDog / New Relic / Grafana Cloud.** Best ergonomics by a distance. Rejected: per-host/per-GB pricing that would dwarf the rest of the dev bill, plus an agent and a long-lived API key in a cluster that is destroyed weekly.
- **CloudWatch only.** Already present, cheap for logs. Rejected: no trace view worth using, no PromQL, painful dashboards-as-code — the A§97 chain would degrade into grep.
- **Jaeger instead of Tempo.** A second storage system to run when Tempo's single binary already reads and writes S3.
- **Thanos / Mimir for long-term metrics.** Unnecessary: 7 days of metrics is enough for a dev environment that is rebuilt more often than that.
- **Logging without tracing (structured logs + correlation id only).** Cheaper and would satisfy D§50's letter. Rejected: the cross-plane chain is what the design calls essential, and a trace is how a decision latency budget is actually diagnosed.

## Consequences

**Positive**

- Everything is a Helm release plus files in git: `destroy-heavy` then `up-all` costs time, not dashboards.
- One Grafana and one query surface for metrics, logs and traces; MSK, Airflow, dbt and Snowflake credits all land on the same operational dashboard (D§51).
- Alerts that page have runbooks by CI rule, and each alert is fired once deliberately via the fault-injection matrix and evidenced — an alert nobody has ever seen fire is not monitoring.

**Negative / caveats**

- **Single-binary Loki and Tempo, 7 d/10 GB Prometheus, no HA** — the observability stack is the least reliable thing in the cluster, competes with services for node capacity, and losing it loses history. It is monitoring, not an SRE platform.
- Alertmanager → SNS email is a notification channel, not on-call: no deduplication across rebuilds, no escalation, no acknowledgement.
- OTel instrumentation is only as good as the `ctx` plumbing: one missed context and the chain breaks *silently*, which is why the round-trip is unit-tested and the chain is a gate assertion rather than a convention.
- S3-backed Loki and Tempo queries are slow enough to be annoying during an incident, and retention/compaction settings are dev-grade.
- MSK metrics come from open monitoring only, so broker-internal detail is limited; Snowflake credit metrics arrive via a pushgateway task and are therefore as fresh as its schedule.
- Dashboards drift from reality unless a phase gate asserts panels return data — which is why "≥10 non-empty panels after a sim day" is a gate line, not a nice idea.
