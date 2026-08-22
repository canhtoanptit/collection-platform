# Enterprise Collections & Debt Management Platform
## Vendor-Neutral Target Architecture and Detailed Solution Design

**Document status:** Architecture Proposal  
**Date:** 22 August 2026  
**Scope:** Enterprise banking collections and debt-management platform  
**Primary technologies discussed:** Go, Airflow, Snowflake, dbt, SFTP/CSV, CDC, APIs/events  
**Strategic principle:** Build the bank-owned platform and avoid vendor lock-in.

---

# 1. Executive Summary

This document proposes a bank-owned, vendor-neutral Collections & Debt Management Platform designed to replace or modernize a legacy collections estate.

The platform is intended to provide end-to-end capabilities across:

- Customer and account delinquency management
- Collection case management
- Collection strategy and treatment management
- Rules and decisioning
- Customer contact and engagement
- Payment arrangements and promises to pay
- Recovery
- Debt collection agency management
- Legal recovery
- Digital/self-service integration
- Analytics and management information
- Predictive modelling and optimization
- Audit, compliance, reconciliation and operational controls
- Batch, file, API, event and database-change-data-capture ingestion

The architecture deliberately separates:

1. **Domain capabilities** — what the collections business does.
2. **Decisioning** — why a particular treatment is selected.
3. **Execution** — how the selected treatment is performed.
4. **Integration and ingestion** — how external data enters/leaves the platform.
5. **Data and analytics** — how historical information is stored and analyzed.
6. **Orchestration** — how batch and data workflows are coordinated.

The proposed technology model is:

- **Go** for operational domain services and APIs.
- **Airflow** for batch/data orchestration.
- **Snowflake** for analytical storage and compute.
- **dbt** for governed analytical transformations.
- **SFTP/CSV ingestion** for traditional banking file interfaces.
- **CDC ingestion** for database replication and legacy migration.
- **API/event ingestion** for real-time integrations.
- **Object storage** for immutable file/raw-data landing.
- **Event bus** for decoupled domain and ingestion events.
- **Configurable decisioning services** for rules, segmentation, scoring and optimization.

The core architectural decision is that **no external product or infrastructure technology becomes the domain contract**. The bank owns the domain model, APIs, event schemas, strategy definitions, audit model, data contracts and decision contracts.

This allows an implementation component to be replaced without redesigning the Collections domain.

---

# 2. Strategic Context

Enterprise collections platforms such as EXUS and FICO provide mature capabilities across collections, decisioning, customer engagement, recovery, optimization and related areas.

A commercial product can accelerate implementation, but creates varying degrees of:

- Vendor dependency
- Product roadmap dependency
- Proprietary data models
- Proprietary decisioning models
- Licensing costs
- Integration constraints
- Upgrade dependency
- Migration complexity if the vendor is eventually replaced

The strategic objective of this proposal is therefore:

> Build a bank-owned Collections & Debt Management Platform that provides comparable enterprise capability while retaining ownership of the domain, data, decision policies, integration contracts and operational architecture.

This does **not** mean implementing every infrastructure component from scratch.

The platform should own the architecture and contracts while reusing proven underlying technology where appropriate.

---

# 3. Architectural Principles

## 3.1 Domain ownership

The bank owns:

- Customer/debt domain model
- Collection case model
- Strategy model
- Treatment model
- Decision contract
- Event contracts
- Data contracts
- Audit model
- Reconciliation model

No infrastructure vendor should define the business domain.

---

## 3.2 Vendor neutrality

Infrastructure components must be replaceable.

For example:

```text
Decision API
    |
    +-- Rules implementation A
    +-- Rules implementation B
    +-- Custom Go implementation
```

Similarly:

```text
File Transfer Interface
    |
    +-- SFTP
    +-- Object Storage
    +-- Managed file transfer
```

And:

```text
Data Platform Contract
    |
    +-- Snowflake
    +-- Other analytical warehouse
    +-- Lakehouse
```

The platform should not expose implementation-specific semantics to domain consumers.

---

## 3.3 API-first

Every major capability exposes a well-defined API.

Examples:

- Account API
- Delinquency API
- Case API
- Strategy API
- Decision API
- Treatment API
- Arrangement API
- Payment API
- Agency API
- Recovery API
- File ingestion API
- Reconciliation API

---

## 3.4 Event-driven where appropriate

Business events are first-class integration mechanisms.

Examples:

- `AccountBecameDelinquent`
- `DelinquencyBucketChanged`
- `CollectionCaseCreated`
- `StrategyAssigned`
- `TreatmentSelected`
- `ContactAttempted`
- `PromiseToPayCreated`
- `PaymentReceived`
- `ArrangementBroken`
- `CaseResolved`
- `DebtPlacedWithAgency`
- `RecoveryCompleted`

Events should be versioned and replayable.

---

## 3.5 At-least-once delivery with idempotency

The platform should not rely on an unrealistic assumption of global exactly-once processing.

Instead:

> Use at-least-once delivery and idempotent consumers.

Every important event/action has a unique identifier.

Duplicate delivery must produce the same business result rather than duplicate customer actions.

---

## 3.6 Immutable auditability

Important decisions must be reconstructable.

For every material collection decision, retain:

- Customer/account context
- Decision timestamp
- Strategy version
- Rule version
- Model version
- Input data snapshot/reference
- Policy version
- Decision result
- Reason codes
- Actor/system
- Correlation ID

---

## 3.7 Data is a product

Collections data should have:

- Ownership
- Data contracts
- Quality rules
- Lineage
- Classification
- Retention
- Reconciliation
- Access controls

---

# 4. Functional Requirements

## 4.1 Customer and account management

The platform must be able to consume and maintain collection-relevant information about:

- Customers
- Accounts
- Products
- Balances
- Due amounts
- Payment history
- Delinquency
- Customer segmentation
- Communication preferences
- Contact restrictions
- Relevant vulnerability/assistance indicators where legally and operationally appropriate

The platform should not necessarily become the enterprise customer master.

It should maintain the minimum operational representation required for collections.

---

# 5. Delinquency Management

The platform must support:

- Identification of delinquency
- Days past due
- Delinquency buckets
- Amount overdue
- Current balance
- Aging
- Product-specific delinquency rules
- Status transitions
- Cure detection
- Re-default detection
- Historical delinquency

Example:

```text
CURRENT
   |
   v
1-30 DPD
   |
   v
31-60 DPD
   |
   v
61-90 DPD
   |
   v
90+ DPD
```

Actual buckets must be configurable rather than hard-coded.

---

# 6. Collection Case Management

A collection case represents the operational work associated with an account/customer debt situation.

Example:

```text
CollectionCase
 |
 +-- caseId
 +-- customerId
 +-- accountId
 +-- openedAt
 +-- status
 +-- stage
 +-- strategy
 +-- priority
 +-- assignedTeam
 +-- assignedCollector
 +-- nextAction
 +-- nextActionAt
 +-- outcome
```

Capabilities:

- Open case
- Assign case
- Reassign case
- Escalate case
- Suspend case
- Resume case
- Close case
- Reopen case
- Track activities
- Track customer contacts
- Track arrangements
- Track outcomes

---

# 7. Strategy Management

Strategy management is one of the most important platform capabilities.

The platform must support configurable strategies based on attributes such as:

- DPD
- Product
- Balance
- Risk segment
- Propensity
- Previous treatment
- Customer interaction history
- Payment history
- Arrangement status
- Contactability
- Regulatory constraints
- Customer treatment constraints
- Operational capacity

A strategy should not require a Go deployment for every business change.

Conceptually:

```text
Customer/account context
        |
        v
Segmentation
        |
        v
Policy/rules
        |
        v
Scores/models
        |
        v
Constraints
        |
        v
Optimization
        |
        v
Treatment
```

---

# 8. Decisioning Requirements

The decisioning platform must support:

- Deterministic rules
- Segmentation
- Score consumption
- ML model consumption
- Policy constraints
- Treatment eligibility
- Channel selection
- Priority
- Next-best-action
- Strategy versioning
- Simulation
- Champion/challenger
- A/B testing where permitted
- Decision explanation
- Decision audit

Example request:

```json
{
  "customerId": "C123",
  "accountId": "A456",
  "delinquency": {
    "dpd": 35,
    "amountDue": 500
  },
  "context": {
    "previousContacts": 2,
    "previousPromiseBroken": false
  }
}
```

Example result:

```json
{
  "decision": "CONTACT",
  "channel": "SMS",
  "strategy": "EARLY_COLLECTION_V2",
  "strategyVersion": "17",
  "ruleVersion": "42",
  "modelVersion": "8",
  "reasonCodes": [
    "DPD_31_60",
    "CHANNEL_ALLOWED"
  ]
}
```

---

# 9. Treatment Execution

Decisioning selects a treatment.

Execution performs it.

This separation is critical.

```text
Decision
   |
   v
Treatment
   |
   +-- SMS
   +-- Email
   +-- Phone
   +-- Letter
   +-- Digital notification
   +-- Payment arrangement
   +-- Collector task
   +-- DCA placement
   +-- Legal escalation
```

The communication channel itself should be abstracted.

```text
Communication API
    |
    +-- Provider A
    +-- Provider B
    +-- Internal channel
```

---

# 10. Payment Arrangement Management

The platform should support:

- Promise to pay
- Arrangement creation
- Arrangement modification
- Arrangement cancellation
- Arrangement completion
- Missed promise
- Broken arrangement
- Payment matching
- Arrangement history

Important distinction:

> A promise is a customer commitment; an arrangement is the structured payment plan.

---

# 11. Recovery Management

Recovery capabilities include:

- Recovery action
- Recovery outcome
- Payment attribution
- Recovery cost
- Recovery channel
- Recovery date
- Recovery status
- Recovery forecasting

Metrics include:

- Gross recovery
- Net recovery
- Recovery rate
- Cost to collect
- Cure rate
- Roll rate
- Time to cure

---

# 12. Debt Collection Agency Management

The platform should support:

- Agency onboarding
- Agency eligibility
- Debt placement
- Debt recall
- Agency allocation
- Agency performance
- Recovery attribution
- Fees/commission
- Reconciliation
- Agency communications
- Agency service-level monitoring

Example:

```text
Eligible debt
    |
    v
Placement strategy
    |
    v
Agency allocation
    |
    v
Agency activity
    |
    v
Recovery
    |
    v
Reconciliation
```

---

# 13. Legal Recovery

Legal recovery should be a separate bounded capability.

Potential functions:

- Legal eligibility
- Legal referral
- Legal case
- Legal status
- External legal provider
- Court-related information
- Recovery
- Closure

This should integrate through APIs/events rather than coupling legal workflows into the core case service.

---

# 14. Data Ingestion Requirements

The platform must support four major ingestion patterns.

```text
1. Database CDC
2. SFTP / CSV
3. API
4. Event streaming
```

All four should converge into common ingestion and data contracts.

---

# 15. Ingestion Platform

Create a dedicated:

> Collections Data Ingestion Platform

This is conceptually similar to a bank-specific DMS capability.

It should provide:

- Source registry
- Connector management
- CDC
- SFTP
- CSV
- API ingestion
- Event ingestion
- Schema validation
- Data validation
- Deduplication
- Checkpointing
- Error handling
- Dead-letter/quarantine
- Audit
- Reconciliation
- Monitoring
- Backfill
- Replay

---

# 16. Ingestion Architecture

```text
                         SOURCE SYSTEMS
                              |
       +----------------------+----------------------+
       |                      |                      |
       v                      v                      v
    Database              SFTP/CSV                API/Event
       |                      |                      |
       v                      v                      v
+----------------------------------------------------------+
|                INGESTION PLATFORM                       |
|                                                          |
| Source Registry                                          |
| Connector Manager                                        |
|                                                          |
| CDC | SFTP | CSV | REST | Event                         |
|                                                          |
| Validation                                               |
| Schema Management                                        |
| Deduplication                                            |
| Checkpointing                                            |
| Audit                                                    |
| Reconciliation                                           |
|                                                          |
+---------------------------+------------------------------+
                            |
             +--------------+--------------+
             |                             |
             v                             v
       Object Storage                 Event Bus
             |                             |
             v                             v
         Snowflake                    Go Services
```

---

# 17. CDC Requirements

Database ingestion should support:

- Initial snapshot
- CDC capture
- Checkpointing
- Transaction ordering
- Schema changes
- Restart
- Backfill
- Replay
- Source lag monitoring
- Reconciliation

Flow:

```text
Source DB
   |
   +-- Snapshot
   |
   +-- Transaction log
          |
          v
       CDC Engine
          |
          v
      Event stream
          |
          +--> Snowflake
          |
          +--> Services
```

---

# 18. DMS-like Design Decision

## Requirement

The platform needs a reusable capability for:

- Database replication
- Legacy migration
- Initial loads
- CDC
- SFTP
- APIs
- Event feeds

## Decision

Build an internal **ingestion control plane and abstraction**, but do not implement every database log parser from scratch.

Use proven connector engines underneath where appropriate.

## Rationale

Implementing Oracle/SQL Server/Postgres transaction-log parsing independently would create a very large maintenance burden.

Our differentiation is:

- Source registry
- Governance
- Contracts
- Checkpointing
- Reconciliation
- Canonical model
- Audit
- Operational controls

not database-log parsing.

---

# 19. SFTP/CSV Requirements

SFTP is a first-class banking integration mechanism.

The platform must support:

- SSH key authentication
- Host-key verification
- Encryption
- PGP decryption/encryption where required
- File naming conventions
- File arrival detection
- Checksum
- Duplicate detection
- Schema validation
- Header/trailer validation
- Control totals
- Row validation
- Quarantine
- Archive
- Replay
- File-level audit

---

# 20. File Registry

Maintain a registry:

```text
file_id
source_system
file_name
file_type
business_date
received_timestamp
file_size
checksum
schema_version
status
row_count
processed_timestamp
error_code
```

Possible states:

```text
RECEIVED
VALIDATING
QUARANTINED
PROCESSING
PROCESSED
FAILED
ARCHIVED
DUPLICATE
```

---

# 21. CSV Validation

CSV files may include:

- Header
- Data records
- Trailer
- Record counts
- Control totals
- Schema version

Example:

```text
HEADER,LOAN,20260822,1245231
DATA,A123,C456,500.00
DATA,A124,C457,1200.50
...
TRAILER,1245231,XXXXXX
```

Validation must verify:

```text
header valid
schema valid
record count correct
control totals correct
mandatory fields present
data types correct
business rules valid
trailer valid
```

---

# 22. Quarantine

Invalid data must not silently enter production.

```text
File
 |
 v
Validation
 |
 +---- PASS ----> Process
 |
 +---- FAIL ----> Quarantine
                     |
                     +--> Alert
                     +--> Investigation
                     +--> Correct/reprocess
```

The original file must remain available according to retention policy.

---

# 23. Airflow

Airflow is the orchestration layer.

It should coordinate:

- Batch ingestion
- File processing
- Backfills
- Data loading
- dbt execution
- Reconciliation
- Batch decisioning
- Dependency management
- Retries
- SLA monitoring

It should not own:

- Core collections business rules
- Long-running customer workflows
- Case state
- Decision policy definitions
- Transactional business state

---

# 24. Example Airflow DAG

```text
start
 |
 v
check_source
 |
 v
trigger_ingestion
 |
 v
wait_for_ingestion
 |
 v
validate_ingestion
 |
 v
load_snowflake
 |
 v
dbt_staging
 |
 v
dbt_intermediate
 |
 v
prepare_collection_population
 |
 v
invoke_decision_batch
 |
 v
load_outcomes
 |
 v
dbt_marts
 |
 v
dbt_test
 |
 v
reconciliation
 |
 v
publish_success
```

---

# 25. Airflow vs Domain Workflow

This separation is mandatory.

### Airflow workflow

```text
Daily data
 -> ingest
 -> transform
 -> score
 -> reconcile
```

### Collection workflow

```text
Case created
 -> strategy assigned
 -> treatment
 -> contact
 -> promise
 -> payment
 -> resolve
```

Airflow orchestrates the first.

A domain workflow mechanism/service owns the second.

---

# 26. Go Services

Potential bounded contexts:

```text
Customer Context
Account Context
Debt Context
Delinquency Context
Case Context
Strategy Context
Decision Context
Treatment Context
Contact Context
Arrangement Context
Payment Context
Recovery Context
Agency Context
Legal Context
```

Avoid creating services solely because a noun exists.

The service boundaries should follow:

- Business ownership
- Transaction boundaries
- Change frequency
- Scaling needs
- Data ownership
- Security boundaries

---

# 27. Operational Data Ownership

Each service owns its operational state.

Example:

```text
Case Service
  |
  +-- case state
  +-- assignment
  +-- lifecycle

Arrangement Service
  |
  +-- arrangement state
  +-- promise state
```

Other services should access state through APIs/events rather than directly querying another service's database.

---

# 28. Event Architecture

Domain events:

```text
AccountBecameDelinquent
DelinquencyChanged
CollectionCaseCreated
CaseAssigned
StrategyAssigned
TreatmentSelected
ContactAttempted
ContactCompleted
PromiseCreated
PromiseBroken
PaymentReceived
ArrangementCreated
ArrangementBroken
DebtPlaced
DebtRecalled
RecoveryRecorded
CaseResolved
```

Event envelope:

```json
{
  "eventId": "uuid",
  "eventType": "PaymentReceived",
  "eventVersion": 1,
  "aggregateType": "Account",
  "aggregateId": "A123",
  "occurredAt": "2026-08-22T08:00:00Z",
  "correlationId": "uuid",
  "producer": "payment-service",
  "payload": {}
}
```

---

# 29. Event Versioning

Never modify event meaning invisibly.

Use:

```text
PaymentReceived.v1
PaymentReceived.v2
```

Consumers must support compatibility rules.

Schema evolution must be governed.

---

# 30. Snowflake Architecture

Recommended layers:

```text
RAW
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
 +-- Collections
 +-- Recovery
 +-- Strategy
 +-- Customer
 +-- Agency
```

---

# 31. Raw Layer

Preserve source information.

Examples:

```text
raw_customer
raw_account
raw_payment
raw_transaction
raw_delinquency
raw_collection_event
raw_agency
```

The raw layer should preserve enough information for traceability and replay, subject to security and retention requirements.

---

# 32. Staging Layer

Standardize:

- Names
- Data types
- Timestamps
- Codes
- Null semantics
- Source-specific quirks

Examples:

```text
stg_account
stg_payment
stg_customer
stg_delinquency
```

---

# 33. Intermediate Layer

Build reusable concepts:

```text
int_account_delinquency
int_customer_exposure
int_collection_case
int_contact_history
int_payment_arrangement
int_collection_outcome
```

---

# 34. Mart Layer

Examples:

```text
fct_collection_action
fct_collection_case
fct_payment_arrangement
fct_recovery
fct_contact
fct_agency_placement

dim_customer
dim_account
dim_product
dim_strategy
dim_agency
dim_date
```

---

# 35. dbt Requirements

dbt should provide:

- Transformation as code
- Tests
- Documentation
- Lineage
- CI/CD
- Reusable macros
- Model ownership
- Source definitions
- Data contracts
- Incremental processing where appropriate

Airflow schedules/orchestrates dbt.

dbt owns analytical transformation logic.

---

# 36. Operational vs Analytical Data

Do not use Snowflake as the primary operational transaction store.

```text
Operational:
Go services
Transactional DB
Current state

Analytical:
Snowflake
Historical state
Aggregations
ML features
Reporting
```

Events connect the two.

---

# 37. Data Quality

Data quality should exist at multiple layers.

## Ingestion

- File checksum
- Schema
- Record count
- Control total

## Staging

- Type validity
- Required fields
- Accepted values

## Business

- Account exists
- Customer exists
- Payment references valid account
- Arrangement dates valid

## Reconciliation

- Source vs target counts
- Amounts
- Balances
- Cases
- Payments

---

# 38. Reconciliation

Reconciliation is a first-class platform service.

```text
Source
  |
  +-- count
  +-- amount
  +-- balance
       |
       v
Target
  |
  +-- count
  +-- amount
  +-- balance
       |
       v
Reconciliation
       |
       +-- PASS
       +-- WARNING
       +-- FAIL
```

For financial data, reconciliation should be explicit rather than inferred from pipeline success.

---

# 39. Decision Audit

For each decision:

```text
decision_id
customer_id
account_id
decision_time
strategy_id
strategy_version
rule_version
model_version
input_reference
decision
reason_codes
outcome
```

This provides traceability.

---

# 40. Simulation

Strategy changes should support simulation before production.

```text
Historical population
       |
       v
New strategy
       |
       v
Simulation
       |
       +-- expected contacts
       +-- expected payments
       +-- expected recovery
       +-- cost
       +-- customer impact
```

This allows business users to compare:

```text
Current strategy
vs
Candidate strategy
```

before activation.

---

# 41. Champion/Challenger

The platform should eventually support:

```text
Population
   |
   +---- Champion 90%
   |
   +---- Challenger 10%
```

Compare:

- Cure rate
- Recovery
- Cost
- Contact rate
- Promise rate
- Customer outcomes

Subject to policy and governance.

---

# 42. ML Platform Integration

The collections platform should consume models through a model contract.

```text
Decision Service
      |
      v
Model API
      |
      +-- Risk model
      +-- Payment propensity
      +-- Contact propensity
      +-- Recovery model
      +-- Channel model
```

The Go services should not embed model implementation details.

---

# 43. Optimization

Optimization should consider:

- Expected recovery
- Cost
- Channel capacity
- Contact restrictions
- Customer treatment policies
- Operational capacity
- Risk
- Regulatory constraints

Conceptually:

```text
maximize expected value

subject to:
  compliance constraints
  customer constraints
  capacity constraints
  strategy constraints
```

---

# 44. Security

Security requirements include:

- Encryption in transit
- Encryption at rest
- Least privilege
- Service identity
- Secrets management
- Network segmentation
- Role-based access
- Attribute-based access where required
- PII masking
- Data classification
- Audit logging
- Key rotation
- Privileged-access monitoring

---

# 45. PII

Collections data contains highly sensitive customer information.

Use:

```text
Production operational data
        |
        +-- tightly controlled
        |
        v
Snowflake
        |
        +-- restricted raw
        +-- masked analytical views
        +-- controlled access
```

Analysts should not automatically receive unrestricted customer-identifying information.

---

# 46. Resilience

Critical services should support:

- Horizontal scaling
- Health checks
- Timeouts
- Retries
- Circuit breakers
- Idempotency
- Dead-letter handling
- Disaster recovery
- Backup
- Restore testing

---

# 47. Failure Model

Every integration must explicitly define:

```text
What happens if:
- source is unavailable?
- file is late?
- file is duplicated?
- file is corrupted?
- schema changes?
- downstream is unavailable?
- event is duplicated?
- event arrives out of order?
- decision service fails?
- payment is received twice?
```

A production architecture is incomplete without these answers.

---

# 48. Backfill

The platform must support historical backfill.

Examples:

```text
Backfill January 2025
Backfill one product
Backfill one source
Backfill one account population
Replay one event type
```

Backfills must be isolated from normal production processing.

---

# 49. Replay

Event-driven architecture should support controlled replay.

Example:

```text
Events
  |
  v
Filter:
eventType = PaymentReceived
date = 2026-08-01
  |
  v
Replay
  |
  v
Consumer
```

Replay must be safe through idempotency.

---

# 50. Observability

Every request/event/file should have:

```text
correlationId
traceId
source
timestamp
```

Monitor:

### Ingestion

- Files received
- Files late
- CDC lag
- API failure
- Records rejected
- Duplicate records

### Services

- Latency
- Error rate
- Throughput
- Queue depth

### Decisioning

- Decision latency
- Rule errors
- Model errors
- Decision volume

### Collections

- Cases created
- Cases closed
- Actions
- Contact rates
- Arrangements
- Payments

---

# 51. Operational Dashboard

The platform should provide a single operational view.

```text
INGESTION
SFTP feeds       98%
CDC lag          2 sec
API failures     0.2%

PIPELINES
Successful       99.8%
Failed           0.2%

COLLECTIONS
Cases today      125,400
Actions          310,200
Arrangements     14,500

DECISIONING
Decisions        310,200
Failures         0.01%
```

---

# 52. Technology Decisions

## Go

### Requirement

High-throughput operational APIs and domain services.

### Decision

Use Go.

### Why

- Strong concurrency
- Good performance
- Simple deployment
- Strong typing
- Good cloud/container support
- Mature networking ecosystem

### Alternatives

**Java/Spring**

Pros:
- Mature banking ecosystem
- Large talent pool
- Strong enterprise libraries

Cons:
- More framework complexity
- Higher runtime footprint

**.NET**

Pros:
- Excellent enterprise platform
- Strong tooling

Cons:
- Ecosystem alignment may differ from target engineering strategy

**Node.js**

Pros:
- Fast development
- Large ecosystem

Cons:
- Less attractive for core high-concurrency transactional services where Go provides a simpler runtime model

### Decision

Go is appropriate for the core domain services, but domain contracts must remain language-independent.

---

# 53. Airflow

### Requirement

Batch/data workflow orchestration.

### Decision

Use Airflow.

### Alternatives

- Kubernetes-native workflow engines
- Dagster
- Prefect
- Cloud-native workflow services
- Custom orchestration

### Why Airflow

- Mature ecosystem
- Strong data engineering adoption
- DAG-based dependencies
- Scheduling
- Retry
- Backfill
- Operational visibility
- Strong integration with data platforms

### Constraint

Do not use Airflow as the domain workflow engine.

---

# 54. Snowflake

### Requirement

Enterprise analytical storage and compute.

### Decision

Use Snowflake as the analytical platform in the proposed target architecture.

### Alternatives

- Databricks/lakehouse
- BigQuery
- Redshift
- PostgreSQL-based analytics
- Open table formats

### Why

- Strong analytical workload
- Separation of compute/storage
- Large-scale SQL
- Enterprise governance
- Strong ecosystem

### Anti-lock-in mitigation

- Canonical data model
- Open event schemas
- dbt
- No Snowflake-specific business logic in Go
- Avoid unnecessary proprietary functions
- Keep raw data in portable object storage where appropriate

---

# 55. dbt

### Requirement

Governed SQL transformation.

### Decision

Use dbt.

### Alternatives

- Stored procedures
- Airflow Python transformations
- Spark
- Dataform
- Custom SQL framework

### Why dbt

- SQL as code
- Testing
- Lineage
- Documentation
- CI/CD
- Reusable models
- Strong analytics engineering model

### Decision

Transformation logic belongs in dbt, not Airflow.

---

# 56. SFTP

### Requirement

Banking and external partner file exchange.

### Decision

Support SFTP as a standard integration capability.

### Alternatives

- API
- Event streaming
- Managed file transfer
- Cloud object storage

### Decision

Do not eliminate SFTP merely because APIs/events are preferred for new systems. Legacy banking systems and external partners may continue to require file exchange for many years.

---

# 57. CDC

### Requirement

Near-real-time replication from legacy/source databases.

### Decision

Use a reusable CDC connector architecture.

### Alternative

Build database-specific log readers.

### Decision rationale

Do not build database transaction-log parsing from scratch unless there is a compelling strategic reason.

Own:

- Control plane
- Contracts
- Governance
- Checkpointing
- Reconciliation
- Audit

Reuse proven CDC engines for database-specific capture.

---

# 58. Event Bus

### Requirement

Decouple services and distribute domain events.

### Decision

Use an event streaming platform behind an internal event abstraction.

Potential implementation:

- Kafka-compatible platform
- Managed event streaming
- Other enterprise messaging technology

### Anti-lock-in

Domain event schemas are ours.

Consumers should not depend on broker-specific semantics beyond what the platform contract exposes.

---

# 59. Architecture Alternatives

## Alternative A — Buy EXUS

### Benefits

- Faster initial capability
- Mature collections functionality
- Existing domain knowledge
- Configurable collections capabilities

### Disadvantages

- Vendor dependency
- Product roadmap dependency
- Licensing
- Proprietary model
- Integration complexity
- Migration away later can be expensive

### Decision

Not selected as the strategic platform.

---

# 60. Alternative B — Buy FICO

### Benefits

- Strong decisioning
- Analytics
- Optimization
- Collections/recovery capabilities
- Mature enterprise product

### Disadvantages

- Vendor dependency
- Cost
- Product-specific integration
- Potential dependency on proprietary decisioning ecosystem

### Decision

Not selected as the core platform.

Potentially use as a benchmark for capabilities rather than the platform foundation.

---

# 61. Alternative C — Build everything from scratch

### Benefits

- Maximum control
- No external product dependency

### Disadvantages

- Very high engineering cost
- Large maintenance burden
- Database CDC complexity
- Messaging complexity
- ML infrastructure complexity
- Long implementation timeline

### Decision

Not selected.

Build the **domain/control plane**, reuse proven infrastructure components.

---

# 62. Alternative D — Hybrid

Potentially use external technology for:

- ML
- Optimization
- Communications
- CDC connectors
- Managed infrastructure

while retaining:

- Domain model
- APIs
- Events
- Strategy definitions
- Data model
- Audit
- Decision contract

### Decision

This is acceptable provided the external capability is behind a replaceable interface.

---

# 63. Build vs Buy Decision Matrix

| Capability | Build/Own | Reuse/Buy underneath |
|---|---|---|
| Collections domain model | Yes | No |
| Case management | Yes | Optional components |
| Strategy model | Yes | No |
| Decision contract | Yes | No |
| Audit model | Yes | No |
| Event schemas | Yes | No |
| Ingestion control plane | Yes | No |
| CDC engine | No | Yes |
| SFTP transport | No | Yes |
| Object storage | No | Yes |
| Event broker | No | Yes |
| Warehouse | No | Yes |
| dbt | No | Yes |
| Airflow | No | Yes |
| ML framework | No | Yes |
| Optimization engine | Potentially | Optional |
| Communication provider | No | Yes |

---

# 64. Migration Strategy

The migration must not be a big-bang rewrite.

Recommended stages:

```text
Discovery
   |
   v
Data foundation
   |
   v
Ingestion platform
   |
   v
Shadow mode
   |
   v
Parallel run
   |
   v
Limited production
   |
   v
Progressive migration
   |
   v
Legacy retirement
```

---

# 65. Phase 1 — Discovery

Inventory:

- Applications
- Databases
- Tables
- Batch jobs
- SFTP feeds
- APIs
- Events
- Reports
- Business rules
- Strategies
- Downstream systems
- Operational processes

Produce:

- Capability map
- Dependency map
- Data lineage
- Business-rule inventory
- Migration candidates

---

# 66. Phase 2 — Data Foundation

Build:

```text
Source
 |
 v
Ingestion Platform
 |
 v
Snowflake RAW
 |
 v
dbt
 |
 v
Collections marts
```

Validate:

```text
Legacy
  =
New analytical representation
```

---

# 67. Phase 3 — Build Operational Platform

Build:

- Account
- Delinquency
- Case
- Strategy
- Decision
- Treatment
- Arrangement
- Payment
- Recovery

Start with a thin vertical slice.

---

# 68. Phase 4 — Shadow Mode

The new system receives the same inputs as the legacy platform.

But:

> The new platform does not execute customer-facing actions.

Compare:

```text
Legacy decision
vs
New decision
```

Measure:

- Match rate
- Difference rate
- Rule discrepancies
- Data discrepancies
- Performance

---

# 69. Phase 5 — Parallel Run

Both platforms operate.

The new platform may execute a controlled subset.

```text
Portfolio
 |
 +-- 5% new
 |
 +-- 95% legacy
```

Then increase gradually.

---

# 70. Phase 6 — Cutover

Use explicit ownership:

```text
Account population
        |
        v
System of record owner
        |
        +-- Legacy
        +-- New
```

Never allow ambiguous ownership.

---

# 71. Migration Reconciliation

Reconcile:

- Account count
- Customer count
- Balance
- Overdue amount
- DPD
- Cases
- Actions
- Arrangements
- Payments
- Recoveries

No migration wave should complete without passing reconciliation criteria.

---

# 72. Non-Functional Requirements

## Availability

Critical customer-facing and operational services should have high availability appropriate to business criticality.

## Performance

Decision APIs should be low latency.

Batch workloads should meet defined processing windows.

## Scalability

Scale independently:

```text
API
Decisioning
Ingestion
Event consumers
Analytics
```

## Reliability

All important operations require:

- Retry
- Timeout
- Idempotency
- Recovery
- Audit

---

# 73. Disaster Recovery

Define:

- RPO
- RTO
- Backup
- Restore
- Failover
- Regional recovery
- Data reconciliation after recovery

Test recovery rather than merely documenting it.

---

# 74. Compliance

The platform should support:

- Complete audit trails
- Decision traceability
- Customer communication history
- Data retention
- Access audit
- Policy versioning
- Regulatory reporting
- Explainability
- Data lineage

Specific regulatory requirements must be confirmed with legal/compliance teams rather than inferred by engineering.

---

# 75. Domain Data Model

Core relationships:

```text
Customer
   |
   +---- Account
            |
            +---- Debt
            |
            +---- Delinquency
            |
            +---- Collection Case
                       |
                       +---- Strategy
                       |
                       +---- Treatment
                       |
                       +---- Contact
                       |
                       +---- Promise
                       |
                       +---- Arrangement
                       |
                       +---- Payment
                       |
                       +---- Recovery
```

---

# 76. Collection Case State Machine

Example:

```text
NEW
 |
 v
OPEN
 |
 +----> SUSPENDED
 |
 v
ACTIVE
 |
 +----> ARRANGEMENT
 |          |
 |          +--> COMPLETED
 |          |
 |          +--> BROKEN
 |
 v
ESCALATED
 |
 +--> DCA
 |
 +--> LEGAL
 |
 v
RESOLVED
 |
 v
CLOSED
```

State transitions should be explicit and audited.

---

# 77. Strategy Lifecycle

```text
DRAFT
  |
  v
SIMULATED
  |
  v
APPROVED
  |
  v
SCHEDULED
  |
  v
ACTIVE
  |
  +--> SUSPENDED
  |
  v
RETIRED
```

Every production decision references a specific strategy version.

---

# 78. Platform APIs

Representative APIs:

```text
GET  /customers/{id}

GET  /accounts/{id}

GET  /accounts/{id}/delinquency

POST /cases

GET  /cases/{id}

POST /decisions

POST /treatments

POST /arrangements

POST /payments

POST /agency-placements

POST /reconciliation-runs
```

Exact API contracts should be defined using OpenAPI and versioned.

---

# 79. Ingestion APIs

Example:

```text
POST /ingestion/sources

GET /ingestion/sources/{id}

POST /ingestion/jobs

GET /ingestion/jobs/{id}

POST /ingestion/files/{id}/reprocess

POST /ingestion/files/{id}/quarantine

GET /ingestion/checkpoints/{source}
```

---

# 80. Data Contract

Every major source should define:

```text
source
owner
schema
version
business date
technical timestamp
expected frequency
quality rules
reconciliation rules
retention
classification
```

---

# 81. File Contract

Example:

```text
File:
loan_accounts_YYYYMMDD.csv

Frequency:
Daily

Header:
Required

Trailer:
Required

Encoding:
UTF-8

Schema:
loan_account_v3

Control total:
Required

Expected arrival:
02:00 local time

Late threshold:
03:00 local time
```

---

# 82. Operational Controls

Every pipeline should have:

```text
Owner
Support group
SLA
Expected schedule
Alert policy
Retry policy
Escalation
Reconciliation
Runbook
```

---

# 83. CI/CD

Code should flow:

```text
Developer
   |
   v
Git
   |
   v
Unit Tests
   |
   v
Integration Tests
   |
   v
Security Scan
   |
   v
Contract Tests
   |
   v
Deploy
   |
   v
Automated Verification
```

For dbt:

```text
Pull Request
 |
 +-- compile
 +-- tests
 +-- lineage validation
 +-- SQL checks
 |
 v
Merge
 |
 v
Deploy
```

---

# 84. Testing Strategy

Testing should include:

### Unit

Domain logic.

### Integration

Database/API/event integrations.

### Contract

API and event schema compatibility.

### Data

dbt tests and data-quality rules.

### Reconciliation

Source/target equality.

### Performance

Decision throughput and latency.

### Resilience

Failures and retries.

### Migration

Legacy/new parity.

---

# 85. Security Testing

Include:

- SAST
- Dependency scanning
- Container scanning
- DAST
- API security tests
- Secrets scanning
- Penetration testing
- Access-control testing

---

# 86. Cost Management

The platform should track cost by:

- Ingestion source
- Data volume
- Snowflake workload
- Airflow workload
- API traffic
- Event volume
- ML inference
- Storage

Do not allow a "vendor-neutral" architecture to become unnecessarily expensive.

---

# 87. Platform Team Structure

Suggested ownership:

```text
Collections Product
      |
      +-- Domain team
      |
      +-- Decisioning team
      |
      +-- Data platform team
      |
      +-- Ingestion team
      |
      +-- Integration team
      |
      +-- SRE/platform engineering
      |
      +-- Security/data governance
```

Teams should own capabilities rather than layers only.

---

# 88. Recommended Initial MVP

Do not attempt to implement the complete enterprise platform immediately.

MVP:

```text
1. Ingestion Platform
2. Account
3. Delinquency
4. Collection Case
5. Basic Strategy
6. Decision API
7. Treatment
8. Payment Arrangement
9. Snowflake
10. dbt
11. Airflow
12. Reconciliation
13. Audit
```

Then expand.

---

# 89. Future Capability Roadmap

```text
MVP
 |
 +--> Strategy Manager
 |
 +--> Decision Rules
 |
 +--> ML scoring
 |
 +--> Optimization
 |
 +--> Omnichannel
 |
 +--> DCA
 |
 +--> Legal
 |
 +--> Field collections
 |
 +--> Digital self-service
 |
 +--> Advanced recovery
 |
 +--> Closed-loop optimization
```

---

# 90. Final Target Architecture

```text
+-------------------------------------------------------------------+
|                        EXPERIENCE LAYER                           |
| Collector UI | Customer | Contact Centre | API                   |
+--------------------------------+----------------------------------+
                                 |
+--------------------------------v----------------------------------+
|                         DOMAIN SERVICES                            |
| Customer | Account | Debt | Delinquency | Case | Arrangement     |
| Contact | Payment | Recovery | Agency | Legal                    |
+--------------------------------+----------------------------------+
                                 |
+--------------------------------v----------------------------------+
|                         DECISION PLATFORM                          |
| Strategy | Rules | Segmentation | Models | Optimization          |
+--------------------------------+----------------------------------+
                                 |
+--------------------------------v----------------------------------+
|                       EVENT / API CONTRACTS                        |
| Versioned Domain Events | REST/gRPC APIs | Data Contracts        |
+--------------------------------+----------------------------------+
                                 |
              +------------------+------------------+
              |                                     |
              v                                     v
+-----------------------------+       +-----------------------------+
|     INGESTION PLATFORM      |       |       OPERATIONAL DATA      |
|                             |       |                             |
| CDC | SFTP | CSV | API     |       | Transactional stores        |
| Event | Validation          |       | Service-owned state        |
| Checkpoint | Reconciliation |       +-----------------------------+
+--------------+--------------+
               |
               v
+-------------------------------------------------------------------+
|                         DATA PLATFORM                              |
|                                                                   |
| Object Storage -> Snowflake RAW -> dbt -> Collections Marts      |
+--------------------------------+----------------------------------+
                                 |
                                 v
+-------------------------------------------------------------------+
|                    ANALYTICS / ML / OPTIMIZATION                  |
| MI | Reporting | Features | Models | Simulation | Optimization   |
+--------------------------------+----------------------------------+
                                 |
                                 +------------------> Decisioning

Airflow:
- orchestrates ingestion
- orchestrates batch workloads
- schedules dbt
- manages backfills
- manages reconciliation
- does not own core business state
```

---

# 91. Key Architecture Decisions Summary

| Decision | Choice | Main reason |
|---|---|---|
| Core services | Go | Performance, simplicity, concurrency |
| Batch orchestration | Airflow | Mature scheduling/data workflow |
| Analytics | Snowflake | Enterprise analytical scale |
| Transformations | dbt | Governed analytics-as-code |
| File ingestion | Dedicated ingestion platform | Reusable banking file capability |
| SFTP | Supported | Banking/partner compatibility |
| CSV | Supported | Legacy banking compatibility |
| Database ingestion | CDC | Low-latency replication/migration |
| Events | Versioned event contracts | Decoupling |
| Operational state | Service-owned transactional stores | Correctness |
| Analytical state | Snowflake | Historical/analytical workloads |
| Decisioning | Separate platform capability | Avoid business logic coupling |
| Strategy | Configurable/versioned | Avoid code deployment for policy changes |
| Audit | Immutable/versioned | Explainability/compliance |
| Reconciliation | First-class capability | Banking financial control |
| Vendor strategy | Own contracts, reuse implementations | Avoid lock-in |
| Migration | Parallel/shadow/progressive | Risk reduction |

---

# 92. The Most Important Architectural Decisions

If the entire design has to be reduced to ten decisions, they are:

1. **Build the Collections domain ourselves.**
2. **Own the canonical domain and data contracts.**
3. **Use Go for operational services.**
4. **Keep decisioning independent from execution.**
5. **Make strategies configurable and versioned.**
6. **Create a reusable ingestion platform supporting CDC, SFTP/CSV, APIs and events.**
7. **Use Airflow for batch/data orchestration, not business workflow.**
8. **Use Snowflake/dbt for the analytical platform.**
9. **Make reconciliation, audit and idempotency first-class capabilities.**
10. **Keep every infrastructure/provider dependency behind an abstraction.**

---

# 93. Final Recommendation

The recommended solution is **not** "build an internal version of EXUS/FICO in Go."

It is:

> **Build a bank-owned Collections Decision & Execution Platform with a vendor-neutral domain architecture.**

The platform should own the things that create long-term strategic value:

- Debt/collections domain
- Case lifecycle
- Strategy
- Decision contracts
- Customer treatment
- Event model
- Data model
- Audit
- Reconciliation
- Integration contracts
- Analytics model

It should reuse proven technology for commodity infrastructure:

- CDC engines
- SFTP libraries/services
- Object storage
- Event brokers
- Airflow
- Snowflake
- dbt
- ML infrastructure

The most important design boundary is:

```text
                BANK-OWNED
+---------------------------------------+
| Domain model                          |
| Business policies                     |
| Strategy definitions                  |
| Decision contracts                    |
| Event contracts                       |
| Data contracts                        |
| Audit model                           |
| Reconciliation model                  |
| Customer treatment                    |
+-------------------+-------------------+
                    |
                    | adapters/contracts
                    v
+---------------------------------------+
| REPLACEABLE IMPLEMENTATION            |
|                                       |
| CDC engine                            |
| SFTP implementation                   |
| Event broker                          |
| Database                              |
| Warehouse                             |
| ML framework                          |
| Communication provider                |
+---------------------------------------+
```

That architecture gives the bank the strongest form of vendor independence: **vendors and infrastructure can change without changing the Collections business platform itself.**

The next design artefacts should be derived from this document:

1. **Level-1 architecture diagram**
2. **Level-2 component architecture**
3. **Bounded-context/service ownership diagram**
4. **Canonical Collections domain model**
5. **API catalogue**
6. **Event catalogue**
7. **Ingestion architecture and connector specification**
8. **Airflow DAG catalogue**
9. **Snowflake/dbt logical data model**
10. **Decisioning/strategy architecture**
11. **Security architecture**
12. **Migration wave plan**
13. **NFR/SLA matrix**
14. **Build-vs-buy decision record**
15. **Operational support/runbook model**
