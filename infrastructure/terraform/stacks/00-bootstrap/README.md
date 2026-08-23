# stack 00-bootstrap

**The only stack a human applies** (ADR-0010, CLAUDE.md §7). Everything else is applied by
environment-gated CI using the roles this stack creates.

It exists to break a circular dependency: CI cannot run Terraform without a state bucket and a
role to assume, and neither can be created by CI. So this stack is applied once, from a laptop,
with local state — and then migrated into the bucket it just created.

## Contents

| Resource | Name | File |
|---|---|---|
| State bucket | `colx-tfstate-<account_id>` | `state.tf` |
| State CMK + alias | `alias/colx-tfstate` | `state.tf` |
| GitHub OIDC provider | `token.actions.githubusercontent.com` | `modules/github-oidc` |
| CI roles ×4 | `colx-gha-{plan,apply,ecr-push,eks-deploy}` | `modules/github-oidc` |
| SNS topic + email sub | `colx-dev-alerts` | `modules/budgets` |
| Budget | `colx-dev-monthly` ($450) | `modules/budgets` |
| Cost anomaly monitor + subscription | `colx-dev-service-monitor` | `modules/budgets` |

Nothing here is part of the platform runtime. Every resource is an account-level singleton, so
after the first apply this stack changes roughly never — which is what makes a human-applied
exception acceptable.

---

## Values you must supply before applying

| Variable | Where | Why there is no usable default |
|---|---|---|
| `alert_email` | **required, no default** | An alerting channel that defaults to someone else's inbox is worse than none. |
| `github_repository` | default `canhtoanptit/collection-platform` | Change it if the repo is forked or renamed, or CI cannot authenticate: it is pinned in all four trust policies. |
| `github_environment` | default `dev` | Must match the GitHub environment whose required-reviewer rule gates applies. |
| `region` | default `eu-west-1` | Change before the first apply only; moving it afterwards recreates the bucket. |
| `create_github_oidc_provider` | default `true` | Set `false` + `existing_github_oidc_provider_arn` if the account already has a GitHub OIDC provider — IAM allows only one per issuer. |
| `state_bucket_name_override` | default `null` | Only if `colx-tfstate-<account_id>` collides (S3 names are global). |

Copy `envs/dev/common.tfvars.example` to `envs/dev/common.tfvars` and fill it in.
`common.tfvars` is **not committed** — `.gitignore` covers `*.tfvars.local`, and the convention in
`envs/dev/README.md` is that no real `.tfvars` is ever committed.

`admin_cidrs` is also in that file. This stack does not use it; `stacks/30-eks` (INF-B) does, to
restrict the Kubernetes API endpoint to your address. Fill it in now so the later stack is not
blocked: `curl -s https://checkip.amazonaws.com` gives you the value, as `x.x.x.x/32`.

---

## The bootstrap sequence

Run every command from this directory: `infrastructure/terraform/stacks/00-bootstrap`.

### 1. Credentials

```bash
aws configure                 # or aws configure sso / export AWS_PROFILE=...
aws sts get-caller-identity   # must print the account you intend to build in
```

This is the one and only place where a human uses long-lived AWS credentials directly. From step 6
onward, everything runs through OIDC federation.

### 2. Apply with local state

The backend block in `versions.tf` is **commented out**, so this apply writes
`terraform.tfstate` next to the configuration.

```bash
terraform init                                                  # no -backend-config yet
terraform plan  -var-file=../../envs/dev/common.tfvars
terraform apply -var-file=../../envs/dev/common.tfvars
```

Expect roughly 20 resources. Read the plan: an unexpected *destroy* at this point means a local
state file from an earlier attempt is still present and disagrees with reality.

### 3. Confirm the SNS subscription

AWS emails a confirmation link to `alert_email`. **Click it.** Until you do, the subscription sits
in `PendingConfirmation`, `terraform plan` stays clean, and no budget, anomaly or (later)
Alertmanager notification is ever delivered.

### 4. Write the backend config

```bash
terraform output -raw backend_config
```

Compare with `../../envs/dev/backend.hcl` and replace the `000000000000` placeholder in `bucket`
with the account id the output shows. Nothing else in that file should need to change.

### 5. Uncomment the backend and migrate state

In `versions.tf`, uncomment the `backend "s3"` block (it keeps `key = "stacks/00-bootstrap.tfstate"`;
everything else comes from `backend.hcl`), then:

```bash
terraform init -backend-config=../../envs/dev/backend.hcl -migrate-state
# answer "yes" when asked to copy existing state to the new backend

terraform plan -var-file=../../envs/dev/common.tfvars   # must report no changes
rm -f terraform.tfstate terraform.tfstate.backup
```

Deleting the local state files matters. A stale `terraform.tfstate` is the most likely way this
stack gets applied twice against two different versions of the truth.

If `init` rejects `kms_key_id = "alias/colx-tfstate"`, substitute the full ARN from
`terraform output -raw state_kms_key_arn` and note it in `backend.hcl`.

### 6. Wire up GitHub

```bash
terraform output gha_role_arns
terraform output gha_trust_subjects   # read this: it is the entire security boundary
```

- Set the four role ARNs as GitHub Actions **variables** (not secrets — they are useless without a
  matching OIDC token, and `gh secret list | grep -c AWS_SECRET` must stay at `0`).
- Create the GitHub environment named by `github_environment` (`dev`) and add yourself as a
  **required reviewer**. The `apply` and `eks-deploy` roles trust only
  `repo:<repo>:environment:dev`, so that reviewer setting is the approval gate — enforced by an AWS
  trust policy, not by workflow YAML a pull request could edit.
- FND-12 (`.github/workflows/terraform.yml`, INF-B) consumes these.

### 7. Verify

```bash
bash ../../../../scripts/verify/INF-A.sh    # fmt/validate/structure, no AWS calls
terraform output next_steps
```

---

## Applying the other stacks

`10-network`, `20-data`, `30-eks` and `40-snowflake` are **never applied from a laptop**. They are
applied by CI through `colx-gha-apply` after an approval on the `dev` environment. Locally, agents
and humans get `plan` only:

```bash
make tf-plan STACK=10-network ENV=dev
```

Apply order is `10-network` → `20-data` → `30-eks` → `40-snowflake` (the last one at Phase 6
kickoff, per plan §6.10). `20-data` reads `10-network`'s outputs through `terraform_remote_state`,
so the network stack's state must exist first.

---

## If you lose the state CMK

Scheduling `alias/colx-tfstate` for deletion makes every state file in the bucket permanently
unreadable after the 30-day window — including this stack's own. The recovery path is not "restore
a backup", it is:

1. Cancel the key deletion if you are still inside the 30-day window (`aws kms cancel-key-deletion`).
   This is the only cheap outcome; everything below is expensive.
2. If the window has elapsed: re-create the bucket and key with local state, then `terraform import`
   the surviving resources into each stack. Every resource this platform creates is named
   deterministically (`colx-dev-*`), which is what makes import feasible at all.

The mitigations that make this survivable are bucket versioning, the 30-day deletion window, and
the deliberate rule that this stack contains only account-level singletons — no data, no databases,
nothing whose loss cannot be re-imported.

---

## Prod deltas

- **Bootstrap runs in a separate tooling account** with the workload accounts trusting it, so the
  human-applied exception exists once for the organization rather than once per environment.
- **No `AdministratorAccess`.** Replace the apply role's policy with a scoped one plus an IAM
  permissions boundary, and split it per stack (`…-apply-network`, `…-apply-data`) so a network
  change cannot delete a database. See `modules/github-oidc/README.md`.
- **State bucket hardening**: MFA-delete, Object Lock in governance mode, replication to a second
  region with its own CMK, and CloudTrail data events on the bucket routed to the alerts topic.
- **`use_lockfile` plus a state-access alarm.** S3-native locking is correct, but prod also wants an
  alert on any `PutObject` to the state prefix from a principal other than the apply role.
- **The stack itself applied by CI** from a bootstrap pipeline in the tooling account, with local
  state used exactly once and then destroyed — closing the "everything is code, with an asterisk"
  gap ADR-0010 accepts in dev.
- **Budgets and anomaly monitors per linked account**, consolidated at the Organization level.
