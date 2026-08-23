# Technical Design — Enterprise Collections & Debt Management Platform

- **Status:** Accepted (2026-08-22 — implementation in progress; Phase 0 complete, Phase 1 contracts freeze underway; amended 2026-08-23: identity provider is Keycloak per [ADR-0017](./adr/0017-identity-keycloak-on-eks.md))
- **Date:** 2026-08-22
- **Related:** ADRs [0001](./adr/0001-build-vendor-neutral-platform.md)–[0016](./adr/0016-data-conventions-money-ids-time.md) + [0017](./adr/0017-identity-keycloak-on-eks.md) · [Implementation plan](./implementation-plan.md) · [CLAUDE.md](../CLAUDE.md) · [conventions](./conventions.md)
- **Normative deep sources:** [`collections_debt_management_platform_design.md`](../collections_debt_management_platform_design.md) (cited **D§n**) and [`collections_debt_management_platform_design_artefacts_1-12.md`](../collections_debt_management_platform_design_artefacts_1-12.md) (cited **A§n**). Where this document and the plan disagree with the design pack, the plan wins and the divergence is recorded in the relevant ADR.

---

## 1. Problem statement

A bank collects on delinquent debt with a set of disconnected capabilities: a core banking system that knows balances, spreadsheets and legacy MI that know yesterday's arrears, a dialler that knows calls, and a vendor product that knows cases. Nothing owns the *decision* — who to contact, on which channel, with what offer, why, and whether it worked — and nothing can prove afterwards what was decided or on what data.

Buying that capability (EXUS, FICO) buys features and rents the domain: the case model, the strategy model, the decision contract and the audit trail become the vendor's (D§59–60). Building everything, down to database log parsing, is years of undifferentiated work (D§61).

This platform takes the third path (D§93, [ADR-0001](./adr/0001-build-vendor-neutral-platform.md)): **build the bank-owned collections domain, decision and audit contracts; reuse proven infrastructure underneath, behind replaceable interfaces.** The closing loop it must support is `Sense → Understand → Decide → Act → Measure → Learn → Improve` (A§111) — data in, a governed decision out, a treatment executed, an outcome measured, and the next decision better because of it.

Secondary, and specific to this repository: the platform is built by a **single developer directing parallel LLM agent sessions** on real AWS and real Snowflake. That constraint is not incidental — it shapes the contracts-first delivery model ([ADR-0013](./adr/0013-llm-agent-delegation-model.md)), the preference for mechanically verifiable choices everywhere, and the cost/teardown design ([ADR-0010](./adr/0010-terraform-stacks-ci-only-applies.md)).

## 2. Goals / non-goals

**Goals**

- **Own the domain:** entities, state machines, business events, decision and strategy contracts, policy versions, audit model, data contracts, reconciliation rules, API contracts (A§1.1).
- **Ingest like a bank:** CDC, SFTP/CSV with control totals, APIs/webhooks and event streams through one control plane with a file registry, checkpoints, quarantine and **explicit** reconciliation (D§14–22, [ADR-0005](./adr/0005-ingestion-platform-control-plane.md)).
- **Decide explainably:** a fixed-order pipeline, declarative versioned strategies, a reviewable rule DSL, immutable decision audit, simulation before activation, champion/challenger ([ADR-0009](./adr/0009-decisioning-layered-pipeline.md)).
- **Execute and measure:** treatment execution with guardrails, contact/promise/arrangement/payment/recovery lifecycles, and outcomes landing in Snowflake marts that feed the next strategy.
- **Be operable:** correlation from file to case (A§97), reconciliation and audit as first-class capabilities, runbooks and alerts that have been seen to fire.
- **Be verifiable by command:** every work package ships a verify script; every phase has a runnable gate ([ADR-0013](./adr/0013-llm-agent-delegation-model.md)).

**Non-goals (for this build)**

- Production availability engineering: single-AZ, no HA for Kafka Connect or the observability stack, dev-grade retention ([ADR-0003](./adr/0003-postgres-per-service-shared-rds.md), [ADR-0006](./adr/0006-cdc-debezium-on-eks.md), [ADR-0015](./adr/0015-observability-otel-grafana-stack.md)).
- Real legacy migration: there is no real bank, so the source system is simulated ([ADR-0012](./adr/0012-source-system-simulator.md)) and parity is proven against a simulated legacy report.
- A full agent desktop, customer self-service, omnichannel digital collections or field collections (D§89 roadmap, beyond MVP).
- Advanced optimization: v1 is constraint filtering plus priority ranking; expected-value optimization arrives in Phase 13.
- Multi-region DR, multi-tenancy, regulatory certification. Specific regulatory requirements must be confirmed with legal/compliance, not inferred by engineering (D§74).

## 3. Users & scope

| User | What they do | Surface |
|---|---|---|
| **Collector** | Works a case queue, opens a case, records a contact outcome, takes a promise-to-pay, reads the decision explanation | Collector workbench ([ADR-0014](./adr/0014-collector-ui-react-vite.md)), scope `cases:read/write`, group `collector` |
| **Operations** | Watches file feeds, reprocesses a quarantined file, drains a DLQ, resolves a reconciliation exception | Ingestion control-plane API + Grafana + runbooks, groups `ops-admin`, `admin` |
| **Strategy author** | Drafts a strategy version, runs a simulation, requests approval | Strategy API, group `strategy-author` |
| **Business / risk approver** | Approves (never authors) a strategy version; distinct identities enforced | Strategy API, groups `business-approver`, `risk-approver` |
| **Analyst** | Reads marts and dashboards; sees PII masked unless explicitly authorized | Snowflake `COLX_REPORTER`, group `analyst` |
| **Machine clients** | `platform-services` (inter-service), `simulator` (acts as the source bank) | Keycloak client-credentials clients ([ADR-0017](./adr/0017-identity-keycloak-on-eks.md)) |

**Build reality.** One developer plus LLM agent sessions execute 113 work packages across 15 phases. Every session is stateless, so the repository carries the shared understanding: frozen contracts, an exemplar service, path ownership, per-WP verify scripts and phase gates ([ADR-0013](./adr/0013-llm-agent-delegation-model.md)). Adversarial review is mandatory wherever money or audit is involved. Infrastructure is real and metered, so `make stop` is part of the daily workflow.

## 4. High-level architecture

Five planes (A§2). Each plane depends only on the one below it through contracts:

```text
+-------------------------------------------------------------------+
| EXPERIENCE   Collector UI | Operations | (Customer digital) | APIs |
+-------------------------------------------------------------------+
| DOMAIN       Customer Account Debt Delinquency Case Arrangement    |
|              Contact Payment Recovery Agency Legal Treatment       |
+-------------------------------------------------------------------+
| DECISION     Policy | Rules | Segmentation | Models | Optimization |
|              | Strategy                                            |
+-------------------------------------------------------------------+
| DATA         Ingestion | Event streaming | Object storage |        |
|              Snowflake | dbt                                       |
+-------------------------------------------------------------------+
| PLATFORM     IAM | Audit | Observability | Config | Secrets | CI/CD|
+-------------------------------------------------------------------+
```

Condensed from the A§107 blueprint — the physical shape of one business day:

```text
   CORE BANKING (simulated)        PARTNERS            CHANNELS
   cb_* tables | SFTP files | payment webhooks        (mock comms provider)
            |                    |                        ^
            v                    v                        |
  +---------------------------------------------+         |
  | INGESTION PLATFORM  (control plane + workers)|        |
  | CDC (Debezium) | SFTP/CSV | webhook | events |        |
  | registry · checksum · validate · quarantine  |        |
  | checkpoint · RECONCILE (explicit)            |        |
  +----------+---------------------+-------------+        |
             |                     |                      |
      canonical events        raw objects                  |
   ingestion.{customers,       s3://colx-dev-raw           |
   accounts,debts,payments}         |                      |
             |                      v                      |
             |               Snowflake RAW -> dbt          |
             |               staging -> intermediate ->    |
             |               marts (+ parity models)       |
             v                      |                      |
  +---------------------------+     |                      |
  | DOMAIN SERVICES (Go)      |     |  population.jsonl    |
  | account -> delinquency -> |     +-----------+          |
  | case ; arrangement,       |                 |          |
  | payment, contact,         |                 v          |
  | recovery, agency, legal   |     +------------------+   |
  +------------+--------------+     | DECISION PLATFORM|   |
               |  DelinquencyChanged| policy>eligibility   |
               |  CaseCreated       | >segment>rules>      |
               +------------------->| models>optimization  |
                                    | -> treatment + AUDIT |
                                    +---------+------------+
                                              |
                                    TreatmentSelected
                                              v
                                    +------------------+
                                    | TREATMENT EXEC   |----+
                                    | guardrails, SMS, |
                                    | email, call task |
                                    +------------------+
   Kafka (MSK) carries every domain event; the Aiven S3 sink lands them in RAW,
   so outcomes return to the marts and the next strategy. Airflow orchestrates
   the batch spine; OpenTelemetry carries one correlation id through all of it.
```

The loop this closes (A§111): `DATA → INGESTION → DOMAIN STATE → DECISIONING → TREATMENT → PAYMENT → RECOVERY → OUTCOME DATA → ANALYTICS → STRATEGY → next decision`. Nothing in the diagram is a case-management application; the value is the loop.

## 5. Monorepo layout

Target layout (plan §3, per A§91; `docs/` rather than A§91's `documentation/`). One repository, `go.work`, one module per service ([ADR-0002](./adr/0002-go-hexagonal-monorepo.md)):

```text
collection-platform/
  CLAUDE.md  Makefile  makefiles/service.mk  go.work  mise.toml  .golangci.yml
  .github/workflows/        # service-ci (reusable) + per-service, contracts, platform, terraform,
                            # images, helmfile, airflow-dags, dbt, ui, e2e, security
  contracts/                # Go module exporting embed.FS (A§95) — FROZEN at tag contracts-v1.0
    openapi/  schemas/{envelope,events,ingestion,decisioning}/  files/  cdc/
    registries/  asyncapi/  examples/  testdata/
  platform/                 # shared Go module: events outbox inbox idempotency kafka postgres
                            # otelkit httpkit apierror authn config health ids clock ruledsl
                            # allocation modelclient testkit
  services/{customer,account,debt,delinquency,case,strategy,decision,treatment,
            arrangement,payment,contact,recovery,agency,legal}/   # A§92 layout each
  services/{model-stub,mock-comms-provider}/                      # deterministic test doubles
  ingestion/                # control-plane, sftp-worker, webhook-receiver, canonicalizer, recon, dlq
  simulator/corebank/       # seeder, drift tick, filedrop, webhooksim, legacyreport
                            # MUST NOT import ingestion/ or platform/  (ADR-0012)
  data/dbt/collections/  data/snowflake/
  airflow/dags/  airflow/tests/
  infrastructure/terraform/{modules,stacks/{00-bootstrap,10-network,20-data,30-eks,40-snowflake},envs/dev}
  deployment/{helmfile.yaml,values,charts,kafka/topics.yaml,observability,images}
  ui/collector-workbench/
  e2e/                      # compose.yaml, mockidp, domain-stub, harness, scenarios
  tools/{contractcheck,layoutcheck,domain-stub,ci}
  scripts/{verify/<WP-ID>.sh,db,dr,cost,ci}
  docs/  security/
```

Each service follows A§92 exactly: `cmd/` · `internal/{domain,application,ports,adapters/{postgres,kafka,http}}` · `migrations/` · `api/` · `tests/`, with `tools/layoutcheck` asserting the shape and the forbidden imports.

## 6. Technology choices

| Layer | Choice | ADR |
|---|---|---|
| Platform strategy | Bank-owned domain + contracts; reuse infrastructure behind interfaces | [0001](./adr/0001-build-vendor-neutral-platform.md) |
| Services | Go, stdlib mux, oapi-codegen v2 strict-server, hexagonal (A§92), one monorepo | [0002](./adr/0002-go-hexagonal-monorepo.md) |
| Operational storage | PostgreSQL 16, database-per-service on 2× RDS (incl. the CDC source); sqlc + pgx + goose | [0003](./adr/0003-postgres-per-service-shared-rds.md) |
| Eventing | MSK Provisioned; A§24 envelope; in-repo JSON Schema; outbox + inbox + Idempotency-Key; per-consumer DLQ | [0004](./adr/0004-kafka-eventing-envelope-outbox.md) |
| Ingestion | Control plane + file registry + explicit reconciliation; containerized SFTP on EKS | [0005](./adr/0005-ingestion-platform-control-plane.md) |
| CDC | Debezium on self-managed Kafka Connect (EKS) + Aiven S3 sink | [0006](./adr/0006-cdc-debezium-on-eks.md) |
| Orchestration | Airflow 2.11 self-hosted, KubernetesExecutor, git-sync — data/batch only | [0007](./adr/0007-airflow-self-hosted-batch-only.md) |
| Analytics | Snowflake Enterprise + dbt; Airflow-triggered `COPY INTO` (`FORCE=FALSE`) | [0008](./adr/0008-snowflake-dbt-analytics.md) |
| Decisioning | Layered pipeline, versioned strategy documents, non-Turing-complete rule DSL, immutable audit | [0009](./adr/0009-decisioning-layered-pipeline.md) |
| Infrastructure | Terraform ≥1.11, 5 stacks, S3-native locking, GitHub OIDC, CI-only applies, budgets + teardown | [0010](./adr/0010-terraform-stacks-ci-only-applies.md) |
| Identity & exposure | Keycloak on EKS (realm as code, colon-form scopes, groups, minimal M2M clients) + IRSA; no public ingress until Phase 12 | [0017](./adr/0017-identity-keycloak-on-eks.md), [0011](./adr/0011-identity-cognito-irsa-no-ingress.md) |
| Source system | Deterministic corebank simulator, hard-isolated from platform code | [0012](./adr/0012-source-system-simulator.md) |
| Delivery model | Contracts-first freeze, exemplar-first, path ownership, per-WP verify, adversarial review | [0013](./adr/0013-llm-agent-delegation-model.md) |
| Collector UI | React 18 + TS strict + Vite, TanStack Query, PKCE, generated clients, S3 + CloudFront | [0014](./adr/0014-collector-ui-react-vite.md) |
| Observability | OpenTelemetry + kube-prometheus-stack + Loki + Tempo + Alloy; Alertmanager → SNS | [0015](./adr/0015-observability-otel-grafana-stack.md) |
| Data conventions | int64 minor units + ISO-4217; prefixed ULIDs; UTC + RFC3339; `tick --as-of` | [0016](./adr/0016-data-conventions-money-ids-time.md) |

Resource prefix `colx`, env `dev`, region `eu-west-1` (variable), tags `project=colx, env=dev, stack=<stack>, managed-by=terraform`. Exact tool versions live in `mise.toml`, `go.work` and `tools/go.mod` — never duplicated into prose.

## 7. Domain model & state machines

**Entities** (A§8.1, A§9). `Customer` (+ contacts, communication preferences, collection constraints) → `Account` (+ product) → `Debt` (+ components) → `Delinquency` → `CollectionCase`, and hanging off the case: `StrategyAssignment`, `Decision`, `Treatment`, `Contact`, `PromiseToPay`, `Arrangement`, `Payment`, `Recovery`, `AgencyPlacement`, `LegalCase`. The platform owns the *collection-specific* representation; the enterprise customer master remains the system of record for customer attributes (A§3.3).

Ownership is strict (A§7.2): one service owns each entity and is the only producer of its events. Nobody reads another service's database (A§7.3) — API or events only.

| Service | Owns | Publishes | Consumes (canonical / domain) |
|---|---|---|---|
| customer | collection customer profile, contactability, constraints | `CustomerUpdated` | `ingestion.customers.v1` |
| account | account state + append-only history | `AccountUpdated` | `ingestion.accounts.v1` |
| debt | debt and components | `DebtUpdated` | `ingestion.debts.v1` |
| delinquency | DPD, buckets, cure/re-default, bucket configs | `DelinquencyChanged` | `AccountUpdated` (single upstream truth) |
| case | case lifecycle, assignment, activity audit | `CaseCreated/Assigned/Resolved` | `DelinquencyChanged`, `PaymentAllocated`, `ArrangementBroken`, `PromiseBroken`, `ContactCompleted` |
| strategy | strategy versions, rule/policy/guardrail sets | `StrategyActivated/StateChanged/Retired`, `RuleSetPublished`, `GuardrailConfigPublished` | — |
| decision | decisions and immutable audit | `DecisionMade`, `TreatmentSelected` | `collections.strategy` (config cache) |
| treatment | treatment execution lifecycle | `TreatmentExecuted/Suppressed` | `TreatmentSelected` |
| arrangement | promises and arrangements + schedules | `PromiseCreated/Broken`, `ArrangementCreated/Broken` | `PaymentAllocated` |
| payment | payment intake, matching, allocation | `PaymentReceived/Allocated` | `ingestion.payments.v1`, `ArrangementCreated/Broken` |
| contact | contact activity (sole producer of contact events) | `ContactAttempted/Completed` | — |
| recovery | recovery records and metrics | `RecoveryRecorded` | `PaymentAllocated` |
| agency | placements, fees, performance | `DebtPlaced/DebtRecalled` | `PaymentAllocated`, `LegalStatusChanged` |
| legal | referrals and legal cases | `LegalStatusChanged` | `DebtPlaced`, `PaymentAllocated` |

**Case (A§10.1).** `NEW → OPEN → ACTIVE`, with `ACTIVE ↔ SUSPENDED`, `ACTIVE → ESCALATED → {AGENCY, LEGAL}`, and `→ RESOLVED → CLOSED`. Implemented as an explicit `map[Status]map[Command]Status` transition table with guards and an exhaustive `status × command` table test. A closed case rejects everything except reopen (`409 CASE_CLOSED`, invariant A§11.1). One open case per account is a partial unique index, not a check-then-insert.

**Promise (A§10.2).** `PROPOSED → ACCEPTED → {KEPT, BROKEN, CANCELLED}`.

**Arrangement (A§10.3).** `DRAFT → ACTIVE → {COMPLETED, BROKEN, CANCELLED}`. A schedule is valid only if installments sum exactly to the total, dates strictly ascend and the first is not in the past (invariant A§11.5) — otherwise `400 ARRANGEMENT_INVALID` with A§20 details.

**Delinquency lifecycle (D§5).** DPD is computed as `asOf − oldestUnpaidDueDate`; buckets (`1-30 / 31-60 / 61-90 / 90+`) are **configurable versioned rows**, validated contiguous and non-overlapping, never hard-coded. Status runs `CURRENT → DELINQUENT → CURED → (REDEFAULTED within window) → DELINQUENT`. A daily `tick evaluate --as-of=<date>` recomputes DPD and emits **transitions only** — no daily event noise. `DelinquencyChanged` is the trigger for case creation (`DELINQUENT` + no open case) and case resolution (`CURED`).

**Invariants (A§11) — each one has a mechanism, not a policy:**

| # | Invariant | Enforced by |
|---|---|---|
| 1 | A closed case receives no new treatment unless reopened | transition table + guard → `409 CASE_CLOSED` |
| 2 | A payment is allocated at most once | `UNIQUE(payment_id)` on allocation + status guard |
| 3 | A strategy version cannot change after activation | status guard (`DRAFT`/`TEST` only) + content hash + `PUBLISHED` immutability |
| 4 | A decision must reference a strategy version | non-null `strategy_id`/`strategy_version` in `decision_audit` |
| 5 | An arrangement must have a valid payment schedule | schedule property tests (sum, ascending, first ≥ today) → `400 ARRANGEMENT_INVALID` |
| 6 | Agency placement and active legal ownership are mutually exclusive | partial unique + cross-service read models, both directions → `409 PLACEMENT_LEGAL_CONFLICT` |
| 7 | An event id is unique | `event_id` unique in the outbox; `(consumer, event_id)` in the inbox |
| 8 | A retryable command is idempotent | `Idempotency-Key` middleware + natural keys (e.g. `UNIQUE(source_system, external_payment_ref)`) |

Plus one open case per account: a partial unique index `UNIQUE(account_id) WHERE status NOT IN ('RESOLVED','CLOSED')`, so concurrent creation has exactly one winner.

**Analytical model** (A§43–49, [ADR-0008](./adr/0008-snowflake-dbt-analytics.md)). `RAW` preserves source fidelity (VARIANT for CDC/webhooks/events; all-VARCHAR plus `_file_id, _row_number, _business_date` for file feeds) → `stg_<source>_<entity>` standardizes names, types, codes, nulls and time zones (A§45), and deduplicates CDC to latest state per key → `int_<concept>` (account current, delinquency, payment history, customer exposure, collection eligibility) → marts: `fct_delinquency_snapshot`, `fct_payment`, `fct_collection_case`, `fct_collection_action`, `fct_arrangement`, `fct_decision`, `fct_contact` with `dim_customer` (SCD2), `dim_account`, `dim_product`, `dim_strategy`, `dim_date`. Marts carry enforced dbt contracts.

## 8. API surface

REST + OpenAPI is the contract for every capability (D§3.3, A§12.1). Specs live in `contracts/openapi/<domain>.v1.yaml`: `customer, account, debt, delinquency, case` (wave A); `arrangement, payment, contact, recovery, agency, legal` (wave B/C); `strategy, decision, treatment, model` (decisioning); `ingestion-control-plane`; plus `common.v1.yaml` for the shared error and header components.

| Spec | Representative operations | Scopes |
|---|---|---|
| `customer.v1` | customer reads, contactability | `cases:read` |
| `account.v1` | account reads, `GET /v1/customers/{id}/accounts`, account history | `cases:read` |
| `debt.v1` | debt + components reads | `cases:read` |
| `delinquency.v1` | `GET /v1/accounts/{id}/delinquency`, `GET\|PUT /v1/delinquency/bucket-configs` (versioned, D§5) | `cases:read/write` |
| `case.v1` | the nine A§15 endpoints + `GET /v1/cases` (filters incl. `assignedCollector`, `sort`, pagination), activities | `cases:read/write/admin` |
| `arrangement.v1` | A§17 arrangements + promises (`POST /v1/promises`, cancel) + `GET /v1/arrangements?accountId=` | `cases:write` |
| `payment.v1` | `POST /v1/payments` (Idempotency-Key + natural key), reads, ops re-allocation | `payments:admin` for overrides |
| `contact.v1` | `POST /v1/contacts`, `POST /v1/contacts/{id}/outcome`, reads by customer/case | `cases:write` |
| `recovery.v1` | record + reads + `GET /v1/recovery-metrics` (D§11) | `cases:read` |
| `agency.v1` | A§18 placements, agencies, fees | `cases:write` |
| `legal.v1` | referrals, legal cases, status changes, `GET /v1/cases/{caseId}/legal` | `cases:write` |
| `strategy.v1` | versioned CRUD, governance transitions, rule-set and guardrail resources | `strategy:author` + groups |
| `decision.v1` | the five A§16 endpoints + `GET /v1/decisions?caseId=&accountId=` + batch / simulation / shadow-run + `GET /v1/reference/reason-codes` | `decisions:read/write` |
| `treatment.v1` | `POST /v1/treatments`, reads by case, provider webhook | `decisions:write` |
| `model.v1` | `POST /v1/models/{id}/versions/{v}:score` (body = context document) | internal |
| `ingestion-control-plane.v1` | D§79 sources/feeds/files/reprocess/quarantine/checkpoints/jobs/DLQ + A§19 reconciliation runs and checks | `ingestion:read/write`, `webhook:write` |

**Conventions, enforced by CI and `platform/` middleware:**

- **Error contract is exactly A§20** — `{code, message, correlationId, details[{field, reason}]}` via `platform/apierror`. No stack traces, no internal messages, `code` a stable SCREAMING_SNAKE business code.
- **`Idempotency-Key` on every POST command** (A§21): same key + same request hash replays the stored response; same key + different hash → `422`; concurrent in-flight → `409`.
- **`If-Match` row versions on PATCH** → `412` on mismatch; pagination is `limit` + opaque `cursor`.
- **Path ownership:** the data-owning service owns the URL path — `GET /v1/customers/{id}/accounts` lives in the account spec, `/v1/accounts/{id}/delinquency` in the delinquency spec (A§13/§14 catalogue preserved, A§7.3 ownership kept).
- **Auth per service**, deny-by-default `RequireScope`; no endpoint ships without a scope. Scope strings are the colon-form names above, carried verbatim in the token ([ADR-0017](./adr/0017-identity-keycloak-on-eks.md)).
- **Schema hygiene:** `additionalProperties:false`, explicit `required`, `$id` per file, money as `{amountMinor, currency}`, RFC3339 UTC. Released files are immutable; a change is a new `vN` file.
- Gates: vacuum lint (operationId, error responses, Idempotency-Key on POSTs) and `oasdiff breaking` against `contracts-v1.0`.

## 9. Event architecture

Events are immutable, versioned, business-meaningful, traceable, replayable and schema-validated (A§22.1). Delivery is at-least-once with idempotent consumers (D§3.5) — never an exactly-once assumption.

- **Envelope: exactly the 10 A§24 fields** (`eventId eventType eventVersion occurredAt producer aggregateType aggregateId correlationId causationId payload`), built and validated only through `platform/events`.
- **Topics** `collections.<context>` for the 14 contexts (A§25) plus canonical `ingestion.{customers,accounts,debts,payments}.v1`; raw `cdc.corebank.*` and raw webhook topics are internal to `ingestion/`. Declared in `deployment/kafka/topics.yaml`; auto-create off.
- **Ordering is per aggregate key only** (A§26) — `accountId` for `DelinquencyChanged`, `caseId` for case events, and so on. No global ordering is assumed anywhere.
- **Publication is always the transactional outbox** (`outbox.Enqueue(ctx, tx, …)` in the same transaction as the state change; advisory-lock leader relay preserving per-key order; payload validated *before* the row is written). **Consumption always dedupes** via `inbox.Dedupe(ctx, tx, consumer, eventId)` in the transaction with the side effects.
- **DLQ** `collections.dlq.<service>` (A§27) and `dlq.ingestion.v1` with origin topic and error headers; retry with backoff, then DLQ, then alert, investigate and replay — never block a partition, never drop.
- **Catalogue:** the 22 core events of A§23 (`CustomerUpdated`, `AccountUpdated`, `DebtUpdated`, `DelinquencyChanged`, `CaseCreated/Assigned/Resolved`, `StrategyActivated`, `DecisionMade`, `TreatmentSelected`, `ContactAttempted/Completed`, `PromiseCreated/Broken`, `ArrangementCreated/Broken`, `PaymentReceived/Allocated`, `RecoveryRecorded`, `DebtPlaced/Recalled`, `LegalStatusChanged`), plus extensions justified by the A§7.2 ownership matrix (plan §6.5): `TreatmentExecuted`, `TreatmentSuppressed`, `StrategyStateChanged`, `StrategyRetired`, `RuleSetPublished`, `GuardrailConfigPublished`, ingestion-internal `FileStatusChanged`, and the four canonical snapshot events. All defined before the freeze.
- **One producer per event.** In particular, treatment-service *calls* contact-service (`POST /v1/contacts`, then the outcome) and contact-service alone emits contact events (plan §6.4); the UI records manual contacts through the same API.
- Load-bearing payloads are specified, not implied: `DelinquencyChanged` carries `dpd`, previous/new bucket, status and overdue amount; `ArrangementCreated` embeds the schedule; `PaymentAllocated` carries the allocation lines.

Normative topic and key map (consolidating A§23 and A§25):

| Topic | Partition key | Events |
|---|---|---|
| `collections.customer` | `customerId` | `CustomerUpdated` |
| `collections.account` | `accountId` | `AccountUpdated` |
| `collections.debt` | `debtId` | `DebtUpdated` |
| `collections.delinquency` | `accountId` | `DelinquencyChanged` |
| `collections.case` | `caseId` | `CaseCreated`, `CaseAssigned`, `CaseResolved` |
| `collections.strategy` | `strategyId` | `StrategyActivated`, `StrategyStateChanged`, `StrategyRetired`, `RuleSetPublished`, `GuardrailConfigPublished` |
| `collections.decision` | `decisionId` | `DecisionMade` |
| `collections.treatment` | `caseId` | `TreatmentSelected`, `TreatmentExecuted`, `TreatmentSuppressed` |
| `collections.contact` | `contactId` | `ContactAttempted`, `ContactCompleted` |
| `collections.arrangement` | `promiseId` / `arrangementId` | `PromiseCreated`, `PromiseBroken`, `ArrangementCreated`, `ArrangementBroken` |
| `collections.payment` | `paymentId` | `PaymentReceived`, `PaymentAllocated` |
| `collections.recovery` | `recoveryId` | `RecoveryRecorded` |
| `collections.agency` | `placementId` | `DebtPlaced`, `DebtRecalled` |
| `collections.legal` | `legalCaseId` | `LegalStatusChanged` |
| `ingestion.{customers,accounts,debts,payments}.v1` | aggregate id | canonical snapshots (`CustomerSnapshot`, `AccountSnapshot`, `DebtSnapshot`, `PaymentNotification`) |
| `ingestion.file.lifecycle.v1` | `fileId` | `FileStatusChanged` |
| `cdc.corebank.public.cb_*`, `ingestion.webhook.payment.v1` | source key | raw — **internal to `ingestion/`** |
| `collections.dlq.<service>`, `dlq.ingestion.v1` | original key | dead-lettered messages with origin + error headers |

## 10. Ingestion & data platform

**Four patterns, one control plane** (A§28.1, D§105, [ADR-0005](./adr/0005-ingestion-platform-control-plane.md)):

1. **CDC** — Debezium on Kafka Connect against the corebank Postgres, snapshot then stream, offsets and slot lag checkpointed every 5 minutes ([ADR-0006](./adr/0006-cdc-debezium-on-eks.md)).
2. **SFTP/CSV** — the full A§31 flow: connect with a pinned host key (fail closed) → discover → download to landing while computing SHA-256 → register → validate → canonicalize to RAW → archive after reconciliation → checkpoint. Workers keep no state of their own.
3. **API/webhook** — `POST /v1/webhooks/payments` with an OIDC JWT (`webhook:write`) **and** HMAC (A§34 defence in depth), schema validation, and idempotency by `webhook_event(event_id)`; a duplicate returns `200 {status:"duplicate"}`.
4. **Events** — the canonical bridge below.

**File states (A§36):** `DISCOVERED → RECEIVED → VALIDATING → VALIDATED → PROCESSING → PROCESSED → RECONCILING → RECONCILED → ARCHIVED`, with `FAILED`, `QUARANTINED` and `DUPLICATE` as the exception paths. Every transition is an append-only audit row with actor and reason. Dedup: checksum repeat → `DUPLICATE`; filename reuse with different content → `QUARANTINED FILENAME_REUSED` (A§33). Validation follows D§21 order and any ERROR row quarantines the whole file with a `rejects.jsonl` alongside the original.

**Reconciliation is explicit, never inferred from pipeline success** (D§37–38, A§37):

| Scope | Check | Tolerance |
|---|---|---|
| `files:<feed>` | COUNT identity `declared == rejected + loaded` | 0 |
| `files:<feed>` | AMOUNT identity `control_total_declared == control_total_computed` | 0 |
| `files:<feed>` | Snowflake `COPY_HISTORY.rows_loaded == row_count_parsed` | 0 |
| `cdc:corebank` | source latest-state count vs Snowflake staging count (posted by the load DAG — authoritative; WARNING until then) | 0 |
| `webhooks:payments` | day's `webhook_event` count vs corebank webhook-channel payments | 0 |
| analytics parity | legacy report vs marts at identical grain, per date | `abs_diff <= 0.01` |

Runs are `PASS | WARNING | FAIL`; a FAIL emits an event, alerts and blocks the day. Files reach `ARCHIVED` only after a PASS.

**Canonicalizer bridge (plan §6.2).** Raw Debezium and raw webhook topics are internal. A stateless canonicalizer emits `ingestion.{customers,accounts,debts,payments}.v1` in the A§24 envelope, keyed by aggregate, with deterministic event ids derived from `(source, PK, LSN/event-id)` so replays dedupe downstream, and **NUMERIC strings converted to int64 minor units** here ([ADR-0016](./adr/0016-data-conventions-money-ids-time.md)). Domain services consume only canonical topics. Payments arrive twice by design — webhook intraday, file/CDC in batch — and payment-service's natural key `UNIQUE(source_system, external_payment_ref)` absorbs the overlap (D§47).

**Warehouse path.** Aiven S3 sink and the SFTP canonicalizer write to `s3://colx-dev-raw/...`; Airflow triggers `COPY INTO` with `FORCE=FALSE` and a correlation `QUERY_TAG`, writes `COPY_HISTORY.rows_loaded` back to the file registry, and posts the `loaded == parsed` check. dbt then builds staging → intermediate → marts with the A§50 test set and dbt contracts on marts. **Parity models** full-outer-join the simulator's legacy report against the new marts at identical grain and fail above `abs_diff > 0.01` — the migration gate of A§76/§88, and the reason the legacy extract must come from an independent code path ([ADR-0012](./adr/0012-source-system-simulator.md)).

## 11. Decisioning & treatment

**Pipeline (A§52, A§104).** Fixed order, one stage interface, a trace document per stage:

| # | Stage | Input | Effect |
|---|---|---|---|
| 0 | Context build | `subject` (+ supplied context for TEST/SIMULATION/BATCH, role-gated) | fetch account/delinquency/customer/arrangement/contact (300 ms timeout, 1 retry, provenance per field), validate vs `context-document.v1.json`, snapshot to S3 with a content hash |
| 1 | Policy | global PUBLISHED policy sets | narrowing constraints only: `Suppress`, `ForbidChannel` |
| 2 | Eligibility | ACTIVE strategies | strategy selection by eligibility + `priority`; none ⇒ `INELIGIBLE / NO_STRATEGY_MATCH` |
| 3 | Segmentation | strategy `segments[]` | first match wins; the last segment is `DEFAULT` |
| 4 | Rules | referenced PUBLISHED rule set | outcome + mandatory reason codes via `platform/ruledsl` |
| 5 | Models | `modelRefs[]` | scores via the model contract; timeout and `onError` honoured per ref |
| 6 | Optimization | candidate treatments | v1: constraint filter + treatment-priority ranking (expected value in Phase 13) |
| 7 | Treatment selection | the above | `TREAT / NO_ACTION / SUPPRESSED / INELIGIBLE` + post-selection policy guard |
| 8 | Explanation + audit | trace | append-only audit row, trace to S3, `DecisionMade` (+ `TreatmentSelected` when TREAT) in one transaction |

Policy precedence is structural — constraints can only narrow — plus the runtime guard that turns any violation into `NO_ACTION` + `POLICY_VIOLATION_GUARD` ([ADR-0009](./adr/0009-decisioning-layered-pipeline.md)). A property test over ≥1000 cases asserts the output never violates the policy constraints it was given.

**Strategy lifecycle (A§60, D§77).** `DRAFT → TEST → SIMULATED → BUSINESS_APPROVED → RISK_APPROVED → SCHEDULED → ACTIVE → RETIRED`, with `SIMULATED|BUSINESS_APPROVED → REJECTED`. The content hash freezes at `TEST → SIMULATED`; `SIMULATED` requires a completed simulation for that hash; approvers must differ from the author; activation atomically retires the previous ACTIVE version. Every transition is an append-only row plus a `StrategyStateChanged` event.

**Rule DSL.** Condition groups `{all|any}` nested ≤5 over leaves `{field, op, value}`; `field` must exist in the versioned context field catalogue; `FORBID_CHANNEL` only in POLICY sets; first match wins by `(priority desc, ruleId asc)`; a missing field is a false leaf plus a `FIELD_MISSING` trace, never an error. Every rule carries a mandatory reason code drawn from the registry. Deliberately not Turing-complete, so it is diffable, fuzzable and parity-testable.

**Audit and explanation (D§39, D§3.6).** `decision_audit` is INSERT + SELECT only (trigger plus role grants; an `UPDATE` attempt is *proven* to fail in the phase gate), range-partitioned by `decided_at`, recording strategy/policy/rule/model versions, experiment arm and bucket, reason codes, decision and treatment, input snapshot reference and content hash, correlation id and mode (`ONLINE | BATCH | TEST | SIMULATION | SHADOW`). The explanation endpoint renders the stage trace with registry descriptions.

**Simulation, shadow and champion/challenger.** Simulation runs a candidate and a baseline over the same population in `SIMULATION` mode (no treatment events) and reports treatment distribution deltas, contact volume by channel, suppressions, reason-code histogram and segment breakdown (D§40, A§59). Shadow runs compare against another strategy or `LEGACY`, categorizing each diff with the A§88 taxonomy (`EXPECTED`, `DATA_DIFFERENCE`, `RULE_TRANSLATION_ERROR`, `MISSING_RULE`, `LEGACY_BUG`, `NEW_BUG`, `UNCATEGORIZED`). Champion/challenger allocation is a deterministic hash of `accountId:salt` over cumulative bps, so a re-decision lands in the same arm (D§41).

**Batch decisioning.** Population lines are built by dbt/Snowflake to be *exactly* context documents, unloaded to `population.jsonl` with a manifest, and decided with the config snapshot pinned at run start. Outcomes carry the control-total identity `populationRows == decided + suppressed + ineligible + errored` (D§37–38), asserted again after the outcomes are loaded into Snowflake.

**Treatment execution.** `TreatmentSelected` → treatment-service (`REQUESTED → VALIDATED → DISPATCHED → DELIVERED|FAILED`, plus `SUPPRESSED`), deduped by inbox and by `UNIQUE(decision_id)`. Before dispatch, guardrails re-evaluate: a fresh customer-policy fetch that **fails closed**, contact windows per channel from the published guardrail config, and frequency caps against recorded attempts; a window block schedules for the next open window. Channels sit behind a `ChannelAdapter` SPI (SMS/EMAIL/LETTER/DIGITAL) with a deterministic mock provider in dev and SES in sandbox; dispatch is idempotent on `provider_ref = treatment_id`. Contacts are recorded through contact-service, which alone emits contact events.

## 12. Security

Layered per A§61: identity, API, service identity, network, data, application, audit, governance.

- **Identity (A§62).** **Keycloak on EKS**, realm `colx` imported as code, backed by a `keycloak` database on the platform RDS. Client scopes are named exactly the logical colon-form scopes (so `platform/authn` needs no mapping); groups arrive as a plain `groups` claim; short-lived tokens; no long-lived shared credentials. Machine clients `platform-services` and `simulator` use client credentials with secrets set post-start from ESO — never in git or the realm JSON. SPA client (PKCE) from Phase 12, when Keycloak is publicly exposed for login redirects ([ADR-0017](./adr/0017-identity-keycloak-on-eks.md), superseding Cognito in [ADR-0011](./adr/0011-identity-cognito-irsa-no-ingress.md)).
- **Service identity (A§63).** Workloads reach AWS through **IRSA** — no node-role permissions, no static keys in pods. Inter-service calls carry a scoped machine token.
- **API (A§64).** Every service validates the JWT itself with deny-by-default scope checks; the gateway (Phase 12) adds rate limiting and correlation injection and contains **no business rules**.
- **Network (A§65).** Private subnets; data subnets have no NAT route; RDS accepts only the EKS security group; **no public ingress before Phase 12** (`make keycloak` and other port-forwards until then), then ALB + ACM + WAF basics in front of the API, CloudFront for the SPA, and Keycloak exposed for browser login redirects — which makes WAF rules and admin-console lockdown mandatory at that point. Default-deny NetworkPolicies in the ingestion and services namespaces with explicit allows.
- **Secrets (A§66).** Every Kubernetes secret is an `ExternalSecret` referencing `colx/dev/*` via External Secrets Operator; **zero secret values in git, values files, DAGs, images or logs**. Terraform creates placeholders only; values arrive out of band. CI runs gitleaks over full history, blocking.
- **Encryption (A§68).** TLS in transit everywhere; SSE-KMS with per-purpose CMKs (`data`, `db`, `msk`, `secrets`) for S3, RDS, MSK and backups; key-pair auth for Snowflake service users.
- **Data classification and PII (A§67, A§69, D§45).** Native Snowflake `MASKING POLICY` on `dim_customer.{full_name, phone, email, dob}` — `COLX_REPORTER` sees `***MASKED***`, `COLX_PII_READER` (granted to nobody by default) sees values, and REPORTER has no grants on RAW at all. The mapping lives in `security/masking-matrix.md`; v1 events carry minimal PII and payload tokenization is deferred with a written note.
- **Audit (A§70, D§3.6).** Append-only everywhere it matters: decision audit, case activities, file state transitions, strategy transitions. `WHO/WHAT/WHEN/WHERE/WHY/RESULT` plus, for decisions, the version set and input reference.
- **Monitoring (A§71).** Failed authentication, privilege changes, bulk access, admin actions, SFTP failures and secret access; CloudTrail management events with a root-usage alert. Security CI (gitleaks, trivy image/config, govulncheck) is blocking.

## 13. Milestones

Fifteen dependency-ordered phases (plan §5), 113 work packages. Streams run in parallel after Phase 1: `INFRA 2→4→5→6→(13)`, `DOMAIN 3→7→9→11`, `DECISION 8→10`, `UI 12`. Peak parallelism 5–6 agents.

| # | Phase | Exit gate |
|---|---|---|
| 0 | Foundations & delegation machinery | Repo scaffold, conventions, CI skeleton green **(complete)** |
| 1 | Contracts v1 — freeze | `contracts-v1.0` tagged; contracts CI green **(underway)** |
| 2 | Cloud foundation (A§74) | Infra live, observability live, a full teardown/rebuild cycle proven |
| 3 | Platform libraries + local dev stack | `platform/` ≥85% coverage; compose stack boots; smoke E2E green |
| 4 | Source-system simulator | 3 consecutive simulated days green |
| 5 | Ingestion platform (A§75) | Reconciliation passes 3 days; CDC lag p95 <60 s; every fault class quarantined; canonical topics flowing |
| 6 | Analytical platform (A§76) | dbt build + tests green; parity ≤0.01 over ≥5 dates; masking verified |
| 7 | Exemplar + domain wave A | E2E-1: ingestion → delinquency → case, one correlation id |
| 8 | Decisioning core | E2E-2: decision → audit → events → explanation; audit `UPDATE` provably rejected |
| 9 | Domain wave B | E2E-3: promise → pay → allocate → recover |
| 10 | Batch, simulation, C/C, treatment execution | **MVP GATE (D§88)** — A§106 steps 1–23 end to end on dev EKS, no harness shortcuts, ×2 consecutive days |
| 11 | Agency & legal | E2E-5: escalation branches + mutual-exclusion invariant both directions |
| 12 | Collector UI | Playwright smoke green on CloudFront ×3 runs, a11y ≥90 |
| 13 | ML & optimization | Batch scoring in the daily DAG; champion/challenger report mart live |
| 14 | Hardening & production readiness | D§90 checklist fully evidenced; perf thresholds met; DR drills executed |

**The MVP is D§88** and lands at the end of Phase 10: ingestion platform, account, delinquency, collection case, basic strategy, decision API, treatment, payment arrangement, Snowflake, dbt, Airflow, reconciliation, audit — each item linked to gate evidence.

## 14. Verification

Verification is commands, at three levels (plan §10, [ADR-0013](./adr/0013-llm-agent-delegation-model.md)).

**Per work package.** `make -C <dir> lint test` green; `make -C <dir> coverage` (domain ≥90%, module ≥80%); `make generate && git diff --exit-code` clean; `make contracts-check` green; `make verify WP=<id>` green (every WP ships `scripts/verify/<WP-ID>.sh` with at least one expected-fail assertion); `make ownership-check WP=<id>` clean; plus `test-integration`, `contract-test`, `layoutcheck` and `helm lint` where they apply. Runbooks are updated and linted whenever operational behaviour changes.

**Per phase.** A gate file `docs/gates/gate-<n>.md` in which every line is runnable, executed by a verifier agent that implemented none of the work packages, with output committed to `docs/gates/evidence/<n>/`. Key gates: Phase 5 (reconciliation 3 days + the full fault matrix), Phase 6 (parity ≤0.01 + masking), Phase 7 (chain with one correlation id), Phase 8 (audit immutability), **Phase 10 (MVP)**, Phase 12 (smoke ×3 + a11y), Phase 14 (A§90 zero unchecked).

**Continuously.** Contracts CI (schema compile, example validation against schema *and* envelope, asyncapi refs, reason-code cross-reference, immutability check, `oasdiff breaking` empty vs `contracts-v1.0`), ownership CI, security CI, and the E2E suite on main.

**The MVP scenario (E2E-4 = A§106 steps 1–23).** Simulator tick → CDC + files + webhooks → ingestion (validate, checkpoint, reconcile) → canonical events → account/debt/delinquency → case → daily decisioning DAG (population from Snowflake → batch decisions → outcomes loaded) → treatment with guardrails → mock provider → contact events → promise and arrangement → payment webhook → allocation → recovery → events in Snowflake → dbt marts → dashboard rows populated. It must hold the batch reconciliation identity, produce **zero duplicate dispatches** on a `TreatmentSelected` replay (D§49), and let one correlation id be traced from file to case (A§97).

## 15. Risks & mitigations

| Risk | Mitigation |
|---|---|
| **Sub-agent drift and collisions** — the dominant risk | Frozen contracts + immutability CI, `docs/ownership.yaml` + ownership CI, exemplar + `layoutcheck`, mandatory L-WP decomposition, per-WP verify scripts ([ADR-0013](./adr/0013-llm-agent-delegation-model.md)) |
| **Terraform state corruption by an agent** | Applies only via env-gated CI; plan role read-only; `00-bootstrap` the sole human apply ([ADR-0010](./adr/0010-terraform-stacks-ci-only-applies.md)) |
| **Debezium replication-slot WAL growth** filling the micro RDS — the most likely real incident | `heartbeat.interval.ms=10000`, retained-WAL alert at 500 MB, drop/recreate runbook ([ADR-0006](./adr/0006-cdc-debezium-on-eks.md)) |
| **Simulation/online context skew** invalidating every simulation | Population lines *are* context documents validated by the same schema, plus a context-parity test for N accounts inside the MVP gate ([ADR-0009](./adr/0009-decisioning-layered-pipeline.md)) |
| **Event payload underspecification** (A§23 names events, not payloads) | Payloads invented in Phase 1 with domain sign-off, kept minimal so a `v2` is cheap; immutability CI prevents silent mutation |
| **Reconciliation tautology** if the simulator shared validation code | Hard isolation: `simulator/` never imports `ingestion/` or `platform/`; reviewer-enforced ([ADR-0012](./adr/0012-source-system-simulator.md)) |
| **Snowflake trial timing / credit burn** | Snowflake stack written in Phase 2, applied at Phase 6 kickoff; XS warehouses, 60 s auto-suspend, 50-credit hard cap ([ADR-0008](./adr/0008-snowflake-dbt-analytics.md)) |
| **Redpanda-in-tests vs MSK-IAM-in-cluster drift** | Same client (franz-go) behind `platform/kafka`; one dev-cluster Kafka smoke test per service wave ([ADR-0004](./adr/0004-kafka-eventing-envelope-outbox.md)) |
| **Money arithmetic** (allocation splits, fee bps, control totals) | int64 minor units, golden arithmetic vectors, adversarial review on allocation, fees, reconciliation and batch totals ([ADR-0016](./adr/0016-data-conventions-money-ids-time.md)) |
| **Cost creep on always-on infra** (MSK, EKS control plane and NAT cannot stop) | Budget alarms + anomaly monitor, `stop` / `destroy-heavy` levers, a weekly `make stop` habit, destroy MSK during long pauses ([ADR-0010](./adr/0010-terraform-stacks-ci-only-applies.md)) |

Architectural risks from A§100 remain live and are managed rather than closed: over-engineering (start from bounded contexts, split only where justified), rebuilding commercial products poorly (MVP scope discipline), Snowflake lock-in (RAW in S3, dbt SQL, no domain logic in the warehouse), Airflow becoming a workflow engine ([ADR-0007](./adr/0007-airflow-self-hosted-batch-only.md)), decisioning becoming ungoverned code ([ADR-0009](./adr/0009-decisioning-layered-pipeline.md)), and data quality blocking migration ([ADR-0012](./adr/0012-source-system-simulator.md) — a simulator understates this one).

## 16. Prerequisites & open questions

**Prerequisites**

- An **AWS account** with permission to create the bootstrap stack by hand once (state bucket, GitHub OIDC provider and roles, SNS, budgets), then nothing manual again ([ADR-0010](./adr/0010-terraform-stacks-ci-only-applies.md)).
- A **Snowflake account** — Enterprise trial first, converted before Phase 6; `ACCOUNTADMIN` creates the Terraform service key-pair ([ADR-0008](./adr/0008-snowflake-dbt-analytics.md)).
- A **public GitHub repository** (`canhtoanptit/collection-platform`) with a `dev` environment requiring a human reviewer, for OIDC-federated CI.
- A **domain name** (~$12/yr) before Phase 12 for HTTPS, the Keycloak SPA client (and Keycloak's own public exposure for login redirects) and the Playwright smoke suite ([ADR-0017](./adr/0017-identity-keycloak-on-eks.md), [ADR-0011](./adr/0011-identity-cognito-irsa-no-ingress.md)).
- Local toolchain via `mise` (Terraform ≥1.11, Go, Python 3.12, helmfile, kubectl, snowflake-cli) plus Docker for testcontainers.
- Out-of-band secret values (SFTP host and user keys, webhook HMAC, Snowflake key-pairs) loaded into Secrets Manager — never into Terraform.

**Open questions (plan §9 — defaults chosen, flag to change)**

1. **Go module prefix** — `github.com/canhtoanptit/collection-platform`; a trivial rename before Phase 3.
2. **Domain purchase for Phase 12** — required for public HTTPS; until then the UI runs against port-forwards.
3. **Snowflake edition after the trial** — default Enterprise for native masking; drop to Standard + secure views if credits exceed ~40/mo.
4. **Simulator scale** — default 30k customers / 50k accounts; 10× is possible at more RDS cost and a longer CDC snapshot.
5. **Business-date timezone** — UTC everywhere (simplest verifiable); local-time SLAs only on request ([ADR-0016](./adr/0016-data-conventions-money-ids-time.md)).
6. **Challenger execution governance** — encoded as "the challenger version must be risk-approved before champion activation"; the business rule behind D§41's "subject to policy and governance" needs confirmation.
7. **PII minimization in events** — v1 events carry minimal PII and masking covers the marts; payload tokenization is deferred with a note in `security/masking-matrix.md`.
8. **Agency inbound file semantics** — outbound placements plus a DRAFT inbound schema ship; real remittance formats need real agency specifications.

Resolved by decision rather than left open: no schema registry ([ADR-0004](./adr/0004-kafka-eventing-envelope-outbox.md), a deliberate divergence from A§1.2), MSK Provisioned over Serverless, containerized SFTP over Transfer Family, Debezium on EKS over MSK Connect, self-hosted Airflow over MWAA, `COPY INTO` over Snowpipe, no public ingress before Phase 12, and — since 2026-08-23, pre-apply — **Keycloak on EKS as the identity provider instead of Cognito** ([ADR-0017](./adr/0017-identity-keycloak-on-eks.md)).
