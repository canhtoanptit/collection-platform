variable "region" {
  description = "AWS region for the VPC and everything in it."
  type        = string
  default     = "eu-west-1"
}

variable "project" {
  description = "Project tag and resource-name prefix."
  type        = string
  default     = "colx"
}

variable "env" {
  description = "Environment tag and resource-name suffix."
  type        = string
  default     = "dev"
}

variable "vpc_cidr" {
  description = "IPv4 CIDR for the VPC. Changing this after apply replaces every subnet, and with them the RDS subnet group and the MSK cluster."
  type        = string
  default     = "10.40.0.0/16"
}

variable "az_count" {
  description = "Availability zones to spread the three subnet tiers across. MSK requires at least 2; 3 is the prod value."
  type        = number
  default     = 2
}

variable "single_nat_gateway" {
  description = "One NAT gateway for the whole VPC (~$33/mo) instead of one per AZ. Dev accepts the single point of failure and the cross-AZ data charge."
  type        = bool
  default     = true
}

variable "enable_flow_log" {
  description = "VPC flow logs. Off in dev -- at this traffic volume the CloudWatch ingestion charge buys logs nobody queries. On in prod (see README)."
  type        = bool
  default     = false
}

variable "eks_cluster_name" {
  description = "Cluster name used to tag subnets `kubernetes.io/cluster/<name> = shared`. Must match the cluster created by stacks/30-eks (INF-B) or the AWS Load Balancer Controller will not discover these subnets."
  type        = string
  default     = "colx-dev"
}
