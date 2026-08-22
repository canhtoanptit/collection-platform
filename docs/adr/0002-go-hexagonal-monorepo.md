# ADR-0002: Services & repository — Go domain services, hexagonal layout, single monorepo

- **Status:** Accepted
- **Date:** 2026-08-22
- **Related:** [ADR-0001](./0001-build-vendor-neutral-platform.md), [ADR-0003](./0003-postgres-per-service-shared-rds.md), [ADR-0013](./0013-llm-agent-delegation-model.md), [ADR-0016](./0016-data-conventions-money-ids-time.md)

## Context

The platform needs high-throughput operational APIs and domain services (D§52) across 14 bounded contexts (D§26). Two constraints shape the choice beyond raw suitability: the code is written by many parallel LLM-agent sessions, so **mechanical verification** is the scarce resource ([ADR-0013](./0013-llm-agent-delegation-model.md)); and domain contracts must stay language-independent (D§52 decision).

## Decision

**Go for all domain services**, hexagonal layout, one monorepo.

- **Language/runtime:** Go (language level 1.24; the exact toolchain is pinned in `go.work` and `mise.toml`, which are the single source of truth). Standard-library `net/http` mux, **oapi-codegen v2 strict-server** so a contract mismatch is a compile error, and no web framework.
- **Layout per service, exactly A§92:** `cmd/` · `internal/{domain,application,ports,adapters/{postgres,kafka,http}}` · `migrations/` · `api/` · `tests/`. Dependencies point inward: domain → application → ports → adapters. Domain packages must not import `pgx`, `kgo` or HTTP types; `tools/layoutcheck` enforces the shape and the forbidden imports mechanically.
- **One monorepo (A§91)** with `go.work`: one module per service, a shared `platform/` module, and `contracts/` as a Go module exporting `embed.FS` so every service gets schemas at compile time with no file copying. Services use `replace` directives, so there is no publish step.
- **OpenAPI tooling is Go-installable** — vacuum (lint) and oasdiff (breaking-change gate) — keeping Node out of the service toolchain.

## Alternatives considered

- **Java/Spring (D§52).** Mature banking ecosystem, large talent pool, strong enterprise libraries. Rejected: more framework complexity and a higher runtime footprint, and the framework magic that speeds a human team up is exactly what makes generated-code verification harder for an agent.
- **.NET (D§52).** Excellent enterprise platform and tooling; ecosystem alignment differs from the target engineering strategy.
- **Node.js (D§52).** Fast development, huge ecosystem; less attractive for core high-concurrency transactional services where Go's runtime model is simpler.
- **A Go web framework (gin/echo/fiber).** Rejected: strict-server generated interfaces plus the 1.22+ stdlib mux cover routing and binding, and the framework would sit between us and the generated contract boundary for no gain.
- **An ORM (GORM/ent).** Rejected in favour of sqlc + pgx explicit SQL ([ADR-0003](./0003-postgres-per-service-shared-rds.md)) — reviewable queries and generated-code drift caught by `git diff --exit-code`.
- **Polyrepo (one repo per service).** Conventional at this service count and better for independent release trains. Rejected: a contract change here has to move 14 services, `platform/`, dbt, DAGs, Helm values and the UI in one commit. Cross-repo version skew is precisely the failure mode a fleet of memoryless agent sessions cannot survive.

## Consequences

**Positive**

- Contract mismatch, generated-code drift and layout violations are all *commands* (`make generate && git diff --exit-code`, `layoutcheck`), not review opinions.
- One clone contains the whole system, so a WP brief can name exact paths and a fresh agent session needs no history.
- The hexagonal split keeps domain logic testable without Docker, and the exemplar service makes the 14th service cheaper than the 2nd.

**Negative / caveats**

- No framework batteries: auth, middleware, error contract, health, config, retries and Kafka plumbing are ours to build and maintain in `platform/` (LIB-1..9) before any service ships.
- A single shared `platform/` module is a serialization point — only one agent may work in it at a time, which queues work by design.
- Monorepo CI must use path filters or every push rebuilds everything; the filters themselves become something to maintain.
- Language independence of the contracts is a claim, not a fact, while every consumer is Go — it holds only because the specs and schemas, not the generated code, are the artefacts under CI.
- Go's explicit error handling makes for verbose adapters; the trade is bought deliberately for readability under review.
