# ADR-0010: Infrastructure — Terraform ≥1.11, five stacks, S3-native locking, CI-only applies

- **Status:** Accepted (cost model amended 2026-08-23: the Cognito line is gone and Keycloak runs on existing nodes — see [ADR-0017](./0017-identity-keycloak-on-eks.md))
- **Date:** 2026-08-22
- **Related:** [ADR-0003](./0003-postgres-per-service-shared-rds.md), [ADR-0004](./0004-kafka-eventing-envelope-outbox.md), [ADR-0011](./0011-identity-cognito-irsa-no-ingress.md), [ADR-0017](./0017-identity-keycloak-on-eks.md), [ADR-0013](./0013-llm-agent-delegation-model.md), [ADR-0015](./0015-observability-otel-grafana-stack.md)

## Context

The substrate is real, metered AWS (EKS, MSK, RDS, S3, KMS/Secrets Manager) plus a real Snowflake account. It is built, torn down and rebuilt repeatedly by agent sessions, so it must be entirely declarative — nothing may depend on a human clicking in a console. Two risks dominate: an agent corrupting or destroying infrastructure state (plan risk 2), and cost creep on always-on components (plan risk 10).

Resource prefix `colx`, env `dev`, region `eu-west-1` (variable), tags `project=colx, env=dev, stack=<stack>, managed-by=terraform`.

## Decision

- **Terraform ≥1.11** (pinned via mise) with **S3-native state locking** (`use_lockfile`) — no DynamoDB table. State bucket versioned, SSE-KMS, TLS-only.
- **Five independent stacks:** `00-bootstrap` (state bucket, GitHub OIDC provider and roles, SNS alerts, budgets) then `10-network`, `20-data`, `30-eks`, `40-snowflake`. Independent state per stack bounds blast radius and makes partial teardown possible.
- **GitHub OIDC federation, zero long-lived AWS keys.** Roles `colx-gha-plan` (read-only), `colx-gha-apply` (environment-gated), `colx-gha-ecr-push`, `colx-gha-eks-deploy`, trust-scoped to the repo and environment. `gh secret list | grep -c AWS_SECRET` == 0 is an acceptance check.
- **Applies happen only in env-gated, human-approved CI.** Agents may run `plan` (`make tf-plan STACK=…`) and nothing else. `00-bootstrap` is the single documented human-applied exception. Kubernetes deploys are Helmfile with pinned charts: `diff` on PR, `apply` on main, environment-gated.
- **Cost guardrails and levers:** AWS Budget $450/mo with 50/80/100% and forecast alerts plus a Cost Anomaly monitor → SNS email; Snowflake resource monitor capping 50 credits/mo; make targets `stop / start / destroy-heavy / up-all / destroy-all` with a documented rebuild path.

| Lever | Monthly cost | Rebuild |
|---|---|---|
| everything running | **$530–565** (EKS $273, MSK $80, RDS $50, NAT $40, KMS+Secrets $12, misc $15, Snowflake $60–90 active; Keycloak ≈ $0 on existing nodes) | — |
| `stop` | ≈ $230 | minutes |
| `destroy-heavy` | ≈ $60 | ~60 min |
| full destroy | < $5 | ~60 min |

## Alternatives considered

- **One Terraform stack.** Simplest mental model and no cross-stack data lookups. Rejected: a single bad plan can take the cluster *and* everything else with it, one state file is one lock and one corruption target, and partial teardown (the main cost lever) becomes impossible.
- **DynamoDB state locking.** The pre-1.10 default: another table to create, pay for and forget. S3 conditional writes remove the component entirely.
- **Local applies / long-lived IAM access keys.** Fastest for a solo developer, and the single most likely way an agent session destroys or leaks something: an agent with apply rights and a stale plan is an outage generator, and a key in a shell history is a breach. Rejected outright — this is the operating rule the rest of the delegation model leans on ([ADR-0013](./0013-llm-agent-delegation-model.md)).
- **CDK / Pulumi.** A real programming language for infrastructure, better abstraction. Rejected: HCL's `plan` output is exactly the reviewable artefact an agent workflow needs, and A§1.2's baseline names Terraform/OpenTofu-compatible IaC.
- **Terraform Cloud.** Remote runs, locking and policy as a service — an extra account and cost for what gated CI already provides.
- **ClickOps for the "quick" bits.** Rejected because a rebuild must be a CI run, not a memory.

## Consequences

**Positive**

- The whole environment is a CI run rather than a runbook; `destroy-heavy` then `up-all` is a supported path, exercised once as an acceptance criterion.
- No credential exists to leak; the plan role cannot mutate anything, so the worst an agent can do is produce a diff.
- Cost has both an alarm and an off switch, and the stack boundaries are what make the off switch selective.

**Negative / caveats**

- CI-only apply means every infrastructure iteration costs a PR plus an approval. Slow by design, and genuinely painful when debugging a broken addon or an IRSA annotation.
- `00-bootstrap` is human-applied, so "everything is code" carries an asterisk — and its state migration (local → S3) is a documented manual flow.
- **EKS control plane, MSK brokers and the NAT gateway bill whether or not anything runs** — hence the ≈ $230/mo `stop` floor. Cost discipline is a weekly habit (`make stop`), not a feature.
- `destroy-heavy` rebuilds take ~60 minutes, a real tax on picking work back up, and every rebuild re-applies Kafka topics, Helm releases and Snowflake grants (idempotent by design, occasionally by luck).
- S3-native locking requires Terraform ≥ 1.11: an older CLI silently means *no* locking, so the mise pin is load-bearing.
- Five stacks means cross-stack outputs (remote state reads) and an apply order — a coupling that has to be documented because it is no longer visible in one graph.
