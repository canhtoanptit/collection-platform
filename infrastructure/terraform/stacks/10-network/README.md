# stack 10-network

The VPC. **CI-applied** (ADR-0010) — locally you get `make tf-plan STACK=10-network ENV=dev` and
nothing else.

Thin by design: everything of substance lives in [`modules/network`](../../modules/network/README.md),
which wraps `terraform-aws-modules/vpc/aws` at exactly `6.7.0` and adds the S3 gateway endpoint.
Read that README for the subnet layout, the CIDR arithmetic and the reasoning behind each forced
setting.

## Position in the apply order

```
00-bootstrap (human)  ->  10-network  ->  20-data  ->  30-eks  ->  40-snowflake
```

First stack applied by CI, and the only one with no upstream dependency. `20-data` and `30-eks`
read its outputs via `terraform_remote_state`, so its state must exist before either can plan.

State key: `stacks/10-network.tfstate` (set in `versions.tf`; bucket and region come from
`envs/dev/backend.hcl`).

## What it produces

```
10.40.0.0/16  eu-west-1  az_count = 2
  public   10.40.0.0/20    10.40.16.0/20     IGW route,  hosts the single NAT gateway
  private  10.40.64.0/20   10.40.80.0/20     NAT route,  EKS nodes + MSK brokers + Connect
  data     10.40.128.0/20  10.40.144.0/20    no egress,  RDS platform + RDS corebank
  + S3 gateway endpoint on all three tiers' route tables
  + DB subnet group "colx-dev-data"
  + default security group adopted and emptied
```

## Values you may want to change

| Variable | Default | Notes |
|---|---|---|
| `vpc_cidr` | `10.40.0.0/16` | Changing it after apply replaces every subnet, and with them the RDS subnet group and the MSK cluster. Decide once. |
| `az_count` | `2` | MSK needs ≥ 2. The CIDR layout reserves space up to 4 and appends rather than renumbers. |
| `single_nat_gateway` | `true` | |
| `enable_flow_log` | `false` | |
| `eks_cluster_name` | `"colx-dev"` | **Must match** the cluster name in `stacks/30-eks` (INF-B), or the AWS Load Balancer Controller will not discover these subnets in Phase 12. |

No user-supplied secret or account-specific value is needed: this stack's defaults are complete.

## Acceptance (FND-2)

Post-apply, from CI or a read-only session:

```bash
terraform output private_subnet_ids     # 2 ids
terraform output nat_gateway_ids        # exactly 1
terraform output data_route_table_ids   # then assert no 0.0.0.0/0 route on them
```

The third check is the one that matters. The data tier's isolation depends on
`create_database_subnet_route_table = true`; without it the upstream module quietly associates the
data subnets with the private route tables, which *do* have a NAT route — the configuration would
look right and the isolation would not exist.

## Teardown

`make destroy-heavy` (FND-13) keeps this stack: a VPC costs nothing, and the NAT gateway is the only
billable resource in it (~$33/mo). Destroying the VPC forces MSK, RDS and EKS to be rebuilt from
scratch, turning a ~15 minute restart into a ~60 minute rebuild.

If the NAT charge matters during a long pause, `enable_nat_gateway = false` removes it while keeping
the VPC, subnets, endpoint and subnet group intact. Private-subnet egress dies with it, so nothing
in the cluster can pull an image until it comes back.

## Prod deltas

Full list in [`modules/network/README.md`](../../modules/network/README.md#prod-deltas). The
stack-level ones:

- **`az_count = 3`** so MSK can run RF 3 / min ISR 2 and survive an AZ loss.
- **`single_nat_gateway = false`** with one NAT per AZ: removes both the single point of failure for
  egress and the cross-AZ data charge on traffic from the second AZ.
- **`enable_flow_log = true`** with delivery to S3, not CloudWatch Logs — this is a
  security-investigation requirement, not observability polish.
- **Interface endpoints** (ECR API/DKR, STS, Secrets Manager, KMS, Logs, `kafka`) so the control
  paths do not depend on NAT or on the internet.
- **A second VPC, or at least dedicated subnets, for anything internet-facing**, keeping the ALB out
  of the same route tables as the workload.
