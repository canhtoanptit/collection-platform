variable "name" {
  description = "VPC name and the prefix for every subnet, route table and endpoint it owns, e.g. \"colx-dev\"."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,30}$", var.name))
    error_message = "name must be lowercase alphanumeric with hyphens, 2-31 characters."
  }
}

variable "region" {
  description = "AWS region, passed explicitly so the S3 gateway endpoint service name does not depend on a data source."
  type        = string
}

variable "vpc_cidr" {
  description = "IPv4 CIDR for the VPC. Must be a /16 through /20; the module carves /20 subnets out of it with a per-tier offset of 4."
  type        = string
  default     = "10.40.0.0/16"

  validation {
    condition     = can(cidrhost(var.vpc_cidr, 0))
    error_message = "vpc_cidr must be a valid IPv4 CIDR block."
  }

  validation {
    condition     = tonumber(split("/", var.vpc_cidr)[1]) <= 20
    error_message = "vpc_cidr must be /20 or larger (smaller prefix length) to fit three subnet tiers."
  }
}

variable "az_count" {
  description = "How many availability zones to spread the three subnet tiers across. MSK requires at least 2; the CIDR layout supports up to 4."
  type        = number
  default     = 2

  validation {
    condition     = var.az_count >= 2 && var.az_count <= 4
    error_message = "az_count must be between 2 and 4."
  }
}

variable "single_nat_gateway" {
  description = "One NAT gateway shared by every private subnet (dev, ~$40/mo) instead of one per AZ (prod, ~$40/mo/AZ plus cross-AZ data charges avoided)."
  type        = bool
  default     = true
}

variable "enable_nat_gateway" {
  description = "Create NAT gateway(s) at all. Private subnets lose egress when false, which breaks image pulls and every AWS API call that is not covered by an endpoint."
  type        = bool
  default     = true
}

variable "enable_flow_log" {
  description = "VPC flow logs to CloudWatch Logs. Off in dev: at this traffic volume the ingestion charge exceeds the value of logs nobody queries. On in prod (see README)."
  type        = bool
  default     = false
}

variable "eks_cluster_name" {
  description = "If set, subnets are tagged `kubernetes.io/cluster/<name> = shared` in addition to the role tags. Must match the cluster name used by stacks/30-eks."
  type        = string
  default     = null
}

variable "enable_s3_gateway_endpoint" {
  description = "Create the S3 gateway endpoint. Keeps S3 traffic (raw partitions, Airflow logs, ECR layers) off the NAT gateway, which is both the cheapest and the largest single saving in this module."
  type        = bool
  default     = true
}
