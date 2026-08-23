# One RDS Postgres instance, its security group, and (when parameters are supplied) its parameter
# group. Instantiated twice by stacks/20-data: the platform instance and the corebank CDC source.
#
# The two things this module refuses to do:
#
#  1. Accept a password. `manage_master_user_password = true` makes RDS generate the master
#     credential and own it in Secrets Manager, so no password exists in HCL, in a tfvars file, or
#     in Terraform state -- only the ARN of the secret that holds it.
#  2. Default to reachable. `ingress_security_group_ids` is empty by default and empty means no
#     ingress rules at all, which is why a freshly applied instance is unreachable until the EKS
#     node security group id is passed in.

locals {
  create_subnet_group    = var.db_subnet_group_name == null
  create_parameter_group = length(var.parameters) > 0

  subnet_group_name = local.create_subnet_group ? aws_db_subnet_group.this[0].name : var.db_subnet_group_name
}

resource "aws_db_subnet_group" "this" {
  count = local.create_subnet_group ? 1 : 0

  name        = "${var.identifier}-subnets"
  description = "Data-tier subnets for ${var.identifier}"
  subnet_ids  = var.subnet_ids

  tags = {
    Name = "${var.identifier}-subnets"
  }
}

# No egress rules. A Postgres instance never initiates a connection: backups, snapshots and log
# exports all travel through the RDS service rather than this ENI. Adding egress here would only
# widen what a compromised instance could reach.
resource "aws_security_group" "this" {
  name        = "${var.identifier}-db"
  description = "Postgres access for ${var.identifier}"
  vpc_id      = var.vpc_id

  tags = {
    Name = "${var.identifier}-db"
  }

  # The security group is referenced by the instance, so replacing it would otherwise require
  # deleting the instance first.
  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_vpc_security_group_ingress_rule" "from_security_groups" {
  for_each = toset(var.ingress_security_group_ids)

  security_group_id = aws_security_group.this.id
  description       = "Postgres from ${each.value}"

  referenced_security_group_id = each.value
  ip_protocol                  = "tcp"
  from_port                    = var.port
  to_port                      = var.port
}

resource "aws_vpc_security_group_ingress_rule" "from_cidrs" {
  for_each = toset(var.ingress_cidr_blocks)

  security_group_id = aws_security_group.this.id
  description       = "Postgres from ${each.value}"

  cidr_ipv4   = each.value
  ip_protocol = "tcp"
  from_port   = var.port
  to_port     = var.port
}

resource "aws_db_parameter_group" "this" {
  count = local.create_parameter_group ? 1 : 0

  name        = "${var.identifier}-pg"
  family      = var.parameter_group_family
  description = "Parameters for ${var.identifier}"

  dynamic "parameter" {
    for_each = var.parameters

    content {
      name         = parameter.key
      value        = parameter.value.value
      apply_method = parameter.value.apply_method
    }
  }

  tags = {
    Name = "${var.identifier}-pg"
  }

  # Changing a parameter group's name forces replacement, and an instance cannot be left without
  # one mid-apply.
  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_db_instance" "this" {
  identifier = var.identifier

  engine                      = "postgres"
  engine_version              = var.engine_version
  auto_minor_version_upgrade  = var.auto_minor_version_upgrade
  allow_major_version_upgrade = false

  instance_class = var.instance_class

  # gp3 below 400 GiB has a fixed 3000 IOPS / 125 MBps baseline; setting iops or storage_throughput
  # at this size is rejected by the API.
  storage_type          = "gp3"
  allocated_storage     = var.allocated_storage
  max_allocated_storage = var.max_allocated_storage
  storage_encrypted     = true
  kms_key_id            = var.kms_key_arn

  username = var.master_username
  # RDS generates and owns the master password in Secrets Manager, encrypted with our CMK. The
  # secret's ARN is an output; the value never enters Terraform state.
  manage_master_user_password   = true
  master_user_secret_kms_key_id = var.kms_key_arn

  db_subnet_group_name   = local.subnet_group_name
  vpc_security_group_ids = [aws_security_group.this.id]
  port                   = var.port
  publicly_accessible    = false
  multi_az               = var.multi_az

  parameter_group_name = local.create_parameter_group ? aws_db_parameter_group.this[0].name : null

  backup_retention_period = var.backup_retention_period
  backup_window           = var.backup_window
  maintenance_window      = var.maintenance_window
  copy_tags_to_snapshot   = true

  enabled_cloudwatch_logs_exports = var.enabled_cloudwatch_logs_exports
  performance_insights_enabled    = var.performance_insights_enabled
  performance_insights_kms_key_id = var.performance_insights_enabled ? var.kms_key_arn : null

  deletion_protection = var.deletion_protection
  skip_final_snapshot = var.skip_final_snapshot
  final_snapshot_identifier = var.skip_final_snapshot ? null : (
    "${var.identifier}-final-${formatdate("YYYYMMDDhhmm", timestamp())}"
  )

  apply_immediately = var.apply_immediately

  tags = {
    Name = var.identifier
  }

  lifecycle {
    ignore_changes = [
      # RDS reports the resolved minor (16.9) against a configured major ("16"). Without this,
      # every plan after an automatic minor upgrade wants to downgrade the instance.
      engine_version,
      # A destroy-time-only value derived from timestamp(); it would otherwise churn every plan.
      final_snapshot_identifier,
    ]
  }
}
