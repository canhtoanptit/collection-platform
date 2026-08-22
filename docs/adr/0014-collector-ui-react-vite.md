# ADR-0014: Collector UI — React 18 + TS strict + Vite, generated API clients, S3 + CloudFront

- **Status:** Accepted
- **Date:** 2026-08-22
- **Related:** [ADR-0011](./0011-identity-cognito-irsa-no-ingress.md), [ADR-0009](./0009-decisioning-layered-pipeline.md), [ADR-0013](./0013-llm-agent-delegation-model.md), [ADR-0002](./0002-go-hexagonal-monorepo.md)

## Context

A minimal collector workbench is in scope: case queue, case detail with a unified timeline, record contact and promise-to-pay, and the decision explanation view. Two constraints shape the technology choice more than taste: the UI must **never become a second source of truth for API shapes**, and it must be verifiable by an agent that cannot look at a screen.

## Decision

A **React 18 + TypeScript (strict) + Vite single-page application**, hosted statically.

- **Stack:** react-router; **TanStack Query** for all server state (no bespoke cache); `oidc-client-ts` **PKCE** against the Cognito SPA client ([ADR-0011](./0011-identity-cognito-irsa-no-ingress.md)), configuration entirely env-driven.
- **One fetch wrapper** injects the bearer token and an `Idempotency-Key` (ULID) on every POST, and parses A§20 error bodies into a typed `ApiError` — so `details[]` can render as inline field errors.
- **API client types are generated from the OpenAPI specs and committed**, with a CI drift check: a contract change the UI has not absorbed is a **compile error**, not a runtime surprise. Same rule as the services ([ADR-0002](./0002-go-hexagonal-monorepo.md)).
- **Tests:** MSW handlers built from the contract examples for unit/component tests; Playwright `@smoke` against the deployed environment (login → queue → detail → record contact → PTP → explanation), with trace artefacts on failure.
- **Hosting:** S3 + CloudFront (origin-access-restricted bucket, SPA rewrite function); hashed assets `max-age=31536000`, `index.html` `no-store`, invalidation on deploy.
- **Deliberate UI-side design points:** the case timeline is a **client-side merge** of activities, contacts, promises/arrangements, payments and the latest decision into a typed union, so a failing source degrades that lane only; the decision explanation renders the stage trace and reason-code chips read-only, and an unregistered reason code renders verbatim rather than crashing.

## Alternatives considered

- **Next.js / SSR.** Routing, data loading and image handling out of the box. Rejected: an SSR server is a second runtime to deploy, secure and give a token story to, for an internal authenticated workbench that benefits from none of SSR's advantages (SEO, cold-cache first paint for anonymous traffic). Static hosting has no runtime to operate.
- **No UI at all.** The API plus curl proves the platform, and the MVP gate is API-level anyway. Rejected: the collector workbench is a locked scope decision, and a UI is the only consumer that proves the API is *usable* rather than merely correct — the missing `assignedCollector` filter and `GET /v1/arrangements?accountId=` were both found by designing screens.
- **Hand-written API clients** (or inline types on `fetch`). Faster on day one, and it drifts silently from the spec — precisely what the OpenAPI-first rule exists to prevent.
- **A full component library (MUI / Mantine) or a design system.** Not decided here; the scaffold stays deliberately plain, and adopting one later is a restyle, not a rewrite.
- **Server-rendered timeline merge.** Rejected: it would need a new backend endpoint that joins five services, re-creating the coupling the domain boundaries exist to avoid, and losing per-lane degradation.
- **Amplify hosting.** Rejected: S3 + CloudFront is already Terraform-managed and has no extra control plane.

## Consequences

**Positive**

- Contract drift is a compile error, and the same examples feed contracts CI and the UI's MSW tests — one source of truth, two consumers.
- Static hosting means no server to patch, and the deploy is `s3 sync` plus an invalidation.
- PKCE keeps no client secret in the bundle; the Idempotency-Key wrapper makes double-submit safety a property of the transport rather than of each form.

**Negative / caveats**

- A generated client is only as good as its spec: a vague schema produces awkward types, and regeneration churn lands in review diffs that reviewers must skim rather than read.
- TanStack Query caching plus a merged timeline makes invalidation subtle — recording a PTP has to invalidate several queries, and getting it wrong shows stale data rather than an error.
- CloudFront invalidations are eventually consistent, so a deploy is not instant and a smoke test can race it.
- Playwright smoke needs a **real deployed environment, a domain and a test user**, tying UI CI to Phase 12 infrastructure; until then the UI is only verified against MSW fixtures.
- Accessibility is asserted by a Lighthouse ≥90 gate on two pages plus zero console errors — a floor, not a guarantee, and no keyboard/screen-reader testing is in scope.
- The UI is single-purpose: no ops console, no strategy authoring screens. Strategy governance stays API-only in the MVP, which is a real gap for the humans who must approve strategies.
