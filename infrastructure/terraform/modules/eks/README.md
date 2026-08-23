# modules/eks

Thin wrapper over [`terraform-aws-modules/eks/aws`] **pinned to `21.25.0`**, plus the EBS CSI driver's
IRSA role and addon.

The wrapper is not abstraction for its own sake. It exists so that three things are stated once instead
of in every stack that ever needs a cluster:

- `authentication_mode = "API"` — no `aws-auth` ConfigMap, every grant is an access entry in the plan.
- `endpoint_public_access_cidrs` has **no default** and rejects `0.0.0.0/0` — the API server is the only
  public endpoint in this environment (ADR-0011), so a permissive default would be the whole security
  posture, silently.
- One AL2023 managed node group, on-demand, `t3.large`.

## Usage

```hcl
module "eks" {
  source = "../../modules/eks"

  name               = "colx-dev"
  kubernetes_version = "1.32"

  vpc_id     = data.terraform_remote_state.network.outputs.vpc_id
  subnet_ids = data.terraform_remote_state.network.outputs.private_subnet_ids

  endpoint_public_access_cidrs = var.admin_cidrs

  access_entries = {
    operator = {
      principal_arn = var.admin_principal_arn
      policy_associations = {
        admin = {
          policy_arn   = "arn:aws:eks::aws:cluster-access-policy/AmazonEKSClusterAdminPolicy"
          access_scope = { type = "cluster" }
        }
      }
    }
  }
}
```

## Inputs worth reading before you change them

| Input | Default | Why the default is what it is |
|---|---|---|
| `kubernetes_version` | `1.32` | Plan FND-7, matched to the `mise.toml` kubectl pin. The upstream module does not validate this; EKS does. |
| `authentication_mode` | `API` | `CONFIG_MAP` means hand-editing `aws-auth` — an unreviewable, un-plannable grant. |
| `endpoint_public_access_cidrs` | *(required)* | Two validations: non-empty, and not `0.0.0.0/0`. |
| `enable_cluster_creator_admin_permissions` | `true` | The creator is `colx-gha-apply`. Off + a wrong `admin_principal_arn` = a cluster nobody can `kubectl` into, and the fix is recreating it. In an environment that is rebuilt weekly (ADR-0010) that trade is not worth making. |
| `node_ami_type` | `AL2023_x86_64_STANDARD` | AL2 is end of life. |
| `node_capacity_type` | `ON_DEMAND` | Spot saves ~60% and costs it back the first time a reclaim kills a task mid-DAG. |
| `addon_versions` | `{}` | Everything else in this repo pins exactly; addon version strings depend on the cluster's Kubernetes version and can only be enumerated from AWS (`aws eks describe-addon-versions --kubernetes-version 1.32`), so they are an operator input rather than a hard-coded default. Empty means "newest compatible". |
| `cluster_encryption_kms_key_arn` | `null` | `null` lets the module create its own CMK; passing `colx-dev-secrets` keeps key lifecycle in stack 20-data. |

## Addons

`coredns`, `kube-proxy` and `vpc-cni` go through the upstream module's `addons` map. `vpc-cni` and
`kube-proxy` are `before_compute` so nodes join with working networking on first boot.

`aws-ebs-csi-driver` is a **standalone `aws_eks_addon` resource in this module**, not an entry in the
upstream map. Its IRSA role's trust policy needs the cluster's OIDC provider, and feeding the resulting
role ARN back into a module *input* is the kind of wiring that only fails when the graph is built —
which `terraform validate` never does, so nothing in this repo's acceptance would catch it. Keeping the
addon local makes the ordering explicit and provably acyclic.

## Prod deltas

Not applied here; recorded so the dev shortcuts stay visible.

| Dev | Prod |
|---|---|
| Public endpoint on, CIDR-restricted | Private endpoint only; access via VPN/SSM bastion. `endpoint_public_access = false` |
| `enabled_log_types = ["api","audit","authenticator"]`, 7-day retention | All five types, ≥90-day retention, shipped off-cluster |
| One node group, one AZ's worth of capacity tolerance | Separate system/workload node groups, ≥3 AZ, PodDisruptionBudgets, Karpenter or cluster-autoscaler |
| `enable_cluster_creator_admin_permissions = true` | `false`; access entries only, break-glass role documented separately |
| Node group scaled to 0 by `make stop` | No such lever; capacity is a reservation |
| `preserve = true` on the CSI addon | Same, plus a tested volume-restore drill |

[`terraform-aws-modules/eks/aws`]: https://registry.terraform.io/modules/terraform-aws-modules/eks/aws/21.25.0
