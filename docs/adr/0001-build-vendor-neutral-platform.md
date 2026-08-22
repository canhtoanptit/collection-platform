# ADR-0001: Platform strategy — build a bank-owned, vendor-neutral collections platform

- **Status:** Accepted
- **Date:** 2026-08-22
- **Related:** [ADR-0002](./0002-go-hexagonal-monorepo.md), [ADR-0004](./0004-kafka-eventing-envelope-outbox.md), [ADR-0006](./0006-cdc-debezium-on-eks.md), [ADR-0008](./0008-snowflake-dbt-analytics.md), [ADR-0009](./0009-decisioning-layered-pipeline.md)

## Context

A bank needs an enterprise collections & debt management capability: delinquency, cases, strategy, decisioning, treatment, arrangements, payments, recovery, agency and legal. Four routes were assessed in D§59–63: buy EXUS, buy FICO, build everything from scratch, or a hybrid.

The strategic question is not "which product has the most features" but **which parts of this create long-term value for the bank**. D§92–93 answers it: the domain model, case lifecycle, strategy, decision contracts, event and data contracts, audit and reconciliation are the bank's; CDC log parsing, brokers, warehouses and SFTP transports are not.

## Decision

Build a **bank-owned Collections Decision & Execution Platform with a vendor-neutral domain architecture** (D§93) — explicitly *not* "an internal EXUS/FICO in Go".

The boundary is the decision:

- **Own** (A§1.1, D§63): domain entities and state machines, business events, decision and strategy contracts, policy versions, audit model, data contracts, reconciliation rules, API contracts, the ingestion control plane.
- **Reuse underneath** (D§63): CDC engines, SFTP transport, object storage, event broker, warehouse, dbt, Airflow, ML frameworks, communication providers.
- Any external capability sits **behind a replaceable interface** — the hybrid of D§62 is acceptable only on that condition. Domain consumers never see implementation-specific semantics (D§3.2).

Everything downstream follows from this line: contracts-first delivery ([ADR-0013](./0013-llm-agent-delegation-model.md)), a rule DSL we own rather than a vendor engine ([ADR-0009](./0009-decisioning-layered-pipeline.md)), a broker behind `platform/kafka` ([ADR-0004](./0004-kafka-eventing-envelope-outbox.md)), a warehouse fed from portable object storage ([ADR-0008](./0008-snowflake-dbt-analytics.md)).

## Alternatives considered

- **Buy EXUS (D§59).** Faster initial capability, mature collections functionality, configurable. Rejected as the strategic platform: vendor and roadmap dependency, licensing, a proprietary model for our core business capability, and an expensive exit later.
- **Buy FICO (D§60).** Strong decisioning, analytics and optimization. Rejected as the core platform — it would make the decision contract and audit model the vendor's. Retained as a **capability benchmark** rather than a foundation.
- **Build everything from scratch (D§61).** Maximum control, no product dependency. Rejected: very high engineering cost, database-CDC complexity, messaging complexity, ML infrastructure complexity, long timeline. Build the *domain/control plane*, not the infrastructure.
- **Hybrid (D§62).** Accepted, in the specific form above: external ML, optimization, communications, CDC connectors and managed infrastructure are fine; domain model, APIs, events, strategy, data model, audit and decision contract are not negotiable.

## Consequences

**Positive**

- Vendors and infrastructure can be replaced without changing the collections business platform (D§93) — the strongest available form of vendor independence.
- The build list is bounded and written down (D§63 matrix), so "should we build this?" is a lookup rather than an argument.
- Owning the decision and audit contracts is what makes explainability, simulation and parity testing possible at all.

**Negative / caveats**

- We carry operational surface for every component we reuse but self-host — Kafka Connect, Airflow, SFTP, the observability stack — instead of paying a vendor to carry it.
- "Vendor-neutral" is a discipline, not a property: an abstraction that is never exercised against a second implementation rots. Only the interfaces we actually test (model API, channel adapter SPI, `platform/kafka`) can be claimed as portable.
- Rebuilding commercial products poorly is a real risk (A§100 risk 2). Mitigation is scope discipline — MVP per D§88 — plus benchmarking against the products we declined.
- This repository realizes the architecture at **single-developer dev scale**: single-AZ, no HA, teardown-friendly. Availability, capacity and multi-region behaviour of the bank-grade deployment are out of scope here and documented as production deltas.
