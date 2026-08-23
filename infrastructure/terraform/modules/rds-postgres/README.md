# module: rds-postgres

One RDS Postgres 16 instance, its security group, and — when parameters are supplied — its
parameter group.

Instantiated twice by `stacks/20-data` (FND-4), per ADR-0003:

| Instance | Class | Purpose | Parameters |
|---|---|---|---|
| `colx-dev-platform` | `db.t4g.small` | databases `ingestion`, `airflow`, plus one per service | none |
| `colx-dev-corebank` | `db.t4g.micro` | the simulator's legacy-shaped CDC source | `rds.logical_replication=1`, `max_replication_slots=5`, `max_wal_senders=10` |

## Two things this module refuses to do

**It will not accept a password.** `manage_master_user_password = true` makes RDS generate the
master credential and own it in Secrets Manager, encrypted with the `db` CMK. No password exists in
HCL, in a tfvars file, or in Terraform state — only `master_user_secret_arn`. Per-database owner
passwords are created later by `scripts/db/provision_databases.sh`, which writes them straight to
Secrets Manager; Terraform never sees those either.

**It will not default to reachable.** `ingress_security_group_ids` defaults to `[]`, and empty means
*no ingress rules exist*. An instance applied with the default is created unreachable. That is the
correct failure: the alternative default (open to the VPC CIDR) would let a misconfiguration ship
silently.

## The two-pass EKS ingress pattern

`20-data` is applied before `30-eks` exists (FND-4 depends on FND-2 and FND-3, not FND-7), so on the
first apply the EKS node security group id is not knowable.

```
pass 1   apply 20-data with eks_node_security_group_id = null   -> databases exist, unreachable
pass 2   apply 30-eks                                            -> node security group now exists
pass 3   re-apply 20-data with the node SG id                    -> ingress rule added
```

Pass 3 adds one `aws_vpc_security_group_ingress_rule` and changes nothing else — the rules are
separate resources precisely so this is not an instance modification.

The alternative (having `20-data` read `30-eks`'s state) would create a dependency cycle, since
`30-eks` needs the network stack and would eventually want database endpoints. An explicit variable
keeps the stack graph acyclic and the two-pass step visible in a plan.

## Usage

```hcl
module "rds_corebank" {
  source = "../../modules/rds-postgres"

  identifier           = "colx-dev-corebank"
  vpc_id               = local.vpc_id
  db_subnet_group_name = local.data_subnet_group_name
  kms_key_arn          = module.kms.key_arns["db"]

  instance_class        = "db.t4g.micro"
  max_allocated_storage = 50

  ingress_security_group_ids = compact([var.eks_node_security_group_id])

  parameters = {
    "rds.logical_replication" = { value = "1" }
    max_replication_slots     = { value = "5" }
    max_wal_senders           = { value = "10" }
  }
}
```

## Inputs

| Name | Type | Default | Description |
|---|---|---|---|
| `identifier` | `string` | — | Names the instance, SG and parameter group. |
| `vpc_id` | `string` | — | |
| `db_subnet_group_name` | `string` | `null` | Use the network stack's group… |
| `subnet_ids` | `list(string)` | `null` | …or build one from these (needs ≥ 2 AZs). |
| `engine_version` | `string` | `"16"` | Tracks the latest 16.x minor. |
| `parameter_group_family` | `string` | `"postgres16"` | Must match the major version. |
| `instance_class` | `string` | `"db.t4g.small"` | |
| `allocated_storage` | `number` | `20` | gp3 minimum. |
| `max_allocated_storage` | `number` | `null` | Autoscaling cap; see the WAL note below. |
| `kms_key_arn` | `string` | — | Storage, backups, snapshots and the master secret. |
| `master_username` | `string` | `"colxadmin"` | |
| `ingress_security_group_ids` | `list(string)` | `[]` | Empty = unreachable. |
| `ingress_cidr_blocks` | `list(string)` | `[]` | `0.0.0.0/0` rejected by validation. |
| `port` | `number` | `5432` | |
| `backup_retention_period` | `number` | `7` | Also the PITR window. |
| `backup_window` / `maintenance_window` | `string` | `01:00-02:00` / `sun:03:00-sun:04:00` | UTC, non-overlapping. |
| `multi_az` | `bool` | `false` | |
| `parameters` | `map(object)` | `{}` | Non-empty creates a parameter group. |
| `enabled_cloudwatch_logs_exports` | `list(string)` | `[]` | |
| `performance_insights_enabled` | `bool` | `false` | See below. |
| `deletion_protection` | `bool` | `false` | |
| `skip_final_snapshot` | `bool` | `true` | |
| `apply_immediately` | `bool` | `true` | |
| `auto_minor_version_upgrade` | `bool` | `true` | |

## Outputs

`identifier`, `arn`, `address`, `endpoint`, `port`, `master_username`, `master_user_secret_arn`,
`security_group_id`, `db_subnet_group_name`, `parameter_group_name`, `resource_id`.

## Details that bite

- **`apply_method` defaults to `pending-reboot`.** `rds.logical_replication`,
  `max_replication_slots` and `max_wal_senders` are *static* parameters; `immediate` on a static
  parameter is an apply-time error. A static parameter changed on a running instance also needs a
  reboot before it takes effect — at creation the parameter group is attached before first boot, so
  no reboot is needed for the initial apply.
- **`engine_version` is in `ignore_changes`.** RDS reports the resolved minor (`16.9`) against the
  configured major (`"16"`), so without it every plan after an automatic minor upgrade proposes a
  downgrade.
- **Performance Insights is off.** The smallest burstable classes (`db.t3.micro/small`,
  `db.t4g.micro/small`) do not support it, and enabling it there fails the apply — the corebank
  instance is exactly one of those.
- **`max_allocated_storage` on the corebank instance is a safety valve, not a growth plan.** An
  unconsumed logical replication slot retains WAL until the volume fills, and a full volume takes
  the instance down hard — ADR-0003 calls the corebank replication slot the most dangerous object in
  the platform. Autoscaling turns that outage into a cost surprise, which is the better failure. It
  does not remove the need for the retained-WAL alert or the drop/recreate runbook.
- **The security group has no egress rules.** Postgres never initiates a connection: backups,
  snapshots and log exports travel through the RDS service, not this ENI.

## Cost

`db.t4g.small` ~$25/mo + `db.t4g.micro` ~$12/mo + ~$5/mo of gp3 and backup storage ≈ the plan's
$50/mo RDS line. Both instances are stoppable for up to 7 days at a time, which is what makes
`make stop` a real lever (ADR-0003) — unlike Aurora Serverless v2, whose ACU floor bills
continuously.

Note that a stopped instance still bills for storage and backups, and AWS restarts it
automatically after 7 days.

## Prod deltas

- **`multi_az = true`** — a single-AZ instance is a single point of failure for every service that
  shares it, and failover is not something dev exercises.
- **One instance per service**, not database-per-service on a shared instance. ADR-0003 is explicit
  that the shared instance is a cost compromise, not the target architecture: a noisy service can
  starve its neighbours and a single `db.t4g.small` is a single point of failure for all of them.
- **`deletion_protection = true`, `skip_final_snapshot = false`**, and snapshot retention beyond the
  automated window (`aws_db_snapshot` copies, or AWS Backup with a vault lock).
- **`backup_retention_period` 7 → 35** with cross-region automated backup replication, plus a
  restore drill that is actually executed (`scripts/dr/`).
- **Performance Insights on** with a non-burstable class, `monitoring_interval = 60` enhanced
  monitoring with its IAM role, and `enabled_cloudwatch_logs_exports = ["postgresql", "upgrade"]`
  feeding Loki.
- **IAM database authentication** (`iam_database_authentication_enabled`) so services connect with a
  short-lived token instead of a password from Secrets Manager, removing the per-database password
  rotation problem entirely. `resource_id` is exported for the `rds-db:connect` policy.
- **`auto_minor_version_upgrade = false`** with upgrades scheduled deliberately, plus a blue/green
  deployment for major versions.
- **Storage: gp3 with provisioned IOPS** once the volume exceeds 400 GiB, and `allocated_storage`
  sized so autoscaling is an alarm rather than a routine event.
