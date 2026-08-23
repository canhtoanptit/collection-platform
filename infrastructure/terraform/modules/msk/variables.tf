variable "cluster_name" {
  description = "MSK cluster name, e.g. \"colx-dev\". Also names the security group and the cluster configuration."
  type        = string

  validation {
    condition     = can(regex("^[a-zA-Z0-9][a-zA-Z0-9-]{0,63}$", var.cluster_name))
    error_message = "cluster_name must be alphanumeric with hyphens, up to 64 characters."
  }
}

variable "kafka_version" {
  description = <<-EOT
    MSK Kafka version string. Not a plain Kafka version: MSK publishes its own identifiers, and the
    set that is valid depends on the region and on whether the cluster is ZooKeeper- or KRaft-based
    ("3.7.x" vs "3.7.x.kraft").

    Enumerate what the account actually offers with `aws kafka list-kafka-versions` before changing
    this. A wrong string fails at apply with `BadRequestException`, not at plan.

    An in-place version upgrade is supported but one-way (MSK cannot downgrade), so this is a
    deliberate change rather than a routine one.
  EOT
  type        = string
  default     = "3.7.x"
}

variable "vpc_id" {
  description = "VPC the broker security group is created in."
  type        = string
}

variable "client_subnet_ids" {
  description = "Private subnets to place brokers in, one per AZ. Length must divide number_of_broker_nodes exactly."
  type        = list(string)

  validation {
    condition     = length(var.client_subnet_ids) >= 2
    error_message = "MSK requires at least two subnets in distinct availability zones."
  }
}

variable "number_of_broker_nodes" {
  description = "Total brokers. Must be an exact multiple of the number of client subnets, because MSK places brokers evenly across AZs."
  type        = number
  default     = 2

  validation {
    condition     = var.number_of_broker_nodes >= 2
    error_message = "At least two brokers are required for replication factor 2."
  }
}

variable "instance_type" {
  description = "Broker instance type. kafka.t3.small is the cheapest provisioned option (~$80/mo for two brokers against ~$550 for MSK Serverless at this shape) and fits a partition budget under ~300 per broker."
  type        = string
  default     = "kafka.t3.small"
}

variable "ebs_volume_size" {
  description = "EBS volume per broker in GiB. Sized for a few days of dev traffic at the retentions in deployment/kafka/topics.yaml; storage can be grown in place but never shrunk."
  type        = number
  default     = 20

  validation {
    condition     = var.ebs_volume_size >= 1 && var.ebs_volume_size <= 16384
    error_message = "ebs_volume_size must be between 1 and 16384 GiB."
  }
}

variable "kms_key_arn" {
  description = "CMK for encryption at rest on the broker volumes (the `msk` key from modules/kms)."
  type        = string

  validation {
    condition     = can(regex("^arn:aws:kms:", var.kms_key_arn))
    error_message = "kms_key_arn must be a KMS key ARN."
  }
}

variable "client_security_group_ids" {
  description = <<-EOT
    Security groups allowed to reach the brokers on the IAM/TLS port and the open-monitoring ports.

    Empty by default, and empty means no client ingress rules at all: a freshly applied cluster is
    unreachable rather than open. In dev this is the EKS node security group, supplied on a second
    apply once stacks/30-eks exists -- the same two-pass pattern as modules/rds-postgres.
  EOT
  type        = list(string)
  default     = []
}

variable "client_cidr_blocks" {
  description = "CIDR blocks allowed to reach the brokers. Normally empty; a VPN or bastion range is the only legitimate use. 0.0.0.0/0 is rejected."
  type        = list(string)
  default     = []

  validation {
    condition     = !contains(var.client_cidr_blocks, "0.0.0.0/0")
    error_message = "0.0.0.0/0 is never a valid Kafka client source."
  }
}

variable "server_properties" {
  description = <<-EOT
    Cluster-level Kafka configuration, rendered into an MSK configuration resource.

    The three defaults are the ones that matter:
    - `auto.create.topics.enable=false` -- topics exist only if they are in
      deployment/kafka/topics.yaml (ADR-0004). With auto-create on, a typo in a topic name creates
      a new topic and the consumer waits forever on an empty one.
    - `default.replication.factor=2` -- matches the two-broker dev cluster.
    - `min.insync.replicas=1` -- with RF 2, requiring 2 in-sync replicas would make a single broker
      restart (including MSK's own patching) stop all acks=all producers. Dev accepts the durability
      trade; prod does not (see README).
  EOT
  type        = map(string)
  default = {
    "auto.create.topics.enable"  = "false"
    "default.replication.factor" = "2"
    "min.insync.replicas"        = "1"
  }
}

variable "enhanced_monitoring" {
  description = "CloudWatch metric granularity: DEFAULT, PER_BROKER, PER_TOPIC_PER_BROKER or PER_TOPIC_PER_PARTITION. DEFAULT because Prometheus scrapes the open-monitoring exporters instead, and per-topic CloudWatch metrics are billed per metric per month."
  type        = string
  default     = "DEFAULT"

  validation {
    condition = contains(
      ["DEFAULT", "PER_BROKER", "PER_TOPIC_PER_BROKER", "PER_TOPIC_PER_PARTITION"],
      var.enhanced_monitoring
    )
    error_message = "enhanced_monitoring must be DEFAULT, PER_BROKER, PER_TOPIC_PER_BROKER or PER_TOPIC_PER_PARTITION."
  }
}

variable "open_monitoring_enabled" {
  description = "Expose the Prometheus JMX and node exporters on ports 11001/11002 for FND-9's Prometheus to scrape. Free, and the only source of broker-level Kafka metrics once enhanced_monitoring stays DEFAULT."
  type        = bool
  default     = true
}

variable "broker_logs_s3_bucket" {
  description = "Bucket for broker log delivery, e.g. colx-dev-ops. Null disables broker log delivery entirely."
  type        = string
  default     = null
}

variable "broker_logs_s3_prefix" {
  description = "Key prefix for broker logs within broker_logs_s3_bucket."
  type        = string
  default     = "msk-broker-logs/"
}
