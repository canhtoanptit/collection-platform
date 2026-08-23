# stacks/30-eks

The `colx-dev` EKS cluster and the IRSA role set (plan FND-7, ADR-0011).

State key `stacks/30-eks.tfstate`. **Applied by CI only** (ADR-0010) — agents run
`make tf-plan STACK=30-eks ENV=dev` at most.

## What it creates

- EKS `colx-dev`, Kubernetes 1.32, `authentication_mode = API`.
- Access entries: the human operator (`var.admin_principal_arn`) and `colx-gha-eks-deploy`, both
  cluster-admin. See the DEV NOTE in `main.tf` — cluster-admin for CI is a dev-only shortcut.
- Public API endpoint restricted to `var.admin_cidrs`, private endpoint on.
- One AL2023 managed node group: `t3.large`, min 2 / desired 3 / max 4, on-demand.
- Addons `vpc-cni`, `coredns`, `kube-proxy`; `aws-ebs-csi-driver` with its own IRSA role.
- 11 IRSA roles (10 with policies, 1 deliberately empty) — see the table below.

## Prerequisites (user-supplied)

| Input | Where it comes from |
|---|---|
| `state_bucket` | `colx-tfstate-<account-id>`, created by stack 00-bootstrap. Same value as `bucket` in `envs/dev/backend.hcl`; documented in `envs/dev/common.tfvars.example`. |
| `admin_principal_arn` | Your IAM user or role ARN. For SSO, the **role** ARN (`arn:aws:iam::<acct>:role/aws-reserved/sso.amazonaws.com/...`), not the assumed-role session ARN. |
| `admin_cidrs` | Your public address as a `/32`, plus any CI egress range needing `kubectl`. `0.0.0.0/0` is rejected by the module. |
| `addon_versions` *(optional)* | `aws eks describe-addon-versions --kubernetes-version 1.32` once the cluster exists, if you want exact pins. |

`envs/dev/common.tfvars` is deliberately **not** committed (only `.example` is). Locally, copy the
example and fill it in. In CI, `.github/workflows/terraform.yml` rebuilds the file from repository
**variables** — non-secret by construction, since a tfvars value ends up in state and in every plan
comment anyway:

| Repository variable | Feeds |
|---|---|
| `AWS_ACCOUNT_ID` | `state_bucket`, and every `role-to-assume` ARN |
| `ADMIN_CIDRS` | `admin_cidrs` — HCL list syntax, e.g. `["203.0.113.10/32"]` |
| `ADMIN_PRINCIPAL_ARN` | `admin_principal_arn` |
| `ALERT_EMAIL` | 00-bootstrap's SNS subscription |
| `SNOWFLAKE_*` *(Phase 6)* | stack 40-snowflake; the private key is the one secret, as `SNOWFLAKE_TF_PRIVATE_KEY` |

`ADMIN_PRINCIPAL_ARN` is the one input in that list which `envs/dev/common.tfvars.example` (INF-A)
does not yet mention — it is required by this stack and has no default.

## Conventions shared with stacks 00/10/20

- `key` lives in this stack's `backend "s3"` block (`stacks/30-eks.tfstate`); `envs/dev/backend.hcl`
  supplies bucket, region, encryption and `use_lockfile`.
- The prefix variable is named **`project`** (not `prefix`), matching `common.tfvars`.
- Upstream state keys are variables (`network_state_key`, `data_state_key`) with the conventional
  defaults, the same shape as 20-data's `network_state_key`.
- `.terraform.lock.hcl` is committed and locked for `linux_amd64` **and** `darwin_arm64`
  (`terraform providers lock -platform=... -platform=...`) — a single-platform lock passes on the
  machine that wrote it and fails in CI.

## Upstream contract

This stack reads two upstream states. Renaming any of these outputs breaks the plan here.

| From | Output | Used for |
|---|---|---|
| `stacks/10-network.tfstate` | `vpc_id` | cluster + node group |
| `stacks/10-network.tfstate` | `private_subnet_ids` | node group and control-plane ENIs |
| `stacks/20-data.tfstate` | `msk_cluster_arn` | `kafka-cluster:*` IRSA policies (override with `var.msk_cluster_arn`) |

Everything else that could have been a remote-state read is deliberately **not** one:

- **Bucket ARNs are constructed** from `project`/`env` (`colx-dev-raw`, …). The bucket names are a fixed
  standard (plan §6.7), so constructing them removes a coupling that would otherwise break on every
  rename in 20-data.
- **CMKs are looked up by alias** (`alias/colx-dev-data`, `alias/colx-dev-secrets`). The alias names are
  also convention, and a missing alias fails the plan with a message that names the alias.
- **The Secrets Manager and SNS ARNs are constructed** from account id + region + `project`.

## IRSA roles

Every role's inline policy is in `policies.tf`. `namespace/serviceaccount` **must** match the
`serviceAccount.name` in the corresponding `deployment/values/<release>/dev.yaml`;
`scripts/verify/INF-B.sh` asserts that the two agree, because a mismatch surfaces at runtime as an
opaque `AccessDenied` rather than at deploy time.

| Role | SA | AWS access |
|---|---|---|
| `external-secrets` | `platform/external-secrets` | Read `colx/dev/*` secrets; decrypt with the secrets CMK |
| `ingestion-cp` | `ingestion/ingestion-cp` | RW landing/raw/quarantine/archive; MSK read+write; secrets |
| `sftp-worker` | `sftp/sftp-worker` | Write landing only (never reads back); secrets |
| `webhook-receiver` | `ingestion/webhook-receiver` | MSK produce only; secrets (HMAC) |
| `kafka-connect` | `kafka/kafka-connect` | MSK topic admin (no delete); write `raw/`; secrets |
| `airflow` | `airflow/airflow` (+6 component SAs) | Write `ops/`, `batch/`; **read-only** raw/quarantine/archive; secrets |
| `simulator` | `simulator/simulator` | Write landing only; secrets |
| `loki` | `platform/loki` | `ops/loki/*` only, ListBucket gated by `s3:prefix` |
| `tempo` | `platform/tempo` | `ops/tempo/*` only, ListBucket gated by `s3:prefix` |
| `alertmanager` | `platform/alertmanager` | `sns:Publish` to `colx-dev-alerts` |
| `alb-controller` | `kube-system/aws-load-balancer-controller` | **Nothing.** Role + trust only; policy lands in Phase 12 (INF-14) |

Two shapes to notice while reviewing:

- **Airflow can read the data it reconciles but not rewrite it.** `ops/` and `batch/` are writable
  because they are Airflow's own artefacts; `raw/`, `quarantine/` and `archive/` are read-only, so a DAG
  cannot "fix" a discrepancy by editing the evidence (D§38).
- **Loki and Tempo share one bucket but cannot enumerate each other.** The `s3:prefix` condition on
  `ListBucket` is what enforces that, and it is also what stops either of them from listing Airflow's
  task logs in the same bucket.

MSK actions are `kafka-cluster:*` (data plane), never `kafka:*` (control plane — create/delete clusters).
Topic and group ARNs are derived from the cluster ARN by substituting the resource type, because they
share the cluster's generated UUID; `var.msk_topic_arn` / `var.msk_group_arn` override that when the
topic inventory is stable enough to narrow per workload.

## Later WPs extend this stack

Adding a workload is one entry in `local.irsa_roles` plus one policy document. Known future additions:
decision-service (`decision-audit` + `batch` buckets, DEC-6), treatment-service (DEC-14), and the ALB
controller's real policy (INF-14).

## Prod deltas

| Dev | Prod |
|---|---|
| `colx-gha-eks-deploy` is cluster-admin | Namespace-scoped editor; CRD installs and version upgrades are a separate, human-approved path |
| Public endpoint, CIDR-allowlisted | Private endpoint only |
| One node group | System/workload split, ≥3 AZ |
| Inline IRSA policies, no permissions boundary | Managed policies + permissions boundary on every IRSA role |
| `alb-controller` role has no policy | Policy attached, WAF in front, ACM cert required |
