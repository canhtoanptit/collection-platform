# module: github-oidc

The GitHub Actions OIDC provider and the four CI roles. Zero long-lived AWS credentials
(ADR-0010).

Used by `stacks/00-bootstrap` (FND-1).

## The four roles

| Role | Trusts (`sub` claim) | Permissions |
|---|---|---|
| `colx-gha-plan` | `repo:<repo>:pull_request`, `repo:<repo>:ref:refs/heads/main` | `ReadOnlyAccess` + state-bucket read/write + state-key KMS use |
| `colx-gha-apply` | `repo:<repo>:environment:dev` | `AdministratorAccess` |
| `colx-gha-ecr-push` | `repo:<repo>:ref:refs/heads/main` | ECR login + push/pull inside `colx/*` |
| `colx-gha-eks-deploy` | `repo:<repo>:environment:dev` | `eks:Describe*` / `List*` only |

**The trust policy is the control, not the permission policy.** Every role pins
`token.actions.githubusercontent.com:aud = sts.amazonaws.com` and a `sub` pattern scoped to this
repository. The two roles that can change anything are pinned to
`repo:<repo>:environment:dev`, so the GitHub environment's required-reviewer rule is enforced by
AWS's trust policy rather than by workflow YAML — which a pull request could otherwise edit.

## Three details that are easy to get wrong

1. **`colx-gha-plan` is not literally read-only.** S3-native state locking writes and deletes a
   `<key>.tflock` object next to the state file, and `ReadOnlyAccess` grants neither `s3:PutObject`
   nor `kms:Decrypt`. The inline `state_access` policy supplies exactly those, scoped to the state
   bucket and the state CMK. Remove it and every plan fails on "Error acquiring the state lock".
2. **`ecr:GetAuthorizationToken` cannot be resource-scoped.** ECR's authorization model has no
   resource for it; `docker login` needs it on `*`. Everything else in the ecr-push policy is scoped
   to `repository/colx/*`.
3. **`colx-gha-eks-deploy` has almost no IAM permissions on purpose.** Cluster authorization comes
   from the EKS *access entry* that `stacks/30-eks` creates for this role ARN. Widening this IAM
   policy grants nothing extra inside the cluster; narrowing cluster access means editing the access
   entry.

## Usage

```hcl
module "github_oidc" {
  source = "../../modules/github-oidc"

  name_prefix        = "colx"
  region             = var.region
  github_repository  = "canhtoanptit/collection-platform"
  github_environment = "dev"
  state_bucket_name  = aws_s3_bucket.tfstate.id
  state_kms_key_arn  = aws_kms_key.tfstate.arn
}
```

## Inputs

| Name | Type | Default | Description |
|---|---|---|---|
| `name_prefix` | `string` | `"colx"` | Role name prefix. |
| `region` | `string` | — | Scopes the ECR ARN. |
| `github_repository` | `string` | — | `owner/name`. |
| `github_environment` | `string` | `"dev"` | Gates apply and eks-deploy. |
| `default_branch` | `string` | `"main"` | Branch trusted for plan and image push. |
| `create_oidc_provider` | `bool` | `true` | See below. |
| `existing_oidc_provider_arn` | `string` | `null` | Required when the above is `false`. |
| `oidc_thumbprints` | `list(string)` | GitHub's published pair | Ignored by AWS for this issuer; see the variable description. |
| `state_bucket_name` | `string` | — | State bucket for the plan role's policy. |
| `state_kms_key_arn` | `string` | `null` | `null` skips the KMS statement (SSE-S3 bucket). |
| `ecr_repository_prefix` | `string` | `"colx"` | Namespace ecr-push may write. |
| `max_session_duration` | `number` | `3600` | AWS minimum, longer than any job here needs. |
| `apply_role_policy_arns` | `list(string)` | `["…/AdministratorAccess"]` | See prod deltas. |

## Outputs

`oidc_provider_arn`, `role_arns`, `role_names`, `eks_deploy_role_arn`, `trust_subjects`.

Role ARNs are **not** secrets — they are useless without a token whose `sub` matches. Put them in
GitHub Actions *variables*, not secrets, so `gh secret list | grep -c AWS_SECRET` stays at 0.

## One OIDC provider per account

IAM allows a single OIDC provider per issuer URL. If the account already has
`token.actions.githubusercontent.com` (another project, or a previous bootstrap), apply fails with
`EntityAlreadyExists`. Set `create_oidc_provider = false` and pass
`existing_oidc_provider_arn`; the roles attach to it identically.

## Prod deltas

- **No `AdministratorAccess` on the apply role.** Replace it with a policy scoped to the services the
  stacks actually touch, plus an IAM **permissions boundary** on every role the apply role can
  create, so a compromised apply cannot mint a more powerful role than itself. This is the single
  largest gap between this module and a production posture.
- **Separate apply roles per stack** (`colx-gha-apply-network`, `-data`, `-eks`), each scoped to its
  own state key prefix and its own resource types. A network change should not carry the permissions
  to delete a database.
- **Tighter subjects**: pin `job_workflow_ref` in addition to `sub`, so only the specific reusable
  workflow file may assume the role. `sub` alone still permits any workflow in the repository.
- **A distinct account per environment**, with the CI roles in the workload account and the
  federation trust granted from a dedicated tooling account.
- **CloudTrail alerting** on `AssumeRoleWithWebIdentity` for the apply role, and on any
  `CreateRole`/`AttachRolePolicy` it performs, routed to the SNS alerts topic.
- **Session tags** carrying the run id and actor (`sts:TagSession` in the trust policy) so CloudTrail
  attributes an action to a workflow run rather than to "the CI role".
