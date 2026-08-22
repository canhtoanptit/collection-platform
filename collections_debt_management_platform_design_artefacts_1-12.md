# Collections & Debt Management Platform
## Detailed Architecture Design Pack — Artefacts 1–12

**Document status:** Target Architecture / Solution Design  
**Date:** 22 August 2026  
**Architecture style:** Cloud-native, API-first, event-driven, data-product oriented, vendor-neutral  
**Primary implementation direction:** Go, Kubernetes, Airflow, Snowflake, dbt, object storage, Kafka-compatible event streaming, PostgreSQL-compatible transactional stores, OpenTelemetry  
**Scope:** Enterprise banking collections, debt management, recovery, decisioning, ingestion, analytics and migration

---

# 0. Purpose and Design Goals

This document expands the first twelve design artefacts requested for the platform:

1. Level-1 architecture diagram
2. Level-2 component architecture
3. Bounded-context/service ownership diagram
4. Canonical Collections domain model
5. API catalogue
6. Event catalogue
7. Ingestion architecture and connector specification
8. Airflow DAG catalogue
9. Snowflake/dbt logical data model
10. Decisioning/strategy architecture
11. Security architecture
12. Migration wave plan

The design is intentionally detailed enough to become the baseline for:

- Architecture Review Board approval
- Product backlog creation
- Detailed solution design
- Platform engineering
- Service scaffolding
- Data engineering
- Security review
- Operational readiness
- Migration planning
- Vendor evaluation

---

# 1. Architecture Principles

## 1.1 Bank-owned business architecture

The platform owns:

- Domain entities
- Domain state machines
- Business events
- Decision contracts
- Strategy definitions
- Policy versions
- Audit model
- Data contracts
- Reconciliation rules
- API contracts

Infrastructure is replaceable.

---

## 1.2 Modern technology without unnecessary technology fragmentation

The target technology stack should be modern, but every technology must have a clear role.

| Capability | Preferred technology |
|---|---|
| Operational services | Go |
| REST API | OpenAPI + HTTP/JSON |
| Internal RPC | gRPC where justified |
| Container runtime | Kubernetes |
| Service networking | Kubernetes-native networking; service mesh only where justified |
| Transactional DB | PostgreSQL-compatible relational database |
| Event streaming | Kafka-compatible platform |
| Batch orchestration | Apache Airflow |
| Analytical warehouse | Snowflake |
| Analytical transformation | dbt |
| Raw object storage | S3-compatible object storage |
| Secrets | Enterprise secrets manager / cloud KMS integration |
| Identity | OIDC/OAuth2 + enterprise IAM |
| Observability | OpenTelemetry |
| Metrics | Prometheus-compatible |
| Dashboards | Grafana-compatible |
| Logs | Structured JSON + centralized log platform |
| API contract | OpenAPI |
| Event contract | AsyncAPI + schema registry |
| Infrastructure | Terraform/OpenTofu-compatible IaC |
| CI/CD | Git-based pipelines |
| Containers | OCI-compatible |
| Model serving | Model API abstraction; implementation replaceable |
| Feature data | Snowflake initially; dedicated feature platform only when justified |

The principle is:

> Use modern technology where it reduces risk or increases engineering leverage, not merely because it is fashionable.

---

# 2. Target Operating Model

The platform is composed of five major planes.

```text
+-------------------------------------------------------------------+
|                         EXPERIENCE PLANE                           |
| Collector UI | Operations | Customer Digital | Partner APIs       |
+-------------------------------------------------------------------+
                              |
                              v
+-------------------------------------------------------------------+
|                         DOMAIN PLANE                              |
| Customer | Account | Debt | Delinquency | Case | Arrangement      |
| Contact | Payment | Recovery | Agency | Legal | Treatment         |
+-------------------------------------------------------------------+
                              |
                              v
+-------------------------------------------------------------------+
|                        DECISION PLANE                             |
| Policy | Rules | Segmentation | Models | Optimization | Strategy  |
+-------------------------------------------------------------------+
                              |
                              v
+-------------------------------------------------------------------+
|                         DATA PLANE                                |
| Ingestion | Event Streaming | Object Storage | Snowflake | dbt   |
+-------------------------------------------------------------------+
                              |
                              v
+-------------------------------------------------------------------+
|                     PLATFORM / CONTROL PLANE                      |
| IAM | Audit | Observability | Config | Secrets | CI/CD | SRE      |
+-------------------------------------------------------------------+
```

---

# 3. Artefact 1 — Level-1 Architecture

## 3.1 Logical architecture

```text
                              EXTERNAL SYSTEMS
                                      |
       +------------------------------+-------------------------------+
       |                              |                               |
       v                              v                               v
   Core Banking                    Partners                      Channels
   Loan/Card DB                    Agencies                       SMS/Email
   Payments                        Legal                         Contact Centre
   CRM                             Credit/Data                   Digital
       |                              |                               |
       +------------------------------+-------------------------------+
                                      |
                                      v
+---------------------------------------------------------------------+
|                     COLLECTIONS PLATFORM                            |
|                                                                     |
|  +-------------------+       +-------------------------------+      |
|  | Ingestion Platform|       | Integration Gateway           |      |
|  | CDC               |       | API / Events / Partner APIs  |      |
|  | SFTP / CSV        |       +-------------------------------+      |
|  | API               |                                              |
|  | Events            |                                              |
|  +---------+---------+                                              |
|            |                                                        |
|            v                                                        |
|  +---------------------------------------------------------------+ |
|  |                    DOMAIN SERVICES                            | |
|  | Customer | Account | Debt | Delinquency | Case                | |
|  | Contact | Arrangement | Payment | Recovery | Agency | Legal  | |
|  +---------------------------+-----------------------------------+ |
|                              |                                     |
|                              v                                     |
|  +---------------------------------------------------------------+ |
|  |                    DECISION PLATFORM                          | |
|  | Strategy | Policy | Rules | Models | Optimization             | |
|  +---------------------------+-----------------------------------+ |
|                              |                                     |
|                              v                                     |
|  +---------------------------------------------------------------+ |
|  |                       EVENT PLATFORM                           | |
|  | Domain Events | Integration Events | Replay | DLQ             | |
|  +---------------------------+-----------------------------------+ |
+------------------------------+--------------------------------------+
                               |
             +-----------------+------------------+
             |                                    |
             v                                    v
+----------------------------+       +-------------------------------+
| Operational Data           |       | Analytical Data Platform      |
| PostgreSQL-compatible DBs   |       | Object Storage                 |
| Service-owned state        |       | Snowflake                      |
+----------------------------+       | dbt                            |
                                     +---------------+---------------+
                                                     |
                                                     v
                                          BI / ML / Analytics
```

---

## 3.2 External source categories

### Banking systems

- Core banking
- Lending
- Credit cards
- Payments
- Deposits
- CRM
- Customer master
- Product systems
- General ledger
- Fraud/risk systems

### External parties

- Debt collection agencies
- Legal firms
- Credit bureaus
- Payment providers
- Communication providers
- Data providers

### Customer channels

- Mobile application
- Web
- SMS
- Email
- Voice
- Contact centre
- Branch
- Agent

---

## 3.3 Platform boundary

The Collections Platform should not become the enterprise system of record for every customer attribute.

Instead:

```text
Enterprise Customer Master
             |
             v
     Customer Reference
             |
             v
Collections Customer View
```

The collections platform owns the collection-specific representation.

---

# 4. Artefact 2 — Level-2 Component Architecture

## 4.1 Ingestion components

```text
+-------------------------------------------------------------------+
|                    INGESTION PLATFORM                             |
|                                                                   |
| Source Registry                                                   |
|      |                                                            |
|      v                                                            |
| Connector Manager                                                 |
|      |                                                            |
|  +---+------+--------+---------+---------+                        |
|  |          |        |         |         |                        |
| CDC       SFTP     CSV        REST      Event                     |
|  |          |        |         |         |                        |
|  +----------+--------+---------+---------+                        |
|                     |                                             |
|                     v                                             |
|              Validation Engine                                    |
|                     |                                             |
|             +-------+--------+                                    |
|             |                |                                    |
|             v                v                                    |
|          Accepted         Quarantine                              |
|             |                |                                    |
|             v                v                                    |
|       Delivery Engine       DLQ                                   |
|             |                                                     |
|      +------+-------+                                             |
|      |              |                                             |
|      v              v                                             |
| Object Storage   Event Bus                                        |
+-------------------------------------------------------------------+
```

---

# 5. Domain Service Components

## 5.1 Customer Service

Responsibilities:

- Collection-specific customer profile
- Customer reference
- Contactability
- Communication preference
- Relevant collection constraints
- Customer status

Not responsible for enterprise customer master ownership.

---

## 5.2 Account Service

Responsibilities:

- Account reference
- Product relationship
- Account status
- Balance snapshot
- Due amount
- Payment status
- Source-system reference

---

## 5.3 Debt Service

Responsibilities:

- Debt exposure
- Principal
- Interest
- Fees
- Arrears
- Recoverable balance
- Debt ownership
- Debt lifecycle

---

## 5.4 Delinquency Service

Responsibilities:

- DPD
- Delinquency bucket
- Delinquency lifecycle
- Cure
- Re-default
- Roll-rate transitions

---

## 5.5 Case Service

Responsibilities:

- Case lifecycle
- Assignment
- Priority
- Collector/team
- Next action
- Case activities
- Case closure

---

## 5.6 Strategy Service

Responsibilities:

- Strategy definitions
- Strategy versions
- Eligibility
- Priority
- Effective dates
- Strategy activation
- Strategy retirement

---

## 5.7 Decision Service

Responsibilities:

- Evaluate policy
- Evaluate rules
- Invoke models
- Apply constraints
- Produce treatment recommendation
- Produce explanation
- Audit decision

---

## 5.8 Treatment Service

Responsibilities:

- Treatment execution request
- Channel abstraction
- Communication request
- Collector task
- Payment arrangement action
- Escalation

---

## 5.9 Arrangement Service

Responsibilities:

- Promise to pay
- Payment plan
- Installments
- Due dates
- Arrangement status
- Broken arrangement

---

## 5.10 Payment Service

Responsibilities:

- Payment reference
- Payment allocation
- Payment matching
- Payment events
- Payment reconciliation

The payment service should not replace the bank's authoritative payment system.

---

## 5.11 Recovery Service

Responsibilities:

- Recovery action
- Recovery amount
- Recovery source
- Recovery cost
- Recovery attribution
- Recovery status

---

## 5.12 Agency Service

Responsibilities:

- Agency
- Contract
- Placement
- Recall
- Agency performance
- Agency fees
- Recovery attribution

---

## 5.13 Legal Service

Responsibilities:

- Legal referral
- Legal case
- Legal status
- External legal provider
- Legal outcome

---

# 6. Platform Services

Common platform capabilities:

```text
Identity Service
Configuration Service
Feature Flag Service
Audit Service
Notification Service
Reference Data Service
Document/Template Service
File Service
Reconciliation Service
Idempotency Service
Scheduler
Observability
```

Not every capability needs to be a standalone microservice.

A modular platform is preferable to excessive microservices.

---

# 7. Artefact 3 — Bounded Context and Service Ownership

## 7.1 Context map

```text
                         Customer Master
                               |
                               v
                        +--------------+
                        |   Customer   |
                        |   Context    |
                        +------+-------+
                               |
                               v
                        +--------------+
                        |   Account    |
                        |   Context    |
                        +------+-------+
                               |
                               v
                        +--------------+
                        |     Debt     |
                        |   Context    |
                        +------+-------+
                               |
                               v
                      +------------------+
                      |   Delinquency    |
                      |     Context      |
                      +--------+---------+
                               |
                               v
                      +------------------+
                      |      Case        |
                      |     Context      |
                      +--------+---------+
                               |
               +---------------+----------------+
               |                                |
               v                                v
       +---------------+                +---------------+
       |   Strategy    |                |   Treatment   |
       |   Context     |                |   Context     |
       +-------+-------+                +-------+-------+
               |                                |
               v                                v
       +---------------+                +---------------+
       |   Decision    |                | Communication |
       |   Context     |                |   Channels    |
       +---------------+                +---------------+
               |
               v
       +---------------+
       | Arrangement   |
       +-------+-------+
               |
               v
       +---------------+
       |   Payment     |
       +-------+-------+
               |
               v
       +---------------+
       |   Recovery    |
       +---------------+
```

---

## 7.2 Ownership matrix

| Context | Owns | Publishes |
|---|---|---|
| Customer | Collection customer profile | CustomerUpdated |
| Account | Account state | AccountUpdated |
| Debt | Debt/exposure | DebtUpdated |
| Delinquency | Delinquency lifecycle | DelinquencyChanged |
| Case | Case lifecycle | CaseCreated, CaseResolved |
| Strategy | Strategy versions | StrategyActivated |
| Decision | Decisions | DecisionMade |
| Treatment | Treatments | TreatmentExecuted |
| Contact | Contact activity | ContactAttempted |
| Arrangement | Arrangements | ArrangementCreated/Broken |
| Payment | Payment matching | PaymentReceived/Allocated |
| Recovery | Recovery | RecoveryRecorded |
| Agency | Placement | DebtPlaced/DebtRecalled |
| Legal | Legal case | LegalStatusChanged |

---

## 7.3 Avoid shared database ownership

Bad:

```text
Case Service
   |
   +--- SELECT account_db.account
   +--- UPDATE payment_db.payment
```

Preferred:

```text
Case Service
   |
   +--- Account API
   |
   +--- Account events
   |
   +--- Payment events
```

---

# 8. Artefact 4 — Canonical Collections Domain Model

## 8.1 Entity model

```text
Customer
 |
 +-- CustomerContact
 |
 +-- CommunicationPreference
 |
 +-- CollectionConstraint
 |
 +-- Account
       |
       +-- Product
       |
       +-- Debt
       |     |
       |     +-- DebtComponent
       |
       +-- Delinquency
       |
       +-- CollectionCase
             |
             +-- StrategyAssignment
             |
             +-- Decision
             |
             +-- Treatment
             |
             +-- Contact
             |
             +-- PromiseToPay
             |
             +-- Arrangement
             |
             +-- Payment
             |
             +-- Recovery
             |
             +-- AgencyPlacement
             |
             +-- LegalCase
```

---

# 9. Entity Definitions

## Customer

```text
customerId
enterpriseCustomerId
status
segment
contactability
createdAt
updatedAt
```

---

## Account

```text
accountId
customerId
sourceSystem
productId
currency
status
openedAt
closedAt
currentBalance
overdueAmount
minimumDue
lastPaymentAt
```

---

## Debt

```text
debtId
accountId
debtType
principal
interest
fees
penalties
recoverableAmount
currency
effectiveDate
```

---

## Delinquency

```text
delinquencyId
accountId
dpd
bucket
firstDelinquencyDate
currentDelinquencyDate
cureDate
redefaultDate
status
```

---

## Collection Case

```text
caseId
customerId
accountId
debtId
status
stage
priority
assignedTeam
assignedCollector
strategyId
strategyVersion
openedAt
nextActionAt
closedAt
outcome
```

---

# 10. State Machines

## 10.1 Case

```text
NEW
 |
 v
OPEN
 |
 v
ACTIVE
 |
 +------> SUSPENDED
 |           |
 |           v
 |         ACTIVE
 |
 +------> ESCALATED
 |           |
 |           +--> AGENCY
 |           |
 |           +--> LEGAL
 |
 v
RESOLVED
 |
 v
CLOSED
```

---

## 10.2 Promise

```text
PROPOSED
   |
   v
ACCEPTED
   |
   +----> KEPT
   |
   +----> BROKEN
   |
   +----> CANCELLED
```

---

## 10.3 Arrangement

```text
DRAFT
 |
 v
ACTIVE
 |
 +--> COMPLETED
 |
 +--> BROKEN
 |
 +--> CANCELLED
```

---

# 11. Domain Invariants

Examples:

1. A closed case cannot receive a new treatment unless explicitly reopened.
2. A payment cannot be allocated more than once.
3. A strategy version cannot be changed after activation.
4. A decision must reference a strategy version.
5. An arrangement must have a valid payment schedule.
6. A debt placement cannot be active simultaneously with an incompatible legal ownership state.
7. An event ID must be unique.
8. A business command must be idempotent where retries are possible.

---

# 12. Artefact 5 — API Catalogue

## 12.1 API principles

- REST for external/domain APIs
- gRPC for high-volume internal synchronous calls where useful
- OpenAPI as contract
- OAuth2/OIDC for user/system authorization
- Correlation IDs
- Idempotency keys for commands
- Pagination
- Filtering
- Optimistic concurrency
- Versioned breaking changes

---

# 13. Customer APIs

```text
GET /v1/customers/{customerId}
GET /v1/customers/{customerId}/accounts
GET /v1/customers/{customerId}/cases
GET /v1/customers/{customerId}/contacts
```

---

# 14. Account APIs

```text
GET /v1/accounts/{accountId}
GET /v1/accounts/{accountId}/balance
GET /v1/accounts/{accountId}/delinquency
GET /v1/accounts/{accountId}/debt
GET /v1/accounts/{accountId}/history
```

---

# 15. Case APIs

```text
POST /v1/cases
GET /v1/cases/{caseId}
PATCH /v1/cases/{caseId}
POST /v1/cases/{caseId}/assign
POST /v1/cases/{caseId}/suspend
POST /v1/cases/{caseId}/resume
POST /v1/cases/{caseId}/close
POST /v1/cases/{caseId}/reopen
GET /v1/cases/{caseId}/activities
```

---

# 16. Decision APIs

```text
POST /v1/decisions
POST /v1/decisions/batch
GET /v1/decisions/{decisionId}
GET /v1/decisions/{decisionId}/explanation
POST /v1/decisions/simulations
```

Example:

```json
{
  "accountId": "A123",
  "customerId": "C123",
  "context": {
    "dpd": 35,
    "overdueAmount": 500.00,
    "previousContacts": 2
  }
}
```

Response:

```json
{
  "decisionId": "D123",
  "strategy": {
    "id": "EARLY_COLLECTION",
    "version": 17
  },
  "treatment": {
    "type": "CONTACT",
    "channel": "SMS"
  },
  "reasonCodes": [
    "DPD_31_60",
    "CHANNEL_ELIGIBLE"
  ],
  "modelReferences": [
    {
      "model": "PAYMENT_PROPENSITY",
      "version": "8"
    }
  ]
}
```

---

# 17. Arrangement APIs

```text
POST /v1/arrangements
GET /v1/arrangements/{id}
POST /v1/arrangements/{id}/confirm
POST /v1/arrangements/{id}/cancel
POST /v1/arrangements/{id}/break
GET /v1/arrangements/{id}/schedule
```

---

# 18. Agency APIs

```text
POST /v1/placements
GET /v1/placements/{id}
POST /v1/placements/{id}/recall
GET /v1/agencies/{id}/performance
```

---

# 19. Reconciliation APIs

```text
POST /v1/reconciliation/runs
GET /v1/reconciliation/runs/{runId}
GET /v1/reconciliation/runs/{runId}/exceptions
POST /v1/reconciliation/exceptions/{id}/resolve
```

---

# 20. API Error Contract

```json
{
  "code": "ARRANGEMENT_INVALID",
  "message": "Arrangement schedule is invalid",
  "correlationId": "01J...",
  "details": [
    {
      "field": "firstPaymentDate",
      "reason": "DATE_IN_PAST"
    }
  ]
}
```

Do not expose internal stack traces.

---

# 21. API Idempotency

Commands that create side effects should support:

```text
Idempotency-Key: 01JABC...
```

Example:

```text
POST /arrangements
Idempotency-Key: X123
```

Retry:

```text
POST /arrangements
Idempotency-Key: X123
```

Result:

> Return the original arrangement rather than creating a duplicate.

---

# 22. Artefact 6 — Event Catalogue

## 22.1 Event principles

Events are:

- Immutable
- Versioned
- Business meaningful
- Traceable
- Replayable
- Schema validated

---

# 23. Core Event Catalogue

| Event | Producer | Key |
|---|---|---|
| CustomerUpdated | Customer | customerId |
| AccountUpdated | Account | accountId |
| DebtUpdated | Debt | debtId |
| DelinquencyChanged | Delinquency | accountId |
| CaseCreated | Case | caseId |
| CaseAssigned | Case | caseId |
| CaseResolved | Case | caseId |
| StrategyActivated | Strategy | strategyId |
| DecisionMade | Decision | decisionId |
| TreatmentSelected | Decision/Treatment | caseId |
| ContactAttempted | Contact | contactId |
| ContactCompleted | Contact | contactId |
| PromiseCreated | Arrangement | promiseId |
| PromiseBroken | Arrangement | promiseId |
| ArrangementCreated | Arrangement | arrangementId |
| ArrangementBroken | Arrangement | arrangementId |
| PaymentReceived | Payment | paymentId |
| PaymentAllocated | Payment | paymentId |
| RecoveryRecorded | Recovery | recoveryId |
| DebtPlaced | Agency | placementId |
| DebtRecalled | Agency | placementId |
| LegalStatusChanged | Legal | legalCaseId |

---

# 24. Event Envelope

```json
{
  "eventId": "01J...",
  "eventType": "ArrangementCreated",
  "eventVersion": 1,
  "occurredAt": "2026-08-22T10:00:00Z",
  "producer": "arrangement-service",
  "aggregateType": "Arrangement",
  "aggregateId": "ARR123",
  "correlationId": "COR123",
  "causationId": "CMD123",
  "payload": {}
}
```

---

# 25. Event Topics

Recommended logical topics:

```text
collections.customer
collections.account
collections.debt
collections.delinquency
collections.case
collections.strategy
collections.decision
collections.treatment
collections.contact
collections.arrangement
collections.payment
collections.recovery
collections.agency
collections.legal
```

Physical topic strategy can differ by broker technology.

---

# 26. Event Ordering

Ordering is normally required per aggregate rather than globally.

Example:

```text
accountId=A123

PaymentReceived
PaymentAllocated
ArrangementUpdated
```

Partition by aggregate key where the event platform supports partitioning.

Do not require global ordering unless a business requirement genuinely exists.

---

# 27. Dead Letter Queue

Events that cannot be processed after retry:

```text
Consumer
   |
   v
Retry
   |
   +--> success
   |
   +--> retry
          |
          v
        DLQ
          |
          +--> alert
          +--> investigate
          +--> replay
```

---

# 28. Artefact 7 — Ingestion Architecture

## 28.1 Ingestion control plane

```text
+---------------------------------------------------------------+
|                    INGESTION CONTROL PLANE                    |
|                                                               |
| Source Registry                                               |
| Connector Configuration                                       |
| Schema Registry                                               |
| Credential Reference                                          |
| Schedule                                                       |
| SLA                                                            |
| Quality Rules                                                  |
| Reconciliation Rules                                          |
+-------------------------------+-------------------------------+
                                |
                                v
+---------------------------------------------------------------+
|                    INGESTION DATA PLANE                        |
|                                                               |
| CDC Worker | SFTP Worker | CSV Processor | API Worker        |
|                                                               |
|              Common Ingestion Envelope                       |
|                           |                                   |
|                           v                                   |
|                 Validation / Dedup                            |
|                           |                                   |
|              +------------+------------+                      |
|              |                         |                      |
|              v                         v                      |
|         Object Storage             Event Bus                  |
|              |                         |                      |
|              v                         v                      |
|          Snowflake                 Services                   |
+---------------------------------------------------------------+
```

---

# 29. Connector Interface

Conceptual Go interface:

```go
type Connector interface {
    Validate(ctx context.Context, config SourceConfig) error
    Discover(ctx context.Context, config SourceConfig) ([]Resource, error)
    Start(ctx context.Context, job Job) error
    Pause(ctx context.Context, jobID string) error
    Resume(ctx context.Context, jobID string) error
    Stop(ctx context.Context, jobID string) error
    Checkpoint(ctx context.Context, jobID string) (Checkpoint, error)
}
```

The actual implementation should avoid forcing every connector to implement meaningless operations.

Use capability interfaces when necessary:

```go
type SnapshotCapable interface {
    Snapshot(...)
}

type CDCCapable interface {
    StartCDC(...)
}
```

---

# 30. CDC Connector

Required capabilities:

- Snapshot
- Log position
- CDC
- Checkpoint
- Restart
- Schema change detection
- Source lag
- Offset persistence
- Reconciliation

Flow:

```text
Source
 |
 +--> Snapshot
 |
 +--> CDC log
        |
        v
Connector
        |
        v
Canonical event
        |
        +--> Event Bus
        +--> Object Storage
```

---

# 31. SFTP Connector

Responsibilities:

1. Connect
2. Verify host key
3. Authenticate
4. Discover files
5. Download
6. Calculate checksum
7. Store immutable copy
8. Register file
9. Validate
10. Process
11. Reconcile
12. Archive
13. Emit completion event

---

# 32. CSV Pipeline

```text
SFTP
 |
 v
File received
 |
 v
Checksum
 |
 v
Schema detection
 |
 v
Header validation
 |
 v
CSV parser
 |
 v
Row validation
 |
 +---- invalid ---> quarantine
 |
 v
Normalize
 |
 v
Object Storage RAW
 |
 v
Snowflake
 |
 v
dbt
```

---

# 33. File Deduplication

Primary mechanisms:

- File checksum
- Source + filename + business date
- File ID
- Content hash

Example:

```text
SHA-256(file)
```

If the same content arrives twice:

```text
first -> PROCESS
second -> DUPLICATE
```

The duplicate should be auditable.

---

# 34. API Ingestion

Support:

- REST
- Webhooks
- Batch APIs
- Pagination
- Rate limits
- Retry
- OAuth2
- mTLS where required
- Idempotency

---

# 35. Ingestion Checkpoint

Checkpoint examples:

CDC:

```text
source = LOAN_DB
position = SCN:12345678
```

SFTP:

```text
source = LOAN_SFTP
lastFile = loan_20260822.csv
checksum = ...
```

API:

```text
source = PAYMENT_API
cursor = abc123
```

Event stream:

```text
topic = payment
partition = 12
offset = 928372
```

---

# 36. Ingestion Status

```text
DISCOVERED
RECEIVED
VALIDATING
VALIDATED
PROCESSING
PROCESSED
RECONCILING
RECONCILED
FAILED
QUARANTINED
ARCHIVED
DUPLICATE
```

---

# 37. Ingestion Reconciliation

Every source should define controls.

Example:

```text
Expected records = 1,000,000
Received records = 1,000,000
Rejected records = 12
Loaded records = 999,988

1,000,000 = 12 + 999,988
```

For money:

```text
Source amount = 1,250,000.00
Target amount = 1,250,000.00
Difference = 0
```

---

# 38. Artefact 8 — Airflow DAG Catalogue

Airflow DAGs should be organized by business/data workflow rather than one giant DAG.

## 38.1 DAG catalogue

```text
ingest_core_daily
ingest_cards_daily
ingest_loans_daily
ingest_payments_daily

cdc_monitor_core
cdc_monitor_cards

process_sftp_daily
process_agency_files

collections_daily_population
collections_daily_decisioning
collections_daily_reconciliation

dbt_raw_to_staging
dbt_staging_to_intermediate
dbt_intermediate_to_marts

ml_feature_build
ml_batch_scoring

agency_reconciliation
payment_reconciliation
```

---

# 39. DAG: Daily Source Ingestion

```text
start
 |
 v
check_source_available
 |
 v
trigger_ingestion_job
 |
 v
wait_for_completion
 |
 v
validate
 |
 +---- fail --> alert
 |
 v
reconcile
 |
 +---- fail --> incident
 |
 v
success
```

---

# 40. DAG: Collections Daily Cycle

```text
Source ingestion complete
        |
        v
Build current account state
        |
        v
Calculate delinquency
        |
        v
Create/update cases
        |
        v
Determine eligible population
        |
        v
Run decisioning
        |
        v
Persist decisions
        |
        v
Generate treatments
        |
        v
Reconcile
        |
        v
Publish completion
```

---

# 41. DAG: dbt

```text
dbt source freshness
        |
        v
staging
        |
        v
intermediate
        |
        v
marts
        |
        v
tests
        |
        v
documentation/metadata
```

---

# 42. Airflow Design Rules

Do:

- Short, observable tasks
- External job references
- Retryable operations
- Clear SLAs
- Parameterized backfills
- Dataset/event-aware scheduling where useful

Avoid:

- Long-running business state
- Huge Python scripts
- Customer-facing API orchestration
- Storing business state in XCom
- Direct business-rule implementation in DAG code

---

# 43. Artefact 9 — Snowflake/dbt Logical Data Model

## 43.1 Layering

```text
                SOURCE SYSTEMS
                      |
                      v
                 RAW / BRONZE
                      |
                      v
                   STAGING
                      |
                      v
                 INTERMEDIATE
                      |
                      v
                    MARTS
                      |
             +--------+--------+
             |        |        |
             v        v        v
          BI/MI      ML     Reporting
```

---

# 44. RAW Layer

Example tables:

```text
RAW_CUSTOMER
RAW_ACCOUNT
RAW_PAYMENT
RAW_DELINQUENCY
RAW_TRANSACTION
RAW_CASE
RAW_CONTACT
RAW_ARRANGEMENT
RAW_AGENCY
RAW_LEGAL
```

Raw data should retain:

- Source
- Ingestion timestamp
- File/event ID
- Source record ID
- Schema version
- Raw payload where appropriate

---

# 45. STAGING

```text
STG_CUSTOMER
STG_ACCOUNT
STG_PAYMENT
STG_DELINQUENCY
STG_CASE
STG_CONTACT
STG_ARRANGEMENT
STG_RECOVERY
```

Standardize:

- Names
- Types
- Time zones
- Codes
- Nulls
- Source mappings

---

# 46. INTERMEDIATE

```text
INT_ACCOUNT_CURRENT
INT_CUSTOMER_EXPOSURE
INT_ACCOUNT_DELINQUENCY
INT_CASE_CURRENT
INT_CONTACT_HISTORY
INT_PAYMENT_HISTORY
INT_ARRANGEMENT_STATUS
INT_RECOVERY_HISTORY
INT_COLLECTION_ELIGIBILITY
```

---

# 47. MARTS

## Fact tables

```text
FCT_COLLECTION_CASE
FCT_COLLECTION_ACTION
FCT_CONTACT
FCT_PAYMENT
FCT_ARRANGEMENT
FCT_RECOVERY
FCT_AGENCY_PLACEMENT
FCT_DECISION
FCT_DELINQUENCY_SNAPSHOT
```

## Dimensions

```text
DIM_CUSTOMER
DIM_ACCOUNT
DIM_PRODUCT
DIM_STRATEGY
DIM_TREATMENT
DIM_CHANNEL
DIM_AGENCY
DIM_DATE
DIM_REASON_CODE
```

---

# 48. Slowly Changing Dimensions

Use SCD Type 2 where historical attribute changes matter.

Example:

```text
customer_segment

effective_from
effective_to
is_current
```

This is important when analyzing historical decisions.

---

# 49. dbt Model Naming

Recommended:

```text
stg_<source>_<entity>

int_<business_concept>

fct_<business_event/process>

dim_<business_entity>
```

Example:

```text
stg_core_account
int_account_delinquency
fct_collection_action
dim_strategy
```

---

# 50. dbt Testing

Examples:

```text
unique(account_id)
not_null(account_id)
relationships(account_id)
accepted_values(status)
```

Business tests:

```text
payment_amount >= 0
arrangement_end >= arrangement_start
case_close_date >= case_open_date
```

---

# 51. Analytical Data Products

Potential products:

### Collections Performance

- Cases
- Actions
- Contacts
- Outcomes

### Recovery

- Recovery amount
- Recovery rate
- Cost
- Time to recovery

### Strategy

- Strategy effectiveness
- Champion/challenger

### Agency

- Placement
- Recovery
- Cost
- SLA

### Customer

- Contactability
- Payment behavior
- Engagement

---

# 52. Artefact 10 — Decisioning / Strategy Architecture

## 52.1 Decision architecture

```text
                    Decision Request
                          |
                          v
                  Context Builder
                          |
              +-----------+-----------+
              |                       |
              v                       v
         Eligibility              Customer
           Policies                Context
              |                       |
              +-----------+-----------+
                          |
                          v
                    Segmentation
                          |
                          v
                       Rules
                          |
                          v
                      Models
                          |
                          v
                    Optimization
                          |
                          v
                     Constraints
                          |
                          v
                  Treatment Choice
                          |
                          v
                   Explanation
                          |
                          v
                    Decision Audit
```

---

# 53. Strategy Object

```yaml
id: EARLY_COLLECTION
version: 17
status: ACTIVE
effectiveFrom: 2026-08-01
effectiveTo: null

eligibility:
  dpd:
    min: 1
    max: 60

priority: 100

treatments:
  - SMS
  - EMAIL
  - CALL
```

The actual production representation may use JSON/database tables, but the conceptual model should be declarative.

---

# 54. Policy Layer

Policy has higher precedence than optimization.

Example:

```text
Policy:
Customer is not eligible for automated contact.

Optimization:
SMS has highest predicted payment probability.

Result:
Do not send SMS.
```

This ordering is important:

```text
Policy constraints
       >
Decision logic
       >
Optimization
```

---

# 55. Rule Engine

Rules should support:

- Conditions
- Operators
- Decision outcomes
- Reason codes
- Versioning
- Effective dates
- Priority

Example:

```text
IF dpd between 31 and 60
AND contactable = true
AND automated_contact_allowed = true
THEN treatment = SMS
```

---

# 56. Model Integration

The decision service should not care whether a model is:

- Python
- XGBoost
- neural network
- external model provider
- internal model service

Use:

```text
Model Contract
```

Example:

```text
Model:
PAYMENT_PROPENSITY

Input:
account context

Output:
probability [0,1]

Version:
8
```

---

# 57. Optimization

Example objective:

```text
maximize:

expected_recovery
-
contact_cost
-
operational_cost
```

Subject to:

```text
customer policy
channel capacity
regulatory constraints
agency capacity
collector capacity
```

---

# 58. Explainability

Every decision should return:

```text
decision
strategy
rules
models
reason codes
constraints
```

Example:

```text
DECISION = SMS

Reasons:
DPD_31_60
HIGH_CONTACTABILITY
PAYMENT_PROPENSITY_0_72
SMS_ALLOWED
NO_ACTIVE_ARRANGEMENT
```

---

# 59. Strategy Simulation

```text
Historical population
        |
        v
Current strategy
        |
        +--> baseline

Candidate strategy
        |
        +--> simulation

Compare:
- expected recovery
- contact volume
- cost
- customer outcomes
- operational capacity
```

---

# 60. Strategy Governance

Strategy lifecycle:

```text
DRAFT
 |
 v
TEST
 |
 v
SIMULATED
 |
 v
BUSINESS_APPROVED
 |
 v
RISK_APPROVED
 |
 v
SCHEDULED
 |
 v
ACTIVE
 |
 v
RETIRED
```

Activation must be auditable.

---

# 61. Artefact 11 — Security Architecture

## 61.1 Security layers

```text
+---------------------------------------------------------------+
| Identity / IAM                                                |
+---------------------------------------------------------------+
| API Security                                                  |
+---------------------------------------------------------------+
| Service Identity                                              |
+---------------------------------------------------------------+
| Network Security                                              |
+---------------------------------------------------------------+
| Data Security                                                 |
+---------------------------------------------------------------+
| Application Security                                          |
+---------------------------------------------------------------+
| Audit / Monitoring                                            |
+---------------------------------------------------------------+
| Governance / Compliance                                       |
+---------------------------------------------------------------+
```

---

# 62. Identity

Use enterprise IAM with:

- OIDC
- OAuth2
- Short-lived tokens
- MFA for human users
- Workload identity for services
- Role-based access
- Attribute-based access where required

Avoid long-lived shared credentials.

---

# 63. Service-to-Service Security

Preferred:

```text
Service A
   |
   | workload identity
   v
Service B
```

Use:

- mTLS where required
- service identity
- authorization policies
- scoped credentials

---

# 64. API Gateway

Responsibilities:

- Authentication
- Authorization
- Rate limiting
- Request validation
- Routing
- API versioning
- Threat detection
- Correlation ID injection

The gateway must not contain business rules.

---

# 65. Network Architecture

```text
Internet
   |
WAF
   |
API Gateway
   |
Private Services
   |
Private Data Layer
```

Data services should not be publicly exposed.

---

# 66. Secrets

Secrets include:

- SFTP private keys
- Database credentials
- API credentials
- Encryption keys
- Partner credentials

Store in enterprise secrets management.

Never:

- Git commit secrets
- Put credentials in container images
- Put secrets in Airflow DAG code
- Put secrets in logs

---

# 67. Data Classification

At minimum:

```text
PUBLIC
INTERNAL
CONFIDENTIAL
RESTRICTED
```

Customer/financial information should generally be treated as high sensitivity according to the bank's classification policy.

---

# 68. Encryption

Use encryption:

```text
Client
 |
TLS
 |
API
 |
TLS
 |
Service
 |
TLS
 |
Database
```

At rest:

- Database encryption
- Object-storage encryption
- Warehouse encryption
- Backup encryption

Keys should be managed through centralized key management.

---

# 69. PII Protection

Use:

- Tokenization where appropriate
- Masking
- Restricted views
- Row/column-level access
- Data minimization
- Purpose-based access
- Controlled exports

Example:

```text
Analyst view

customer_id
segment
dpd
balance

No direct phone/email unless explicitly authorized.
```

---

# 70. Audit

Audit events:

```text
WHO
WHAT
WHEN
WHERE
WHY
RESULT
```

For decisions additionally:

```text
strategy
strategy_version
rule_version
model_version
input_reference
reason_codes
```

Audit data must be protected from unauthorized modification.

---

# 71. Security Monitoring

Monitor:

- Failed authentication
- Privilege changes
- Bulk data access
- Unusual exports
- Administrative actions
- API anomalies
- Service identity misuse
- SFTP failures
- Secret access

---

# 72. Artefact 12 — Migration Wave Plan

## 72.1 Migration principles

1. No big bang.
2. Establish data parity before functional cutover.
3. Run shadow mode.
4. Migrate by portfolio.
5. Reconcile every wave.
6. Preserve rollback capability.
7. Keep legacy ownership explicit.
8. Retire legacy only after operational evidence.

---

# 73. Wave 0 — Discovery

Duration: typically several weeks to a few months depending on estate size.

Deliverables:

- Application inventory
- Source inventory
- Data lineage
- Rule inventory
- Strategy inventory
- SFTP catalogue
- Batch catalogue
- API catalogue
- Report catalogue
- Regulatory dependency map

Exit criteria:

- Critical dependencies identified
- Data owners identified
- Business owners identified
- Migration candidate populations defined

---

# 74. Wave 1 — Platform Foundation

Build:

```text
Kubernetes
CI/CD
IAM
Secrets
Observability
Event Bus
Object Storage
PostgreSQL-compatible platform
Airflow
Snowflake
dbt
Ingestion Control Plane
```

Exit criteria:

- Platform operational
- Security approved
- Monitoring operational
- Backup/recovery tested

---

# 75. Wave 2 — Ingestion

Prioritize:

1. Core account data
2. Customer data
3. Payment data
4. Delinquency
5. Product data
6. Historical collection data

Build:

```text
CDC
SFTP
CSV
API
```

Exit criteria:

- Source reconciliation passes
- CDC lag within target
- File pipelines meet SLA
- Data quality baseline established

---

# 76. Wave 3 — Analytical Parity

Build Snowflake/dbt models.

Compare:

```text
Legacy report
vs
New data product
```

For example:

```text
Daily overdue balance
Legacy = 125,230,100
New    = 125,230,100
Diff   = 0
```

Do this before replacing operational decisioning.

---

# 77. Wave 4 — Delinquency and Case Foundation

Implement:

- Account
- Debt
- Delinquency
- Case
- Assignment
- Basic lifecycle

Run in shadow mode.

---

# 78. Wave 5 — Strategy and Decisioning

Implement:

- Eligibility
- Rules
- Strategy versions
- Decision API
- Reason codes
- Audit
- Simulation

Run:

```text
Legacy decision
vs
New decision
```

Measure differences.

---

# 79. Wave 6 — Treatment Execution

Add:

- SMS
- Email
- Collector tasks
- Contact centre integration
- Payment arrangement

Initially limit execution to a controlled population.

---

# 80. Wave 7 — Parallel Run

Example:

```text
Portfolio
100%

Legacy execution: 90%
New execution:    10%
```

Then:

```text
80/20
60/40
40/60
20/80
0/100
```

Do not increase allocation simply because technical tests pass.

Review:

- Customer outcomes
- Recovery
- Complaints
- Operational workload
- Reconciliation
- Incident rates

---

# 81. Wave 8 — Agency and Recovery

Migrate:

- Agency placement
- Recall
- Recovery
- Agency reconciliation
- Fee calculation
- Performance reporting

---

# 82. Wave 9 — Legal

Migrate legal workflows separately.

Reason:

- Different operational process
- Different external dependencies
- Higher risk
- Potentially lower volume
- More complex case lifecycle

---

# 83. Wave 10 — Optimization

Once sufficient clean historical data exists:

```text
Rules
  |
  v
Scoring
  |
  v
Optimization
  |
  v
Champion/challenger
```

Optimization should come after reliable foundational decisioning.

---

# 84. Wave 11 — Advanced Digital Collections

Add:

- Self-service
- Payment portal
- Digital arrangements
- Real-time decisioning
- Event-driven triggers
- Personalized treatment

---

# 85. Wave 12 — Legacy Retirement

Retire legacy components only when:

- All required portfolios migrated
- Reconciliation stable
- Reporting migrated
- Regulatory dependencies migrated
- Operational runbooks complete
- Historical data retained
- Audit access preserved
- Rollback window expired

---

# 86. Migration Decision Matrix

| Capability | Migration strategy |
|---|---|
| Customer data | Replicate + validate |
| Account data | CDC + snapshot |
| Payments | CDC/API + reconciliation |
| SFTP feeds | Redirect through new ingestion platform |
| Delinquency | Recalculate + compare |
| Cases | Historical migration + new lifecycle |
| Strategies | Translate to canonical model |
| Rules | Re-implement + parity test |
| Models | Re-platform or wrap |
| Communications | Adapter migration |
| Agencies | Parallel integration |
| Legal | Separate migration |
| Reporting | Rebuild in dbt/Snowflake |
| Historical data | Preserve in analytical/archive layer |

---

# 87. Strategy Translation

Legacy strategy:

```text
IF DPD > 30
AND balance > 500
THEN CALL
```

New representation:

```text
Strategy:
EARLY_COLLECTION_V17

Eligibility:
DPD > 30
Balance > 500

Treatment:
CALL

Reason:
BALANCE_HIGH_DPD_30
```

The original rule must remain traceable.

---

# 88. Rule Parity Testing

For historical population:

```text
Legacy result
New result
Difference
```

Categorize differences:

```text
EXPECTED
DATA DIFFERENCE
RULE TRANSLATION ERROR
MISSING RULE
LEGACY BUG
NEW BUG
```

Do not assume 100% matching is always desirable; every difference needs an explanation.

---

# 89. Migration Rollback

Every wave must define:

```text
Rollback trigger
Rollback owner
Rollback procedure
Rollback data reconciliation
Customer-impact procedure
Communication procedure
```

Example trigger:

```text
Recovery performance < agreed threshold
AND
technical cause unresolved
```

---

# 90. Production Readiness Checklist

## Application

- [ ] API contracts approved
- [ ] Event contracts approved
- [ ] Error handling tested
- [ ] Idempotency tested
- [ ] Load tested
- [ ] Security tested

## Data

- [ ] Data contracts approved
- [ ] dbt tests passing
- [ ] Reconciliation passing
- [ ] Lineage available
- [ ] Retention configured

## Operations

- [ ] Alerts configured
- [ ] Dashboards configured
- [ ] Runbooks complete
- [ ] On-call defined
- [ ] DR tested

## Migration

- [ ] Rollback tested
- [ ] Parallel run completed
- [ ] Business sign-off
- [ ] Compliance sign-off

---

# 91. Recommended Repository Structure

A modern platform can use multiple repositories or a carefully managed monorepo.

Example:

```text
platform/
  api-contracts/
  event-contracts/
  ingestion/
  services/
    customer/
    account/
    debt/
    delinquency/
    case/
    strategy/
    decision/
    treatment/
    arrangement/
    payment/
    recovery/
    agency/
    legal/
  data/
    dbt/
  airflow/
  infrastructure/
  deployment/
  security/
  documentation/
```

---

# 92. Go Service Structure

Example:

```text
case-service/
  cmd/
    server/
  internal/
    domain/
    application/
    ports/
    adapters/
      postgres/
      kafka/
      http/
  migrations/
  api/
  tests/
```

Prefer:

```text
Domain
  |
Application
  |
Ports
  |
Adapters
```

This reduces infrastructure coupling.

---

# 93. Deployment Architecture

```text
                         Kubernetes Cluster
+----------------------------------------------------------------+
|                                                                |
|  API Gateway                                                   |
|       |                                                        |
|       +--> customer-service                                    |
|       +--> account-service                                     |
|       +--> case-service                                        |
|       +--> decision-service                                    |
|       +--> arrangement-service                                 |
|       +--> payment-service                                     |
|       +--> recovery-service                                    |
|                                                                |
|  Ingestion Workers                                             |
|  Event Consumers                                               |
|                                                                |
+----------------------------------------------------------------+
       |               |                |
       v               v                v
 PostgreSQL        Event Bus       Object Storage
                                         |
                                         v
                                     Snowflake
                                         |
                                         v
                                         dbt
```

---

# 94. Kubernetes Principles

Use Kubernetes for:

- Service deployment
- Scaling
- Health management
- Rolling deployments
- Configuration references
- Workload identity

Avoid putting every batch/data process into Kubernetes if Airflow or managed data services are more appropriate.

---

# 95. API and Event Contract Repository

Keep contracts centrally governed:

```text
contracts/
  openapi/
    customer.yaml
    account.yaml
    case.yaml
    decision.yaml

  asyncapi/
    case-events.yaml
    payment-events.yaml
    arrangement-events.yaml

  schemas/
    customer/
    account/
    payment/
```

CI should validate compatibility.

---

# 96. Observability Architecture

```text
Go services
Airflow
Ingestion
Kubernetes
Snowflake
 |
 v
OpenTelemetry
 |
 +--> traces
 +--> metrics
 +--> logs
 |
 v
Observability platform
```

Correlation should survive:

```text
SFTP file
 -> ingestion job
 -> Snowflake load
 -> dbt run
 -> decision batch
 -> case creation
```

---

# 97. Operational Correlation Example

```text
correlationId = COR-123

SFTP file
  |
  +-- fileId = FILE-99
       |
       +-- ingestionJob = ING-100
            |
            +-- dbtRun = DBT-22
                 |
                 +-- decisionBatch = DEC-77
                      |
                      +-- case = CASE-88
```

This is essential for incident investigation.

---

# 98. Modernization Without Lock-In

The strongest anti-lock-in architecture is not simply "use open source."

It is:

```text
Stable domain contracts
+
Stable data contracts
+
Portable infrastructure interfaces
+
Replaceable adapters
+
Automated migration/reconciliation
```

For example:

```text
Decision Contract
       |
       +--> Internal rules
       +--> FICO
       +--> Custom ML
       +--> Other provider
```

The domain does not change.

---

# 99. Vendor Evaluation Framework

If an external product is evaluated later, score it against:

| Dimension | Weight |
|---|---:|
| Domain capability | 20% |
| API openness | 10% |
| Data portability | 10% |
| Event support | 5% |
| Configuration/versioning | 10% |
| Integration | 10% |
| Security | 10% |
| Operational maturity | 10% |
| Cost | 10% |
| Exit/migration capability | 5% |

A product that performs well functionally but poorly on data portability should not become the platform's system of record.

---

# 100. Key Architecture Risks

## Risk 1 — Over-engineering

Too many microservices create operational complexity.

Mitigation:

- Start with bounded contexts
- Use modular services
- Split only where justified

---

## Risk 2 — Rebuilding commercial products poorly

Mitigation:

- Start with core differentiating capabilities
- Benchmark enterprise products
- Use mature infrastructure components
- Prioritize configuration and operational quality

---

## Risk 3 — Excessive vendor lock-in through Snowflake

Mitigation:

- dbt
- portable SQL where practical
- canonical models
- object storage raw layer
- avoid embedding domain logic in warehouse-specific procedures

---

## Risk 4 — Airflow becomes a business workflow engine

Mitigation:

- Airflow only for data/batch orchestration
- Domain state remains in services

---

## Risk 5 — Decisioning becomes ungoverned code

Mitigation:

- Versioned strategy
- Versioned rules
- Decision audit
- Simulation
- Approval lifecycle

---

## Risk 6 — Data quality prevents migration

Mitigation:

- Reconciliation first
- Source profiling
- Data contracts
- Quarantine
- Historical parity

---

# 101. Recommended Technology Baseline

```text
                    EXPERIENCE
                         |
                   API Gateway
                         |
              +----------+----------+
              |                     |
          REST APIs             Events
              |                     |
              v                     v
             Go                 Kafka-compatible
          Services                platform
              |
              v
       PostgreSQL-compatible
          operational DB
              |
              +--------------------------+
                                         |
                                         v
                                   Object Storage
                                         |
                                         v
                                     Snowflake
                                         |
                                         v
                                        dbt
                                         |
                                         v
                                   Analytics / ML

Airflow:
  ingestion orchestration
  batch
  dbt
  reconciliation
  backfills

Kubernetes:
  Go services
  ingestion workers
  event consumers

OpenTelemetry:
  traces / metrics / logs
```

---

# 102. Technology Alternative Considerations

## PostgreSQL vs distributed SQL

### PostgreSQL

Preferred initial choice.

Advantages:

- Mature
- Simple
- Strong transactional semantics
- Large ecosystem
- Easy local development
- Portable

Distributed SQL should be introduced only when workload characteristics justify it.

---

## Kafka vs managed messaging

Use a Kafka-compatible event model.

Whether Kafka is self-managed or managed should be an infrastructure decision.

The application contract remains:

```text
publish(event)
subscribe(topic)
checkpoint(offset)
```

---

## Kubernetes vs serverless

Kubernetes is appropriate for:

- Long-lived services
- Event consumers
- Ingestion workers
- Consistent runtime

Serverless can be appropriate for:

- Low-volume event handlers
- Scheduled utility jobs
- Certain integration adapters

Do not force one deployment model everywhere.

---

# 103. Data Architecture Decision

The recommended pattern is:

```text
Operational truth
        |
        v
Service-owned transactional state
        |
        v
Events
        |
        v
Analytical ingestion
        |
        v
Snowflake
        |
        v
dbt semantic/analytical models
```

Avoid:

```text
Snowflake
   ^
   |
All services directly read/write
```

That creates tight coupling and makes operational correctness difficult.

---

# 104. Decisioning Architecture Decision

Use a layered decision architecture:

```text
Policy
  >
Eligibility
  >
Segmentation
  >
Rules
  >
Models
  >
Optimization
  >
Treatment
```

Policy constraints must not be overridden by optimization.

---

# 105. Ingestion Architecture Decision

Use a common ingestion control plane:

```text
                  Source Registry
                       |
              +--------+--------+
              |        |        |
             CDC     SFTP      API
              |        |        |
              +--------+--------+
                       |
                Common contract
                       |
            +----------+----------+
            |                     |
       Object Storage          Event Bus
            |                     |
            +----------+----------+
                       |
                    Snowflake
```

---

# 106. Final End-to-End Example

A customer becomes delinquent.

```text
1. Core banking changes account status.

2. CDC connector captures the database change.

3. Ingestion platform validates and checkpoints the change.

4. Event is published.

5. Delinquency service updates current state.

6. DelinquencyChanged event is emitted.

7. Case service creates/updates a collection case.

8. CaseCreated event is emitted.

9. Decision service receives a decision request.

10. Strategy service identifies the active strategy.

11. Policy engine checks eligibility.

12. Rules evaluate the account.

13. Payment propensity model returns a score.

14. Optimization selects the treatment.

15. Decision audit records:
    - strategy version
    - rule version
    - model version
    - reason codes

16. Treatment service requests SMS.

17. Communication adapter sends the message.

18. Contact event is recorded.

19. Customer makes a payment.

20. Payment event is ingested.

21. Payment service allocates the payment.

22. Case state is updated.

23. Recovery metrics are updated.

24. Events and operational data reach Snowflake.

25. dbt updates analytical models.

26. Management reporting reflects the outcome.

27. Strategy analytics measure whether the treatment worked.
```

This demonstrates why the platform must be more than a case-management application. It is an integrated **data + decision + execution + recovery platform**.

---

# 107. Final Architecture Blueprint

```text
                                      BANKING ECOSYSTEM
                                             |
        +----------------------+-------------+----------------------+
        |                      |                                    |
        v                      v                                    v
    CORE SYSTEMS            PARTNERS                             CHANNELS
        |                      |                                    |
        +----------------------+------------------------------------+
                               |
                               v
                    +------------------------+
                    | INGESTION PLATFORM     |
                    |                        |
                    | CDC                    |
                    | SFTP / CSV             |
                    | API                    |
                    | Events                 |
                    | Validation             |
                    | Checkpoint             |
                    | Reconciliation         |
                    +-----------+------------+
                                |
                 +--------------+--------------+
                 |                             |
                 v                             v
          +-------------+                +-------------+
          | Object      |                | Event       |
          | Storage     |                | Platform    |
          +------+------+                +------+------+
                 |                              |
                 v                              |
          +-------------+                       |
          | Snowflake   |                       |
          | RAW         |                       |
          +------+------+                       |
                 |                              |
                 v                              |
              dbt                             |
                 |                              |
                 v                              |
          +-------------+                       |
          | Analytics   |                       |
          | / Features  |                       |
          +-------------+                       |
                                                |
                                                v
                              +-------------------------------+
                              | DOMAIN SERVICES               |
                              |                               |
                              | Customer                       |
                              | Account                        |
                              | Debt                           |
                              | Delinquency                    |
                              | Case                           |
                              | Arrangement                    |
                              | Payment                        |
                              | Recovery                       |
                              | Agency                         |
                              | Legal                          |
                              +---------------+---------------+
                                              |
                                              v
                              +-------------------------------+
                              | DECISION PLATFORM              |
                              |                               |
                              | Policy                         |
                              | Rules                          |
                              | Strategy                       |
                              | Models                         |
                              | Optimization                   |
                              +---------------+---------------+
                                              |
                                              v
                              +-------------------------------+
                              | TREATMENT / EXECUTION          |
                              |                               |
                              | SMS                            |
                              | Email                          |
                              | Phone                          |
                              | Collector                      |
                              | Arrangement                    |
                              | Agency                         |
                              | Legal                          |
                              +-------------------------------+

Airflow:
  Data orchestration / batch / dbt / reconciliation / backfill

Kubernetes:
  Domain services / ingestion workers / event consumers

OpenTelemetry:
  End-to-end observability

IAM:
  Human and workload identity

Audit:
  Immutable decision and operational history
```

---

# 108. Architecture Decision Summary

| # | Decision | Rationale |
|---:|---|---|
| 1 | Go for domain services | High-performance, simple operational model |
| 2 | PostgreSQL-compatible operational stores | Transactional correctness and portability |
| 3 | Kafka-compatible event architecture | Decoupling and replay |
| 4 | Airflow for batch/data orchestration | Mature data workflow platform |
| 5 | Snowflake for analytics | Enterprise-scale analytical platform |
| 6 | dbt for analytical transformation | Testing, lineage and code-based analytics |
| 7 | Dedicated ingestion platform | Common control plane for CDC/file/API/event |
| 8 | SFTP/CSV first-class support | Banking ecosystem compatibility |
| 9 | CDC abstraction | Migration and near-real-time integration |
| 10 | Strategy separate from execution | Business flexibility |
| 11 | Decision audit mandatory | Explainability and governance |
| 12 | Reconciliation first-class | Financial control |
| 13 | Domain-owned contracts | Vendor independence |
| 14 | Infrastructure adapters | Replaceability |
| 15 | Progressive migration | Reduced operational risk |

---

# 109. What Should Be Built First

The recommended engineering order is:

```text
Phase A
  Platform foundation
  IAM
  Kubernetes
  CI/CD
  Observability
  Secrets

Phase B
  Event platform
  Object storage
  PostgreSQL
  Snowflake
  Airflow
  dbt

Phase C
  Ingestion control plane
  CDC
  SFTP
  CSV
  API ingestion
  Reconciliation

Phase D
  Account
  Debt
  Delinquency
  Case

Phase E
  Strategy
  Decision
  Treatment

Phase F
  Arrangement
  Payment
  Recovery

Phase G
  Agency
  Legal
  Digital

Phase H
  ML
  Optimization
  Simulation
  Champion/challenger
```

---

# 110. Architecture Exit Criteria

The architecture is ready for detailed implementation when:

- Bounded contexts are approved.
- Service ownership is agreed.
- API contracts have owners.
- Event contracts have owners.
- Source systems are inventoried.
- Ingestion contracts are defined.
- Data ownership is defined.
- Reconciliation rules are defined.
- Security classification is defined.
- Decision governance is defined.
- Migration populations are defined.
- NFRs are quantified.
- Operational ownership is defined.
- Build-vs-buy decisions are recorded.

---

# 111. Final Recommendation

The platform should be treated as a strategic bank capability, not simply a replacement application.

The central architecture is:

```text
                         DATA
                          |
                          v
                     INGESTION
                          |
                          v
                    DOMAIN STATE
                          |
                          v
                    DECISIONING
                          |
                          v
                      TREATMENT
                          |
                          v
                      PAYMENT
                          |
                          v
                      RECOVERY
                          |
                          v
                    OUTCOME DATA
                          |
                          +----------------+
                                           |
                                           v
                                      ANALYTICS
                                           |
                                           v
                                      STRATEGY
                                           |
                                           +----> next decision
```

This creates a closed-loop collections platform:

> **Sense → Understand → Decide → Act → Measure → Learn → Improve**

The critical anti-lock-in boundary is:

```text
             OUR PLATFORM CONTRACTS
+------------------------------------------------+
| Domain                                         |
| APIs                                           |
| Events                                         |
| Strategy                                       |
| Decision                                       |
| Data                                           |
| Audit                                          |
| Reconciliation                                 |
+------------------------+-----------------------+
                         |
                    Adapter layer
                         |
+------------------------+-----------------------+
| Replaceable technology                         |
| CDC | Kafka | SFTP | Cloud | Snowflake | ML   |
+------------------------------------------------+
```

This allows the platform to evolve from a legacy migration solution into a long-term enterprise Collections Decision & Execution Platform without making any single infrastructure or collections software vendor the owner of the bank's core business capability.
