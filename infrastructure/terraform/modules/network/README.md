# module: network

The platform VPC. Wraps [`terraform-aws-modules/vpc/aws`](https://registry.terraform.io/modules/terraform-aws-modules/vpc/aws)
at **exactly `6.7.0`** and adds the S3 gateway endpoint.

Used by `stacks/10-network` (FND-2).

## Shape

```
10.40.0.0/16                     az_count = 2, /20 per subnet
  public   10.40.0.0/20   10.40.16.0/20     IGW route    -> NAT gateway (1), future ALB
  private  10.40.64.0/20  10.40.80.0/20     NAT route    -> EKS nodes, MSK brokers, Connect
  data     10.40.128.0/20 10.40.144.0/20    no route out -> RDS platform + corebank
```

Per-tier CIDR offset is 4 (`public` 0-3, `private` 4-7, `data` 8-11, 12-15 spare), so raising
`az_count` appends subnets instead of renumbering — renumbering would destroy and recreate every
subnet in the VPC, and with it the RDS subnet group and the MSK cluster.

## What the wrapper adds or forces

- **The data tier genuinely has no egress.** `create_database_subnet_route_table = true` plus
  `create_database_nat_gateway_route = false`. Without the first flag the upstream module
  associates the data subnets with the *private* route tables, which do have a NAT route — the
  isolation would look configured and not exist. `data_route_table_ids` is exported so a verify
  script can assert the absence of a `0.0.0.0/0` route.
- **S3 gateway endpoint** on all three tiers' route tables. Gateway endpoints are free, and this is
  the largest cost lever in the module: raw partitions, Airflow remote logs, Loki/Tempo chunks and
  ECR layer pulls (ECR layers are S3 objects) all stop paying NAT data-processing charges.
- **The default security group is adopted and emptied.** A resource created without an explicit
  security group ends up isolated rather than reachable from the whole VPC.
- **EKS subnet tags now, not in Phase 12.** `kubernetes.io/role/elb = 1` on public and
  `kubernetes.io/role/internal-elb = 1` on private, plus an optional
  `kubernetes.io/cluster/<name> = shared` when `eks_cluster_name` is set. Phase 12's ingress is then
  a flag flip rather than a network change during a UI push.
- **Availability zones are data-sourced and filtered** to `opt-in-not-required`, excluding local and
  wavelength zones. Those sort into the middle of the returned list and cannot host RDS, MSK or a
  node group, so `slice(names, 0, 2)` without the filter is an apply-time failure that reads like a
  quota problem.

## Usage

```hcl
module "network" {
  source   = "../../modules/network"
  name     = "colx-dev"
  region   = var.region
  vpc_cidr = "10.40.0.0/16"
  az_count = 2

  single_nat_gateway = true
  enable_flow_log    = false
  eks_cluster_name   = "colx-dev"
}
```

## Inputs

| Name | Type | Default | Description |
|---|---|---|---|
| `name` | `string` | — | VPC name and prefix for everything it owns. |
| `region` | `string` | — | Region, used for the endpoint service name. |
| `vpc_cidr` | `string` | `"10.40.0.0/16"` | Must be `/20` or larger. |
| `az_count` | `number` | `2` | 2-4. MSK needs ≥ 2. |
| `enable_nat_gateway` | `bool` | `true` | |
| `single_nat_gateway` | `bool` | `true` | One NAT for the whole VPC. |
| `enable_flow_log` | `bool` | `false` | See prod deltas. |
| `eks_cluster_name` | `string` | `null` | Adds the cluster subnet tag. |
| `enable_s3_gateway_endpoint` | `bool` | `true` | |

## Outputs

`vpc_id`, `vpc_cidr_block`, `azs`, `public_subnet_ids`, `private_subnet_ids`, `data_subnet_ids`,
`data_subnet_group_name`, `private_route_table_ids`, `data_route_table_ids`, `nat_gateway_ids`,
`nat_public_ips`, `default_security_group_id`, `s3_gateway_endpoint_id`.

Note the vocabulary translation: upstream calls the third tier `database_*`, we call it `data_*`
everywhere else in the repo. The wrapper is where that mapping lives.

## Cost notes

| Item | Dev |
|---|---|
| NAT gateway (1) | ~$33/mo + $0.045/GB processed |
| Elastic IP (attached) | included |
| S3 gateway endpoint | free |
| VPC, subnets, route tables | free |
| Flow logs | off (~$5-15/mo of CloudWatch ingestion avoided) |

A single NAT gateway is a single point of failure for private-subnet egress and a cross-AZ data
charge for traffic from the other AZ. Both are accepted in dev: the alternative is one NAT per AZ
at ~$33/mo each.

## Prod deltas

- **One NAT gateway per AZ** (`single_nat_gateway = false`, `one_nat_gateway_per_az = true`). The dev
  setup means an AZ failure takes egress for the whole VPC, and every byte from the other AZ pays a
  cross-AZ transfer charge on top of NAT processing.
- **Flow logs on**, to S3 (cheaper than CloudWatch Logs at volume) with a parquet layout, retained
  90 days and queried by Athena. This is a security-investigation requirement, not an observability
  nice-to-have: without it there is no record of who talked to what.
- **Interface endpoints** for the services pods actually call — ECR API + ECR DKR, STS, Secrets
  Manager, KMS, CloudWatch Logs, SQS, MSK (`kafka`), Snowflake PrivateLink. ~$7/mo each plus data
  charges, so ~$50/mo, in exchange for removing the NAT dependency (and the internet) from the
  control paths. Cheaper than NAT at prod volume and strictly better for security.
- **Network ACLs per tier** (`*_dedicated_network_acl = true`) as a second, stateless layer under the
  security groups, with the data tier denying egress to 0.0.0.0/0 explicitly rather than by absence
  of a route.
- **`az_count = 3`** so a quorum-based service (MSK with RF 3, min ISR 2) can survive one AZ loss.
  The CIDR layout already reserves the space.
- **Transit Gateway / VPN attachment** if the corebank source is a real on-premises system rather
  than the simulator; the data tier's "no egress" becomes "no *internet* egress" plus a route to the
  corporate CIDR.
