# ADR-0004: Eventing — Kafka-compatible MSK, A§24 envelope, in-repo schemas, outbox + inbox

- **Status:** Accepted
- **Date:** 2026-08-22
- **Related:** [ADR-0003](./0003-postgres-per-service-shared-rds.md), [ADR-0006](./0006-cdc-debezium-on-eks.md), [ADR-0015](./0015-observability-otel-grafana-stack.md), [ADR-0016](./0016-data-conventions-money-ids-time.md)

## Context

Business events are a first-class integration mechanism (D§3.4) and must be versioned and replayable (D§49). The platform explicitly refuses to assume global exactly-once processing: **at-least-once delivery with idempotent consumers** (D§3.5). Ordering is required per aggregate, not globally (A§26). The event bus must sit behind an internal abstraction so the broker stays replaceable (D§58, A§102: the application contract is `publish` / `subscribe` / `checkpoint`).

## Decision

A **Kafka-compatible event platform** with the reliability pattern in application code, not the broker.

- **Infrastructure:** Amazon MSK **Provisioned**, 2× `kafka.t3.small`, TLS + IAM auth, auto-create **off**. Topics are declared in `deployment/kafka/topics.yaml` and applied by an idempotent Job; the partition budget is documented so nobody grows it casually (fits under ~300 partitions). ≈ $80/mo against ≈ $550 for MSK Serverless at this shape.
- **Contract:** JSON payloads inside **exactly the 10-field A§24 envelope** (`eventId eventType eventVersion occurredAt producer aggregateType aggregateId correlationId causationId payload`), validated against JSON Schema 2020-12 (`santhosh-tekuri/jsonschema/v6`) loaded from the `contracts` `embed.FS` — at CI time for every example and at runtime for every message. **No schema registry.**
- **Topics/keys:** `collections.<context>` (14 contexts, A§25) plus canonical ingestion topics `ingestion.{customers,accounts,debts,payments}.v1`; partition key is the aggregate id from the CON-2 topic/key map (A§26).
- **Reliability:** transactional **outbox** (advisory-lock leader relay, ordered per key) for every publication; **inbox dedupe** (`processed_events` keyed `(consumer, eventId)`) in the same transaction as the side effects for every consumption; a Postgres-backed **`Idempotency-Key`** middleware per A§21 on every POST command; per-consumer DLQ `collections.dlq.<service>` (A§27) plus `dlq.ingestion.v1`, with origin topic and error headers, alerting and replay.
- **Client:** franz-go wrapped by `platform/kafka`; services never import `kgo` (D§58 anti-lock-in).

## Alternatives considered

- **MSK Serverless.** No capacity planning, scales with load — and ≈ $550/mo at this shape, the single largest saving available anywhere in the stack. Rejected on cost.
- **EventBridge / SNS+SQS.** Managed, near-free at idle, no brokers to run. Rejected: no partition-ordered log, no replay by offset, no consumer groups. D§49 replay and A§26 per-aggregate ordering are requirements, and the platform contract would end up exposing provider semantics.
- **A schema registry (Confluent or AWS Glue).** A§1.2's baseline names "AsyncAPI + schema registry", so this is a **deliberate divergence** (recorded in the plan): the `contracts` module already ships every schema to every consumer at compile time, CI validates every example against schema *and* envelope, and the runtime validates every message. A registry would add infrastructure and a second source of truth for no additional guarantee at this scale. Revisit when a non-Go or out-of-repo consumer appears.
- **Kafka transactions / EOS.** Rejected per D§3.5 — the outbox already binds publication to the state change, and idempotent consumers are needed for replay and backfill regardless.
- **Self-managed Kafka (or Redpanda) on EKS.** Cheaper still, but it makes us broker operators for no learning this project wants. Redpanda *is* used in tests via testcontainers.

## Consequences

**Positive**

- One envelope and one validation path for every event; an invalid payload cannot reach the broker because `outbox.Enqueue` validates before the row is written.
- Exactly-once *business effect* on at-least-once delivery, proven by tests (duplicate delivery → one side effect; killed relay mid-batch → all events published, per-key order preserved).
- Replay and DLQ handling are first-class operations rather than incidents; ordering guarantees are explicit and narrow.

**Negative / caveats**

- **MSK Provisioned cannot be stopped.** Brokers bill ≈ $80/mo whether or not anything is running. The only lever is destroying the cluster, which is safe by design (Kafka is transport, not a system of record — D§47) but means a rebuild plus a topics re-apply after every long pause.
- No registry means compatibility rests entirely on CI (immutability + breaking-change checks) and runtime validation. Both are mandatory, not best-effort — if either is skipped the governance story is gone.
- Validating every message against JSON Schema costs CPU on the hot path; acceptable at this volume, and worth re-measuring before claiming it scales.
- A DLQ nobody drains is a slower outage; the `DLQDepthNonZero` alert and replay endpoint are part of this decision, not an add-on.
- Redpanda in tests versus MSK-with-IAM in the cluster is real drift (plan risk 8). Same client mitigates it; one dev-cluster smoke test per service wave is required.
