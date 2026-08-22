# ADR-0013: Delivery model — contracts-first, exemplar-first LLM-agent delegation

- **Status:** Accepted
- **Date:** 2026-08-22
- **Related:** [ADR-0002](./0002-go-hexagonal-monorepo.md), [ADR-0009](./0009-decisioning-layered-pipeline.md), [ADR-0010](./0010-terraform-stacks-ci-only-applies.md), [ADR-0016](./0016-data-conventions-money-ids-time.md)

## Context

The platform is built by many parallel LLM-agent sessions working through 113 work packages, with no shared memory between sessions and no human able to read every diff. The scarce resource is not code — it is **agreement between sessions**. The failure modes are drift (two agents inventing incompatible shapes) and collision (two agents editing the same files), and the quality bar is enterprise banking: auditability, reconciliation, idempotency and security are first-class.

Every choice below optimizes for *mechanical verifiability*: a claim that is not a command someone else can run is not evidence.

## Decision

The protocol (plan §4), packaged in the repo as `CLAUDE.md`, `docs/conventions.md`, `docs/wp-template.md`, `docs/ownership.yaml`, `docs/review-policy.md`, `docs/gates/`:

1. **Contracts first, then fan out.** Phase 1 freezes every interface and tags **`contracts-v1.0`**; all codegen pins the tag. **Released contract files are immutable** — a change ships as a new `vN` file and CI fails a PR that modifies one. No parallel fan-out before the freeze.
2. **Exemplar first.** `services/case` is built with maximum scrutiny (strongest model + adversarial pass), then `docs/service-playbook.md` turns every other service WP into "clone the exemplar plus these deltas". `tools/layoutcheck` asserts the shape mechanically — required directories and make targets, and forbidden imports (domain must not import pgx/kgo/adapters). The UI scaffold is the UI exemplar.
3. **Path ownership.** `docs/ownership.yaml` maps WP → allowed globs; a CI job fails any PR touching unowned paths. Parallel WPs never share a directory; `platform/*` and `contracts/*` changes are serialized into dedicated WPs.
4. **Per-WP verification.** Every WP ships `scripts/verify/<WP-ID>.sh` (exit 0 = pass, at least one expected-fail assertion). Definition of done: `lint test` green, coverage ≥90% domain / ≥80% module, `make generate && git diff --exit-code` clean, `make contracts-check` green, `make verify WP=<id>` green, `make ownership-check WP=<id>` clean. Testing is table-driven with testcontainers (postgres:16 + redpanda) and `exhaustive` lint on state-machine switches; OpenAPI is gated by vacuum + oasdiff.
5. **Review is mandatory and never by the implementer.** **Adversarial verification** — an independent agent writes tests from the brief alone, without reading the implementation, and attacks the invariants — is mandatory for the money and audit WPs: outbox relay, payment allocation, arrangement schedules, reconciliation engine, rule DSL, decision-audit immutability, batch control totals, champion/challenger allocation, treatment guardrails, and the UI promise-to-pay command.
6. **Phase gates** are executed by a verifier agent that implemented none of the WPs under test; every gate line is a runnable command and the output is committed to `docs/gates/evidence/`. No phase starts on a red gate.
7. **Model assignment:** implementation WPs → Opus-class implementation agents; exemplar, L-WP decomposition (an L must become ≤4 sub-briefs before delegation), adversarial verification and gate verification → the strongest available model.
8. **Commits are local-only.** Agents may commit to a local branch when asked; they never `git push`, never open or merge PRs, never force-push. Publishing is a human action, as is `terraform apply` ([ADR-0010](./0010-terraform-stacks-ci-only-applies.md)).

## Alternatives considered

- **Ad-hoc agent development** (one long conversation, code as it comes). What this replaces: without frozen contracts every session invents its own shapes; without ownership parallel sessions overwrite each other; without a verify script "done" is an opinion. It works until the second agent starts.
- **Single-agent sequential build.** Maximally consistent and much simpler to supervise. Rejected: it is throughput-bound and wastes the one thing a fleet of agents is good at — parallel breadth across independent work packages.
- **Trusting review instead of mechanical gates.** Reviewers, human or model, do not reliably catch generated-code drift, a coverage regression, a breaking OpenAPI change or an out-of-scope file edit. A command does, every time, for free.
- **Letting agents push branches and open PRs.** Convenient. Rejected: publishing is irreversible in a way a local commit is not, and one mistaken force-push costs more than every convenience it buys.
- **Mutable contracts with a compatibility promise.** Rejected: "we'll keep it backwards compatible" is unverifiable across sessions; a new `vN` file is.
- **One giant brief per phase.** Rejected: briefs must fit a session and name exhaustive deliverable paths, which is also what makes ownership enforceable.

## Consequences

**Positive**

- Mechanical verification carries most of the review load — contract mismatch, generated-code drift, coverage, breaking changes and out-of-scope edits are all commands.
- Parallelism is safe at the plan's peak of 5–6 agents, and the exemplar makes the 14th service cheaper than the 2nd.
- A brief is self-contained, so a fresh session with no history can execute it, and a gate re-run reproduces the evidence rather than trusting a summary.

**Negative / caveats**

- The up-front cost is an entire contracts phase plus a maximum-scrutiny exemplar before a single feature works end to end — expensive if the design turns out to be wrong in a way contracts encode.
- Contract immutability means early mistakes live on as a `v1` file next to a `v2`. That is the deliberate trade against silent mutation, and it accumulates clutter.
- Path ownership serializes `platform/` and `contracts/` work into a queue, so the shared-library path is a throughput bottleneck by construction.
- Adversarial review roughly doubles the cost of the WPs it covers, and picking that list is a judgement call — a money bug outside the list gets ordinary review.
- Model assignment is a cost/quality bet, not a guarantee: a cheaper model on a money-handling WP is exactly where a mechanical gate has to catch what review missed.
- Local-only commits make the human the integration bottleneck. Chosen deliberately, but it means nothing merges while nobody is watching.
