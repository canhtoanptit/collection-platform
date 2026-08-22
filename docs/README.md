# Collections Platform — Design Docs

Design documentation for an **enterprise Collections & Debt Management Platform**: a bank-owned, vendor-neutral platform that ingests from legacy banking systems (CDC, SFTP/CSV with control totals, webhooks, events), maintains the collections domain (customer, account, debt, delinquency, case, arrangement, payment, contact, recovery, agency, legal) as Go services with their own transactional state, decides treatments through a governed and fully audited decisioning pipeline, executes them through guardrailed channels, and returns every outcome to Snowflake/dbt so the next strategy is measurably better — the closed loop *Sense → Understand → Decide → Act → Measure → Learn → Improve*. It runs on real AWS and real Snowflake, is built by a single developer directing parallel LLM agent sessions, and is designed so every claim about it is a command someone else can run.

## Status: Accepted — implementation in progress; Phase 0 (foundations & delegation machinery) complete, Phase 1 (contracts freeze) underway, 2026-08-22

The 16 ADRs below are accepted and the [technical design](./tech-design.md) is the current picture of the system. Execution proceeds phase by phase through the **[implementation plan](./implementation-plan.md)** (15 phases, 113 work packages); no phase starts on a red [gate](./gates/README.md).

## How to read these

Start with the **[Technical Design](./tech-design.md)** for the whole system — problem, architecture, domain model, APIs, events, ingestion, decisioning, security, milestones, verification, risks. Then read the **ADRs** for the reasoning behind each load-bearing choice (format: _Status · Context · Decision · Alternatives considered · Consequences_, with honest negatives). The **[implementation plan](./implementation-plan.md)** turns the design into dependency-ordered phases and self-contained work-package briefs.

If you are about to *write code here*, read **[CLAUDE.md](../CLAUDE.md)** and **[conventions.md](./conventions.md)** first — they win over everything else on conventions — then your WP brief.

The two root design documents are the normative deep source and are never edited: [`collections_debt_management_platform_design.md`](../collections_debt_management_platform_design.md) (cited **D§n**) and [`collections_debt_management_platform_design_artefacts_1-12.md`](../collections_debt_management_platform_design_artefacts_1-12.md) (cited **A§n**).

## Index

| Doc | What it covers |
|---|---|
| [tech-design.md](./tech-design.md) | Problem, goals, users, architecture, layout, domain model, APIs, events, ingestion & data, decisioning, security, milestones, verification, risks, prerequisites |
| [implementation-plan.md](./implementation-plan.md) | Global tech decisions, repo layout, delegation protocol, 15-phase map, 113 work packages, risks, open questions |
| [adr/0001](./adr/0001-build-vendor-neutral-platform.md) | Build a bank-owned, vendor-neutral platform (own domain + contracts, reuse infrastructure) |
| [adr/0002](./adr/0002-go-hexagonal-monorepo.md) | Go domain services, hexagonal layout (A§92), single monorepo |
| [adr/0003](./adr/0003-postgres-per-service-shared-rds.md) | PostgreSQL 16, database-per-service on 2× RDS incl. the CDC source |
| [adr/0004](./adr/0004-kafka-eventing-envelope-outbox.md) | Kafka-compatible eventing: MSK, A§24 envelope, in-repo schemas, outbox + inbox + DLQ |
| [adr/0005](./adr/0005-ingestion-platform-control-plane.md) | Dedicated ingestion platform: control plane, file registry, explicit reconciliation, containerized SFTP |
| [adr/0006](./adr/0006-cdc-debezium-on-eks.md) | CDC via Debezium on EKS Kafka Connect + Aiven S3 sink |
| [adr/0007](./adr/0007-airflow-self-hosted-batch-only.md) | Airflow 2.11 self-hosted on EKS — data/batch orchestration only |
| [adr/0008](./adr/0008-snowflake-dbt-analytics.md) | Snowflake Enterprise + dbt; Airflow-triggered `COPY INTO` |
| [adr/0009](./adr/0009-decisioning-layered-pipeline.md) | Decisioning: layered pipeline, versioned strategies, non-Turing-complete rule DSL, immutable audit |
| [adr/0010](./adr/0010-terraform-stacks-ci-only-applies.md) | Terraform ≥1.11, 5 stacks, S3-native locking, GitHub OIDC, CI-only applies, cost model |
| [adr/0011](./adr/0011-identity-cognito-irsa-no-ingress.md) | Cognito + IRSA; no public ingress until the UI phase |
| [adr/0012](./adr/0012-source-system-simulator.md) | Source-system simulator as the legacy-bank stand-in, hard-isolated |
| [adr/0013](./adr/0013-llm-agent-delegation-model.md) | Contracts-first, exemplar-first LLM-agent delegation model |
| [adr/0014](./adr/0014-collector-ui-react-vite.md) | Collector UI: React 18 + TS strict + Vite, generated clients, S3 + CloudFront |
| [adr/0015](./adr/0015-observability-otel-grafana-stack.md) | OpenTelemetry + kube-prometheus-stack + Loki + Tempo + Alloy |
| [adr/0016](./adr/0016-data-conventions-money-ids-time.md) | Money as int64 minor units, prefixed ULIDs, UTC + RFC3339 |

Contributor machinery (owned elsewhere, linked here): [conventions.md](./conventions.md) (ids, time, correlation, verify scripts, model assignment) · [review-policy.md](./review-policy.md) (code review + adversarial verification) · [wp-template.md](./wp-template.md) (work-package brief format) · [gates/](./gates/README.md) (phase-gate procedure and evidence) · [ownership.yaml](./ownership.yaml) (WP → path globs) · [CLAUDE.md](../CLAUDE.md) (code conventions, hard rules).

## Decision summary

| # | Decision | Chosen | Rejected alternatives |
|---|---|---|---|
| 1 | Platform strategy | Bank-owned vendor-neutral platform: own domain + contracts, reuse infrastructure behind interfaces | Buy EXUS; buy FICO; build everything from scratch; unbounded hybrid |
| 2 | Services & repo | Go, stdlib mux, oapi-codegen strict-server, hexagonal A§92, one monorepo | Java/Spring; .NET; Node.js; Go web frameworks; ORM; polyrepo |
| 3 | Operational storage | PostgreSQL 16, database-per-service on 2× RDS (platform + corebank CDC source) | Aurora Serverless v2 (ACU floor + permanent slot); distributed SQL; shared database; one instance; NoSQL |
| 4 | Eventing | MSK Provisioned, A§24 envelope, in-repo JSON Schema, outbox + inbox + Idempotency-Key, per-consumer DLQ | MSK Serverless (~$550/mo); EventBridge/SQS; Glue/Confluent schema registry; Kafka EOS; self-managed brokers |
| 5 | Ingestion | Control plane + file registry + explicit reconciliation; containerized SFTP on EKS | AWS Transfer Family ($216/mo, bypasses the A§31 flow); ad-hoc pipelines; land-and-validate-in-warehouse; row-level rejection |
| 6 | CDC | Debezium on self-managed Kafka Connect (EKS) + Aiven S3 sink | MSK Connect (cost, 10–20 min cycles, opaque logs); own log parsers (D§18 forbids); polling watermarks; DMS |
| 7 | Orchestration | Airflow 2.11 self-hosted on EKS, KubernetesExecutor, git-sync — batch only | MWAA (cost, unstoppable); Dagster/Prefect; Argo/CronJobs; Step Functions; Airflow as business workflow (forbidden) |
| 8 | Analytics | Snowflake Enterprise + dbt; Airflow-triggered `COPY INTO FORCE=FALSE` + `COPY_HISTORY` audit | Snowpipe; Databricks/BigQuery/Redshift; Postgres-as-warehouse; Standard edition (no native masking); dbt Cloud |
| 9 | Decisioning | Layered pipeline, declarative versioned strategies, non-Turing-complete JSON rule DSL, immutable audit, simulation gate, hash-based C/C | CEL/Lua/scripted rules; vendor rules engines; rules in Go code; ML end-to-end selection; stored random arms |
| 10 | Infrastructure | Terraform ≥1.11, 5 stacks, S3-native locking, GitHub OIDC, budgets + teardown levers, CI-only applies | One stack; DynamoDB locks; local applies / long-lived keys; CDK/Pulumi; Terraform Cloud |
| 11 | Identity & exposure | Cognito (scopes, groups, minimal M2M) + IRSA; no public ingress until Phase 12 | Keycloak; public ALB from day 1; EKS Pod Identity; service mesh mTLS; static simulator API keys |
| 12 | Source system | Deterministic corebank simulator (drift, file drops, webhooks, legacy reports), hard-isolated | Static fixtures (no CDC, no faults, no truth); sharing validation code (recon tautology); real bank extract |
| 13 | Delivery model | Contracts-first freeze (`contracts-v1.0`), exemplar-first, path-ownership CI, per-WP verify, adversarial review, local-only commits | Ad-hoc agent development; single-agent sequential; review instead of gates; agents pushing PRs; mutable contracts |
| 14 | Collector UI | React 18 + TS strict + Vite, TanStack Query, PKCE, OpenAPI-generated clients, MSW + Playwright, S3 + CloudFront | Next.js/SSR; no UI; hand-written clients; server-side timeline merge; Amplify |
| 15 | Observability | OpenTelemetry + kube-prometheus-stack + Loki + Tempo (S3) + Alloy + Alertmanager → SNS | AMP/AMG (per-sample + per-seat); DataDog/New Relic/Grafana Cloud; CloudWatch only; Jaeger; Thanos/Mimir |
| 16 | Data conventions | int64 minor units + ISO-4217 (major-unit decimal strings only in decision context documents), prefixed ULIDs, UTC + RFC3339 | Float/decimal money in APIs; decimals everywhere; UUIDv4; auto-increment business keys; local timezones |

## Prerequisites

- An **AWS account** — one manual `00-bootstrap` apply (state bucket, GitHub OIDC provider + roles, SNS, budgets), then nothing manual again.
- A **Snowflake account** — Enterprise trial first, converted before Phase 6; `ACCOUNTADMIN` creates the Terraform service key-pair.
- A **public GitHub repo** with a `dev` environment requiring a human reviewer (OIDC-federated CI, zero long-lived AWS keys).
- A **domain name** (~$12/yr) before Phase 12 — HTTPS, the Cognito SPA client and the Playwright smoke suite need it.
- Local toolchain via `mise` (Terraform ≥1.11, Go, Python 3.12, helmfile, kubectl, snowflake-cli) plus Docker for testcontainers.
- Secret values (SFTP host/user keys, webhook HMAC, Snowflake key-pairs) loaded into Secrets Manager out of band — never into Terraform.

## Milestones (see [tech-design §13](./tech-design.md#13-milestones) for gates)

- **Phase 0 — Foundations & delegation machinery.** Monorepo skeleton, toolchain, CI skeleton, conventions and delegation pack. ✅ complete
- **Phase 1 — Contracts v1 (freeze).** Every OpenAPI spec, event schema, file/CDC contract and registry; tag `contracts-v1.0`. ← underway
- **Phase 2 — Cloud foundation.** Terraform bootstrap, network, data, EKS, Cognito, observability, Airflow, Snowflake (written), infra CI/CD, cost controls.
- **Phase 3 — Platform libraries + local dev stack.** `platform/` (events, outbox, inbox, idempotency, kafka, authn, ruledsl, allocation, testkit) + compose stack.
- **Phase 4 — Source-system simulator.** Corebank schema, deterministic seeder, drift tick, SFTP, file drops with fault injection, webhooks, legacy reports.
- **Phase 5 — Ingestion platform.** Control plane, SFTP/CSV worker, CDC, webhooks, DLQ, reconciliation engine, DAGs, dashboards, canonicalizer.
- **Phase 6 — Analytical platform.** Snowflake RAW, load DAG, dbt staging/intermediate/marts, tests, masking, parity harness, slim CI.
- **Phase 7 — Exemplar + domain wave A.** `services/case` exemplar + playbook, then customer, account, debt, delinquency; E2E-1.
- **Phase 8 — Decisioning core.** Strategy CRUD + governance, rule/policy/guardrail sets, decision service, context builder, pipeline, immutable audit; E2E-2.
- **Phase 9 — Domain wave B.** Arrangement, payment, contact, recovery; E2E-3.
- **Phase 10 — Batch, simulation, champion/challenger, treatment execution. ⭐ MVP GATE (D§88)** — A§106 steps 1–23 end to end on dev EKS with no harness shortcuts, ×2 consecutive days.
- **Phase 11 — Agency & legal.** Placements, fees, referrals, legal cases, mutual-exclusion invariant; E2E-5.
- **Phase 12 — Collector UI.** Public exposure, scaffold + auth, case queue, case detail timeline, contact + PTP, decision explanation, smoke + deploy.
- **Phase 13 — ML & optimization.** Feature marts, payment-propensity model behind the model contract, batch scoring DAG, champion/challenger analytics, optimization v2.
- **Phase 14 — Hardening & production readiness.** DR drills, security verification, cost reporting, runbooks, decisioning observability, performance verification, A§90 audit.
