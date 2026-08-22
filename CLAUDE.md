# CLAUDE.md — conventions for every agent in this repo

## 0. How to use this file

Read this first, then your work-package (WP) brief. The brief decides **what** you build; this file
decides **how**, and wins on every convention. If a convention here blocks the brief, stop and report
the conflict — do not invent a third option.

Authoritative design sources (read-only, never edited):

- `collections_debt_management_platform_design.md` — cited as **D§n**
- `collections_debt_management_platform_design_artefacts_1-12.md` — cited as **A§n**

Every WP is delegated as a brief built from `docs/wp-template.md`. Related reading:
`docs/conventions.md` (ops conventions), `docs/review-policy.md` (review + adversarial verification),
`docs/gates/README.md` (phase gates), `docs/service-playbook.md` (clone-the-exemplar recipe, from EXE-2).

## 1. Repo map

| Path | Contents |
|---|---|
| `contracts/` | Go module exporting `embed.FS`: OpenAPI specs, JSON Schemas, file/CDC contracts, registries, golden vectors. Frozen at tag `contracts-v1.0`. |
| `platform/` | Shared Go module: `events outbox inbox idempotency kafka postgres otelkit httpkit apierror authn config health ids clock ruledsl allocation modelclient testkit`. |
| `services/<name>/` | One Go module per domain service, layout exactly A§92 (`cmd/ internal/{domain,application,ports,adapters} migrations/ api/ tests/`). |
| `ingestion/` | Ingestion control plane + sftp-worker + webhook-receiver + canonicalizer + recon + DLQ. |
| `simulator/corebank/` | Fake source system: seeder, drift tick, file drop, webhook sim, legacy report. |
| `data/` | `dbt/collections/` transformations, `snowflake/` idempotent DDL. |
| `airflow/` | `dags/` (git-synced) + `tests/`. Orchestration only — no business logic. |
| `infrastructure/terraform/` | `modules/`, `stacks/{00-bootstrap,10-network,20-data,30-eks,40-snowflake}`, `envs/dev/`. |
| `deployment/` | Helmfile, values, charts, `kafka/topics.yaml`, observability dashboards/alerts, images. |
| `e2e/` | Compose dev stack, harness, scenarios (the only place cross-service tests live). |
| `ui/collector-workbench/` | React 18 + TS strict + Vite collector workbench. |
| `tools/` | Repo tooling: pinned build tools (`tools/go.mod`), `contractcheck`, `layoutcheck`, `domain-stub`, `check-ownership.sh`, `lint-runbook.sh`. |
| `scripts/` | `verify/<WP-ID>.sh` (one per WP), `ci/`, `db/`, `cost/`, `dr/`. |
| `docs/` | Conventions, WP template, ownership map, review policy, ADRs, gates, runbooks, cost model. |
| `security/` | Security checklist, masking matrix, threat notes. |
| root | `Makefile`, `makefiles/service.mk`, `go.work`, `mise.toml`, `.golangci.yml`, `CLAUDE.md`. |

**You may only modify paths owned by your WP — see `docs/ownership.yaml`.** Before you finish:

```bash
make ownership-check WP=<WP-ID>          # or: bash tools/check-ownership.sh <WP-ID>
```

It fails on any file outside your globs. Do not widen your entry to make it pass: unowned files mean
either you strayed, or the lead agent must amend the brief *and* the ownership entry first (only the
lead edits `docs/ownership.yaml`). Parallel WPs never share a directory.

## 2. Build and test commands

Root (`make help` lists everything):

| Target | Effect |
|---|---|
| `make bootstrap` | Install `./bin` tools (golangci-lint), `go work sync`, download pinned tool deps. Run once. |
| `make lint` / `make fmt` | golangci-lint run / fix across every module. |
| `make build-all` / `make test-all` | `go build` / `go test` across every module. |
| `make verify WP=<id>` | Run `scripts/verify/<id>.sh`. The per-WP gate. |
| `make ownership-check WP=<id>` | Fail if the working tree touches paths the WP does not own. |
| `make contracts-check` | Validate contract artefacts (schemas compile, examples validate, specs lint). |
| `make compose-up` / `make compose-down` | Local dev stack from `e2e/compose.yaml`. |
| `make tf-plan STACK=<nn-name> ENV=dev` | Terraform plan only. Never apply locally. |

Per service/module — canonical targets from `makefiles/service.mk`, identical for every service, run as
`make -C services/<name> <target>`:

`generate` (oapi-codegen + sqlc) · `build` · `lint` · `test` (race + coverage profile) ·
`coverage` (enforce floors) · `test-integration` (testcontainers; needs Docker) ·
`contract-test` · `migrate-up` · `run` · `image`.

Pinned tools are invoked through the tools module — `go -C tools tool <name>` (oapi-codegen, sqlc,
goose, vacuum, oasdiff, go-test-coverage). Never `go install` a tool into the environment and never add
a dependency to `tools/go.mod` outside a WP that owns it.

## 3. Go standards

- **Version**: language level Go 1.24+, toolchain pinned 1.26 (`go.work`, `mise.toml`). Module path
  prefix `github.com/canhtoanptit/collection-platform`.
- **Layering** (A§92): `domain` → `application` → `ports` → `adapters`. Domain packages must not import
  `pgx`, `kgo`, HTTP types, or any adapter. `tools/layoutcheck` enforces this mechanically.
- **Tests are table-driven.** One test table per behaviour; assert behaviour, not implementation.
  State machines get an exhaustive `state × command` table test that fails when a pair is unhandled.
- **`exhaustive` lint is on** (`.golangci.yml`). Every switch over a state/status/enum covers every case;
  no `default:` used to hide a missing state.
- **Context everywhere**: `ctx context.Context` is the first parameter of every function that does I/O,
  and is plumbed through domain → adapters. Never `context.Background()` outside `main` and tests.
- **Errors wrap**: `fmt.Errorf("loading case %s: %w", id, err)`. Sentinel/typed errors for decisions the
  caller makes. Never discard an error; never log-and-return the same error twice.
- **No panic in a request path.** `httpkit`'s Recover middleware exists as a backstop, not a strategy.
  `panic` is acceptable only in `init`/`main` wiring that cannot proceed.
- **Money is `int64` minor units + an ISO-4217 `currency` string, everywhere** — APIs, events, service
  DBs, in-memory domain types (`amountMinor`, `currency`). No floats, ever. No implicit currency.
  Decimal strings in *major* units appear in exactly one place: decision **context documents**
  (business rules read `"500"`, not `50000`), converted by the context builder; the field catalogue
  documents the unit per field. Analytics uses `NUMBER(18,2)`. Arithmetic on split/remainder cases
  (allocations, fees) is integer arithmetic with the remainder assigned by an explicit documented rule.
- **IDs are ULIDs** (`oklog/ulid/v2`) stored in `TEXT` columns; ops entities carry prefixes
  `FIL_ JOB_ REC_ COR_` (see `docs/conventions.md`). No UUIDv4, no auto-increment business keys.
- **Time is UTC** `time.Time`, serialized RFC3339 with `Z`. Time comes from `platform/clock`, never
  `time.Now()` in domain or application code — scheduled work is `server tick <task> --as-of=<date>`.
- **SQL is explicit**: sqlc + pgx/v5, goose migrations embedded. No ORM, no query builders.

## 4. OpenAPI-first

1. Contracts live in `contracts/openapi/<domain>.v<n>.yaml`. The spec changes first, never the code.
2. Then `make -C services/<name> generate` → oapi-codegen **strict-server** types and interfaces.
3. **Never hand-edit generated code.** Generated files are committed and CI runs
   `make generate && git diff --exit-code`; a hand edit is a red build. Fix the spec and regenerate.
4. A contract mismatch must be a compile error, not a runtime surprise — that is why strict-server is
   mandatory. If the generated interface fights you, the spec is wrong.
5. Handler tests validate request *and* response against the spec (`platform/testkit` contract
   validator). Coverage of an endpoint without contract validation does not count.

## 5. Events, outbox, inbox

- **Envelope is exactly A§24** — `eventId eventType eventVersion occurredAt producer aggregateType
  aggregateId correlationId causationId payload` — built and validated only through `platform/events`.
  No service constructs an envelope by hand; no extra top-level fields.
- **Producers publish only through `platform/outbox.Enqueue(ctx, tx, ...)`, in the same transaction as
  the state change.** No service calls a Kafka producer directly. The relay (advisory-lock leader,
  ordered per key) does the publishing. Enqueue validates the payload against its schema *before* the
  row is written, so an invalid event can never reach the broker.
- **Consumers dedupe through `platform/inbox.Dedupe(ctx, tx, consumer, eventID)`**, keyed
  `(consumer, eventId)`, in the same transaction as the side effects. Delivery is at-least-once;
  business effect is exactly-once (D§3.5). Every consumer is idempotent under replay.
- **Topics** are `collections.<context>` per A§25 (14 contexts); ingestion canonical topics are
  `ingestion.{customers,accounts,debts,payments}.v1`. Topics are declared in
  `deployment/kafka/topics.yaml` — auto-create is off.
- **Partition key is the aggregate id** named in the CON-2 topic/key map (A§26) — `accountId` for
  `DelinquencyChanged`, `caseId` for case events, and so on. Ordering guarantees are per key only.
- **DLQ is `collections.dlq.<service>`** (A§27) with original topic + error headers; ingestion uses
  `dlq.ingestion.v1`. Retry with backoff, then DLQ; never block a partition forever, never drop.
- Domain services consume **canonical** ingestion topics only. Raw Debezium (`cdc.corebank.*`) and raw
  webhook topics are internal to `ingestion/`.

## 6. HTTP APIs

- **Error contract is exactly A§20** — `{code, message, correlationId, details[{field, reason}]}` —
  written only via `platform/apierror`. Never a bare `http.Error`, never a stack trace, never an
  internal message in `message`. `code` is a stable SCREAMING_SNAKE business code.
- **`Idempotency-Key` is required on every POST command** (A§21), enforced by the
  `platform/idempotency` middleware: same key + same request hash → replay the stored response; same
  key + different hash → `422`; concurrent in-flight → `409`.
- **Correlation-ID middleware from `platform/httpkit`** runs on every server: accept an inbound
  correlation id or mint one, put it in the context, echo it in responses, propagate it into Kafka
  headers and every log line. See `docs/conventions.md` for the full file → Kafka → Airflow →
  Snowflake → case chain (A§97).
- Auth is per-service JWT (`platform/authn`) with deny-by-default `RequireScope`. No endpoint ships
  without a scope. `PATCH` uses `If-Match` row versions → `412` on mismatch. Pagination is
  `limit` + opaque `cursor`.
- Timeouts, graceful shutdown, `/healthz`, `/readyz`, and structured logs come from
  `platform/httpkit` + `platform/health` + `platform/otelkit`. Do not re-implement them.

## 7. What NOT to touch

- **Released files under `contracts/**` are immutable.** After the `contracts-v1.0` tag, a change is a
  new `vN` file plus a new schema/topic version — never an edit. CI fails PRs that modify a released
  contract file.
- **Other services' directories.** Need something from another service? Use its published contract, or
  raise it to the lead. Never reach into another service's DB, package, or migrations.
- **Generated code** (`*/gen/**`, generated `api/` types, `sqlc` output). Regenerate; never edit.
- **Merged migration files.** A migration that exists on `main` is frozen — append a new one. Never
  renumber, never rewrite history, never `goose down` in shared environments.
- **Other WPs' paths.** `docs/ownership.yaml` is the whole answer. `platform/*` and `contracts/*`
  changes are serialized, dedicated WPs — never two agents in the same module at once.
- **Infrastructure state.** `terraform apply` runs only in gated CI; agents run `plan` at most
  (stack `00-bootstrap` is the single documented human-applied exception).
- **Secrets.** Every Kubernetes secret is an `ExternalSecret` referencing `colx/dev/*`. No values in
  git, values files, DAGs, or tests. No AWS keys — OIDC federation only.

## 8. Verification and definition of done

Every WP ships `scripts/verify/<WP-ID>.sh` (`set -euo pipefail`, exit 0 = pass, no interactive input,
runnable from any cwd) — see `scripts/verify/README.md`. A WP is done when **all** of these are green:

```bash
make -C <dir> lint test                       # zero findings, tests green
make -C <dir> coverage                        # domain >=90%, module >=80%
make -C <dir> generate && git diff --exit-code # generated code committed and current
make contracts-check                          # contract artefacts still valid
make verify WP=<WP-ID>                        # the WP's own acceptance script
make ownership-check WP=<WP-ID>               # nothing outside your paths
```

Plus, where the WP applies: `test-integration` and `contract-test` green,
`go run ./tools/layoutcheck services/<name>` clean, `helm lint` clean, and the service runbook under
`docs/runbooks/` updated and passing `bash tools/lint-runbook.sh <file>` (D§82 heading set) whenever
operational behaviour changed. Acceptance criteria are commands, not prose: if a claim is not a command
someone else can run, it is not evidence. Never mark a WP done with a failing or skipped check — report
the failure instead.

## 9. Commits

- Conventional Commits, **with the WP id in the subject**: `feat(case): SVC-x add PTP command`,
  `fix(outbox): LIB-7 preserve per-key order on leader loss`, `docs(ops): OPS-1 add review policy`.
  Scope = service/module/area.
- One WP per branch; the diff must match the brief's deliverable paths exactly.
- **Never `git push`.** Never force-push, never rewrite shared history, never commit unless the task
  asked for a commit. Never commit secrets, credentials, tokens, `.env`, `*.pem`, `*.p8`, or tfstate.
- Body: what changed and why, plus the acceptance commands you ran. No "WIP" commits on shared branches.

## 10. Hard rules (never negotiable)

1. **`simulator/` must never import `ingestion/` or `platform/` code.** The simulator re-implements the
   file format from the YAML contracts. Sharing validation code would make reconciliation tautological
   — green checks would prove nothing (plan risk 6).
2. **Policy constraints can never be overridden by optimization** (A§54, D§43). Precedence is
   policy > decision logic > optimization. Constraint types expose narrowing only (`Suppress`,
   `ForbidChannel`), and a post-selection guard re-validates the chosen treatment against the policy
   snapshot; a violation is `NO_ACTION` + `POLICY_VIOLATION_GUARD`, never a dispatch.
3. **No business logic in Airflow DAGs** (A§42, D§25). DAGs orchestrate: call an API, poll a status,
   trigger a load, assert a reconciliation. Short idempotent tasks; no business state in XCom; no
   secrets in DAGs. Business rules live in services and dbt.
4. **Auditability is append-only.** Decision audit, case activities, file state transitions, and
   strategy transitions are INSERT + SELECT only, enforced by triggers and role grants (D§3.6, D§39).
   Never add an `UPDATE` path to an audit table.
5. **Reconciliation is explicit.** Pipeline success is never evidence that data is correct (D§38);
   counts and control totals must be asserted against the source.
