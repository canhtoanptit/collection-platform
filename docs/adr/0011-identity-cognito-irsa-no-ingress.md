# ADR-0011: Identity & exposure — Cognito + IRSA, and no public ingress until the UI phase

- **Status:** Accepted
- **Date:** 2026-08-22
- **Related:** [ADR-0002](./0002-go-hexagonal-monorepo.md), [ADR-0010](./0010-terraform-stacks-ci-only-applies.md), [ADR-0014](./0014-collector-ui-react-vite.md), [ADR-0012](./0012-source-system-simulator.md)

## Context

The platform needs human identity (collectors, ops, strategy authors, business and risk approvers, analysts) and workload identity for services, with OIDC/OAuth2, short-lived tokens, role-based access and **no long-lived shared credentials** (A§62–63). An API gateway may authenticate, rate-limit and inject correlation ids, but must contain no business rules (A§64), and data services are never publicly exposed (A§65).

There is also a timing question: for most of the build there is no user interface and no external consumer, so any public endpoint is pure attack surface protecting nothing.

## Decision

**Cognito for identity, IRSA for workload identity, and zero public ingress until Phase 12.**

- **Cognito** user pool `colx-dev` with a hosted domain; resource server `colx-api` with fine-grained scopes (`ingestion/read`, `ingestion/write`, `webhook/write`, `cases/read`, `cases/write`, `decisions/read`, `decisions/write`, `strategy/author`, `payments/admin`, …); groups `strategy-author`, `business-approver`, `risk-approver`, `admin`, `collector`, `ops-admin`, `analyst`; **minimal M2M clients** (`platform-services`, `simulator`, ~$6/mo each); the SPA client (PKCE) is added in Phase 12 when callback URLs exist.
- **Every service validates JWTs itself from day 1** (`platform/authn`, JWKS cached, deny-by-default `RequireScope`). The gateway is not the authorization boundary — services stay safe if it is bypassed or removed.
- **Workload identity is IRSA:** a map-driven set of roles (external-secrets, ingestion-cp, sftp-worker, webhook-receiver, kafka-connect, airflow, simulator, loki, tempo, alertmanager, plus the ALB controller unattached) — no node-role permissions, no static AWS credentials in pods. Every Kubernetes secret is an **ExternalSecret** referencing `colx/dev/*`; zero secret values in git or values files (A§66).
- **No public ingress until the UI phase.** Access is `kubectl port-forward` make targets. The ALB + ACM + Cognito-OIDC ingress module is written and **flag-gated, default off** (it needs a domain); Phase 12 turns it on together with CloudFront for the SPA and WAF basics on the ALB.

## Alternatives considered

- **Keycloak self-hosted.** Full-featured, portable, no licence cost. Rejected: another stateful service to run, patch and back up, with its own database, for a handful of users and two machine clients — Cognito's ~$12/mo is cheaper than the operational surface. Portability survives because services depend only on OIDC discovery and JWT claims.
- **A public ALB from day 1.** Convenient for demos and for testing the ingress path early. Rejected as the worst risk/benefit trade available: an internet-exposed dev API in front of freshly agent-written auth middleware, for ~$25/mo. Zero exposure is the best security posture a dev environment can have.
- **EKS Pod Identity.** Newer and simpler than IRSA (no OIDC provider, no trust-policy editing). Rejected for now: the pinned Terraform modules, Helm charts and every piece of surrounding documentation assume IRSA, and the service-account annotation is trivially assertable in a verify script. A candidate for later migration.
- **mTLS / a service mesh between services.** A§63 permits it "where required"; unjustified complexity at this size, and it would not replace the JWT scope checks that carry the authorization semantics.
- **Static API keys for the simulator.** Simpler than a machine client — and it would skip the OAuth2 client-credentials path a real partner integration must exercise.
- **IAM-only auth (SigV4) for internal APIs.** Rejected: the human and machine paths would then differ, and the collector UI needs OIDC anyway.

## Consequences

**Positive**

- The authorization model is exercised end to end (token → scope → 200/403) long before anything is exposed, and the 403 body is the A§20 error contract.
- No long-lived credentials anywhere: OIDC to AWS in CI, IRSA in-cluster, ESO for secrets, key-pair auth for Snowflake.
- Attack surface is zero until Phase 12, and turning ingress on is a feature flag plus a domain rather than a redesign.

**Negative / caveats**

- **Port-forward development is friction** that spreads: make targets, verify scripts and E2E harnesses all have to know about it — and the ingress path itself stays *unexercised* until Phase 12, so the flag-gated module is untested code carrying an implicit promise.
- Cognito is an AWS-specific control plane with a limited hosted UI and limited token customization; the mitigation (depend only on standard OIDC) is real but thin.
- IRSA plus ESO means a secret rotation touches three places (Secrets Manager value, ExternalSecret refresh, pod restart), and a missing annotation fails at runtime with an unhelpful AWS error.
- Group-based role checks live in service code, so changing who may approve a strategy is a deploy, not a configuration change.
- Phase 12 needs a purchased domain (~$12/yr) — an unresolved prerequisite that blocks HTTPS, the SPA client and the Playwright smoke suite.
