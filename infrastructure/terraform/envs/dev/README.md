# envs/dev

Per-environment Terraform inputs. Two files, and one that must never be committed.

| File | Committed | Purpose |
|---|---|---|
| `backend.hcl` | yes | Shared S3 backend config: bucket, region, encryption, `use_lockfile`. |
| `common.tfvars.example` | yes | Template for every user-supplied variable, with placeholders. |
| `common.tfvars` | **no** | Your filled-in copy. |

```bash
cp common.tfvars.example common.tfvars
$EDITOR common.tfvars        # at minimum: alert_email, state_bucket, admin_cidrs
```

`.gitignore` already refuses `*.tfstate*` and `*.tfvars.local`. `common.tfvars` is excluded by
convention and by review: it holds no secrets by design (see §4 of the example file), but it does
hold your IP address and account id, and neither belongs in a public repository.

## State key convention

Each stack sets its own `key` inside its `backend "s3"` block; `backend.hcl` carries only what every
stack shares. The convention is:

```
stacks/<nn-name>.tfstate
```

| Stack | State key | Owner |
|---|---|---|
| `00-bootstrap` | `stacks/00-bootstrap.tfstate` | INF-A (human-applied) |
| `10-network` | `stacks/10-network.tfstate` | INF-A |
| `20-data` | `stacks/20-data.tfstate` | INF-A |
| `30-eks` | `stacks/30-eks.tfstate` | INF-B |
| `40-snowflake` | `stacks/40-snowflake.tfstate` | INF-B |

Splitting it this way means a stack's identity is in the stack (visible when you read it) while the
account-specific parts are in one file (changed once per account). Putting `key` in `backend.hcl`
would force a `-backend-config="key=..."` argument on every command, and forgetting it silently
initializes the wrong state.

## `state_bucket` appears twice, deliberately

- `backend.hcl` → `bucket` — where **this** stack's state lives.
- `common.tfvars` → `state_bucket` — where the `terraform_remote_state` data sources look for
  **other** stacks' state.

These are separate mechanisms: a backend configuration is not visible to a data source, and
Terraform will not reconcile them. `scripts/verify/INF-A.sh` asserts the two files carry the same
bucket name, because the failure mode otherwise is a plan that reads stale outputs from a bucket in
a different account.

## Usage

```bash
# Plan (the only thing agents and humans may run against a CI-applied stack)
make tf-plan STACK=10-network ENV=dev

# Equivalent, by hand
terraform -chdir=infrastructure/terraform/stacks/10-network \
  init -backend-config=../../envs/dev/backend.hcl
terraform -chdir=infrastructure/terraform/stacks/10-network \
  plan -var-file=../../envs/dev/common.tfvars
```

`terraform apply` runs only in environment-gated CI (ADR-0010). `00-bootstrap` is the single
documented human-applied exception; its README has the sequence.

## Expect "Value for undeclared variable" warnings

One shared tfvars file serves five stacks, so every plan warns about the values the current stack
does not declare. That is a warning, never an error. The alternative — five near-identical tfvars
files — drifts, and a drifted `admin_cidrs` is a locked-out cluster.

## Provider lock files are multi-platform

Each stack's `.terraform.lock.hcl` is committed and carries checksums for **both** `darwin_arm64`
(developer laptops) and `linux_amd64` (GitHub Actions runners), produced with:

```bash
terraform -chdir=infrastructure/terraform/stacks/<nn-name> \
  providers lock -platform=linux_amd64 -platform=darwin_arm64
```

Without the second platform, a lock file written on a laptop makes CI either rewrite the lock file
mid-run or fail outright under `terraform init -lockfile=readonly`. Re-run the command above after
any provider version bump, and commit the result — that is what makes a CI plan reproduce a local
one exactly.

## Adding a new environment

Copy this directory to `envs/<name>/`, change `region` and the bucket if the environment lives in
another account, and add the environment to FND-12's workflow matrix. Nothing in the stacks is
hard-coded to `dev` beyond the `env` variable's default, which becomes the `env` tag and the
`colx-<env>-*` name segment.
