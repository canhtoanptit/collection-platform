# modules/irsa-role

One IAM role per Kubernetes service account, created from a map. This is the module that makes "which
pod can read which bucket" answerable from a single diff.

## Usage

```hcl
module "irsa" {
  source = "../../modules/irsa-role"

  name_prefix       = "colx-dev"
  oidc_provider_arn = module.eks.oidc_provider_arn
  oidc_provider     = module.eks.oidc_provider

  roles = {
    loki = {
      namespace   = "platform"
      policy_json = data.aws_iam_policy_document.loki.json
      description = "Loki: s3://colx-dev-ops/loki/*"
    }

    airflow = {
      namespace              = "airflow"
      policy_json            = data.aws_iam_policy_document.airflow.json
      extra_service_accounts = ["airflow-scheduler", "airflow-webserver"]
    }
  }
}
```

Role name is `<name_prefix>-<map key>`. Service account name defaults to the map key.

## Trust policy

```
Federated: <oidc_provider_arn>
Action:    sts:AssumeRoleWithWebIdentity
Condition: <oidc_provider>:sub == system:serviceaccount:<namespace>:<sa>   (exact, one per SA)
           <oidc_provider>:aud == sts.amazonaws.com
```

Two details are load-bearing:

- **`sub` is always an exact match.** `system:serviceaccount:<ns>:*` would let any pod in the namespace
  assume the role, and `system:serviceaccount:*:*` would hand every role to every pod — which is
  precisely the node-role permission model IRSA exists to replace (ADR-0011). Where one chart creates
  several service accounts with identical AWS needs (Airflow), list them in `extra_service_accounts`;
  the condition then holds a list of exact subjects, which IAM evaluates as OR.
- **`aud` is checked.** Without it a token minted for a different audience can be replayed.

## Permission-less roles

`policy_json = null` with `managed_arns = []` creates a role that can do nothing. The `roles` variable
validates against this by default and allows exactly one exception, keyed `alb-controller`: the AWS Load
Balancer Controller role is created now so its trust relationship is in place, and its policy arrives in
Phase 12 with the ingress flag and a domain (ADR-0011). If you find yourself wanting a second exception,
that is the signal to write the policy instead.

## Outputs

| Output | Use |
|---|---|
| `role_arns` | `logical name -> role ARN`. The only legitimate source for a `eks.amazonaws.com/role-arn` annotation. |
| `annotations` | Pre-built `{ "eks.amazonaws.com/role-arn" = ... }` maps, so a values file never carries a hand-typed ARN. |
| `service_accounts` | `logical name -> namespace/serviceaccount`. `scripts/verify/INF-B.sh` asserts the values files agree with this. |
| `trusted_subjects` | Every subject in each trust policy — useful when an AssumeRole fails and you need to see what the role actually trusts. |

## Prod deltas

| Dev | Prod |
|---|---|
| `max_session_hours = 1` | Same; 1 hour is already the floor worth having |
| No permissions boundary | Attach a permissions boundary to every IRSA role so a policy edit cannot escalate |
| Inline policies | Managed policies with versions, so a bad change can be rolled back without a Terraform apply |
| One role per workload | Same, plus per-role CloudTrail alarms on unexpected `AssumeRoleWithWebIdentity` sources |
