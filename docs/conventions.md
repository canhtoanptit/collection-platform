# Operational conventions

Code conventions live in `CLAUDE.md`. This file covers the operational ones: identifiers, time,
correlation, verification harness, and agent/model assignment. Both are binding.

---

## 1. Identifiers and prefixes

All ids are ULIDs (`oklog/ulid/v2`, `platform/ids.NewULID()`) stored in `TEXT` columns — lexically
sortable, time-ordered, matches the `01J…` examples in the design docs. Never UUIDv4; never an
auto-increment value as a business key (bigserial is fine for internal outbox rows).

Operational entities carry a human-readable prefix so an id in a log line or an alert is
self-describing (D§20, A§97):

| Prefix | Entity | Produced by |
|---|---|---|
| `FIL_` | File registry entry (one ingested file) | ingestion control plane |
| `JOB_` | Ingestion job / run | ingestion control plane, Airflow tasks |
| `REC_` | Reconciliation run | reconciliation engine |
| `COR_` | Correlation id (a business flow end to end) | first component in the flow |

Format: `<PREFIX><ULID>`, e.g. `FIL_01J9Z8Q7K3M4N5P6R7S8T9V0W1`. Domain aggregate ids
(`caseId`, `accountId`, `decisionId`, …) are unprefixed ULIDs; source-system keys (`acct_no`,
`cust_no`) keep their legacy shape and are never used as platform ids.

---

## 2. Time and business dates

- **UTC everywhere.** Storage (`timestamptz`), APIs and events (RFC3339 with `Z`), logs, Airflow, cron
  schedules, Snowflake, dbt. No local-time arithmetic anywhere; no naive timestamps.
- **`business_date`** is a calendar date (`YYYY-MM-DD`, UTC) naming the *business day the data belongs
  to*, not the day it was processed. It comes from the source: the named `business_date` capture group
  in a file name, the file header, the tick's `--business-date`, or the DAG's logical date. A file that
  arrives late still carries its original `business_date`; a re-processed file keeps it. Reconciliation,
  partitions (`business_date=<date>` S3 prefixes), incremental dbt models, and batch decisioning runs
  are all keyed by `business_date`, which is what makes re-runs idempotent instead of duplicating.
- Source systems using `int YYYYMMDD` columns (`open_dt`, `last_pay_dt`, `oldest_unpaid_dt`) are
  converted to real dates at the canonicalization/staging boundary — never carried inward as integers.
- **Scheduled work is deterministic**: `server tick <task> --as-of=<date>` (k8s CronJob passes the
  date). Time-dependent logic reads `--as-of` and `platform/clock`; tests inject a fixed clock. No
  clock mocking in production code paths.

---

## 3. Correlation-ID flow (A§97)

One correlation id follows a business flow from the moment data enters the platform to the case
it changes. Every component propagates the id it received and never mints a new one mid-flow.

FORMAT RULE (corrected 2026-08-23 — the frozen envelope schema wins): **on the wire and in the
envelope the correlation id is a BARE ULID** (`01J...`) — `contracts/schemas/envelope` and the
`X-Correlation-Id` header both require the unprefixed 26-char form, and `platform/httpkit`
accepts an inbound header only if it is a bare ULID (else mints one). The `COR_` prefix is for
OPERATIONAL RECORDS ONLY (file registry, job tables, human-facing run logs) via
`ids.NewCorrelationID()`; `ids.Strip()` recovers the bare form when such a record's id enters
the envelope world.

```text
01J...       minted when the flow starts (file discovered / webhook received / API request)
  |
  +-- file registry           file_registry.correlation_id  (stored COR_01J..., bare on the wire)
  |     |
  +-- Kafka                   envelope.correlationId + message header (platform/kafka, otelkit)
  |     |
  +-- Airflow                 dag_run.conf {"correlation_id": "01J..."} -> task logs, XCom-free
  |     |
  +-- Snowflake               ALTER SESSION SET QUERY_TAG = '{"correlation_id":"01J..."}'
  |                           (visible in QUERY_HISTORY / COPY_HISTORY; dbt sets it per run)
  +-- decision batch          decision_audit.correlation_id
        |
        +-- case              case_activities.correlation_id
```

Rules:

- HTTP: `platform/httpkit` correlation middleware accepts an inbound correlation header, else mints
  `COR_<ULID>`; it is echoed on the response and in every A§20 error body.
- Kafka: `correlationId` is an envelope field (A§24) **and** a message header, plus `causationId` = the
  id of the command/event that caused this one. Consumers continue the correlation id into everything
  they emit.
- Airflow: DAGs pass `correlation_id` in `dag_run.conf` and forward it to every API call, `COPY INTO`
  and dbt invocation. A DAG that starts a flow mints one; a Dataset-triggered DAG inherits it.
- Snowflake: every session/statement carries `QUERY_TAG` with the correlation id, so a load can be
  traced back to the file that produced it.
- Logs: `platform/otelkit.Logger(ctx)` emits `correlation_id` and `trace_id` on every line. An incident
  starts as `correlation_id = COR_…` in Loki and ends at the case.

Verification: E2E-1 asserts one correlation id across the ingestion → delinquency → case chain; the
Phase-10 gate samples the full file → case chain.

---

## 4. Verification scripts (`scripts/verify/`)

- One script per WP: `scripts/verify/<WP-ID>.sh`. Run it with `make verify WP=<WP-ID>`.
- Requirements: `#!/usr/bin/env bash`, `set -euo pipefail`, exit `0` = pass and non-zero = fail, no
  interactive input, no reliance on the caller's cwd (resolve the repo root from `BASH_SOURCE`), cleans
  up temporary files, prints one line per check.
- Scripts assert **observable outcomes** — HTTP responses, DB rows, Kafka messages, S3 objects, metric
  values, exit codes — not log text. Each script includes at least one expected-fail assertion proving a
  guard actually rejects bad input.
- Scripts that need infrastructure state which reason: local compose stack (`make compose-up`), dev EKS,
  or nothing. A script that cannot run in the stated environment must exit non-zero with a clear reason,
  never skip silently.
- Details and the annotated example: `scripts/verify/README.md`.

Related mechanical gates: `make ownership-check WP=<id>` (path ownership, `docs/ownership.yaml`),
`bash tools/lint-runbook.sh docs/runbooks/<x>.md` (D§82 heading set), `make contracts-check`,
`go run ./tools/layoutcheck services/<name>`.

---

## 5. Runbooks

Every pipeline, service and scheduled job that can page someone has a runbook under `docs/runbooks/`
containing the D§82 control set as headings — `Owner`, `Support group`, `SLA`, `Expected schedule`,
`Alert policy`, `Retry policy`, `Escalation`, `Reconciliation`, `Runbook steps` (D§82's "Runbook" item,
named for the actionable step list). `tools/lint-runbook.sh` enforces presence; reviewers enforce that
the content is true. Every Prometheus alert rule carries a `runbook_url` annotation pointing at one.

---

## 6. Model and agent assignment

| Work | Model |
|---|---|
| Implementation WPs (the default) | Opus-class implementation agents |
| Exemplar EXE-1 (`services/case`) | strongest available model |
| L-WP decomposition into ≤4 sub-briefs | strongest available model |
| Adversarial verification (`docs/review-policy.md` §3) | strongest available model |
| Phase-gate verification (`docs/gates/`) | strongest available model |

Independence rules matter more than model choice: the adversarial agent must not have read the
implementation, and the gate verifier must not have implemented the WPs it is verifying. Code review
may use an implementation-class model, but never the implementing agent.

---

## 7. Cost and environment hygiene

Infrastructure is real and metered. `make stop` when a stream pauses for the day; `make destroy-heavy`
for longer pauses (everything is declarative and rebuildable, ~60 min). Terraform `apply` happens only
in gated CI. Budgets and the Snowflake resource monitor are hard limits, not suggestions — see
`docs/cost-model.md` and `docs/runbooks/cost-and-teardown.md`.
