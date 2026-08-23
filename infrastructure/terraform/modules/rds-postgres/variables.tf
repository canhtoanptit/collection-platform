variable "identifier" {
  description = "RDS instance identifier, e.g. \"colx-dev-platform\". Also names the security group and parameter group."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,60}$", var.identifier))
    error_message = "identifier must be lowercase alphanumeric with hyphens, 2-61 characters."
  }
}

variable "vpc_id" {
  description = "VPC the instance's security group is created in."
  type        = string
}

variable "db_subnet_group_name" {
  description = "Existing DB subnet group spanning the data subnets (created by modules/network). Mutually exclusive with `subnet_ids`."
  type        = string
  default     = null
}

variable "subnet_ids" {
  description = "Data subnet ids to build a dedicated DB subnet group from. Leave null when `db_subnet_group_name` is supplied."
  type        = list(string)
  default     = null

  validation {
    condition     = var.subnet_ids == null || length(var.subnet_ids) >= 2
    error_message = "RDS requires a subnet group spanning at least two availability zones, even for a single-AZ instance."
  }
}

variable "engine_version" {
  description = "Postgres version. \"16\" tracks the latest 16.x minor, which is what auto_minor_version_upgrade expects; pin a full x.y only to hold a specific minor."
  type        = string
  default     = "16"
}

variable "parameter_group_family" {
  description = "Parameter group family. Must match the major version in `engine_version`."
  type        = string
  default     = "postgres16"
}

variable "instance_class" {
  description = "Instance class, e.g. db.t4g.small. Graviton (t4g) is ~10-20% cheaper than t3 at the same size."
  type        = string
  default     = "db.t4g.small"
}

variable "allocated_storage" {
  description = "Initial gp3 storage in GiB. 20 is the gp3 minimum; below 400 GiB gp3 gives a fixed 3000 IOPS / 125 MBps baseline that cannot be configured."
  type        = number
  default     = 20

  validation {
    condition     = var.allocated_storage >= 20
    error_message = "gp3 storage starts at 20 GiB."
  }
}

variable "max_allocated_storage" {
  description = <<-EOT
    Upper bound for RDS storage autoscaling, or null to disable it.

    Worth setting on the CDC source: while Kafka Connect is down, an unconsumed logical replication
    slot retains WAL until the volume fills, and a full volume takes the instance down hard
    (ADR-0003 calls this the most dangerous object in the platform). Autoscaling converts that
    outage into a cost surprise, which is the better failure.
  EOT
  type        = number
  default     = null

  validation {
    condition     = var.max_allocated_storage == null || var.max_allocated_storage > var.allocated_storage
    error_message = "max_allocated_storage must be greater than allocated_storage, or null to disable autoscaling."
  }
}

variable "kms_key_arn" {
  description = "CMK for storage encryption. Also encrypts automated backups and snapshots, and the RDS-managed master password secret."
  type        = string

  validation {
    condition     = can(regex("^arn:aws:kms:", var.kms_key_arn))
    error_message = "kms_key_arn must be a KMS key ARN."
  }
}

variable "master_username" {
  description = "Master user name. `rdsadmin` is reserved by RDS; `postgres` works but is the first name any scanner tries."
  type        = string
  default     = "colxadmin"

  validation {
    condition     = !contains(["rdsadmin", "admin"], lower(var.master_username))
    error_message = "master_username must not be a name RDS reserves (rdsadmin) or a trivially guessable one (admin)."
  }
}

variable "ingress_security_group_ids" {
  description = <<-EOT
    Security groups allowed to reach port 5432.

    Empty by default, and empty means *no ingress at all* -- the instance is created unreachable
    rather than open. This is the two-pass pattern described in the module README: 20-data is
    applied before 30-eks exists, so the EKS node security group id is not knowable on the first
    apply. Fill it in and re-apply once the cluster exists.
  EOT
  type        = list(string)
  default     = []
}

variable "ingress_cidr_blocks" {
  description = "CIDR blocks allowed to reach port 5432. Intended for a bastion or VPN range; must never contain 0.0.0.0/0, which a validation enforces."
  type        = list(string)
  default     = []

  validation {
    condition     = !contains(var.ingress_cidr_blocks, "0.0.0.0/0")
    error_message = "0.0.0.0/0 is never a valid database ingress source. The instance lives in a subnet with no internet route; an open CIDR here means something else is wrong."
  }
}

variable "port" {
  description = "Postgres port."
  type        = number
  default     = 5432
}

variable "backup_retention_period" {
  description = "Days of automated backups. Also the point-in-time-recovery window; 0 disables both."
  type        = number
  default     = 7

  validation {
    condition     = var.backup_retention_period >= 1
    error_message = "Keep at least one day of backups: 0 disables point-in-time recovery entirely."
  }
}

variable "backup_window" {
  description = "Daily backup window in UTC (hh:mm-hh:mm). Must not overlap the maintenance window."
  type        = string
  default     = "01:00-02:00"
}

variable "maintenance_window" {
  description = "Weekly maintenance window in UTC (ddd:hh:mm-ddd:hh:mm)."
  type        = string
  default     = "sun:03:00-sun:04:00"
}

variable "multi_az" {
  description = "Standby in a second AZ. False in dev: it doubles the instance cost and the failover path is not something dev exercises."
  type        = bool
  default     = false
}

variable "parameters" {
  description = <<-EOT
    DB parameters. A parameter group is created only when this map is non-empty.

    `apply_method` defaults to "pending-reboot" because that is the only valid value for static
    parameters (`rds.logical_replication`, `max_replication_slots`, `max_wal_senders`), and it is
    also accepted for dynamic ones. "immediate" on a static parameter is an apply-time error.

    A static parameter changed on a running instance needs a reboot before it takes effect.
  EOT
  type = map(object({
    value        = string
    apply_method = optional(string, "pending-reboot")
  }))
  default = {}

  validation {
    condition     = alltrue([for k, v in var.parameters : contains(["immediate", "pending-reboot"], v.apply_method)])
    error_message = "apply_method must be \"immediate\" or \"pending-reboot\"."
  }
}

variable "enabled_cloudwatch_logs_exports" {
  description = "Log types to ship to CloudWatch Logs (\"postgresql\", \"upgrade\"). Empty in dev: ingestion is billed per GB and the same logs are readable via the console's log viewer."
  type        = list(string)
  default     = []
}

variable "performance_insights_enabled" {
  description = "Performance Insights. Off by default: the smallest burstable classes (db.t3.micro/small, db.t4g.micro/small) do not support it, and enabling it there fails the apply."
  type        = bool
  default     = false
}

variable "deletion_protection" {
  description = "Refuse to delete the instance. False in dev so `make destroy-heavy` works."
  type        = bool
  default     = false
}

variable "skip_final_snapshot" {
  description = "Skip the final snapshot on destroy. True in dev: the data is regenerated by the simulator seeder, and snapshots bill until deleted."
  type        = bool
  default     = true
}

variable "apply_immediately" {
  description = "Apply modifications at once instead of waiting for the maintenance window. True in dev so a CI apply is not silently deferred by a week."
  type        = bool
  default     = true
}

variable "auto_minor_version_upgrade" {
  description = "Allow RDS to apply minor version upgrades during the maintenance window."
  type        = bool
  default     = true
}
