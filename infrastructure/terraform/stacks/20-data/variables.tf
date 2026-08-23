variable "region" {
  description = "AWS region. Must match the region stack 10-network was applied in."
  type        = string
  default     = "eu-west-1"
}

variable "project" {
  description = "Project tag and resource-name prefix."
  type        = string
  default     = "colx"
}

variable "env" {
  description = "Environment tag and resource-name segment."
  type        = string
  default     = "dev"
}

variable "state_bucket" {
  description = <<-EOT
    Terraform state bucket, used by the terraform_remote_state data source that reads stack
    10-network's outputs.

    USER-SUPPLIED and account-specific (`colx-tfstate-<account_id>`). It must be the same value as
    `bucket` in envs/dev/backend.hcl -- the backend config and the remote-state lookup are two
    separate mechanisms and Terraform will not reconcile them for you. scripts/verify/INF-A.sh
    asserts the two files agree.
  EOT
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9.-]{2,62}$", var.state_bucket))
    error_message = "state_bucket must be a valid S3 bucket name."
  }
}

variable "network_state_key" {
  description = "State key of the network stack. Convention: stacks/<name>.tfstate."
  type        = string
  default     = "stacks/10-network.tfstate"
}

variable "eks_node_security_group_id" {
  description = <<-EOT
    Security group of the EKS node group, allowed to reach Postgres (5432) and MSK (9098, 11001,
    11002).

    Null on the first apply: this stack is applied before stacks/30-eks exists, so the id is not
    knowable yet. Null means no client ingress rules are created and the databases and brokers are
    unreachable -- which is the correct state for infrastructure with no clients. Set it and
    re-apply once the cluster exists; the change adds ingress rules only and modifies neither the
    instances nor the cluster.
  EOT
  type        = string
  default     = null
}

variable "rds_platform_instance_class" {
  description = "Instance class for colx-dev-platform, which hosts the ingestion, airflow and keycloak databases plus one per service."
  type        = string
  default     = "db.t4g.small"
}

variable "rds_corebank_instance_class" {
  description = "Instance class for colx-dev-corebank, the simulator's CDC source."
  type        = string
  default     = "db.t4g.micro"
}

variable "rds_allocated_storage" {
  description = "gp3 storage in GiB for each instance."
  type        = number
  default     = 20
}

variable "rds_backup_retention_days" {
  description = "Automated backup retention, which is also the point-in-time-recovery window."
  type        = number
  default     = 7
}

variable "msk_kafka_version" {
  description = "MSK Kafka version string. Confirm against `aws kafka list-kafka-versions` before changing -- an invalid value fails at apply, not at plan."
  type        = string
  default     = "3.7.x"
}

variable "msk_instance_type" {
  description = "MSK broker instance type."
  type        = string
  default     = "kafka.t3.small"
}

variable "msk_broker_count" {
  description = "Number of MSK brokers. Must be an exact multiple of the private subnet count."
  type        = number
  default     = 2
}

variable "msk_ebs_volume_size" {
  description = "EBS volume per MSK broker in GiB. Growable in place, never shrinkable."
  type        = number
  default     = 20
}

variable "msk_broker_logs_bucket" {
  description = <<-EOT
    Bucket for MSK broker log delivery, e.g. "colx-dev-ops". Null disables delivery.

    Off by default. Enabling it needs a bucket policy statement granting the AWS log-delivery
    principal PutObject on the bucket, and MSK's S3 delivery path has to accept a bucket encrypted
    with a customer managed key -- neither is verifiable without an account, and a wrong guess fails
    the apply. Broker metrics already reach Prometheus through MSK open monitoring, so nothing is
    lost by leaving this off until it can be tested.
  EOT
  type        = string
  default     = null
}

variable "archive_expiration_days" {
  description = "Lifecycle expiration for objects in colx-dev-archive. 90 days is a dev cost choice; prod is the regulatory retention."
  type        = number
  default     = 90
}

variable "s3_force_destroy" {
  description = "Allow `terraform destroy` to delete non-empty buckets. Keep false; `make destroy-all` (FND-13) is the only caller that should ever set it."
  type        = bool
  default     = false
}

variable "ecr_force_delete" {
  description = "Allow `terraform destroy` to delete ECR repositories that still contain images. Same caveat as s3_force_destroy."
  type        = bool
  default     = false
}

variable "secret_recovery_window_days" {
  description = "Secrets Manager recovery window for the placeholder secrets. 0 in dev: a 7-30 day window makes the secret name unavailable for re-creation, which breaks rebuild-after-teardown."
  type        = number
  default     = 0
}
