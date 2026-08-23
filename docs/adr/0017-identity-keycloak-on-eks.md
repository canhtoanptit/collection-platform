# ADR-0017: Identity provider — Keycloak on EKS (supersedes Cognito)

- **Status:** Accepted — **supersedes the identity-provider decision in [ADR-0011](./0011-identity-cognito-irsa-no-ingress.md)**; that ADR's IRSA workload identity and no-public-ingress decisions stand unchanged
- **Date:** 2026-08-23
- **Related:** [ADR-0011](./0011-identity-cognito-irsa-no-ingress.md) (partially superseded), [ADR-0003](./0003-postgres-per-service-shared-rds.md), [ADR-0010](./0010-terraform-stacks-ci-only-applies.md), [ADR-0014](./0014-collector-ui-react-vite.md)

## Context

[ADR-0011](./0011-identity-cognito-irsa-no-ingress.md) chose Cognito on 2026-08-22. A user directive on 2026-08-23 changed the identity provider to **Keycloak self-hosted on EKS**. Nothing had been applied — the switch happened pre-deploy, so there is no migration, no live user pool and no token in circulation.

Two technical points support the directive beyond preference. First, **D§3.2 vendor neutrality** and A§62's "enterprise IAM with OIDC" are better served by an OIDC server the bank can run anywhere than by an AWS-specific control plane. Second, Cognito's resource-server scope format is provider-coupled: it forced a normalization layer inside `platform/authn` between the scope strings in a token and the **logical colon-form scopes the API contracts declare** (`cases:read`, `strategy:author`). Keycloak lets a client scope be named exactly the logical name, so the mapping layer disappears.

## Decision

**Keycloak on EKS, realm as code, replacing Cognito as the identity provider.**

- **Deployment:** the official `quay.io/keycloak/keycloak` image via a pinned community chart (codecentric `keycloakx`) or plain manifests in the helmfile — explicitly **not Bitnami**, whose images have been subscription-gated since 2025; the implementer verifies availability and documents the pick. ~750m CPU / 1Gi memory, ServiceMonitor metrics, admin credentials via ESO (`colx/dev/keycloak/admin`).
- **Storage:** `KC_DB=postgres` against a `keycloak` database on `colx-dev-platform` RDS ([ADR-0003](./0003-postgres-per-service-shared-rds.md)), provisioned alongside the other per-service databases.
- **Realm as code:** `deployment/values/keycloak/realm-colx.json` imported at startup (`--import-realm`); realm `colx`.
- **Scopes are the logical names.** Client scopes are named exactly the colon-form scopes used in the OpenAPI specs — `cases:read/write/admin`, `delinquency:read/admin`, `payments:read/write/admin`, `recovery:read/write`, `agency:read/admin`, `decisions:read/write`, `strategy:author`, `treatments:read/write`, `ingestion:read/write`, `webhook:write`, `customers:read`, `accounts:read`, `debts:read` — so `platform/authn`'s pass-through path applies with **zero mapping**. The Cognito prefix/dot normalization remains as dormant compatibility code.
- **Groups:** `strategy-author`, `business-approver`, `risk-approver`, `admin`, `collector`, `ops-admin`, `analyst`, exposed through a group-membership protocol mapper as a plain **`groups` claim**.
- **Machine clients:** client-credentials clients `platform-services` (all service scopes) and `simulator` (`webhook:write`), with service accounts. **Client secrets never live in git or the realm JSON** — they are set post-start by a `kcadm.sh` Job reading ESO-synced secrets, then mirrored to Secrets Manager for workload consumption.
- **SPA client (PKCE) deferred to Phase 12**, when Keycloak itself is publicly exposed for browser login redirects ([ADR-0014](./0014-collector-ui-react-vite.md)). Until then access is `make keycloak` port-forward only.
- **Unchanged from [ADR-0011](./0011-identity-cognito-irsa-no-ingress.md):** workloads reach AWS through IRSA; every service validates JWTs itself with deny-by-default scope checks; no public ingress before Phase 12.

## Alternatives considered

- **Cognito — the superseded choice.** Managed, nothing to operate, ~$12/mo for two M2M clients, and it was the accepted decision for one day. Rejected on the user directive plus the two points above: a provider-coupled scope format needing a mapping layer, and an AWS-specific control plane that scores worse on the D§3.2 portability test. Because nothing was applied, the switch cost is documentation only.
- **Okta / Auth0.** Best-in-class managed identity with real enterprise features. Rejected: SaaS cost plus an external dependency, another account and another long-lived secret, for a dev environment with a handful of users.
- **Dex.** Much thinner than Keycloak and a natural fit for a Kubernetes-only world. Rejected: no admin console, no user federation, and no group/role management worth the name — users would be hand-maintained in YAML and the realm-as-code round trip would be lost.
- **Bitnami's Keycloak chart.** The obvious chart choice historically. Rejected: subscription-gated images since 2025, so it is not a dependency this repository can pin.
- **Static client secrets or API keys for machine clients.** Rejected for the same reason as in ADR-0011 — the OAuth2 client-credentials path a real partner would use must be genuinely exercised.

## Consequences

**Positive**

- **Zero external identity dependency.** The whole auth path — issuer, JWKS, tokens, groups — lives inside the cluster and is rebuildable from a realm JSON, so `destroy-heavy` then `up-all` restores identity too.
- **Genuinely portable OIDC** (D§3.2): services depend only on discovery plus JWT claims, so the provider is now replaceable rather than nominally abstracted, and the same realm can back the compose stack for E2E.
- **$0 incremental cost** — it runs on nodes already paid for, and the ~$12/mo Cognito line leaves the cost model (everything-running ≈ $530–565/mo; see [ADR-0010](./0010-terraform-stacks-ci-only-applies.md)).
- **Scope pass-through:** a token carries `cases:read` verbatim, so the contracts' scope names are the wire truth and one layer of translation (and its bug surface) is gone.

**Negative / caveats**

- **We now operate identity.** Keycloak major upgrades change realm representations; the `keycloak` database needs backup and a tested restore; the admin console can mint any token and must be locked down. An identity outage is now self-inflicted, and it takes every service's auth with it.
- ~1Gi memory and ~750m CPU come out of the existing 3× t3.large node group — real capacity taken from services.
- **Realm import is create-if-absent, not converge.** Editing `realm-colx.json` after first start does not reliably update a live realm, so changes go through `kcadm.sh` or a documented re-import — a wrinkle that undercuts what "as code" implies.
- Client secrets need a post-start Job because they cannot live in the realm JSON: one more moving part between ESO, Keycloak and Secrets Manager, and one more thing to get wrong on a rebuild.
- Login latency and availability now depend on our pod rather than a managed service — visible in Phase 12's Playwright smoke and in any collector's first impression.
- Phase 12 must **expose Keycloak publicly** for browser redirects, so the no-ingress posture ends with an internet-facing identity server we operate. WAF rules and admin-console lockdown become mandatory rather than optional.
