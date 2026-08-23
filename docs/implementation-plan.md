# Implementation Plan — Enterprise Collections & Debt Management Platform

## 1. Context

The repo (`/Users/toannguyen/workspace/learn/blog/collection-platform`, public GitHub `canhtoanptit/collection-platform`) contains two authoritative design documents and no code:

- **D§n** = `collections_debt_management_platform_design.md` — vendor-neutral target architecture (principles, functional requirements, tech decisions, build-vs-buy, migration strategy, MVP §88).
- **A§n** = `collections_debt_management_platform_design_artefacts_1-12.md` — detailed design pack (L1/L2 architecture, bounded contexts §5–7, domain model §8–11, API catalogue §12–21, event catalogue §22–27, ingestion §28–37, Airflow §38–42, Snowflake/dbt §43–51, decisioning §52–60, security §61–71, migration waves §72–90, repo layout §91, Go service shape §92).

This plan turns those designs into **15 dependency-ordered phases of 113 work packages (WPs)**, each a self-contained brief executable by a cheaper-model LLM sub-agent (Opus-class), with runnable acceptance criteria and verifier-run gates between phases. Quality bar: enterprise banking — auditability, reconciliation, idempotency, and security are first-class.

**Locked scope decisions (user):**
1. **Real AWS infrastructure** via Terraform (EKS, MSK Kafka, S3, RDS Postgres, KMS/Secrets Manager), GitHub Actions CI with OIDC federation (zero long-lived keys), Helm/Helmfile deploys. Identity: **Keycloak on EKS** (user directive 2026-08-23; superseded Cognito).
2. **Real Snowflake account** — Terraform-managed objects, dbt transformations, native dynamic masking.
3. **Full platform, MVP-gated** — MVP (D§88) is the release gate at end of Phase 10.
4. **Minimal Collector UI included** — React+TS workbench (case queue, case detail/timeline, record contact + promise-to-pay, decision explanation).

Because there is no real legacy bank, a **source-system simulator** (fake core-banking RDS for CDC, D§21-format SFTP file drops, payment webhooks, "legacy report" truth extracts) is a mandatory enabler — ingestion, reconciliation, parity, and E2E flows cannot be built or verified without it.

---

## 2. Global technology decisions

Resource prefix `colx`, env `dev`, region `eu-west-1` (variable), tags `project=colx, env=dev, stack=<stack>, managed-by=terraform`.

| Area | Decision | Rationale |
|---|---|---|
| Services | Go 1.24 (pinned toolchain), stdlib `net/http` 1.22+ mux, **oapi-codegen v2 strict-server** (contract mismatch = compile error), no framework | Cheapest mechanical verification for sub-agents |
| Workspace | `go.work`; one module per service; shared module `platform/`; **`contracts/` as a Go module exporting `embed.FS`** | Compile-time access to schemas everywhere, no file copying |
| DB access | sqlc + pgx/v5, goose migrations (embedded, `server migrate` subcommand as Helm pre-upgrade hook) | Explicit SQL is reviewable; generated code drift caught by `make generate && git diff --exit-code` |
| Events | JSON on Kafka; envelope exactly A§24; JSON Schema 2020-12 (`santhosh-tekuri/jsonschema/v6`); topics `collections.<context>` per A§25; partition key per aggregate (A§26); **no schema registry** — contracts in repo + CI + runtime validation | A§95 governance without extra infra |
| Reliability | **Transactional outbox** (advisory-lock leader relay, ordered per key) for all publication; **inbox dedupe** (`processed_events` keyed `(consumer, eventId)`) for all consumption; Postgres-backed `Idempotency-Key` middleware per A§21; per-consumer DLQ `collections.dlq.<service>` (A§27) | Effective exactly-once business effect over at-least-once delivery (D§3.5) |
| Kafka client | franz-go (pure Go, MSK IAM auth) wrapped by `platform/kafka` — services never import kgo | Broker swap stays possible (D§58) |
| IDs | ULID (`oklog/ulid/v2`), TEXT columns; prefixes `FIL_ JOB_ REC_ COR_` for ops entities | Sortable, matches doc examples (`01J…`) |
| Money | **int64 minor units + ISO-4217 currency** in all APIs/events/service DBs. Ingestion canonicalizer converts source NUMERIC strings → minor units. Decision **context documents** carry decimal strings in major units (business rules read "500", not "50000") converted by the context builder; units documented per field in the field catalogue. Analytics uses NUMBER(18,2) | One convention chain, no float money anywhere |
| Kafka infra | **MSK Provisioned** 2× kafka.t3.small, TLS + IAM auth, auto-create off, declarative `deployment/kafka/topics.yaml` applied by idempotent Job (~$80/mo vs ~$550 Serverless) | Cost + fits <300 partitions |
| CDC | **Debezium on EKS** (Kafka Connect distributed, 1 replica) — not MSK Connect (~$88/mo, 10–20 min config cycles, opaque logs); **Aiven S3 sink** (Apache-2.0) for Kafka→S3 | Faster iteration, real logs in Loki; MSK Connect documented as prod alternative |
| SFTP | **Containerized `atmoz/sftp` on EKS** (ClusterIP, host key + user keys via Secrets Manager/ESO) — not Transfer Family ($216/mo AND it writes straight to S3, bypassing the A§31 connect/verify-host-key/download/checksum flow we must build) | The SFTP connector must be genuinely exercised |
| Airflow | Self-hosted on EKS, official chart, Airflow 2.11, **KubernetesExecutor**, git-sync DAGs from `airflow/dags`, metadata on platform RDS, remote logs to S3 — not MWAA ($90–350/mo, unstoppable) | Cost + full control |
| Postgres | 2× RDS Postgres 16: `colx-dev-platform` (db.t4g.small; DBs `ingestion`, `airflow`, + database-per-service with per-DB roles) and `colx-dev-corebank` (db.t4g.micro, `rds.logical_replication=1` — simulator/CDC source) | Aurora Sv2 ACU floor + permanent replication slot defeats scale-to-zero; RDS stop is a real teardown lever |
| Identity | **Keycloak on EKS** (official quay.io image via pinned community chart or plain manifests — NOT Bitnami, subscription-gated since 2025), backed by a `keycloak` DB on the platform RDS; realm `colx` as code (import JSON); **client scopes ARE the logical colon-form names** (`cases:read` → authn pass-through); groups (`strategy-author`, `business-approver`, `risk-approver`, `admin`, `collector`, `ops-admin`, `analyst`) via membership mapper → plain `groups` claim; M2M client-credentials clients `platform-services`, `simulator` (secrets via ESO, never in git/realm JSON); SPA PKCE client added in Phase 12; workloads→AWS via **IRSA** (unchanged). Supersedes Cognito (user directive 2026-08-23; portability per D§3.2) | Vendor-neutral OIDC, zero external identity dependency, ≈$0 incremental |
| Ingress | **None until Phase 12** — `kubectl port-forward` make targets (incl. `make keycloak`); per-service JWT middleware built from day 1; flag-gated ALB ingress module ready (needs a domain; Phase 12 also exposes Keycloak for browser login redirects) | Zero exposure is the best dev security posture; saves ~$25/mo |
| Observability | kube-prometheus-stack + Grafana, Loki + Tempo (single-binary, S3 backend), Grafana Alloy logs, OTel Collector traces, Alertmanager → SNS email; correlation/causation IDs propagate HTTP↔Kafka↔Airflow↔Snowflake `QUERY_TAG` (A§97) | Fully declarative → teardown/rebuild free |
| Snowflake | **Enterprise** (30-day trial first; idle ≈ $0 with XS warehouses auto-suspend 60s); resource monitor 50 credits/mo suspend at 100%; native `MASKING POLICY` (A§69); RBAC `COLX_LOADER/TRANSFORMER/REPORTER/PII_READER`; key-pair auth for all service users; storage integration to S3 | Native masking is mechanically verifiable |
| S3→Snowflake | **Airflow-triggered `COPY INTO`** (`FORCE=FALSE` dedup + `COPY_HISTORY` audit tied to file registry) — not Snowpipe | Loads must be explicit and reconciled (A§36–37) |
| Terraform | ≥1.11 via mise, S3-native state locking (no DynamoDB), **5 independent stacks** (00-bootstrap human-applied once; 10-network, 20-data, 30-eks, 40-snowflake CI-applied), tflint + trivy in CI | Blast radius + partial teardown |
| Deploys | Helmfile (pinned charts, `diff` on PR, `apply` on main, env-gated) | Declarative and diffable |
| Testing | Table-driven unit tests; testcontainers-go (postgres:16 + redpanda); contract tests via kin-openapi validator; coverage floors **domain ≥90% / module ≥80%**; `exhaustive` lint for state-machine switches | High test pressure = cheap-model safety net |
| OpenAPI tooling | vacuum (lint) + oasdiff (breaking-change gate) — Go-installable, no Node in the Go toolchain | Fewer moving parts |
| UI | React 18 + TS strict + Vite, TanStack Query, `oidc-client-ts` (PKCE), **API client types generated from OpenAPI** (drift = compile error), MSW tests, Playwright smoke; S3+CloudFront hosting | Everything generateable/checkable |
| Scheduled work | `server tick <task> --as-of=<date>` subcommands run by k8s CronJobs; `--as-of` makes time logic deterministic in tests | One code path, no clock mocking in prod |

**Cost model:** everything running ≈ **$530–565/mo** (EKS $273, MSK $80, RDS $50, NAT $40, KMS/Secrets $12, misc $15, Snowflake $60–90 active; Keycloak runs on existing nodes ≈ $0 — the Cognito ~$12/mo row is gone). Teardown levers (make targets): `stop` ≈ $230/mo, `destroy-heavy` ≈ $60/mo (rebuild ~60 min, all declarative), full destroy <$5/mo. AWS Budget $450/mo with 50/80/100% + forecast alerts; Snowflake resource monitor hard-caps 50 credits/mo.

---

## 3. Repository layout (target, per A§91 — merged across tracks)

```
collection-platform/
  CLAUDE.md                      # delegation conventions (OPS-1)
  Makefile  makefiles/service.mk  go.work  mise.toml  .golangci.yml
  .github/workflows/             # service-ci (reusable) + svc-<name>.yml, contracts-ci, platform-ci,
                                 # terraform, images, helmfile, airflow-dags, dbt, ui, ui-deploy, e2e, security
  contracts/                     # Go module exporting embed.FS (A§95) — FROZEN after Phase 1 (tag contracts-v1.0)
    openapi/                     # 14 domain specs + ingestion-control-plane, strategy, decision, treatment, model
    schemas/envelope/EventEnvelope.v1.json
    schemas/events/<context>/<Event>.v1.json          # 22 catalogue events + extensions (see CON-2)
    schemas/ingestion/{CustomerSnapshot,AccountSnapshot,DebtSnapshot,PaymentNotification}.v1.json
    schemas/decisioning/{strategy-document,rule-set,context-document,context-field-catalogue,
                         population-line,decision-outcome-line,guardrail-config}.v1.json
    files/{loan_accounts,payments,delinquency_snapshot,legacy_daily_summary}.v1.yaml + SPEC.md
    cdc/corebank.v1.yaml         # expected source tables/columns/PKs (drift monitor spec)
    registries/reason-codes.v1.json
    asyncapi/collections.v1.yaml # topic → key → schema index
    examples/  testdata/allocation-golden-vectors.json
  platform/                      # shared Go module: events outbox inbox idempotency kafka postgres otelkit
                                 # httpkit apierror authn config health ids clock ruledsl allocation modelclient testkit
  services/{customer,account,debt,delinquency,case,strategy,decision,treatment,
            arrangement,payment,contact,recovery,agency,legal}/       # A§92 shape each
  services/{model-stub,mock-comms-provider}/                          # deterministic test doubles (real deployables)
  ingestion/                     # control-plane + sftp-worker + webhook-receiver + canonicalizer + recon + dlq
  simulator/corebank/            # seeder, drift tick, filedrop, webhooksim, legacyreport (MUST NOT import ingestion/)
  data/dbt/collections/          # staging/intermediate/marts/snapshots/parity + seeds
  data/snowflake/                # RAW DDL, stages, file formats (idempotent SQL)
  airflow/dags/  airflow/tests/
  infrastructure/terraform/{modules,stacks/{00-bootstrap,10-network,20-data,30-eks,40-snowflake},envs/dev}
  deployment/{helmfile.yaml,values/,charts/collections-service,charts/<svc>,kafka/,observability/,images/}
  ui/collector-workbench/
  e2e/                           # compose.yaml, mockidp, domain-stub wiring, harness, scenarios
  tools/{contractcheck,layoutcheck,domain-stub,ci,dlq-replay(later)}
  scripts/{verify/<WP-ID>.sh,db,dr,cost,ci}
  docs/{service-playbook.md,wp-template.md,ownership.yaml,review-policy.md,conventions.md,
        adr/,gates/,runbooks/,cost-model.md,production-readiness.md}
  security/{security-checklist.md,masking-matrix.md,threat-notes.md}
```

---

## 4. Delegation & verification protocol (how sub-agents execute this plan)

This protocol is packaged into the repo by **OPS-1** and governs every WP.

1. **Contracts first, then fan out.** Phase 1 freezes all interfaces (git tag `contracts-v1.0`); all codegen pins the tag. Released contract files are immutable — any change is a new `vN` file, enforced by CI. No parallel fan-out before the freeze.
2. **Exemplar-first.** `services/case` (EXE-1) is built with maximum scrutiny (strongest model + adversarial review), then `docs/service-playbook.md` (EXE-2) makes every other service WP "clone the exemplar + these deltas". `ui/collector-workbench` scaffold (UI-1) is the UI exemplar. `tools/layoutcheck` mechanically asserts the shape (dirs, targets, forbidden imports: domain must not import pgx/kgo/adapters).
3. **WP brief template** (`docs/wp-template.md`): Context (design § refs, contract files, exemplar path) → Consumes/Provides (frozen interfaces) → Deliverable paths (exhaustive; nothing else may change) → numbered testable requirements → acceptance commands → out-of-scope list. Sizes: S ≈ 1 agent session, M ≈ 2–3, **L must be decomposed into ≤4 sub-briefs by the lead agent before delegation**.
4. **Path ownership.** `docs/ownership.yaml` maps WP → allowed path globs; a CI job fails PRs touching unowned paths. Parallel WPs never share a directory; `platform/*` and `contracts/*` changes are serialized dedicated WPs.
5. **Per-WP definition of done:** `make -C <dir> lint test` green; `make generate && git diff --exit-code` clean; `make contract-check` green; coverage ≥90% domain / ≥80% module; `make verify WP=<id>` green (every WP ships `scripts/verify/<WP-ID>.sh`, exit 0 = pass); runbook stub updated if service behavior changed.
6. **Review policy** (`docs/review-policy.md`): every WP gets a code-review agent pass (checklist: acceptance met, contract adherence, idempotency, error contract A§20, tests assert behavior not implementation). **Adversarial verification** — a second agent writes independent tests from the brief only, without reading the implementation, and attempts to violate invariants — is mandatory for: outbox relay (LIB-7), payment allocation (SVC-6), arrangement schedules (SVC-5), reconciliation engine (ING-8), rule DSL (DEC-5), decision-audit immutability (DEC-9), batch control totals (DEC-10), champion/challenger allocation (DEC-11), treatment guardrails (DEC-16), PTP command in UI (UI-4).
7. **Phase gates** run by a verifier agent that did not implement the WPs: each gate is `docs/gates/gate-<phase>.md` where every line is a runnable command (E2E scenario, `oasdiff breaking` empty, dashboards return data, runbooks lint, duplicate-delivery/replay tests pass). Evidence committed to `docs/gates/evidence/`. No phase starts on a red gate.
8. **Model assignment:** implementation WPs → cheaper model (Opus-class). Exemplar EXE-1, adversarial reviews, gate verification, and L-WP decomposition → strongest available model.
9. **Infra safety:** Terraform applies **only via CI** (env-gated, human-approved); `colx-gha-plan` role is read-only; agents never run `terraform apply` locally (stack 00-bootstrap is the single documented human-applied exception).

---

## 5. Phase map

| # | Phase | WPs | Depends on | Can overlap with | Exit gate |
|---|-------|-----|-----------|------------------|-----------|
| 0 | Foundations & delegation machinery | FND-0, OPS-1 | — | — | Repo scaffold + conventions + CI skeleton green |
| 1 | Contracts v1 (freeze) | CON-1,2,3,4,6,7 · DEC-1 · ING-3 | 0 | 2 | `contracts-v1.0` tagged; contract CI green |
| 2 | Cloud foundation (Wave 1, A§74) | FND-1..13 | 0 | 1, 3 | Infra live, observability live, teardown cycle proven |
| 3 | Platform libraries + local dev stack | LIB-1..9, E2E-0 | 1 | 2 | `platform/` ≥85% cov; compose stack boots; smoke E2E green |
| 4 | Source-system simulator | SIM-1..6 | 2 (partial: FND-3/4/7/8/12) | 3, 5-code | 3 consecutive simulated days green |
| 5 | Ingestion platform (Wave 2, A§75) | ING-1,2,4,5,6,7,8,9,10,11 | 1,2,4 | 6-code, 7 | Recon passes 3 days; CDC lag p95<60s; faults quarantined; canonical topics flowing |
| 6 | Analytical platform (Wave 3, A§76) | ANA-1..9 | 5 (data exists), FND-11 applied | 7, 8 | dbt build+tests green; parity ≤0.01 over ≥5 dates; masking verified |
| 7 | Exemplar + domain wave A (Wave 4) | EXE-1,2 · SVC-1..4 · E2E-1 | 3; full E2E needs 5 | 5, 6, 8-code | E2E-1: ingestion→delinquency→case green |
| 8 | Decisioning core (Wave 5) | DEC-2..9 · E2E-2 | 3; context via domain-stub, real via 7 | 7, 9 | E2E-2: decision→audit→events→explanation green; audit UPDATE provably rejected |
| 9 | Domain wave B (Wave 6a) | SVC-5..8 · E2E-3 | 7 | 8, 10-code | E2E-3: promise→pay→allocate→recover green |
| 10 | Batch, simulation, C/C, treatment execution | DEC-10..17 · ANA-10,11,12 · E2E-4 | 6,8,9 | 11-code | **MVP GATE (D§88)**: A§106 steps 1–23 E2E green end-to-end |
| 11 | Agency & legal (Waves 8–9) | SVC-9,10 · E2E-5 | 9 | 10, 12 | E2E-5: escalation branches + mutual-exclusion invariant green |
| 12 | Collector UI | INF-14 · UI-1..7 | UI-1..5 need only 1+8 (domain-stub); deploy needs 2 | 11, 13 | Playwright smoke green on CloudFront ×3 runs |
| 13 | ML & optimization (Wave 10) | ML-1..4 | 6, 10 | 12, 14 | Batch scoring in daily DAG; C/C report mart live |
| 14 | Hardening & production readiness | DEC-18,19 · XCT-1..4 · OPS-7 | all | interleaves from mid-5 | D§90 checklist fully evidenced; perf thresholds met; DR drills executed |
```
Streams after Phase 1:  INFRA 2→4→5→6→(13)     DOMAIN 3→7→9→11     DECISION 8→10     UI 12 (UI-1..5 from Phase 8)
Peak parallelism: 5–6 agents (contracts batch, service waves). MVP = end of Phase 10.
```

---

## 6. Cross-track integration decisions (binding)

These resolve conflicts between the three planning tracks; WPs below already reflect them.

1. **Single `contracts/` Go module** owns everything (layout in §3). The decisioning track's specs (`strategy`, `decision`, `treatment`, `model` OpenAPI + `schemas/decisioning/*`) are authored by DEC-1 **inside** this module. The reconciliation API (A§19) lives in `ingestion-control-plane.v1.yaml` (ING-2) — no separate spec.
2. **Canonical ingestion events bridge CDC → services** (A§30). Raw Debezium topics (`cdc.corebank.public.cb_*`) and the raw webhook topic are internal to ingestion. A **canonicalizer** (ING-11) emits `ingestion.{customers,accounts,debts,payments}.v1` topics carrying the CON-6 snapshot schemas in the A§24 envelope, keyed by aggregate. Domain services consume only canonical topics. Payments arrive via both webhook (intraday) and CDC/file (batch) — payment-service's natural-key dedup (`UNIQUE(source_system, external_payment_ref)`) absorbs the overlap by design (D§47).
3. **Simulator schema deltas** to serve domain needs: `cb_account` gains `oldest_unpaid_dt int` (nullable, YYYYMMDD — DPD input for delinquency); new `cb_debt(debt_id, acct_no, principal, interest, fees, penalties)` table, included in CDC + canonicalizer (feeds DebtSnapshot).
4. **Contact events have one producer.** treatment-service **calls contact-service** (`POST /v1/contacts` on dispatch, `POST /v1/contacts/{id}/outcome` on provider webhook — both with Idempotency-Key); contact-service alone emits `ContactAttempted`/`ContactCompleted` (A§7.2 ownership). The UI records manual contacts through the same API.
5. **Event catalogue extensions** beyond the 22 in A§23 (justified by the A§7.2 ownership matrix): `TreatmentExecuted.v1`, `TreatmentSuppressed.v1`, `StrategyStateChanged.v1`, `StrategyRetired.v1`, `RuleSetPublished.v1`, `GuardrailConfigPublished.v1`, plus ingestion-internal `FileStatusChanged.v1` and the 4 canonical snapshot events. All defined in CON-2/DEC-1 before the freeze.
6. **UI-driven contract additions** land before the freeze: `GET /v1/cases` gains `assignedCollector` filter + `sort`; `arrangement.v1.yaml` gains `GET /v1/arrangements?accountId=`. (`POST /v1/contacts`, `POST /v1/promises`, case listing already exist in CON-3/4.)
7. **Buckets standardized:** `colx-dev-{landing,raw,quarantine,archive,ops,decision-audit,batch}` (+ UI bucket in Phase 12). Decision snapshots → `s3://colx-dev-decision-audit/...`; batch populations/outcomes → `s3://colx-dev-batch/...` (added to FND-3).
8. **Money convention chain** as in §2 (minor units in services; major-unit decimal strings only inside decision context documents; NUMBER(18,2) in analytics).
9. **One local dev stack**: `e2e/compose.yaml` (postgres, redpanda, localstack S3, mockidp, domain-stub, later mock-comms-provider + model-stub + services by profile). The decisioning track builds against `tools/domain-stub` fixtures until real services exist; E2E scenarios then swap to real services.
10. **Snowflake timing:** FND-11 Terraform is written in Phase 2 but **applied at Phase 6 kickoff** to avoid burning the 30-day Enterprise trial early (idle cost ≈ $0 after conversion anyway).
11. **`documentation/` vs `docs/`:** standardize on `docs/` (conscious simplification of A§91).
12. **Verification harness:** every WP ships `scripts/verify/<WP-ID>.sh`; root `make verify WP=<id>` runs it against the compose stack (or dev EKS where stated).

---

## 7. Work packages by phase

Format: **ID — title — size** · deps · parallel-with. All service WPs implicitly include the exemplar-clone requirements + standard acceptance block:
```
make -C services/<name> generate && git diff --exit-code
make -C services/<name> lint test coverage test-integration contract-test image
go run ./tools/layoutcheck services/<name> && helm lint deploy/charts/<name>
```

### Phase 0 — Foundations & delegation machinery

**FND-0 — Monorepo skeleton + toolchain + CI skeleton — M** · deps: none
- Paths: `go.work`, root `Makefile` (`bootstrap, lint, build-all, verify WP=, contracts-check, tf-plan STACK=, compose-up/down, e2e-*`), `makefiles/service.mk` (canonical targets `generate build lint test test-integration coverage contract-test image`), `.golangci.yml` (incl. `exhaustive` for state-machine switches), `mise.toml` (terraform≥1.11, go 1.24, python 3.12, helmfile, kubectl, snowflake-cli), `tools/go.mod` (pinned oapi-codegen, sqlc, goose, vacuum, oasdiff, go-test-coverage — invoked via `go run`), `.github/workflows/{service-ci.yml (reusable),platform-ci.yml}`, `deploy/charts/collections-service` library chart (Deployment/Service/SA+IRSA/HPA/CronJob/migrate-hook), stub `contracts/` + `platform/` modules, dir skeleton per §3, ADR coverage of the §2 table (delivered as the numbered per-decision set `docs/adr/0001..0016` — see DOC-1).
- Requirements: services use `replace` directives to `../../platform` + `../../contracts`; distroless Dockerfile template; `.gitignore` covers tfstate/secrets.
- Accept: `mise install && make bootstrap lint build-all` green; `go work sync && git diff --exit-code`; `helm lint deploy/charts/collections-service`; trivial PR runs CI green.

**OPS-1 — Conventions & delegation pack — M** · deps: FND-0 · parallel: CON-1
- Paths: `CLAUDE.md` (repo map, path-ownership pointer, build/test commands, Go standards, OpenAPI-first + never-edit-generated, event/outbox/inbox/idempotency rules, error contract, what-NOT-to-touch, Conventional Commits with WP id), `docs/{wp-template.md,ownership.yaml,review-policy.md,conventions.md,gates/}`, `tools/{check-ownership.sh,lint-runbook.sh}`, `scripts/verify/` harness + example, root `make verify WP=`.
- Accept: `make verify WP=OPS-1` self-hosts; `tools/check-ownership.sh` fails a synthetic out-of-scope diff and passes an in-scope one; ownership CI job wired.

### Phase 1 — Contracts v1 (freeze)

Conventions (CON-1, enforced by CON-7): every schema `additionalProperties:false` + explicit `required`; `$id=https://contracts.collections.internal/<path>`; money `{amountMinor:int64, currency}`; RFC3339 UTC; pagination `limit`+opaque `cursor`; `Idempotency-Key` on all POST commands (A§21); A§20 error on every operation; `If-Match` row-version where PATCH exists. **Released files immutable — changes = new vN file.**

**CON-1 — Conventions + envelope + common components + module — S** · deps: FND-0 · parallel: OPS-1
- Paths: `contracts/{README.md (conventions + URL path-ownership matrix),go.mod,embed.go}`, `schemas/envelope/EventEnvelope.v1.json` (exactly the 10 A§24 fields), `openapi/common.v1.yaml` (Error/Detail per A§20, shared headers/params).
- Accept: `go build ./contracts/...`; envelope example validates.

**CON-2 — 22 catalogue event schemas + extensions + golden examples + topic map — M** · deps: CON-1 · parallel: CON-3..6, DEC-1
- One schema + validating example per event. Normative topic/key map (consolidating A§23+§25): `CustomerUpdated→collections.customer/customerId`, `AccountUpdated→collections.account/accountId`, `DebtUpdated→collections.debt/debtId`, `DelinquencyChanged→collections.delinquency/accountId`, `CaseCreated|CaseAssigned|CaseResolved→collections.case/caseId`, `StrategyActivated(+StateChanged,Retired)→collections.strategy/strategyId`, `DecisionMade→collections.decision/decisionId`, `TreatmentSelected→collections.treatment/caseId` (+`TreatmentExecuted`,`TreatmentSuppressed`), `ContactAttempted|ContactCompleted→collections.contact/contactId`, `PromiseCreated|PromiseBroken/promiseId` + `ArrangementCreated|ArrangementBroken/arrangementId→collections.arrangement`, `PaymentReceived|PaymentAllocated→collections.payment/paymentId`, `RecoveryRecorded→collections.recovery/recoveryId`, `DebtPlaced|DebtRecalled→collections.agency/placementId`, `LegalStatusChanged→collections.legal/legalCaseId`.
- Load-bearing payloads: `DelinquencyChanged` carries `accountId, customerId, dpd, previousBucket, newBucket, status(DELINQUENT|CURED|REDEFAULTED), overdueAmountMinor, currency` (drives case creation/resolution). `ArrangementCreated` embeds `schedule[]{installmentId,dueDate,amountMinor}`. `PaymentAllocated` carries `accountId` + `allocations[]{target:ARRANGEMENT_INSTALLMENT|DEBT, arrangementId?, installmentId?, debtId?, amountMinor}`.
- Accept: `make contracts-check` — every schema compiles (2020-12), every example validates against schema **and** envelope; every A§23 event has schema+example+asyncapi entry (contractcheck).

**CON-3 — OpenAPI: wave-A services — M** · deps: CON-1 · parallel: CON-2/4/6, DEC-1
- `openapi/{customer,account,debt,delinquency,case}.v1.yaml`. Path ownership: data-owning service owns the path (`GET /v1/customers/{id}/accounts` lives in account spec; `/v1/accounts/{id}/delinquency` in delinquency spec — A§13/14 catalogue preserved, A§7.3 ownership kept). `case.v1.yaml` = the 9 A§15 endpoints verbatim + `GET /v1/cases` (filters accountId, customerId, status, assignedTeam, **assignedCollector**; **sort**; pagination) + `GET /v1/customers/{id}/cases`; PATCH takes If-Match. `delinquency.v1.yaml` includes `GET|PUT /v1/delinquency/bucket-configs` (versioned, D§5 "configurable").
- Accept: `make contracts-check` (vacuum + oasdiff vs empty baseline + $ref resolution).

**CON-4 — OpenAPI: wave-B/C services — M** · deps: CON-1 · parallel: CON-2/3/6, DEC-1
- `openapi/{arrangement,payment,contact,recovery,agency,legal}.v1.yaml`. `arrangement` = A§17 verbatim + promises (`POST /v1/promises`, `GET /v1/promises/{id}`, `POST /v1/promises/{id}/cancel`) + **`GET /v1/arrangements?accountId=`**. `payment` = intake `POST /v1/payments` (Idempotency-Key + natural key), reads, `POST /v1/payments/{id}/allocations` (ops override, scope `payments:admin`). `contact` = `POST /v1/contacts`, `POST /v1/contacts/{id}/outcome`, reads by customer/case. `recovery` = record + reads + `GET /v1/recovery-metrics` (D§11 metrics). `agency` = A§18 verbatim + agencies CRUD + fees. `legal` = referrals, cases, status-changes, `GET /v1/cases/{caseId}/legal`.
- Accept: same as CON-3.

**DEC-1 — Decisioning contracts + registries + golden vectors — L (decompose: openapi / schemas / registries+vectors)** · deps: CON-1 · parallel: CON-2..6
- Paths: `openapi/{strategy,decision,treatment,model}.v1.yaml`; `schemas/decisioning/*` (7 schemas); `registries/reason-codes.v1.json`; `testdata/allocation-golden-vectors.json` (≥50 rows `{accountId,salt,armsBps,expectedArm,expectedBucket}`); event schemas for strategy/decision/treatment extensions; examples for every schema.
- **strategy-document.v1.json** (A§53 expanded): `{schemaVersion, id ^[A-Z][A-Z0-9_]{2,63}$, name, effectiveFrom/To, priority (strategy selection: highest wins, tie→lexicographic id), eligibility:<condition-group>, segments:[{name,when}] ordered first-match (last = DEFAULT with empty all), ruleSetRef:{ruleSetId,version}, modelRefs:[{modelId,version,required,timeoutMs,onError:DEFAULT|FAIL,defaultScore}], treatments:[{treatmentCode,type,channel,priority,params}], experiment:{enabled,salt,arms:[{name,version|null(=own),percentBps}] sum=10000}}`. Policy sets are global, never pinned per strategy (A§54).
- **rule-set.v1.json** (A§55): `{ruleSetId, kind:RULES|POLICY, version, effectiveFrom/To, rules:[{ruleId,priority,when:<condition-group>,then:{outcome:SELECT_TREATMENT|SUPPRESS|FORBID_CHANNEL, treatmentCode?, channel?, reasonCode(required)}}]}`; condition groups `{"all":[…]}|{"any":[…]}` nest ≤5; leaf `{field,op∈{EQ,NE,GT,GTE,LT,LTE,BETWEEN,IN,NOT_IN,IS_NULL,NOT_NULL},value}`; `field` must exist in **context-field-catalogue.v1.json** (path+type+unit); `FORBID_CHANNEL` only in POLICY kind. Deliberately not Turing-complete — deterministic, auditable, parity-testable (A§88).
- **context-document.v1.json** (A§52): `subject{customerId,accountId,caseId?}`, `delinquency{dpd,bucket,amountOverdue(decimal-string major units),currency}`, `account{productCode,currentBalance,status}`, `customer{contactability,preferredChannel,doNotContact,vulnerabilityFlag,segment}`, `arrangement{hasActiveArrangement,hasOpenPromise,brokenPromises90d}`, `contactHistory{contacts7d,contacts30d,byChannel7d,lastContactAt}`, `provenance[]{path,source,asOf}`.
- Also: `population-line.v1` (`{subject, context, legacyDecision?}`), `decision-outcome-line.v1`, `guardrail-config.v1` (contact windows per channel, frequency caps), `model.v1.yaml` (`POST /v1/models/{id}/versions/{v}:score` body=context doc, response `{modelId,version,score 0..1 decimal-string,band,reasonCodes[]}`), `decision.v1.yaml` = A§16 five endpoints + `GET /v1/decisions?caseId=&accountId=` + batch/shadow/simulation endpoints (shapes per DEC-10/12/13) + `GET /v1/reference/reason-codes`; `strategy.v1.yaml` = versioned CRUD + governance transitions + rule-set/guardrail resources; `treatment.v1.yaml` = `POST /v1/treatments`, reads by case, provider webhook path.
- Accept: `make contracts-check`; reason-code cross-reference (every reasonCode in any example exists in registry); **tag `contracts-v1.0` created at phase end**.

**CON-6 — Ingestion canonical-snapshot contracts — S** · deps: CON-1 · parallel: CON-2..4
- `schemas/ingestion/{CustomerSnapshot,AccountSnapshot,DebtSnapshot,PaymentNotification}.v1.json` + examples; canonical topics documented in asyncapi: `ingestion.{customers,accounts,debts,payments}.v1`. **AccountSnapshot must carry `oldestUnpaidDueDate`, `overdueAmountMinor`, `minimumDueMinor`, `currentBalanceMinor`, `lastPaymentAt`** (delinquency DPD inputs). `PaymentNotification` carries `{sourceSystem, externalPaymentRef, accountId, amountMinor, currency, paidAt, channel}`.
- Accept: `make contracts-check`; joint sign-off recorded by ingestion + domain leads (one review comment each).

**ING-3 — SFTP feed contracts + shared validation library — M** · deps: CON-1 · parallel: CON-2..6
- Paths: `contracts/files/{loan_accounts,payments,delinquency_snapshot,legacy_daily_summary}.v1.yaml` + `SPEC.md` (meta-schema); `ingestion/internal/feedspec/` (pure validation lib).
- Contract YAML per D§80/81: `feed_id, version, source_id, filename_regex (named group business_date), encoding, header:{fields:[feed_code,business_date,record_count]}, trailer:{fields:[record_count,control_total], control_total_column}, columns:[{name,type(string|integer|decimal|date_yyyymmdd|enum),required,pattern,min,max,scale,enum}], business_rules:[{id,expr(cel-go),severity}], sla:{expected_by,late_by,timezone:UTC}, reconciliation:{count,amount:{column,vs:trailer_control_total}}`.
- feedspec API: `Load, ValidateHeader/Trailer/Row, ControlTotal` — pure functions, golden-file tests (valid + every fault class per feed).
- Accept: `go test ./ingestion/internal/feedspec/... -run TestGolden` green (≥6 cases/feed); CI compat check fails a PR adding a required column without a major-version bump.

**CON-7 — Contracts CI — S** · deps: CON-1..6, DEC-1 · parallel: LIB WPs
- Paths: `.github/workflows/contracts-ci.yml`, `tools/contractcheck/` (compile all schemas; validate all examples vs schema+envelope; asyncapi refs resolve; every catalogue event covered; reason-code cross-ref), vacuum ruleset (operationId, error responses, Idempotency-Key on POSTs required), `scripts/ci/check-contract-immutability.sh` (modified released file under `contracts/**` fails), `oasdiff breaking` vs main per spec.
- Accept: `make contracts-check` green in CI; negative test: branch mutating a released schema → CI fails (verifier runs once, evidence in gates dir).

### Phase 2 — Cloud foundation (runs parallel with Phases 1 & 3 after FND-1)

**FND-1 — Terraform bootstrap + GitHub OIDC + budgets — M** · deps: FND-0
- `stacks/00-bootstrap` (ONLY human-applied stack; README documents apply-then-migrate-state flow): state bucket `colx-tfstate-<acct>` (versioned, SSE-KMS, TLS-only, `use_lockfile` S3-native locking); GH OIDC provider + roles `colx-gha-{plan(read-only),apply(env-gated),ecr-push,eks-deploy}` trust-scoped to the repo+environment; SNS `colx-dev-alerts` + email sub; AWS Budget $450 (50/80/100% actual + forecast → SNS); Cost Anomaly monitor.
- Accept: `terraform plan -detailed-exitcode` → 0; budget listed via CLI; workflow-dispatch job assumes plan role and `aws sts get-caller-identity` succeeds; `gh secret list | grep -c AWS_SECRET` == 0.

**FND-2 — Network stack — S** · deps: FND-1 · parallel: FND-3/6
- `stacks/10-network` wrapping `terraform-aws-modules/vpc` (pinned): 2 AZ, public/private/data subnets (data = no NAT route), **single NAT**, S3 gateway endpoint (ingestion S3 traffic bypasses NAT), EKS subnet tags, no flow logs (prod deltas in README).
- Accept: outputs show 2 private subnets; exactly 1 NAT gateway available.

**FND-3 — KMS + S3 buckets + ECR + secrets baseline — M** · deps: FND-1 · parallel: FND-2
- CMKs `colx-dev-{data,db,msk,secrets}`. Buckets (block-public, SSE-KMS, TLS-only): `colx-dev-{landing,raw(versioned),quarantine,archive(versioned, 90d dev lifecycle),ops(airflow-logs/loki/tempo/dbt-artifacts prefixes),decision-audit,batch}`. ECR repos `colx/{ingestion,simulator,dbt,connect,airflow,services}` (scan-on-push, keep-10). Secrets Manager placeholders (values out-of-band, never in TF): sftp host/user keys, webhook HMAC, snowflake key-pairs, RDS creds (prefer RDS-managed masters).
- Accept: encryption + public-access-block asserted on all buckets (`scripts/verify/FND-3.sh`); ECR repos ≥6.

**FND-4 — RDS ×2 + database-per-service provisioning — M** · deps: FND-2,3 · parallel: FND-5/6/7
- `colx-dev-platform` (db.t4g.small, Postgres 16) + `colx-dev-corebank` (db.t4g.micro, `rds.logical_replication=1`, `max_replication_slots=5`); data subnets, EKS-SG-only ingress, CMK, 7-day backups, single-AZ dev. `scripts/db/provision_databases.sh`: DBs `ingestion`, `airflow` + per-service DBs with per-DB owner roles (passwords → Secrets Manager); corebank gets `debezium` (REPLICATION) + `simulator` users.
- Accept: both `available`; `psql $INGESTION_URL -c 'select 1'`; `show rds.logical_replication` → on.

**FND-5 — MSK + declarative topics — M** · deps: FND-2,3 (job needs FND-7/8) · parallel: FND-4/6/7
- MSK Provisioned per §2. `deployment/kafka/topics.yaml` (grows per phase; topic budget documented — no casual partition increases): `ingestion.file.lifecycle.v1` (3p), `ingestion.webhook.payment.v1` (3p), `ingestion.{customers,accounts,debts,payments}.v1` (3p), `cdc.corebank.public.cb_{customer,account,debt,payment,delq}` (3p), `cdc.corebank.schema-history` (1p compact), `connect.{offsets,configs,status}`, `collections.<context>` ×14 (3p), `collections.dlq.<service>`, `dlq.ingestion.v1`. Idempotent topic-apply Job using the connect toolbox image (Debezium base + aws-msk-iam-auth + Aiven S3 sink + JMX agent).
- Accept: cluster ACTIVE; topics listed via IAM-auth `kafka-topics.sh` from an in-cluster pod.

**FND-6 — Keycloak identity (SUPERSEDES the original Cognito WP; user directive 2026-08-23) — M** · deps: FND-4 (RDS), FND-8 (helmfile/ESO) — realm import happens at deploy time
- Keycloak on EKS: official `quay.io/keycloak/keycloak` image via a pinned community chart (codecentric `keycloakx`) or plain manifests in the helmfile (NOT Bitnami — subscription-gated since 2025; implementer verifies availability and documents the pick); `KC_DB=postgres` against a `keycloak` database on `colx-dev-platform` RDS (added to `scripts/db/provision_databases.sh`); admin credentials via ESO (`colx/dev/keycloak/admin`); ~750m/1Gi resources; ServiceMonitor metrics; NO ingress (dev access via `make keycloak` port-forward).
- Realm-as-code: `deployment/values/keycloak/realm-colx.json` imported at startup (`--import-realm`): realm `colx`; **client scopes named exactly the logical colon-form scopes** (`cases:read`, `cases:write`, `cases:admin`, `delinquency:read`, `delinquency:admin`, `payments:read`, `payments:write`, `payments:admin`, `recovery:read`, `recovery:write`, `agency:read`, `agency:admin`, `decisions:read`, `decisions:write`, `strategy:author`, `treatments:read`, `treatments:write`, `ingestion:read`, `ingestion:write`, `webhook:write`, `customers:read`, `accounts:read`, `debts:read`) so `platform/authn`'s pass-through path applies with zero mapping; groups `strategy-author, business-approver, risk-approver, admin, collector, ops-admin, analyst` + group-membership protocol mapper emitting a plain `groups` claim; M2M client-credentials clients `platform-services` (all service scopes) + `simulator` (`webhook:write`) with service accounts. **Client secrets never in git/realm JSON** — set post-start via a kcadm.sh Job reading ESO-synced secrets (or a verified placeholder-substitution mechanism if the implementer proves Keycloak supports it), then mirrored to Secrets Manager for workload consumption. SPA PKCE client deferred to Phase 12.
- The SCOPE-FORMAT RULING simplifies: logical colon-form scopes appear verbatim in Keycloak tokens' `scope` claim; `platform/authn` keeps the Cognito prefix/dot normalization as dormant compatibility code.
- Accept (deploy-time): client-credentials token minted via curl against the port-forwarded Keycloak contains `cases:read`-style scopes and the `groups` claim; realm re-import is idempotent.

**FND-7 — EKS + IRSA + addons — L (decompose: cluster / access+addons / IRSA map)** · deps: FND-2,3 · parallel: FND-4/5/6
- Wrap `terraform-aws-modules/eks` (pinned): v1.32, `authentication_mode=API`, access entries (you = admin, `colx-gha-eks-deploy` = admin dev), endpoint public **restricted to `var.admin_cidrs`** + private; 1 managed node group 3× t3.large (min2/max4); addons vpc-cni, coredns, kube-proxy, EBS CSI (IRSA). Map-driven IRSA roles: external-secrets, ingestion-cp, sftp-worker, webhook-receiver, kafka-connect, airflow, simulator, loki, tempo, alertmanager, alb-controller (unattached, flag-gated); **extended later for decision-service (decision-audit + batch buckets) and treatment-service** (values-only change, noted in DEC-6/DEC-14).
- Accept: ≥2 nodes Ready; public access CIDR == yours; ≥6 annotated SAs after FND-8.

**FND-8 — Cluster baseline: Helmfile + namespaces + ESO — M** · deps: FND-7
- `deployment/helmfile.yaml` (exact chart pins), namespaces `platform, airflow, ingestion, kafka, simulator, sftp, services`, external-secrets + `ClusterSecretStore` (IRSA); **convention: every k8s secret is an ExternalSecret referencing `colx/dev/*` — zero secrets in git/values (D§66)**; metrics-server; flag-gated aws-load-balancer-controller release (default off).
- Accept: `helmfile -e dev diff` clean post-apply; ClusterSecretStore Ready; 7 namespaces.

**FND-9 — Observability stack — L (decompose: kps+grafana / loki+tempo+alloy+otel / alerts+SNS)** · deps: FND-8 · parallel: FND-10/11
- kube-prometheus-stack (Grafana admin via ESO, dashboard sidecar, Prometheus 7d/10GB, Alertmanager → **SNS email receiver**), Loki + Tempo single-binary on `colx-dev-ops`, Alloy daemonset, OTel Collector (OTLP→Tempo), MSK open-monitoring scrape, base alert rules (node NotReady, PV>80%, CrashLoop, deadman).
- Accept: Grafana API health ok; `up{job=~".*msk.*"}` ≥2 series; `amtool alert add test` → email received.

**FND-10 — Airflow on EKS — L (decompose: image+chart / connections+ESO / smoke DAG+CI)** · deps: FND-8, FND-4 · parallel: FND-9/11
- Chart pinned; image `colx/airflow` = apache/airflow:2.11-python3.12 + providers (cncf-kubernetes, snowflake, amazon, statsd); KubernetesExecutor; pgbouncer; git-sync (`airflow/dags`, 60s); remote logs `s3://colx-dev-ops/airflow-logs`; statsd→Prometheus ServiceMonitor; connections/variables via ExternalSecrets (`AIRFLOW_CONN_*`); `airflow/dags/lib/{defaults.py,cp_client.py}`; DAG-integrity pytest in CI (imports all DAGs, deprecation warnings = errors); `platform_smoke` DAG (KPO busybox + S3 write).
- Accept: scheduler+webserver Running; smoke DAG triggered → success; `airflow-dags.yml` CI green.

**FND-11 — Snowflake account Terraform — L (decompose: rbac+warehouses / integration+aws-role / masking+monitors)** · deps: FND-3 · **written now, applied at Phase 6 kickoff** (trial timing)
- `stacks/40-snowflake` (provider `snowflakedb/snowflake` pinned; bootstrap README: trial Enterprise account, ACCOUNTADMIN creates key-pair `TF_SVC`): warehouses `WH_{INGEST,TRANSFORM,ANALYTICS}` (XS, auto-suspend 60, start suspended); DBs `RAW{CDC_COREBANK,FILES_COREBANK,WEBHOOKS,EVENTS,LEGACY_REPORTS}`, `ANALYTICS{STAGING,INTERMEDIATE,MARTS,SNAPSHOTS,GOVERNANCE}`, `ANALYTICS_CI`; roles `COLX_ADMIN←SYSADMIN`, `COLX_LOADER`, `COLX_TRANSFORMER`, `COLX_REPORTER`, `COLX_PII_READER` (granted to nobody) with future grants; service users `AIRFLOW_SVC, DBT_SVC, DBT_CI_SVC` (key-pair, privates in Secrets Manager); resource monitor 50 credits/mo suspend@100%; storage integration `S3_RAW_INT` → same-apply AWS IAM role (read `colx-dev-raw`); masking policies `GOVERNANCE.MASK_STRING_PII` (`IS_ROLE_IN_SESSION('COLX_PII_READER')`), `MASK_DATE_PII`.
- Accept: `snow sql "select 1"` as each service user; `DESC INTEGRATION` shows role ARN; resource monitor quota 50.

**FND-12 — Infra CI/CD — M** · deps: FND-1..8 · parallel: FND-9..11
- `.github/workflows/{terraform.yml (per-stack paths-filter: fmt/validate/tflint/trivy-config/plan-as-PR-comment via plan role; env-gated apply on main),images.yml (build+push colx/* on path change, tag sha+latest, trivy image HIGH+ fails),helmfile.yml (diff on PR, gated apply)}`, CODEOWNERS, GH environment `dev` (required reviewer: you).
- Accept: PR touching one stack triggers only that stack's plan; merge triggers gated apply; zero long-lived AWS keys.

**FND-13 — Cost controls + teardown automation — M** · deps: FND-2..11 · parallel: FND-12
- Make targets `stop / start / destroy-heavy / up-all / destroy-all / grafana / airflow / pf-cp`; `scripts/cost/{stop.sh,start.sh}`; `docs/runbooks/cost-and-teardown.md`; `docs/cost-model.md` (estimates → actuals).
- Accept: one full `stop → start` cycle executed with post-start verify green (nodes Ready, RDS available, smoke DAG success); budget alert test-fired and evidenced.

### Phase 3 — Platform libraries + local dev stack

Common acceptance for every LIB WP: `make -C platform lint test coverage` (module ≥85%) + WP specifics. `platform/*` changes are serialized (never two agents in the module at once).

**LIB-1 — events + ids + clock — S** · deps: CON-1,2
- `platform/events`: `Envelope` (A§24), `New(...)`, `Registry` loaded from `contracts.FS`, `Validate(env)` (envelope + payload schema by type+version). `platform/ids`: `NewULID()`. `platform/clock`: `Clock` iface + `System()/Fixed(t)`.
- Accept: every `contracts/examples/events/*.json` passes `Registry.Validate`; invalid payload/unknown type fail.

**LIB-2 — apierror + httpkit + health + config — M** · deps: FND-0 · parallel: LIB-1/3..6
- `apierror`: A§20 contract exactly (`Write` maps kinds→status, injects correlationId, never leaks internals). `httpkit`: server with timeouts/graceful shutdown, middleware chain (CorrelationID, Recover→500-with-contract, AccessLog). `health`: `/healthz` + `/readyz` with checks. `config`: `Load[T]()` env-only, aggregates all missing vars.
- Accept: golden JSON for error body; panic → 500 contract body with correlationId.

**LIB-3 — otelkit — S** · deps: FND-0
- `Init(ctx, ServiceInfo)`; `Logger(ctx)` slog with trace_id/correlation_id; HTTP middleware; `KafkaHeaders(ctx)/ContextFromHeaders` (traceparent + correlation/causation per A§24/§97).
- Accept: in-memory exporter proves HTTP→ctx→Kafka-header→ctx round-trip preserves IDs.

**LIB-4 — authn — S** · deps: LIB-2
- go-oidc/v3 vs the OIDC issuer — Keycloak in dev (JWKS cached); `Principal{sub,scopes,groups}`; `RequireScope(...)` deny-by-default; `authtest.NewIssuer(t)` (RSA + JWKS httptest + `Token(scopes…)`).
- Accept: table tests — valid→200, missing scope→403 (contract body), expired/bad-issuer→401.

**LIB-5 — postgres — S** · deps: FND-0
- `Connect` (otel, pool caps), `WithTx`, `Migrate` (goose, embedded FS, advisory lock), `ReadyCheck`.
- Accept: testcontainers migrate up/down/up idempotent; rollback-on-error verified.

**LIB-6 — kafka — M** · deps: LIB-1,3
- `Publisher` (franz-go, acks=all, idempotent producer, MSK IAM config from env); `Consumer` (group=service, per-partition sequential, envelope decode+validate, retry w/ backoff → `collections.dlq.<service>` with original topic/error headers, commit after handle), `ReadyCheck`.
- Accept: testcontainers(redpanda): ordered per key; poison → DLQ with headers, consumption continues; malformed envelope → DLQ; otel headers propagate.

**LIB-7 — outbox — M · ADVERSARIAL REVIEW** · deps: LIB-1,5,6 · parallel: LIB-8
- Canonical DDL (`ddl/outbox.sql`, copied verbatim into service migrations): `(id BIGSERIAL PK, event_id TEXT UNIQUE, topic, key, envelope JSONB, created_at, published_at NULL)`. `Enqueue(ctx, tx, reg, env, topic, key)` validates **before** enqueue. `Relay`: **Postgres advisory-lock leader**, ordered batch (`ORDER BY id`), mark published after broker ack; metrics `outbox_lag`, `oldest_unpublished_age`.
- Accept: rolled-back tx → nothing on broker; commit → exactly one record; **two relays, kill leader mid-batch → all events published, per-key order preserved** (dedupe by event_id proven in LIB-8); invalid payload → Enqueue error.

**LIB-8 — inbox + idempotency — M** · deps: LIB-5,6,7 · parallel: LIB-7
- `inbox`: `ddl/processed_events.sql` `(consumer, event_id, processed_at, PK(consumer,event_id))`; `Dedupe(ctx, tx, consumer, eventID)` in the same tx as side effects. `idempotency`: `ddl/idempotency_keys.sql` (key PK, endpoint, request_hash, status_code, response_body, expires_at); middleware per A§21 — same key+hash → replay stored response; same key+different hash → 422; concurrent in-flight → 409; janitor for expiry.
- Accept: same envelope delivered twice through a LIB-6 consumer → side effect once; HTTP replay semantics all three cases proven.

**LIB-9 — ruledsl + allocation + modelclient + testkit — L (decompose: ruledsl / allocation+modelclient / testkit)** · deps: LIB-1..8, DEC-1 · **ruledsl + allocation ADVERSARIAL REVIEW**
- `platform/ruledsl`: pure evaluator over context documents (typed comparisons via `shopspring/decimal`; missing field ⇒ leaf false + trace `FIELD_MISSING`, never error; BETWEEN inclusive; first-match-wins by (priority desc, ruleId asc); returns `(outcome, matchedRuleId, reasonCodes, trace)`); golden-file format `{context,ruleSet,expected}` so adversarial agents add vectors without code; fuzz harness.
- `platform/allocation`: `Allocate(accountId, salt, arms) (arm, bucket)` = first 8 bytes SHA-256(`accountId:salt`) big-endian uint64 mod 10000 over cumulative bps.
- `platform/modelclient`: interface + HTTP client for `model.v1.yaml` with timeout + typed `ErrModelTimeout`.
- `platform/testkit`: `pgtest.StartPostgres(t, migrationsFS)`, `kafkatest.StartRedpanda/Consume/Publish`, `authtest`, `apitest.NewContractValidator` (kin-openapi request+response validation), `eventtest` golden builders.
- Accept: ruledsl ≥30 golden vectors (every operator, nesting, tie-break, missing-field) + `go test -fuzz=FuzzEvaluate -fuzztime=30s` clean; allocation passes `testdata/allocation-golden-vectors.json` + 100k-id distribution within ±1% of 90/10; testkit example test boots pg+redpanda and round-trips an event.

**E2E-0 — E2E harness + compose stack + domain-stub — M** · deps: LIB-9, CON-2/6
- `e2e/` module: `compose.yaml` (postgres:16 w/ per-service DB init, redpanda, **localstack S3**, `mockidp` (JWKS+token endpoint reusing authtest), `tools/domain-stub` (fixture-backed domain GETs incl. account A123/dpd 35 per D§8 + `POST /v1/cases/{id}/activities` recorder + fixture-mutation endpoint), profiles for services as they land); `e2e/harness/` (API clients w/ minted tokens, `kafkatap` collector, `PublishIngestion(feed, payload)`, polling asserts); `e2e/scenarios/smoke_test.go`.
- Accept: `make e2e-smoke` green locally + in `e2e.yml` CI (publish DelinquencyChanged fixture → consume it back; stub endpoints all serve fixtures; localstack bucket writable).

### Phase 4 — Source-system simulator (SIM must NOT import platform/ or ingestion/ code — file format re-implemented from YAML contracts, else validation is tautological)

**SIM-1 — Corebank schema + deterministic seeder — M** · deps: FND-4,8,12
- Legacy-shaped Postgres (deliberately quirky per A§45): `cb_customer(cust_no varchar(10) PK, full_name, dob, segment_cd char(1), phone, email, created_dt int)`; `cb_account(acct_no varchar(12) PK, cust_no, prod_cd, open_dt int YYYYMMDD, status_cd char(2), curr_bal numeric(18,2), od_amt, min_due, last_pay_dt int, oldest_unpaid_dt int NULL)`; `cb_debt(debt_id bigserial PK, acct_no, principal, interest, fees, penalties numeric(18,2))`; `cb_payment(pay_id bigserial PK, acct_no, pay_dt int, amount, channel_cd, reversed_flag)`; `cb_delq(delq_id bigserial PK, acct_no, as_of_dt int, dpd int, bucket_cd, od_amt)`. Seeder `--customers 30000 --accounts 50000 --seed 42` deterministic; ~8% initially delinquent across D§5 buckets. K8s Job.
- Accept: counts exact; re-seed on fresh DB yields identical `md5(string_agg(...))` fingerprint.

**SIM-2 — Daily drift engine — L (decompose: transitions/payments / config+determinism / idempotent upsert)** · deps: SIM-1
- `cmd/tick --business-date=…` (catch-up capable): config-driven roll-rate matrix, payment propensity by bucket, cure probability, new accounts, data-quirk injection rate; updates balances/od_amt/oldest_unpaid_dt, inserts payments, upserts `cb_delq` snapshot. Deterministic per (seed, date). CronJob 01:00 UTC.
- Accept: tick produces delq+payment rows for the date; same date re-run idempotent (counts unchanged).

**SIM-3 — SFTP server — S** · deps: FND-8,3 · parallel: SIM-1/2
- `atmoz/sftp` Deployment, ClusterIP `sftp.sftp.svc:22`, PVC, user `corebank` chrooted to `outbound/{loan_accounts,payments,delinquency_snapshot}/`, stable host key from Secrets Manager (host-key verification per A§31 testable).
- Accept: `sftp -o StrictHostKeyChecking=yes` with pinned known_hosts works; changed host key → same command fails (then revert).

**SIM-4 — File-drop generator (D§21 + fault injection) — L (decompose: writer / sftp upload / fault matrix)** · deps: SIM-2,3
- `cmd/filedrop`: writes exactly `HEADER,<FEED>,<YYYYMMDD>,<count>` / `DATA,…` / `TRAILER,<count>,<control_total>` per feed contract (control-total columns: loan_accounts→sum(curr_bal), payments→sum(amount), delinquency_snapshot→sum(od_amt)); UTF-8 RFC4180; `.tmp` upload + atomic rename; faults `--inject bad-control-total|bad-record-count|malformed-row|duplicate-file|late`. CronJob 02:00 UTC.
- Accept: `scripts/verify/SIM-4.sh <date>` awk-verifies count+control totals per file; each `--inject` mode produces the expected violation (expected-fail branches).

**SIM-5 — Payment webhook simulator — S** · deps: SIM-2, FND-6 · parallel: SIM-4/6
- `cmd/webhooksim`: intraday trickle of the day's `cb_payment` rows (same payments that later appear in files — enables cross-recon) → `POST /v1/webhooks/payments` with HMAC `X-Signature` + deterministic `X-Event-Id` (pay_id-derived ULID → replays are true duplicates) + an OIDC `simulator` client-credentials JWT (Keycloak); `--replay <event-id>`; retries/backoff. CronJob 15-min business hours.
- Accept: against an echo pod: 5 requests, one signature verified with openssl in the script.

**SIM-6 — Legacy report extractor (parity truth) — M** · deps: SIM-2 · parallel: SIM-4/5
- `cmd/legacyreport` (CronJob 02:30): "legacy MI" SQL directly on corebank → `daily_collections_summary` (business_date, bucket_cd, account_count, total_overdue, total_balance) + `daily_payments_summary` → `s3://colx-dev-raw/legacy_reports/<report>/business_date=<date>/*.csv`.
- Accept: file lands; report total_overdue == `sum(od_amt)` in corebank (2dp script check).

**Phase 4 exit:** `make sim-day DATE=…` (tick→filedrop→legacyreport) ×3 consecutive dates, all verify scripts green.

### Phase 5 — Ingestion platform (Wave 2 — A§28–37)

**ING-1 — Control-plane DB schema + skeleton — M** · deps: FND-4, CON-7
- `ingestion/` module (golang-migrate + sqlc). DDL (final in migration; **states exactly A§36**, registry superset of D§20): `source` (source_id, source_type CDC|SFTP|API|EVENT, connection_secret_arn, config JSONB); `feed` (feed_id, source_id, contract_ref, schema_version, filename_regex, expected_by, late_by); `file_registry` (file_id `FIL_ULID`, feed/source refs, file_name, business_date, received_at, size, checksum_sha256, status ENUM(DISCOVERED RECEIVED VALIDATING VALIDATED PROCESSING PROCESSED RECONCILING RECONCILED FAILED QUARANTINED ARCHIVED DUPLICATE), row_count_{declared,parsed,rejected,loaded}, control_total_{declared,computed}, s3_{landing_key,raw_prefix,quarantine_prefix,archive_key}, error_code/detail, correlation_id, **UNIQUE(source_id, checksum_sha256)**, **UNIQUE(feed_id, file_name)**); `file_state_transition` (append-only audit — every transition, actor, reason; D§3.6); `ingestion_checkpoint` (source_id, key, value JSONB — A§35 shapes); `quality_rule`; `quarantine_row`; `webhook_event` (event_id PK, payload_sha256, status ACCEPTED|DUPLICATE|REJECTED|PUBLISHED, kafka partition/offset, correlation_id); `dlq_event`; `ingestion_job`; `reconciliation_run` (status RUNNING|PASS|WARNING|FAIL) + `reconciliation_check` (subject, check_type COUNT|AMOUNT|BALANCE, expected, actual, diff, tolerance, status). Transition map table-driven; illegal → error.
- Accept: migrate idempotent; transition-map unit tests incl. illegal transitions; `sqlc diff` clean.

**ING-2 — Control-plane API service — L (decompose: API+auth / events+metrics / bootstrap-seeding)** · deps: ING-1, FND-5, FND-6
- `contracts/openapi/ingestion-control-plane.v1.yaml` implementing D§79 + reconciliation A§19: sources/feeds CRUD, `GET /v1/ingestion/files?…`, `POST /v1/ingestion/files/{id}/reprocess|quarantine`, checkpoints GET/PUT, jobs, `POST /v1/reconciliation/runs`, `GET …/runs/{id}(/checks)`, exceptions resolve. A§20 errors; OIDC JWT (Keycloak) scopes read/write; publishes `FileStatusChanged` to `ingestion.file.lifecycle.v1`; Prometheus metrics contract (`colx_ingestion_files_total{feed,status}`, `colx_ingestion_file_lateness_seconds`, `colx_ingestion_quarantine_rows_total`, `colx_recon_checks_total{status}`); `bootstrap` subcommand seeds sources/feeds idempotently from `contracts/files/*.yaml`.
- Accept: `scripts/verify/ING-2.sh` — M2M token flow; bootstrap idempotent; feeds ≥3; 401/403 paths; metrics endpoint non-empty; vacuum lint green.

**ING-4 — SFTP/CSV pipeline worker — L (decompose: connect/download/register / validate/quarantine / canonicalize/archive) · ADVERSARIAL-adjacent (recon depends on it)** · deps: ING-1,2,3, SIM-3,4
- Full A§31/32 flow, 60s poll + on-demand scan job; worker holds **no direct DB access** — all state via CP API (keeps the API honest):
  1. Connect (pinned host key, fail closed) → list feeds' dirs, match regex, skip `.tmp`.
  2. Register DISCOVERED→RECEIVED: stream to landing `s3://colx-dev-landing/sftp/{source}/{feed}/received_date=…/{name}` computing SHA-256 en route.
  3. Dedup (A§33): checksum hit → DUPLICATE (terminal, audited); name reuse w/ different checksum → QUARANTINED `FILENAME_REUSED`.
  4. VALIDATING via feedspec in D§21 order (header→schema→rows→business rules→trailer→control total). Any ERROR row → **whole file QUARANTINED** (control totals are meaningless otherwise; documented policy): original + `rejects.jsonl` → quarantine bucket + `quarantine_row`. WARN recorded, continue.
  5. VALIDATED→PROCESSING: canonical raw `s3://colx-dev-raw/files/{source}/{feed}/business_date=…/{file_id}.csv.gz` (RFC4180, canonical snake_case header, ISO dates, plain decimals, appended `_file_id,_row_number,_business_date`) → PROCESSED with counts/totals.
  6. Archive copy + SFTP delete; ARCHIVED only after RECONCILED (flipped by CP when recon passes).
  7. Checkpoint (`lastFile`, checksum per A§35); lifecycle events; crash-safe resume from registry state; reprocess path `QUARANTINED→VALIDATING` exercised.
- Accept: `scripts/verify/ING-4.sh <date>` — clean drop → 3 files PROCESSED <10 min with declared==parsed and control totals equal; `--inject bad-control-total` → QUARANTINED + rejects.jsonl present; `--inject duplicate-file` → DUPLICATE; canonical header verified; transition audit rows complete; reprocess of a corrected quarantined file succeeds.

**ING-5 — CDC pipeline (Debezium + S3 sink + checkpoints) — L (decompose: connect deploy+connectors / checkpointer / drift monitor wiring)** · deps: FND-5, SIM-1, ING-2
- Kafka Connect (colx/connect image) Deployment; Debezium Postgres connector: slot `colx_debezium`, `table.include.list=cb_customer,cb_account,cb_debt,cb_payment,cb_delq`, `snapshot.mode=initial`, `decimal.handling.mode=string`, `topic.prefix=cdc.corebank`, **`heartbeat.interval.ms=10000`** (WAL growth guard), JSON no-schemas; Aiven S3 sink: `cdc.corebank.*` + `ingestion.webhook.payment.v1` → `s3://colx-dev-raw/cdc/corebank/{table}/ingest_date=…/{topic}+{partition}+{offset}.jsonl.gz` (flush 5 min/10k). `cdc-checkpointer` CronJob (5 min): Connect offsets + `pg_replication_slots` lag/retained-WAL → CP checkpoints + gauges `colx_cdc_slot_retained_bytes`, `colx_cdc_lag_seconds` (from Debezium JMX). Schema-change policy (D§47): additive flows via VARIANT; drift monitor compares information_schema vs `contracts/cdc/corebank.v1.yaml` → WARN + runbook.
- Accept: `scripts/verify/ING-5.sh` — connector RUNNING; snapshot count ≥ table count; live UPDATE appears on topic <60s; sink objects exist; checkpoint <5 min old; **kill connect pod mid-tick → no gap after restart** (recon closes in ANA-2).

**ING-6 — Webhook/API ingestion — M** · deps: ING-2, SIM-5, FND-5
- `POST /v1/webhooks/payments`: OIDC JWT (`webhook:write`, Keycloak) **and** HMAC verification (A§34 defense in depth); JSON Schema validation; idempotency via `webhook_event(event_id)` insert — conflict → 200 `{status:"duplicate"}` (D§3.5); publish raw to `ingestion.webhook.payment.v1` keyed `acct_no` (A§26); Kafka down → 503 (simulator retries). Separate Deployment for independent scaling.
- Accept: `scripts/verify/ING-6.sh` — 20 events → 20 PUBLISHED; `--replay` → duplicate, no new row; tampered HMAC → 401 + `rejected` metric; invalid body → 422 A§20 shape.

**ING-7 — DLQ + replay — M** · deps: ING-2, FND-5 · parallel: ING-8
- Consumer retry 3× exp → `dlq.ingestion.v1` with origin headers; DLQ consumer persists `dlq_event`; CP endpoints `GET /v1/ingestion/dlq`, `POST /{id}/replay|discard` (replay = re-produce to origin; safe via idempotent consumers D§49); metric `colx_dlq_depth`; runbook.
- Accept: poison message → visible in API <1 min; replay advances origin topic + status REPLAYED; `DLQDepthNonZero` alert fires.

**ING-8 — Ingestion reconciliation engine — L (decompose: engine+rules / scopes / API wiring) · ADVERSARIAL REVIEW** · deps: ING-4,6 (ING-5 partial)
- Explicit checks, never pipeline-success inference (D§38): scope `files:<feed>` — COUNT identity `declared == rejected + loaded` and AMOUNT `control_total_declared == control_total_computed` (tolerance 0); scope `cdc:corebank` — source-vs-target counts marked WARNING until ANA-2 posts Snowflake-side counts into the same run (documented, then authoritative); scope `webhooks:payments` — day's webhook_event count vs corebank webhook-channel payment count (cross-source corroboration). Results → run/check rows PASS|WARNING|FAIL; FAIL emits event + alert; file feeds flip PROCESSED→RECONCILING→RECONCILED→ARCHIVED on PASS.
- Accept: `scripts/verify/ING-8.sh <date>` — clean day all diff==0, files ARCHIVED; tampered `row_count_loaded` → FAIL naming feed and numbers.

**ING-9 — Airflow ingestion DAGs — M** · deps: FND-10, ING-2/4/8 · parallel: ING-10/11
- Per A§38–42 (short idempotent tasks, no business state in XCom, no secrets in DAGs): `process_sftp_corebank` (02:05) — trigger_scan → mapped per-feed deferrable sensor until PROCESSED (timeout at `late_by` = late-file alert) → assert_no_quarantine → trigger recon → wait PASS → emit `Dataset("colx://raw/files/corebank")`; `cdc_monitor_corebank` (*/15) — connector RUNNING, `colx_cdc_lag_seconds`<300, retained WAL<500MB, schema fingerprint vs contract (WARN on drift); `ingestion_reconciliation_daily` (04:30) — day-scope run; FAIL → DAG fail → incident alert (A§39).
- Accept: DAG-integrity pytest green; clean day → success; `--inject late` → sensor timeout + alert visible in Alertmanager.

**ING-10 — Ingestion observability + ops dashboard v1 — M** · deps: ING-2..9, FND-9 · parallel: ING-9
- `deployment/observability/dashboards/colx-ops.json` (D§51 INGESTION+PIPELINES rows from real metrics: on-time %, files-by-status, CDC lag, webhook failure %, rejected records, DLQ depth, recon pass/fail, Airflow success; COLLECTIONS/DECISIONING rows as placeholders); alert rules `FileFeedLate, CDCLagHigh, QuarantineNonEmpty, DLQDepthNonZero, ReconFailed, ConnectorDown` → SNS; every alert has `runbook_url`.
- Accept: dashboard panels ≥10 non-empty after a sim day; **each alert fired once via the fault-injection matrix**, evidenced in `docs/runbooks/alert-verification.md`.

**ING-11 — Canonicalizer (CDC/webhook → canonical snapshot events) — L (decompose: cdc mapping / webhook mapping / dedupe+delivery) ** · deps: ING-5,6, CON-6, LIB-1..8
- `ingestion/cmd/canonicalizer`: consumes `cdc.corebank.public.cb_{customer,account,debt}` (Debezium after-state) and `ingestion.webhook.payment.v1` (+ `cb_payment` CDC) → emits A§24-envelope canonical events to `ingestion.{customers,accounts,debts,payments}.v1` keyed by aggregate id, payloads per CON-6 (NUMERIC strings → **minor units int64**; `oldest_unpaid_dt` → `oldestUnpaidDueDate` ISO date); deterministic eventIds derived from (source, PK, LSN/event-id) so replays dedupe downstream; snapshot-mode floods handled (initial snapshot → full emission is correct: services upsert idempotently); inbox-dedupe + outbox not needed (stateless transform, at-least-once acceptable — consumers dedupe); Prometheus lag metrics.
- Accept: `scripts/verify/ING-11.sh` — corebank UPDATE → canonical `AccountSnapshot` on `ingestion.accounts.v1` <90s validating vs schema with correct minor-unit conversion (spot-check math); webhook payment → `PaymentNotification` with same deterministic eventId on replay; kafkatap shows envelope fields per A§24.

**Phase 5 exit (Wave 2 criteria):** recon PASS 3 consecutive sim days; CDC lag p95 <60s during tick; file drop→PROCESSED <10 min; every fault-injection mode produces its quarantine/duplicate/alert; every A§36 state reachable + audited; canonical topics flowing.

### Phase 6 — Analytical platform (Wave 3 — A§43–51; apply FND-11 first)

**ANA-1 — Snowflake RAW DDL + stages + file formats — M** · deps: FND-11 applied, ING-4/5 (objects exist)
- `data/snowflake/raw/*.sql` idempotent: file formats `FF_CSV_CANONICAL`, `FF_JSONL_GZ`; external stages on `S3_RAW_INT` for files/cdc/webhooks/legacy/events prefixes; RAW tables per A§44 fidelity — CDC/webhooks/events: `(record VARIANT, _file_name, _loaded_at, _ingest_date)`; file feeds: all-VARCHAR per contract + `_file_id,_row_number,_business_date,_file_name,_loaded_at`; legacy typed. `make sf-raw-apply`.
- Accept: 2nd apply zero errors; `list @STG_FILES` non-empty (integration works); RAW tables present.

**ANA-2 — Airflow load DAG (COPY) + load audit + CDC recon closure — L (decompose: file COPY+audit / CDC COPY+counts / recon postbacks)** · deps: ANA-1, ING-8, FND-10
- `load_snowflake_raw.py`: Dataset-triggered per feed — `COPY INTO … FORCE=FALSE ON_ERROR=ABORT_STATEMENT` with `QUERY_TAG='{"correlation_id":…}'` → `COPY_HISTORY` rows_loaded → PATCH registry `row_count_loaded` + POST recon check (loaded==parsed) → CP flips RECONCILED; hourly CDC COPY (VARIANT, ingest_date window, FORCE=FALSE dedups) + source-vs-Snowflake latest-state count checks POSTed into the day's recon run (**closes ING-8's CDC WARNING gap — authoritative**). Emits `Dataset("colx://snowflake/raw")`.
- Accept: `scripts/verify/ANA-2.sh <date>` — Snowflake count == registry `row_count_loaded`; re-run loads 0 rows (FORCE=FALSE proof); day's recon includes PASS cdc counts.

**ANA-3 — dbt project + staging layer — L (decompose: scaffold+image+sources / CDC staging+dedup / file+webhook+legacy staging)** · deps: ANA-1/2, FND-11
- `data/dbt/collections`: profiles env-driven (key-pair), `colx/dbt` image; sources RAW with freshness (warn 26h/error 50h); naming exactly A§49 (`stg_<source>_<entity>`; sources `corebank`(CDC), `corefiles`, `webhook`, `legacy`); CDC staging parses `record:payload.after.*` + latest-state `QUALIFY ROW_NUMBER() OVER (PARTITION BY pk ORDER BY lsn DESC)=1` + soft-delete from op='d'; standardization per A§45 (int YYYYMMDD→DATE, char codes→seed decode, trims, NUMBER(18,2), UTC); layer→schema macro; `query_tag` correlation; layer tags for Airflow selectors.
- Accept: `dbt debug && dbt build --select tag:staging` green; `dbt source freshness` green post-load; `STG_COREBANK_ACCOUNT` count == corebank live count (dedup proof vs psql).

**ANA-4 — Intermediate + marts + SCD2 — L (decompose: intermediate / facts+dims+contracts / snapshots+seeds)** · deps: ANA-3
- MVP mart subset of A§46–47 (case/decision/agency marts arrive in ANA-12/ML): `int_account_current, int_account_delinquency (DPD/bucket per D§5 from seed bucket_codes), int_payment_history, int_customer_exposure`; `fct_delinquency_snapshot` (account×business_date), `fct_payment` (union file+webhook, `source_type`, natural-key dedup); `dim_customer` (SCD2 via `snap_customer` — effective_from/to,is_current, surrogate keys), `dim_account, dim_product, dim_date` (seed); **enforced dbt model contracts on marts**; incremental facts on `_business_date`.
- Accept: `dbt build --select tag:intermediate tag:marts snapshots` green; SCD2 proof: change segment in corebank → 2 dim rows correct is_current split; contract violation PR fails CI.

**ANA-5 — dbt tests + docs — M** · deps: ANA-4 · parallel: ANA-6/7
- Per A§50: unique/not_null on all PKs, relationships fct→dim, accepted_values on codes; business singular tests (`payment_amount >= 0`, `overdue <= balance`, `effective_to >= effective_from`, snapshot no-overlap); `dbt docs generate` on main → manifest to `s3://colx-dev-ops/dbt-artifacts/` (doubles as slim-CI state).
- Accept: `dbt test` ≥40 tests green; corrupt-row negative test documented; manifest uploaded.

**ANA-6 — PII masking application + access verification — M** · deps: ANA-4, FND-11 · parallel: ANA-5/7
- dbt post-hooks apply `GOVERNANCE.MASK_*` to `dim_customer.{full_name,phone,email,dob}` (policies TF-owned, application dbt-owned — survives rebuilds); `security/masking-matrix.md`; REPORTER sees `customer_id, segment, dpd, balance` unmasked, PII masked (A§69 example verbatim); RAW: no REPORTER grants at all (D§45).
- Accept: `scripts/verify/ANA-6.sh` — REPORTER select → `***MASKED***`; PII_READER → real; REPORTER on RAW → insufficient privileges.

**ANA-7 — Analytical parity harness — M** · deps: ANA-4, SIM-6, ANA-2 · parallel: ANA-5/6
- `models/parity/{parity_daily_overdue_balance,parity_daily_payments}`: full-outer-join legacy report vs new marts at identical grain; columns `…,legacy_value,new_value,diff,abs_diff`; tests `abs_diff <= 0.01` + count parity (A§76); runbook classifies diffs per A§88 taxonomy.
- Accept: over ≥5 sim days `abs_diff > 0.01` count == 0; skipping one file load for a scratch date makes parity fail (negative proof).

**ANA-8 — dbt slim CI — M** · deps: ANA-5
- PR: fetch prod manifest → `dbt build --select state:modified+ --defer --target ci` into `ANALYTICS_CI.PR_<n>` as `DBT_CI_SVC`; `sqlfluff` gate; schema dropped on PR close; main: full build + docs + manifest upload.
- Accept: PR touching one model builds ≤5 models (run_results assertion); close drops schema.

**ANA-9 — Ops dashboard v2 (D§51 complete for data plane) — S/M** · deps: ANA-2..7, FND-9
- Adds: Airflow success % (24h), dbt tests passed/failed (pushed from run_results), Snowflake credits MTD by warehouse (ACCOUNT_USAGE → pushgateway), parity status tile, AWS cost MTD (Cost Explorer task).
- Accept: all v2 panels non-empty after one daily cycle; credits within 10% of console.

**Phase 6 exit (Wave 3):** dbt build+tests green; parity ≤0.01 across ≥5 dates; masking verified; slim CI operating. Record `docs/adr/0017-analytics-gate.md` (0001–0016 are the design ADR set).

### Phase 7 — Exemplar + domain services wave A (Wave 4)

**EXE-1 — `services/case` exemplar — L (decompose: domain+state machine / API+idempotency / consumers+outbox / ops+chart) · MAX SCRUTINY (strongest model + human-style review + adversarial pass)** · deps: LIB-1..9, CON-2/3, E2E-0
- Purpose: case lifecycle, assignment, activities audit (A§5.5, §10.1, §15; D§6, §76). Directory exactly A§92.
- DDL: `cases(case_id TEXT PK, customer_id, account_id, debt_id NULL, status, stage, priority INT, assigned_team, assigned_collector, strategy_id, strategy_version, opened_at, next_action_at, closed_at, outcome, row_version BIGINT, …)` with **partial unique `UNIQUE(account_id) WHERE status NOT IN ('RESOLVED','CLOSED')`** (one open case per account, A§106 step 7); `case_activities` append-only (activity_id, case_id, seq, activity_type, actor, occurred_at, correlation_id, detail JSONB) — **every command and consumed event appends one** (D§3.6); outbox/inbox/idempotency DDL verbatim from platform.
- State machine: exactly A§10.1 as explicit transition table `map[Status]map[Command]Status` + guards; closed case rejects everything except reopen → 409 `CASE_CLOSED` (invariant A§11.1).
- API: case.v1.yaml strict-server; scopes `cases:read|write|admin`; POST requires Idempotency-Key; PATCH If-Match → 412 on mismatch.
- Consumes (group `case-service`): `DelinquencyChanged` (DELINQUENT + no open case → create + CaseCreated; CURED → resolve outcome CURED + CaseResolved; else update priority/activity); `PaymentAllocated`, `ArrangementBroken`, `PromiseBroken`, `ContactCompleted` → activities (+ ArrangementBroken sets next_action_at=now). Produces via outbox: `CaseCreated, CaseAssigned, CaseResolved`.
- Tests: **exhaustive status×command table test** (fails if any pair lacks an entry); integration (pg+redpanda): POST→outbox→relay→broker envelope validates; duplicate DelinquencyChanged (same eventId) → one case; Idempotency-Key retry → same caseId, one event; If-Match conflict → 412; concurrent create → one winner via partial unique; contract tests via apitest validator.
- Accept: standard block + all the above test names green.

**EXE-2 — Service playbook + layoutcheck — S** · deps: EXE-1
- `docs/service-playbook.md`: the 11-step copy-exact recipe (scaffold → migrations (copy platform DDL verbatim) → sqlc → codegen → domain/state-machine pattern → application → adapters → main.go `serve|migrate|tick` → 4 test categories → Dockerfile/Makefile/chart/workflow → acceptance block). `tools/layoutcheck`: required dirs/files/targets exist; forbidden imports (domain must not import adapters/pgx/kgo) — mechanical.
- Accept: `go run ./tools/layoutcheck services/case` exit 0; one handler re-derived from the doc by reviewer.

**SVC-1 — customer-service — S** · deps: EXE-2, CON-3/6 · parallel: SVC-2/3/4
- Collection-specific profile, contactability, constraints (A§5.1); consumes `ingestion.customers.v1`; produces `CustomerUpdated`; contactability derived (constraints override preferences). Read-only API v1.
- Accept: snapshot twice same eventId → one event; changed snapshot → second event with deltas.

**SVC-2 — account-service — M** · deps: EXE-2, CON-3/6 · parallel: wave
- Account state + `account_history` append-only (A§5.2, §9); consumes `ingestion.accounts.v1` (hash-compare to suppress no-op noise); produces `AccountUpdated` (carries overdueAmountMinor, currentBalanceMinor, minimumDueMinor, lastPaymentAt, oldestUnpaidDueDate — delinquency's inputs).
- Accept: replay idempotent; event schema-valid; history paginates.

**SVC-3 — debt-service — S** · deps: EXE-2 · parallel: wave
- Debt + components (A§5.3); consumes `ingestion.debts.v1`; produces `DebtUpdated`; invariants: components ≥0, single currency, `recoverable = Σ components` (v1).
- Accept: component-sum property test; duplicate snapshot → no event.

**SVC-4 — delinquency-service — M** · deps: EXE-2; contract-only dep on SVC-2 · parallel: wave
- DPD calc, configurable buckets, cure/re-default (A§5.4, D§5). Consumes `AccountUpdated` (account is the single upstream truth, not raw ingestion); BucketConfig versioned rows validated contiguous/non-overlapping; `tick evaluate --as-of=` daily CronJob recomputes DPD (`asOf − oldestUnpaidDueDate`) and emits transitions only (no daily noise); status machine CURRENT→DELINQUENT→CURED→(REDEFAULTED within window→DELINQUENT); produces `DelinquencyChanged`.
- Accept: bucket/cure/re-default table tests across day boundaries; integration: overdue>0 snapshot → DELINQUENT(1-30); `tick --as-of=+35d` → 31-60 transition; overdue=0 → CURED.

**E2E-1 — Wave-A gate scenario — M (verifier agent)** · deps: SVC-1..4, E2E-0; full-chain variant needs Phase 5
- `e2e/scenarios/wave1_test.go`: A§106 steps 1–8 — harness publishes CustomerSnapshot+AccountSnapshot(+DebtSnapshot) overdue → `AccountUpdated`/`DebtUpdated` → `DelinquencyChanged(DELINQUENT,1-30)` → case auto-created + `CaseCreated` → assign → `CaseAssigned` + activity → overdue=0 → CURED → `CaseResolved(CURED)`; **one correlationId across the whole chain** (A§97); duplicate-snapshot replay changes nothing. Full-chain variant swaps harness-published snapshots for real simulator→ingestion→canonicalizer flow on dev EKS.
- Accept: `make e2e-wave1` green in CI; full-chain evidence in gates dir.

### Phase 8 — Decisioning core (Wave 5) — services follow the exemplar; context via domain-stub until wave A deployed

**DEC-2 — strategy-service skeleton + versioned CRUD — M** · deps: EXE-2, DEC-1
- `services/strategy`: DDL `strategy(id PK,…)`, `strategy_version(strategy_id, version, status, definition JSONB, content_hash, effective_from/to, PK(strategy_id,version))` + **partial unique `(strategy_id) WHERE status='ACTIVE'`**; endpoints: create (v1 DRAFT), clone version, `PUT …/definition` (DRAFT/TEST only, schema-validated vs `strategy-document.v1.json`, unknown fields refused), reads, `GET /v1/strategies/active?onDate=`; OIDC role middleware (Keycloak groups per FND-6); outbox to `collections.strategy`.
- Accept: create→edit→read round-trip; invalid doc → 400 A§20 body; edit non-DRAFT → 409; arms bps ≠10000 → 400.

**DEC-3 — governance lifecycle + approvals + activation — L (decompose: state machine+approvals / scheduler+auto-retire / events+audit)** · deps: DEC-2
- Exactly A§60: DRAFT→TEST (refs resolvable) →SIMULATED (**system-only; requires completed simulation for current content_hash** — feature-flagged until DEC-12) →BUSINESS_APPROVED (approver≠author) →RISK_APPROVED→SCHEDULED (effectiveFrom≥today) →ACTIVE (scheduler tick or admin; **atomically retires prior ACTIVE in same tx**) →RETIRED; SIMULATED|BUSINESS_APPROVED→REJECTED. Content hash frozen at TEST→SIMULATED. Tables `strategy_transition` (append-only: from,to,actor_sub,roles,comment,content_hash) + `strategy_approval`. Events `StrategyStateChanged` always; `StrategyActivated`+`StrategyRetired` on activation.
- Accept: scripted walk to ACTIVE with two approver identities; author-as-approver → 403; edit after SIMULATED → 409; activate v2 → v1 RETIRED + both events on kcat with A§24 fields; transition row per step.

**DEC-4 — rule-set + policy-set + guardrail-config resources — M** · deps: DEC-2 · parallel: DEC-3/5
- `rule_set(rule_set_id, version, kind RULES|POLICY, status DRAFT|PUBLISHED|RETIRED, definition JSONB, content_hash, effective_from/to)`; publish = immutable; validation: schema + every field in catalogue + every reasonCode in registry + FORBID_CHANNEL only in POLICY; guardrail-config resource same semantics; `RuleSetPublished`/`GuardrailConfigPublished` events; strategy TEST transition validates ruleSetRef PUBLISHED.
- Accept: publish→mutate → 409; unknown field path → 400 naming it; unknown reason code → 400; events observed.

**DEC-5 — (absorbed into LIB-9 ruledsl — listed for traceability; adversarial vectors added here)** · deps: LIB-9
- Adversarial agent adds ≥10 hostile golden vectors (deep nesting, tie-breaks, unit edge cases, missing fields) from spec only.
- Accept: all pass; any discrepancy blocks phase.

**DEC-6 — decision-service skeleton + config cache + context builder + S3 snapshots — L (decompose: skeleton+cache / context builder / snapshot writer)** · deps: DEC-3/4
- Config cache of ACTIVE strategies + PUBLISHED rule/policy/guardrail sets (initial API load; refresh via `collections.strategy` events + 60s poll fallback; versions pinned alongside data). Context builder (A§52): caller supplies `subject` (+ full context allowed for TEST/SIMULATION/BATCH, role-gated); fetches account/delinquency/customer/arrangement/contact APIs (domain-stub in dev; 300ms timeout, 1 retry, provenance recorded); validates vs `context-document.v1.json`; **money converted minor→major decimal strings here**. Snapshot → `s3://colx-dev-decision-audit/input-snapshots/date=…/{decisionId}.json` (SSE-KMS), `input_content_hash` = SHA-256 canonical JSON. IRSA extension for the bucket (values-only change).
- Accept: fixture A123 context equals committed golden byte-for-byte post-canonicalization; upstream 500 → `CONTEXT_UNAVAILABLE` A§20 error; snapshot in localstack with matching hash.

**DEC-7 — pipeline stages + policy precedence + selection + trace — L (decompose: framework+policy/eligibility/segmentation / rules+models / optimization+selection+trace)** · deps: LIB-9, DEC-6, DEC-8 (iface)
- Fixed-order stages (A§52): Policy → Eligibility (strategy selection: eligibility+priority; none → INELIGIBLE/NO_STRATEGY_MATCH) → Segmentation (first-match) → Rules (ruledsl) → Models (per modelRefs, timeout/onError honored) → Optimization (v1 = constraint-filter + treatment priority ranking; stage interface reserved for real optimizer) → TreatmentSelection. `Constraints` type exposes **only narrowing** (`Suppress`, `ForbidChannel`); **post-selection guard** re-validates final treatment vs policy snapshot — violation ⇒ NO_ACTION + `POLICY_VIOLATION_GUARD` + error metric (A§54/§104 enforced by construction AND runtime). Deterministic reason-code ordering; trace document per stage (A§58).
- Accept: D§8 worked example (dpd 35 → SMS w/ DPD_31_60-class codes); A§54 example (policy forbids, optimization prefers SMS → no SMS); **property test ≥1000 cases: output never violates policy constraints**.

**DEC-8 — model contract client + deterministic stub model server — M** · deps: DEC-1 · parallel: DEC-6/7
- `services/model-stub`: scores = `hash(accountId|modelId|version) mod 1000 / 1000` + fixture-override file; band strings per A§58 style. Real-model path documented: implement same OpenAPI, switch base URL — zero decision-service change (D§42).
- Accept: same request twice → identical; fixture override exact; slow-handler test → typed `ErrModelTimeout`.

**DEC-9 — immutable decision audit + read APIs + events — M · ADVERSARIAL REVIEW** · deps: DEC-6/7
- DDL (D§39 + D§3.6): `decision_audit(decision_id TEXT PK, correlation_id, customer_id, account_id, case_id, decided_at, mode ONLINE|BATCH|TEST|SIMULATION|SHADOW, strategy_id, strategy_version, policy_set_version, rule_set_id, rule_set_version, model_versions JSONB, experiment_arm, allocation_bucket, input_snapshot_ref, input_content_hash, decision TREAT|NO_ACTION|SUPPRESSED|INELIGIBLE, treatment_code, channel, reason_codes TEXT[], trace_ref, batch_id, created_at) PARTITION BY RANGE (decided_at)`; **UPDATE/DELETE blocked by trigger + app role INSERT+SELECT only**. Trace → S3. `POST /v1/decisions` persists audit + outbox `DecisionMade` (+`TreatmentSelected` when TREAT) in one tx; `GET /{id}`, `GET /{id}/explanation` (trace + registry descriptions), `GET ?caseId=&accountId=`, `GET /v1/reference/reason-codes`.
- Accept: `psql UPDATE decision_audit …` exits non-zero with append-only error (DELETE too); explanation matches golden for A123; events schema-valid; duplicate Idempotency-Key returns original decisionId.

**E2E-2 — Online decision gate — M (verifier agent)** · deps: DEC-2..9
- Compose E2E of A§106 steps 9–15: governance walk to ACTIVE → POST /v1/decisions for A123 → audit row + S3 snapshot + both events + explanation renders full stage trace; duplicate-event replay on strategy consumer; `oasdiff breaking contracts-v1.0..HEAD` empty.
- Accept: `./e2e/scenarios/online_decision.sh` exit 0 in CI; evidence committed.

### Phase 9 — Domain services wave B (Wave 6a)

**SVC-5 — arrangement-service — L (decompose: schedule domain / state machines+API / breach tick+consumers) · ADVERSARIAL REVIEW (schedules)** · deps: EXE-2, CON-4 · parallel: SVC-6/7/8
- Promise (A§10.2) + Arrangement (A§10.3) exemplar-pattern state machines with exhaustive tests; `POST /v1/arrangements` generates schedule (Σ installments == total, strictly ascending dates, first ≥ today, remainder-to-last rounding) else 400 `ARRANGEMENT_INVALID` with A§20 details (invariant A§11.5); consumes `PaymentAllocated` (installments PAID via installmentId; all paid → COMPLETED; promise satisfied → KEPT); `tick breach-check --as-of=` → overdue past grace → ArrangementBroken; promise past due → PromiseBroken; produces the 4 arrangement/promise events (ArrangementCreated embeds schedule).
- Accept: schedule property tests (sum/ascending/rounding); both machines exhaustive; create→confirm→PaymentAllocated → KEPT; breach tick → events on wire.

**SVC-6 — payment-service — M · ADVERSARIAL REVIEW (allocation)** · deps: EXE-2, CON-4/6 · parallel: wave
- Intake `POST /v1/payments` (Idempotency-Key) **and** `UNIQUE(source_system, external_payment_ref)` → duplicate returns original 200, never a second event (layered defense, D§47); consumes `ingestion.payments.v1` (inbox-deduped) + `ArrangementCreated/Broken` (read model `active_arrangements`); allocation v1: active-arrangement installments oldest-first, remainder → DEBT; `PaymentReceived` then `PaymentAllocated` via outbox in the same tx; invariant A§11.2 `UNIQUE(payment_id)` on allocation + status guard; manual re-allocation ops endpoint audited.
- Accept: same webhook twice + same external ref different key → exactly one Received + one Allocated; split across two installments with exact minor-unit math.

**SVC-7 — contact-service — S** · deps: EXE-2, CON-4 · parallel: wave
- Contact recording (A§7.2 owner of contact events): `POST /v1/contacts` (Idempotency-Key — treatment executors and UI retry), `POST /v1/contacts/{id}/outcome`; produces `ContactAttempted`, `ContactCompleted`.
- Accept: outcome on unknown contact → 404; both events schema-valid on wire; duplicate POST replays.

**SVC-8 — recovery-service — M** · deps: EXE-2, CON-4 · parallel: wave
- Consumes `PaymentAllocated` → auto Recovery rows (v1 channel DIRECT; AGENCY/LEGAL via read models when Phase 11 lands); `UNIQUE(payment_id)`; produces `RecoveryRecorded`; `GET /v1/recovery-metrics` computes gross/net/rate/cost-to-collect (D§11).
- Accept: duplicate PaymentAllocated → one RecoveryRecorded; metrics golden test over fixtures.

**E2E-3 — Wave-B gate — M (verifier agent)** · deps: SVC-5..8, E2E-1
- Continues wave-1 world (A§106 steps 16–23 analog with decision-track events stubbed where needed): record contact via API → activity appears; promise + arrangement confirm; PaymentNotification → `PaymentReceived → PaymentAllocated → installment KEPT → RecoveryRecorded → case activity`; unpaid second arrangement + `tick breach-check` → `ArrangementBroken`+`PromiseBroken` → case activity + next_action set; duplicate-payment scenario end-to-end.
- Accept: `make e2e-wave2` green in CI.

### Phase 10 — Batch decisioning, simulation, champion/challenger, treatment execution → **MVP GATE**

**DEC-10 — batch decisioning + Airflow task contract — L (decompose: batch API+job store / worker+outcome writer / recon+contract doc) · ADVERSARIAL REVIEW (control totals)** · deps: DEC-9
- Population in: `s3://colx-dev-batch/population/date=<D>/run=<runId>/population.jsonl` + manifest (rowCount, sha256, contextVersion); lines = `population-line.v1`. API: `POST /v1/decisions/batch {populationRef, businessDate, mode}` **idempotent on (businessDate, runId)**; `GET /batch/{id}` status+counts; worker streams JSONL, `(batch_id, account_id)` dedupe, config snapshot pinned at start. Outcomes out: `decisions.jsonl` + `errors.jsonl` + `summary.json` with **control-total identity `populationRows == decided + suppressed + ineligible + errored`** (D§37–38). Events emitted for executed modes. `airflow/dags/contracts/collections_daily_decisioning.md` documents invoke/poll/load tasks.
- Accept: 1k-line fixture → COMPLETED; jq control-total assertions; re-POST same run → same batchId, zero new audit rows; 3 malformed lines land in errors.jsonl and are counted.

**DEC-11 — champion/challenger allocation integration — S · ADVERSARIAL REVIEW** · deps: DEC-7, LIB-9 · parallel: DEC-10/12/13
- Evaluated after strategy selection when ACTIVE version has `experiment.enabled`; challenger version must be content-frozen + risk-approved (validated at champion activation); arm + bucket recorded in audit (D§41).
- Accept: golden vectors pass; same account decided twice → same arm; E2E audit rows carry arm+bucket.

**DEC-12 — simulation runs + report + lifecycle hook — M** · deps: DEC-10
- `POST /v1/decisions/simulations {strategyId, candidateVersion, baselineVersion, populationRef}` → both versions in mode=SIMULATION (no treatment events); report → S3 + API: `{byTreatment{baseline,candidate,delta}, contactVolumeByChannel, suppressions, reasonCodeHistogram, segmentBreakdown}` (D§40, A§59 — distribution comparisons for MVP); completion recorded in strategy-service `simulation_record(strategy_id, version, content_hash, sim_id)` → **enables TEST→SIMULATED (removes DEC-3 flag)**.
- Accept: fixture population + two versions → report matches golden; TEST→SIMULATED without sim → 409, after → 200; definition edit invalidates via hash mismatch.

**DEC-13 — shadow-run comparator + diff categorization — M** · deps: DEC-10 · parallel: DEC-12
- `POST /v1/decisions/shadow-runs {populationRef, strategyA, strategyB|"LEGACY"}` (LEGACY compares `legacyDecision` from population lines — migration parity per A§87–88); diff rows with `category ∈ {UNCATEGORIZED, EXPECTED, DATA_DIFFERENCE, RULE_TRANSLATION_ERROR, MISSING_RULE, LEGACY_BUG, NEW_BUG}` (exactly A§88); PATCH to categorize (role-gated, audited); summary = match rate + counts by category.
- Accept: 100 subjects w/ 12 seeded diffs → matchRate 0.88; categorize shifts summary; invalid category → 400.

**DEC-14 — treatment-service + TreatmentSelected consumer + execution state machine — L (decompose: skeleton+DDL / consumer+dedup / APIs+events)** · deps: DEC-9 events, CON-4
- Consumes `TreatmentSelected` (inbox dedupe + business `UNIQUE(decision_id)`); `treatment` table + machine `REQUESTED→VALIDATED→DISPATCHED→DELIVERED|FAILED` + `→SUPPRESSED`; transitions audited; APIs POST (manual, Idempotency-Key)/reads; emits `TreatmentExecuted`/`TreatmentSuppressed`; CALL_TASK dispatches via Case API activity.
- Accept: fixture TreatmentSelected twice → one row; transition audit complete; CALL_TASK hits case activity endpoint; illegal transition → 409.

**DEC-15 — channel adapter SPI + mock provider + delivery webhooks + contact wiring — M** · deps: DEC-14, SVC-7
- `ChannelAdapter{Channel(); Dispatch(ctx, req)}` for SMS/EMAIL/LETTER/DIGITAL; dispatch idempotent via `provider_ref = treatment_id`; `services/mock-comms-provider` (stores message, posts delivery webhook after configurable delay; destination ending "00" ⇒ FAILED — deterministic); HMAC-signed provider webhook → treatment status; **treatment-service records contacts via contact-service API** (`POST /v1/contacts` on dispatch, outcome on webhook) — contact-service alone emits contact events (integration decision §6.4).
- Accept: decision→treatment→mock dispatch→webhook → DELIVERED + `ContactAttempted`/`ContactCompleted` on `collections.contact` schema-valid; bad HMAC → 401 no state change; "…00" → FAILED with code.

**DEC-16 — execution guardrails — M · ADVERSARIAL REVIEW** · deps: DEC-14, DEC-4
- Before dispatch re-evaluate: (a) fresh customer policy fetch — **fail closed** (`POLICY_RECHECK_UNAVAILABLE` ⇒ SUPPRESSED); (b) contact windows per channel from published guardrail-config (injectable clock); (c) frequency caps vs `contact_attempt` counts; violations ⇒ SUPPRESSED + reasons + event; window-blocked ⇒ `scheduled_for` next open + poller.
- Accept: fake-clock tests (21:30 SMS → 08:00 next day; cap=2 → 3rd SUPPRESSED; doNotContact flip between decision and execution → SUPPRESSED via stub mutation); **property test: no dispatch outside window or above cap, ever**.

**DEC-17 — SES sandbox email adapter — S** · deps: DEC-15
- SES v2 adapter behind same SPI; sandbox verified-identities documented; localstack SES unit tests; dev EKS: one real EMAIL treatment DELIVERED to a verified address (gate evidence).
- Accept: localstack tests green; dev delivery evidenced.

**ANA-10 — population export (Snowflake → population.jsonl) — M** · deps: ANA-4, DEC-1 · parallel: DEC-10
- dbt model `int_collection_eligibility` (A§46) shaping **exactly `context-document.v1` fields**; Airflow task unloads to `s3://colx-dev-batch/population/date=…/run=…/population.jsonl` + manifest (COPY INTO @stage / Snowflake UNLOAD), validates a sample of lines against the JSON Schema in-task; **context-parity test**: for N sample accounts, Snowflake-built context == online context-builder output (guards simulation/online skew — top risk).
- Accept: population for a sim date validates (ajv sample check); parity test green for N=50 accounts; manifest rowCount == line count.

**ANA-11 — collections_daily_decisioning DAG — M** · deps: ANA-10, DEC-10, FND-10
- Per A§40: upstream datasets (ingestion + dbt) → build population (ANA-10 task) → `invoke_decision_batch` (HTTP POST, retry) → `wait` (poll to terminal) → `load_outcomes` (COPY outcomes.jsonl → RAW) → dbt marts refresh → reconciliation task asserts the DEC-10 control-total identity against Snowflake-loaded counts → publish completion.
- Accept: full DAG green on a sim date; deliberately corrupt one outcome line in a scratch run → recon task fails naming the identity.

**ANA-12 — domain-event sink + event marts — L (decompose: sink extension+RAW / staging / marts)** · deps: ING-5 sink, ANA-3/4, Phases 7–10 events flowing
- Extend Aiven S3 sink to `collections.*` topics → `s3://colx-dev-raw/events/<topic>/ingest_date=…/*.jsonl.gz`; RAW.EVENTS VARIANT tables + COPY in load DAG; dbt `stg_events_*` (envelope unpack, latest-per-aggregate where appropriate) → marts per A§47: `fct_collection_case` (from case events), `fct_collection_action`, `fct_arrangement`, `fct_decision` (join outcomes + audit refs), `fct_contact`, `dim_strategy` (from strategy events, SCD2); dashboard COLLECTIONS/DECISIONING rows (D§51) go live from these marts + service metrics.
- Accept: after an E2E-4 run, `fct_decision` row count == decisions made; case fact reflects lifecycle; dashboards non-empty.

**E2E-4 — MVP gate: full loop — L (verifier agent, strongest model)** · deps: everything above
- **A§106 steps 1–23 end-to-end on dev EKS with no harness shortcuts**: simulator tick → CDC + files + webhooks → ingestion (validate/checkpoint/recon) → canonical events → account/debt/delinquency → case → daily decisioning DAG (population from Snowflake → batch decisions → outcomes loaded) → treatment (guardrails) → mock provider → contact events → promise/arrangement → payment webhook → allocation → recovery → events land in Snowflake → dbt marts → dashboard rows populated; batch reconciliation identity holds; `TreatmentSelected` topic-slice replay produces **zero duplicate dispatches** (D§49); correlation ID traceable file→case (A§97 chain sampled).
- Accept: `docs/gates/gate-10.md` all commands green ×2 consecutive days; MVP checklist D§88 items 1–13 each linked to evidence; `docs/adr/0018-mvp-gate.md` recorded.

### Phase 11 — Agency & legal (Waves 8–9)

**SVC-9 — agency-service — L (decompose: entities+placement machine / fees+performance / file exchange)** · deps: E2E-3; read-model dep on SVC-10 events · parallel: SVC-10
- Placement machine `REQUESTED→PLACED→RECALL_REQUESTED→RECALLED` (+`PLACED→CLOSED` on CaseResolved); guards: one active placement per account (partial unique) + **no active legal case** (A§11.6, via LegalStatusChanged read model) → 409 `PLACEMENT_LEGAL_CONFLICT`; consumes `PaymentAllocated` while PLACED → FeeEntry (`amount × commissionBps`, minor-unit math) + performance aggregates; produces `DebtPlaced`/`DebtRecalled`; `tick export-placements` writes D§21-format CSV (header/trailer/control totals) to `s3://colx-dev-raw/agency-out/<agency>/placements_YYYYMMDD.csv` via an `io.Writer` port (S3 adapter smoke-tested on dev); inbound agency-response schema added as DRAFT to contracts.
- Accept: place→pay→fee row exact bps math; recall lifecycle events; placement blocked while legal active; export CSV passes a control-total awk check.

**SVC-10 — legal-service — M** · deps: E2E-3 · parallel: SVC-9
- LegalReferral (REQUESTED→ACCEPTED|REJECTED), LegalCase (OPEN→IN_PROCEEDINGS→JUDGMENT→ENFORCEMENT→CLOSED) with append-only status history; produces `LegalStatusChanged` per transition; consumes `DebtPlaced` (reject referral while placement active — A§11.6 mirror), `PaymentAllocated`.
- Accept: history append-only; referral blocked while placed; every transition emits schema-valid event.

**E2E-5 — full-platform gate — M (verifier agent)** · deps: SVC-9/10, E2E-4
- Extends E2E-4 with escalation: place → `DebtPlaced` → payment while placed → recovery channel AGENCY + fee → recall → `DebtRecalled` → legal referral → `LegalStatusChanged` → placement attempt now 409 (**A§11.6 both directions**) → resolve+close; asserts every produced event type observed schema-valid across the suite (kafkatap coverage report: 22+ produced, decision-plane included).
- Accept: `make e2e-full` green in CI; coverage report committed.

### Phase 12 — Collector UI (UI-1..5 may run in parallel from Phase 8 against domain-stub)

**INF-14 — public exposure for UI + API — M** · deps: FND-7/8; **needs a domain (~$12/yr) — user decision**
- Enable flag-gated ALB ingress (aws-load-balancer-controller) + ACM cert + Route53 zone for the API gateway host (JWT validation stays in services; gateway adds rate limiting + correlation injection per A§64); CloudFront + S3 bucket for the SPA (OAI, SPA-rewrite function); Keycloak SPA client (PKCE) with callback URLs + public Keycloak exposure for browser login redirects; WAF basic rules on ALB.
- Accept: `curl https://api.<domain>/healthz` 200; unauthenticated API call → 401; CloudFront serves `index.html` on deep links.

**UI-1 — scaffold + auth + generated client (UI exemplar) — M** · deps: DEC-1, OPS-1 CI
- Vite + TS strict + react-router + TanStack Query; `oidc-client-ts` PKCE vs Keycloak (env-driven issuer); fetch wrapper injects Bearer + `Idempotency-Key` (ULID) on POSTs, parses A§20 errors into typed `ApiError`; client types generated from OpenAPI (committed, drift-checked); MSW handlers from contract examples.
- Accept: `pnpm lint && typecheck && test && build` green; tests: unauth → redirect; 401 → refresh retry once; error body → typed ApiError.

**UI-2 — case queue — M** · deps: UI-1 · parallel: UI-3
- `GET /v1/cases?assignedCollector=me&status=ACTIVE&sort=nextActionAt` + customer names; sortable columns (nextActionAt, priority, dpd, overdue), filters, pagination, loading/error/empty states.
- Accept: MSW tests — renders 25 fixture cases, sort toggles, API error shows retry banner.

**UI-3 — case detail + unified timeline — L (decompose: layout+data / timeline merge / panels)** · deps: UI-1
- Header (case + account + delinquency); timeline = client-side merge of activities, contacts, promises/arrangements, payments, latest decision — typed union (ACTIVITY|CONTACT|PROMISE|PAYMENT|DECISION), virtualized, per-source failure degrades that lane only.
- Accept: merge unit tests (interleaving, same-timestamp stability, per-source failure); page renders all five item types from fixtures.

**UI-4 — record contact + promise-to-pay — M · ADVERSARIAL REVIEW (PTP command)** · deps: UI-3
- Forms → `POST /v1/contacts` and `POST /v1/promises` with Idempotency-Key; client validation mirrors contract (amount > 0 decimal, dueDate ≥ today, enums); double-submit prevented; server `details[]` → inline field errors; success invalidates timeline queries.
- Accept: MSW asserts payload incl. Idempotency-Key; rapid double-click → exactly one request; 400 details render inline.

**UI-5 — decision explanation view — S** · deps: UI-3, DEC-9
- Stage-by-stage trace (policy→…→treatment) with reason-code chips + registry descriptions, strategy id/version, experiment-arm badge, model scores/bands; read-only.
- Accept: snapshot vs DEC-9 golden explanation; unknown reason code renders verbatim + "unregistered" tooltip, no crash.

**UI-6 — Playwright smoke vs deployed env — M** · deps: UI-2..5, INF-14
- `@smoke`: Keycloak login (test collector) → queue shows seeded case → open detail → record contact → timeline updates → create PTP → explanation renders; headless vs `UI_BASE_URL`, trace artifacts on failure.
- Accept: green in post-deploy CI job.

**UI-7 — UI CI + S3/CloudFront deploy — S** · deps: UI-1, INF-14
- Build w/ env injection → `aws s3 sync --delete` (hashed assets max-age=31536000; index.html no-store) → CloudFront invalidation `/index.html`; deploy from main after UI CI green.
- Accept: workflow deploys; deep-link curl serves SPA; post-deploy smoke green. **Phase gate: smoke ×3 consecutive runs, zero console errors, Lighthouse a11y ≥90 on queue + detail.**

### Phase 13 — ML & optimization (Wave 10 — thin but real)

**ML-1 — feature marts — M** · deps: ANA-4/12
- dbt `features` schema: account/customer behavioral features (payment velocity, contact response, broken-promise rates) with point-in-time correctness notes; documented as the model-training source (A§Feature-data decision).
- Accept: dbt build+tests green; feature docs published.

**ML-2 — payment-propensity model service — M** · deps: ML-1, DEC-8
- Train a simple model (e.g. gradient boosting) offline on simulator history; serve it implementing `model.v1.yaml` exactly (containerized, versioned); decision-service switches base URL from stub — **zero code change** (proves D§42 contract).
- Accept: contract conformance tests (same suite as stub) green; A/B: decisions with real model produce sane score distributions (report committed).

**ML-3 — batch scoring DAG + score marts — M** · deps: ML-2, ANA-11
- `ml_batch_scoring` DAG per A§38: score eligible population nightly → scores to RAW → `fct_account_score` mart consumed by population builder (scores available to rules via context field catalogue addition — **new catalogue vN**, not mutation).
- Accept: DAG green; population lines carry scores; catalogue version bump verified by contract CI.

**ML-4 — champion/challenger analytics + optimization upgrade — M** · deps: DEC-11, ANA-12
- dbt mart `fct_experiment_outcome` (arm × outcome: contact/promise/payment/recovery rates per D§41 compare list); Grafana panel; optimization stage v2: expected-value ranking `p(pay|treatment) × amount − cost` under capacity constraints (greedy; LP documented as future) — behind the existing stage interface, property-tested that policy constraints still dominate (A§54).
- Accept: mart validates against seeded outcomes; optimization v2 property tests green; A§54 precedence property re-run.

### Phase 14 — Hardening & production readiness (interleaves from mid-Phase 5)

**XCT-1 — DR/backup drills (D§73) — M**
- RDS PITR restore drill (restore→verify counts→destroy, <60 min, scripted); rebuild-from-zero drill = `destroy-heavy` + `up-all` + ING verify scripts green; S3 versioning + Snowflake Time Travel + `COPY FORCE=TRUE` rebuild runbook; Kafka = transport not SoR (stated D§47 answer). **Test recovery, not documentation.**
- Accept: both drills executed + logged in `docs/runbooks/dr.md` with timings.

**XCT-2 — security verification (A§61–71) — M**
- `security/security-checklist.md` mapping every A§61–71 control → implementation → evidence command; `security.yml` CI: gitleaks (full history), trivy image/config, govulncheck — blocking; k8s hardening (resource limits, runAsNonRoot, default-deny NetworkPolicy in ingestion/services namespaces with explicit allows); CloudTrail management events + root-usage alert.
- Accept: CI green + blocking; gitleaks clean; cross-namespace curl on non-allowed port fails.

**XCT-3 — cost reporting (D§86) — S**
- `scripts/cost/report.sh <month>`: Cost Explorer by `stack` tag + Snowflake metering → markdown; `docs/cost-model.md` actuals vs estimates.
- Accept: report reconciles ±10% with consoles.

**XCT-4 — runbooks + operational controls (D§82) — M**
- ≥12 runbooks (sftp, cdc, recon, dlq, airflow, snowflake-load, parity, alerts, cost/teardown, dr, decision-batch, treatment-webhooks), each with the 8 D§82 fields; every Prometheus alert's `runbook_url` resolves.
- Accept: runbook lint green; alert-annotation check green.

**DEC-18 — decisioning observability pack — M**
- Metrics per D§50 (decision latency histogram, volume by mode/decision, rule/model errors, batch progress, dispatch/suppression counts, webhook lag, outbox lag, consumer lag); Grafana dashboards per service; alert rules; runbooks.
- Accept: dashboard queries return data post-E2E; `promtool check rules` green.

**DEC-19 — performance verification — M**
- k6: POST /v1/decisions p99 <500ms (with context fetch) / <150ms (supplied context), ≥200 rps/pod ×10 min, error <0.1% (D§72); batch 500k <60 min with reconciliation still exact; MSK partition + audit-partition capacity notes.
- Accept: k6 thresholds pass (exit 0) vs dev; `docs/perf-report.md` committed.

**OPS-7 — production readiness audit (A§90) — M (verifier agent)**
- Walk the full A§90 checklist; every item links to evidence (CI run, gate log, dashboard, runbook, drill log); confirm all adversarial reviews closed.
- Accept: zero unchecked items in `docs/production-readiness.md`; sign-off recorded.

---

## 8. Risks (top 10, consolidated)

1. **Sub-agent drift/collisions** — mitigated by frozen contracts, ownership CI, exemplar+layoutcheck, L-WP mandatory decomposition, verify scripts. Highest-leverage mitigation in the plan.
2. **Terraform state corruption by agents** — applies only via gated CI; plan role read-only; bootstrap is the sole human apply.
3. **Debezium replication-slot WAL growth** (fills the micro RDS when Connect is down/idle) — heartbeat + retained-WAL alert at 500MB + drop/recreate runbook. The most likely real incident.
4. **Simulation/online context skew** invalidating simulations — population lines ARE context-documents validated by the same schema + ANA-10 context-parity test in the MVP gate.
5. **Event payload underspecification** (A§23 names events, not payloads) — CON-2/DEC-1 invent them; domain sign-off before Phase 7; immutability CI prevents silent mutation; payloads kept minimal so v2s are cheap.
6. **Recon tautology** (simulator sharing validation code would make green checks meaningless) — hard isolation rule, reviewer-enforced: `simulator/` never imports `ingestion/` or `platform/`.
7. **Snowflake trial timing / credit burn** — FND-11 applied at Phase 6 kickoff; XS + auto-suspend + 50-credit hard cap.
8. **Redpanda-in-tests vs MSK-IAM-in-prod drift** — same client (franz-go); one dev-env Kafka smoke test per service wave.
9. **Money arithmetic** (allocation splits, fee bps, control totals) — int64 minor units, golden arithmetic tests, adversarial review on SVC-6/SVC-9/ING-8/DEC-10.
10. **Cost creep on always-on infra** (MSK/EKS-CP/NAT can't stop) — budget alarms, `stop`/`destroy-heavy` levers, weekly `make stop` habit documented; destroy MSK during long pauses (holds no SoR data by design).

## 9. Open questions (defaults chosen — flag to change)

1. **Go module org prefix** — placeholder `github.com/canhtoanptit/collection-platform` (matches repo); trivial rename before Phase 3.
2. **Domain purchase for Phase 12** (~$12/yr) — required for HTTPS UI + public API. Until then UI runs locally against port-forwards.
3. **Snowflake edition after trial** — default Enterprise (native masking); drop to Standard + secure views if credits exceed ~40/mo.
4. **Simulator scale** — default 30k customers / 50k accounts; 10× possible (+$13/mo RDS, longer CDC snapshot).
5. **Business-date timezone** — UTC everywhere (simplest verifiable); switch to local-time SLAs only if wanted.
6. **Challenger execution governance** — encoded as "challenger version must be risk-approved before champion activation"; business rule to confirm (D§41 "subject to policy and governance").
7. **PII minimization in events** (`CustomerUpdated` → Snowflake) — v1 events carry minimal PII; masking covers marts; event-payload tokenization deferred with a note in `security/masking-matrix.md`.
8. **Agency inbound file semantics** — outbound + DRAFT inbound schema shipped; real remittance formats need real agency specs.

## 10. Verification (global)

- **Per WP:** `make verify WP=<id>` + standard service acceptance block + coverage floors.
- **Per phase:** verifier-agent gate (`docs/gates/gate-<n>.md`, every line runnable; evidence committed). Key gates: Phase 5 (recon 3 days + fault matrix), Phase 6 (parity ≤0.01 + masking), Phase 7 (E2E-1 chain + one correlationId), Phase 8 (E2E-2 + audit UPDATE rejected), Phase 10 (**MVP: A§106 steps 1–23 with no harness shortcuts, ×2 consecutive days, replay-safety proven**), Phase 12 (smoke ×3 + a11y), Phase 14 (A§90 zero unchecked).
- **Continuous:** contracts CI (immutability + breaking-change), ownership CI, security CI, e2e.yml on main, `oasdiff` empty vs `contracts-v1.0` everywhere.

## 11. Suggested execution order for the first month

1. Phase 0 (FND-0, OPS-1) → Phase 1 contracts batch (5–6 parallel agents) + FND-1 bootstrap (human applies stack 00 once).
2. Phase 2 infra via CI (FND-2..10, 12, 13; FND-11 written not applied) in parallel with Phase 3 libraries (serialize platform/ WPs) + E2E-0.
3. Phase 4 simulator (3 parallel agents) → Phase 5 ingestion (ING-1/2/3 then 4/5/6 parallel, then 7/8/9/10/11).
4. EXE-1 exemplar (strongest model) while Phase 5 finishes → fan out SVC-1..4 → E2E-1 → then Phases 6 and 8 in parallel streams.
