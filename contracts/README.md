# contracts

Single source of truth for every interface on the Collections Platform: OpenAPI
specs, JSON Schemas (event envelope, event payloads, ingestion snapshots,
decisioning documents), SFTP file-feed contracts, the CDC source spec,
registries, the AsyncAPI topic index, golden examples and shared test vectors
(A§95).

This directory is a **Go module** (`github.com/canhtoanptit/collection-platform/contracts`)
that exports the artefacts as an `embed.FS`, so every service compiles against —
and validates at runtime with — exactly the files it was built from. No copying,
no drift:

```go
import "github.com/canhtoanptit/collection-platform/contracts"

b, err := contracts.FS.ReadFile("schemas/envelope/EventEnvelope.v1.json")
```

Services and tools reach it through a `replace` directive to `../../contracts`;
codegen (oapi-codegen, `platform/events` registry loading) pins the
`contracts-v1.0` tag created at the end of Phase 1.

## Layout

| Path | Contents | Authored by |
|---|---|---|
| `openapi/` | One OpenAPI 3.0.3 document per service + `common.v1.yaml` (shared components) | CON-1, CON-3, CON-4, DEC-1, ING-2 |
| `schemas/envelope/` | `EventEnvelope.v1.json` — the normative event envelope (A§24) | CON-1 |
| `schemas/events/<context>/` | One payload schema per event type per version | CON-2, DEC-1 |
| `schemas/ingestion/` | Canonical snapshot schemas (`CustomerSnapshot`, `AccountSnapshot`, `DebtSnapshot`, `PaymentNotification`) | CON-6 |
| `schemas/decisioning/` | Strategy document, rule set, context document, field catalogue, population/outcome lines, guardrail config | DEC-1 |
| `files/` | SFTP feed contracts (`*.v1.yaml`) + `SPEC.md` meta-schema | ING-3 |
| `cdc/` | `corebank.v1.yaml` — expected source tables/columns/PKs (drift monitor) | ING-* |
| `registries/` | `reason-codes.v1.json` and other closed vocabularies | DEC-1 |
| `asyncapi/` | `collections.v1.yaml` — topic → key → schema index | CON-2 |
| `examples/` | Golden examples, mirroring `schemas/` (see the mirror rule below) | every schema-owning WP |
| `testdata/` | Cross-service test vectors (e.g. `allocation-golden-vectors.json`) | DEC-1 |
| `validate_test.go` | The self-validation harness for everything above | CON-1 |

## Conventions (normative)

### 1. Released files are immutable

Once a file is on `main` it never changes meaning. Any change that a consumer
could notice — new required field, removed field, narrowed type, renamed enum
value, changed semantics — ships as a **new `vN` file** alongside the old one
(`EventEnvelope.v2.json`, `case.v2.yaml`), and both are served until every
consumer has migrated. Purely additive, optional changes are only permitted
before the `contracts-v1.0` tag. CI enforces this
(`scripts/ci/check-contract-immutability.sh`, CON-7): a modified released file
fails the build. Fixing a typo in a `description` is the one exception a reviewer
may wave through.

### 2. JSON Schema rules

- Dialect: **JSON Schema draft 2020-12** only
  (`"$schema": "https://json-schema.org/draft/2020-12/schema"`).
- `"$id": "https://contracts.collections.internal/<path within this module>"` —
  e.g. `schemas/events/case/CaseCreated.v1.json` has
  `$id: https://contracts.collections.internal/schemas/events/case/CaseCreated.v1.json`.
  The host is a naming authority, not a reachable address: nothing is ever
  fetched over the network. Cross-file `$ref`s use these absolute URLs
  (`{"$ref": "https://contracts.collections.internal/schemas/envelope/EventEnvelope.v1.json#/$defs/ulid"}`)
  and the test harness resolves them from the embedded FS. A `$id` that does not
  match the file's path is a test failure.
- Every object: `"additionalProperties": false` **and** an explicit `required`
  list. Optional fields are omitted, never sent as `null`. No bare `{}` or
  untyped members — the sole deliberate exception is the envelope's `payload`,
  which is validated separately per event type.
- Every field carries a `description` that says what it means and, where it is
  not obvious, its unit. Schemas are read by humans doing audits.
- File naming: `<Name>.v<N>.json`, where `N` is the major version. Event and
  snapshot documents are `PascalCase` (`CaseCreated.v1.json`,
  `AccountSnapshot.v1.json`); decisioning documents are `kebab-case`
  (`strategy-document.v1.json`) per the plan layout.

### 3. Examples and the mirror rule

Every schema ships at least one golden example, and examples are validated in
CI, so a wrong example is a build failure rather than misleading documentation.

- **Mirror rule** — `examples/<p>/<Name>.v<N>.example.json` is validated against
  `schemas/<p>/<Name>.v<N>.json`. Any `*.json` under `examples/` that is not
  named `*.example.json`, or whose mirrored schema does not exist, fails the
  test (orphan guard).
- **Event rule** — examples under `examples/events/**` are *envelope-wrapped*:
  the whole document is validated against `schemas/envelope/EventEnvelope.v1.json`
  and its `payload` against the mirrored payload schema. The file name must
  agree with the envelope: `CaseCreated.v1.example.json` ⇒ `eventType`
  `CaseCreated`, `eventVersion` `1`.
- Every schema under `schemas/events/**` **must** have an example
  (`TestEveryEventSchemaShipsAnExample`); the same discipline is expected for
  `schemas/ingestion/**` and `schemas/decisioning/**` and is enforced by
  `tools/contractcheck` (CON-7).
- `examples/envelope/EventEnvelope.v1.example.json` illustrates the envelope
  only. Its `payload` is *not* a normative `CaseCreated` payload — that is
  `schemas/events/case/CaseCreated.v1.json` (CON-2).

### 4. Money

Every monetary value is the pair `{ amountMinor: int64, currency: <ISO-4217
alpha-3> }`, e.g. `{"amountMinor": 50000, "currency": "EUR"}` = EUR 500.00.
Minor units in `int64` only: never floats, never major units, never a formatted
string. A scalar amount is named `<thing>AmountMinor` (`overdueAmountMinor`,
`installmentAmountMinor`) and always travels with a sibling `currency`.

Two documented conversions exist in the chain (plan §6.8): the ingestion
canonicalizer converts source `NUMERIC` strings to minor units, and the decision
**context document** carries major-unit decimal *strings* so business rules read
`"500"` rather than `50000` — the context builder converts, and every such field
declares its unit in `context-field-catalogue.v1.json`. Analytics stores
`NUMBER(18,2)`. Nothing else deviates.

### 5. Time

RFC3339, UTC, `Z` suffix: `2026-08-22T10:00:00Z`. Local offsets are rejected by
schema (`format: date-time` plus a UTC-only pattern on the envelope). Calendar
dates are `YYYY-MM-DD`; file-feed business dates are `YYYYMMDD` because the
source systems emit them that way (`files/*.v1.yaml`). Durations and ages are
expressed in explicit units in the field name (`timeoutMs`, `dpd`,
`lateBySeconds`).

### 6. Identifiers

Platform-generated identifiers are **ULIDs** (26 characters, Crockford base32,
pattern `^[0-9A-HJKMNP-TV-Z]{26}$`) stored as `TEXT`: sortable by creation time,
safe in URLs and log lines. Operational entities carry a type prefix
(`FIL_`, `JOB_`, `REC_`, `COR_`). Identifiers that travel on the event envelope
(`eventId`, `correlationId`, `causationId`) are **bare ULIDs, no prefix**.
Identifiers owned by source systems (account numbers, external payment
references) keep their source format and are never re-minted.

### 7. Errors

Every non-2xx response of every operation returns the A§20 `Error` body from
`openapi/common.v1.yaml`:

```json
{
  "code": "ARRANGEMENT_INVALID",
  "message": "Arrangement schedule is invalid",
  "correlationId": "01M0MEKBHXV37E3S3E28JT97KB",
  "details": [{ "field": "firstPaymentDate", "reason": "DATE_IN_PAST" }]
}
```

No operation defines its own error schema; specs `$ref` the canned responses
(`BadRequest`, `Unauthorized`, `Forbidden`, `NotFound`, `Conflict`,
`PreconditionFailed`, `UnprocessableEntity`, `InternalError`). `code` and
`reason` are stable `SCREAMING_SNAKE_CASE` vocabulary — clients branch on them,
never on `message`. Error bodies never contain stack traces, SQL, upstream
payloads, internal hostnames or PII.

### 8. Idempotency

Every `POST` that creates a side effect requires the `Idempotency-Key` header
(A§21, `common.v1.yaml#/components/parameters/IdempotencyKey`):

- same key + same request body → the stored response is replayed, no second
  side effect;
- same key + different body → `422`;
- duplicate arriving while the first request is in flight → `409`;
- keys are scoped per endpoint and retained ≥24h (`platform/idempotency`).

Non-mutating `POST`s (simulations, searches) must not reference the parameter.
`PUT`/`PATCH`/`DELETE` are idempotent by construction and do not use the header.

### 9. Optimistic concurrency

Resources that support `PATCH` return a row version as `ETag` and require
`If-Match` on write; a stale value is `412`. `If-Match: *` is not accepted —
blind overwrites are not allowed.

### 10. Pagination, filtering, sorting

`limit` (1–200, default 50) + opaque `cursor`; list responses are
`{ "items": [ … ], "nextCursor": "<opaque>|null" }`. Offsets and total counts are
not part of the contract (they are not cheap on the operational stores). A cursor
is only valid for the same filter and sort arguments; clients must not construct
one. Filters are explicit named query parameters, `sort` is a documented enum,
never a free-form expression.

### 11. Correlation and causation

`X-Correlation-Id` (ULID) is accepted on every request, generated when absent,
echoed on every response, copied into the envelope `correlationId` of every
event the request produces, and carried into Airflow runs and the Snowflake
`QUERY_TAG` (A§97). `causationId` links an event to its direct cause — the
`eventId` being handled, or the `Idempotency-Key` of the originating command.
Together they reconstruct the full chain of a business interaction.

### 12. OpenAPI rules

- OpenAPI **3.0.3**, one document per service, named `<service>.v1.yaml`.
- Shared shapes come from `common.v1.yaml` by relative `$ref`
  (`$ref: './common.v1.yaml#/components/responses/Conflict'`). It declares
  `paths: {}` and is never served.
- Every operation has a unique `operationId` in `camelCase` (it becomes the Go
  method name via oapi-codegen strict-server), a `summary`, a `tags` entry, the
  required security scopes, and the full set of error responses.
- Paths are versioned in the URL (`/v1/...`); breaking changes ship as a new
  spec version, and `oasdiff breaking` gates every PR (CON-7).
- Path ownership follows the matrix below, not the URL prefix.

### 13. Events

- The envelope is `schemas/envelope/EventEnvelope.v1.json` — exactly the ten
  A§24 fields, `additionalProperties: false`, everything required except
  `causationId` (omitted, never null, for the first event in a chain).
- Payload schemas live at `schemas/events/<context>/<EventType>.v<N>.json`;
  `eventType` + `eventVersion` in the envelope locate the payload schema
  mechanically, which is how `platform/events` validates at runtime.
- Topics are `collections.<context>` (A§25) with the partition key defined per
  event type in `asyncapi/collections.v1.yaml`; ordering is guaranteed per
  aggregate only, never globally (A§26). Canonical ingestion events use
  `ingestion.{customers,accounts,debts,payments}.v1`; per-consumer dead letters
  go to `collections.dlq.<service>` (A§27).
- Versioning (D§29): never change the meaning of an existing
  `(eventType, eventVersion)` pair. A breaking payload change is a new version
  (`PaymentReceived.v2`) published alongside `v1` until consumers migrate.
  Consumers ignore unknown event types and unknown versions rather than failing.
- One producer per event type (A§7.2). Notably, `contact-service` alone emits
  `ContactAttempted`/`ContactCompleted`; `treatment-service` calls the contact
  API instead of emitting them (plan §6.4).

## URL path-ownership matrix

**Rule: the service that owns the data owns the path** — even when the URL is
nested under another aggregate (A§7.3: no service reads another service's
store). This means several specs legitimately define paths under the same
prefix; routing is by full path, so there is no conflict, but authors must check
this table before adding a path to their spec.

| URL path | Owning service | Spec file |
|---|---|---|
| `GET /v1/customers/{customerId}` | customer-service | `customer.v1.yaml` |
| `GET /v1/customers/{customerId}/accounts` | **account-service** | `account.v1.yaml` |
| `GET /v1/customers/{customerId}/cases` | **case-service** | `case.v1.yaml` |
| `GET /v1/customers/{customerId}/contacts` | **contact-service** | `contact.v1.yaml` |
| `GET /v1/accounts/{accountId}`, `…/balance`, `…/history` | account-service | `account.v1.yaml` |
| `GET /v1/accounts/{accountId}/debt` | **debt-service** | `debt.v1.yaml` |
| `GET /v1/accounts/{accountId}/payments` | **payment-service** | `payment.v1.yaml` |
| `GET /v1/accounts/{accountId}/recoveries` | **recovery-service** | `recovery.v1.yaml` |
| `GET /v1/accounts/{accountId}/delinquency*`, `GET\|PUT /v1/delinquency/bucket-configs` | **delinquency-service** | `delinquency.v1.yaml` |
| `/v1/cases`, `/v1/cases/{caseId}`, `…/assign\|suspend\|resume\|close\|reopen\|activities` | case-service | `case.v1.yaml` |
| `GET /v1/cases/{caseId}/legal` | **legal-service** | `legal.v1.yaml` |
| `/v1/arrangements*` (incl. `?accountId=`, `…/schedule`, `…/confirm\|cancel\|break`) | arrangement-service | `arrangement.v1.yaml` |
| `/v1/promises*` | **arrangement-service** | `arrangement.v1.yaml` |
| `/v1/payments*` (incl. `…/allocations`) | payment-service | `payment.v1.yaml` |
| `/v1/contacts*` (incl. `…/outcome`) | contact-service | `contact.v1.yaml` |
| `/v1/recoveries*`, `/v1/recovery-metrics` | recovery-service | `recovery.v1.yaml` |
| `/v1/placements*`, `/v1/agencies*` | agency-service | `agency.v1.yaml` |
| `/v1/legal*` (referrals, legal cases, status changes) | legal-service | `legal.v1.yaml` |
| `/v1/strategies*`, `/v1/rule-sets*`, `/v1/guardrail-configs*` | strategy-service | `strategy.v1.yaml` |
| `/v1/decisions*` (incl. `…/explanation`, `…/batch`, `…/simulations`), `GET /v1/reference/reason-codes` | decision-service | `decision.v1.yaml` |
| `/v1/treatments*` (incl. the provider webhook path) | treatment-service | `treatment.v1.yaml` |
| `POST /v1/models/{modelId}/versions/{version}/score` | model scoring provider (`services/model-stub` in dev) | `model.v1.yaml` |
| `/v1/sources*`, `/v1/feeds*`, `/v1/ingestion/*`, `/v1/reconciliation/*` | ingestion control plane | `ingestion-control-plane.v1.yaml` |

## Spec file → service

16 service specs plus the components-only `common.v1.yaml`. That is the 14
domain services (11 authored by CON-3/CON-4, 3 by DEC-1) plus the model scoring
contract and the ingestion control plane.

| Spec file | Service / deployable | Authoring WP |
|---|---|---|
| `common.v1.yaml` | — (components only: error contract, shared parameters, responses, headers) | CON-1 |
| `customer.v1.yaml` | `services/customer` — customer-service | CON-3 |
| `account.v1.yaml` | `services/account` — account-service | CON-3 |
| `debt.v1.yaml` | `services/debt` — debt-service | CON-3 |
| `delinquency.v1.yaml` | `services/delinquency` — delinquency-service | CON-3 |
| `case.v1.yaml` | `services/case` — case-service (the exemplar, EXE-1) | CON-3 |
| `arrangement.v1.yaml` | `services/arrangement` — arrangement-service (arrangements **and** promises) | CON-4 |
| `payment.v1.yaml` | `services/payment` — payment-service | CON-4 |
| `contact.v1.yaml` | `services/contact` — contact-service | CON-4 |
| `recovery.v1.yaml` | `services/recovery` — recovery-service | CON-4 |
| `agency.v1.yaml` | `services/agency` — agency-service | CON-4 |
| `legal.v1.yaml` | `services/legal` — legal-service | CON-4 |
| `strategy.v1.yaml` | `services/strategy` — strategy-service | DEC-1 |
| `decision.v1.yaml` | `services/decision` — decision-service (A§16 + batch/shadow/simulation) | DEC-1 |
| `treatment.v1.yaml` | `services/treatment` — treatment-service | DEC-1 |
| `model.v1.yaml` | `services/model-stub` in dev; the real model server in ML-4 implements the same spec | DEC-1 |
| `ingestion-control-plane.v1.yaml` | `ingestion/` control plane (file registry, checkpoints, jobs **and** the A§19 reconciliation API) | ING-2 |

## Validation

```bash
go -C contracts test ./...          # schemas compile, examples validate, no orphans
make contracts-check                # + JSON syntax, embed integrity, vacuum lint of every spec
```

`validate_test.go` is the permanent harness and applies to everything added
later. It uses `santhosh-tekuri/jsonschema/v6` (the same validator services use
at runtime) with a loader that resolves
`https://contracts.collections.internal/...` `$id`s from the embedded FS, so
cross-file `$ref`s work and no test ever touches the network. It fails on: a
schema that does not compile, a wrong or missing `$id`/`$schema`, an example
that does not validate, an example whose `payload` does not match its event
schema, an example with no mirrored schema, and an event schema with no example.

CON-7 layers the repo-wide gates on top: `tools/contractcheck` (catalogue
coverage, AsyncAPI ref resolution, reason-code cross-reference), the vacuum
ruleset (operationId, error responses, `Idempotency-Key` on POST commands),
`oasdiff breaking` versus the latest `contracts-v*` freeze tag, and the
immutability check.
