# ADR-0003: Operational storage — PostgreSQL 16, database-per-service on two shared RDS instances

- **Status:** Accepted
- **Date:** 2026-08-22
- **Related:** [ADR-0002](./0002-go-hexagonal-monorepo.md), [ADR-0004](./0004-kafka-eventing-envelope-outbox.md), [ADR-0006](./0006-cdc-debezium-on-eks.md), [ADR-0010](./0010-terraform-stacks-ci-only-applies.md), [ADR-0012](./0012-source-system-simulator.md)

## Context

Each service owns its operational state (D§27); shared database ownership across services is an explicit anti-pattern (A§7.3). The storage engine must support a **transactional outbox in the same transaction as the state change**, inbox dedupe and `Idempotency-Key` records ([ADR-0004](./0004-kafka-eventing-envelope-outbox.md)) — i.e. real transactions, not eventual consistency. Separately, CDC needs a *source* database that behaves like a legacy core-banking system, with logical replication enabled ([ADR-0012](./0012-source-system-simulator.md)). And because this is a metered dev environment, "can it be stopped?" is a first-class requirement ([ADR-0010](./0010-terraform-stacks-ci-only-applies.md)).

## Decision

**PostgreSQL 16 on two RDS instances**, database-per-service:

- `colx-dev-platform` (db.t4g.small) — databases `ingestion`, `airflow`, plus one database per service, each with its own owner role and password in Secrets Manager.
- `colx-dev-corebank` (db.t4g.micro) — the simulator's legacy-shaped source, `rds.logical_replication=1`, `max_replication_slots=5`, with `debezium` (REPLICATION) and `simulator` users.
- Both in data subnets with no NAT route, EKS security-group-only ingress, CMK encryption, 7-day backups, single-AZ (dev).
- **Access pattern:** sqlc + pgx/v5 with goose migrations embedded in the binary, applied by `server migrate` as a Helm pre-upgrade hook. No ORM, no query builder.
- **No cross-database access.** A service that needs another service's data calls its API or consumes its events (A§7.3). Postgres FDW/dblink are not installed.

## Alternatives considered

- **Aurora Serverless v2.** The obvious "scale to zero" answer, and it does not work here: the ACU floor bills continuously, and a Debezium logical replication slot must be consumed continuously, so the idle state the pricing model rewards never occurs. A provisioned instance's `rds stop` is a genuine teardown lever; Aurora's is not.
- **Distributed SQL (CockroachDB, Yugabyte).** A§102 is explicit: PostgreSQL is the preferred initial choice — mature, simple, strong transactional semantics, large ecosystem, easy local development, portable — and distributed SQL should be introduced only when workload characteristics justify it. Dev scale never will.
- **A shared database for all services.** A§7.3's rejected shape (`case-service` running `SELECT account_db.account`). It trades a clear ownership boundary for short-term convenience and makes independent migration impossible.
- **One RDS instance for everything.** Rejected: the CDC source must be owned by the simulator, carries different parameter-group settings, and should not share a blast radius (or a WAL) with platform data.
- **DynamoDB or another NoSQL store.** Rejected: outbox, inbox dedupe and idempotency records all need multi-row transactions, and the domain is relational by nature.
- **A separate RDS instance per service.** Correct isolation, ~15× the cost. Rejected on price alone.

## Consequences

**Positive**

- One boring engine everywhere, including tests — testcontainers runs the same `postgres:16` image the services run against.
- Per-database owner roles give real isolation (a compromised service cannot read its neighbour) without paying for 15 instances.
- Two instances means `make stop` actually stops the databases; migrations are embedded, so a rebuild is a deploy.

**Negative / caveats**

- "Shared instance, separate databases" is a **cost compromise, not the target architecture**: a noisy service can starve its neighbours, and a single db.t4g.small is a single point of failure for every service. Single-AZ with 7-day backups is a dev posture; the production delta is per-service instances (or at least Multi-AZ) and is documented as such.
- Database-per-service on one instance still *tempts* cross-database queries. Nothing but convention, review and the absent FDW extension prevents them.
- The corebank replication slot is the most dangerous object in the platform: while Kafka Connect is down or idle, retained WAL grows until it fills db.t4g.micro. Mitigated by `heartbeat.interval.ms`, a retained-WAL alert at 500 MB and a drop/recreate runbook — plan risk 3, and the most likely real incident.
- Goose migrations run as a pre-upgrade hook, so a bad migration blocks a deploy rather than corrupting data — but a merged migration is frozen forever (append only, never renumber).
