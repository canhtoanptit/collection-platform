variable "name" {
  description = "Cluster name, e.g. `colx-dev`."
  type        = string
}

variable "kubernetes_version" {
  description = <<-EOT
    Kubernetes `<major>.<minor>` for the control plane. Default 1.32 per plan FND-7
    and the `mise.toml` kubectl pin. The upstream module does not constrain this
    value; EKS does. Bump only together with the kubectl pin and after checking
    `aws eks describe-addon-versions --kubernetes-version <v>`.
  EOT
  type        = string
  default     = "1.32"

  validation {
    condition     = can(regex("^1\\.[0-9]{2}$", var.kubernetes_version))
    error_message = "kubernetes_version must be a `1.<minor>` string such as \"1.32\"."
  }
}

variable "vpc_id" {
  description = "VPC to place the cluster in (from stack 10-network)."
  type        = string
}

variable "subnet_ids" {
  description = "Private subnet ids for the data plane (node groups)."
  type        = list(string)
}

variable "control_plane_subnet_ids" {
  description = "Subnets for the control-plane ENIs. Defaults to `subnet_ids` when null."
  type        = list(string)
  default     = null
}

variable "authentication_mode" {
  description = "`API`, `API_AND_CONFIG_MAP` or `CONFIG_MAP`. `API` only — aws-auth ConfigMap is not used."
  type        = string
  default     = "API"

  validation {
    condition     = contains(["API", "API_AND_CONFIG_MAP", "CONFIG_MAP"], var.authentication_mode)
    error_message = "authentication_mode must be API, API_AND_CONFIG_MAP or CONFIG_MAP."
  }
}

variable "access_entries" {
  description = <<-EOT
    EKS access entries, passed through to the upstream module. Map key is a stable
    logical name; `principal_arn` is the IAM principal and `policy_associations`
    attach EKS access policies (`arn:aws:eks::aws:cluster-access-policy/...`).
  EOT
  type = map(object({
    principal_arn     = string
    type              = optional(string, "STANDARD")
    user_name         = optional(string)
    kubernetes_groups = optional(list(string))
    policy_associations = optional(map(object({
      policy_arn = string
      access_scope = object({
        type       = string
        namespaces = optional(list(string))
      })
    })), {})
  }))
  default = {}
}

variable "enable_cluster_creator_admin_permissions" {
  description = <<-EOT
    Grant the applying principal (`colx-gha-apply`) cluster-admin via an access
    entry. Kept `true` deliberately: this environment is destroyed and rebuilt
    often (ADR-0010) and a wrong `admin_principal_arn` with this off is a locked
    cluster that can only be fixed by recreating it.
  EOT
  type        = bool
  default     = true
}

variable "endpoint_private_access" {
  description = "Enable the private API endpoint (always on for in-VPC access)."
  type        = bool
  default     = true
}

variable "endpoint_public_access" {
  description = "Enable the public API endpoint. On in dev, restricted to `endpoint_public_access_cidrs`."
  type        = bool
  default     = true
}

variable "endpoint_public_access_cidrs" {
  description = <<-EOT
    CIDRs allowed to reach the public API endpoint. NEVER 0.0.0.0/0 — there is no
    ingress in this environment (ADR-0011) and the API server is the only public
    surface, so it is restricted to the operator's address plus the CI egress
    range. An empty list is rejected because it would silently mean "no access".
  EOT
  type        = list(string)

  validation {
    condition     = length(var.endpoint_public_access_cidrs) > 0
    error_message = "endpoint_public_access_cidrs must list at least one CIDR."
  }

  validation {
    condition     = !contains(var.endpoint_public_access_cidrs, "0.0.0.0/0")
    error_message = "0.0.0.0/0 is not an acceptable public endpoint CIDR for this cluster."
  }
}

variable "enabled_log_types" {
  description = "Control-plane log types shipped to CloudWatch. Dev keeps the audit trail, drops the chatty ones."
  type        = list(string)
  default     = ["api", "audit", "authenticator"]
}

variable "cloudwatch_log_group_retention_in_days" {
  description = "Retention for the control-plane log group. 7 days in dev; prod deltas in README."
  type        = number
  default     = 7
}

variable "cluster_encryption_kms_key_arn" {
  description = <<-EOT
    CMK for EKS secrets envelope encryption (`colx-dev-secrets` from stack 20-data).
    When null the module creates its own key; passing the shared CMK keeps key
    management in one stack.
  EOT
  type        = string
  default     = null
}

variable "node_group_name" {
  description = "Name of the single managed node group."
  type        = string
  default     = "default"
}

variable "node_instance_types" {
  description = "Instance types for the managed node group."
  type        = list(string)
  default     = ["t3.large"]
}

variable "node_ami_type" {
  description = "Managed node group AMI type. AL2023 — AL2 is end of life."
  type        = string
  default     = "AL2023_x86_64_STANDARD"
}

variable "node_min_size" {
  description = "Minimum nodes. `make stop` scales this to 0 out of band via the AWS CLI (see scripts/cost/stop.sh)."
  type        = number
  default     = 2
}

variable "node_desired_size" {
  description = "Desired nodes at apply time."
  type        = number
  default     = 3
}

variable "node_max_size" {
  description = "Maximum nodes."
  type        = number
  default     = 4
}

variable "node_disk_size" {
  description = "Root EBS volume size (GiB) per node."
  type        = number
  default     = 50
}

variable "node_capacity_type" {
  description = "`ON_DEMAND` or `SPOT`. On-demand in dev: a spot reclaim mid-DAG is a debugging tax, not a saving."
  type        = string
  default     = "ON_DEMAND"
}

variable "addon_versions" {
  description = <<-EOT
    Optional exact addon version pins, keyed by addon name (`coredns`,
    `kube-proxy`, `vpc-cni`, `aws-ebs-csi-driver`). Addons not listed here track
    the most recent version compatible with `kubernetes_version`.

    Pinning is preferred but the valid strings depend on the cluster's Kubernetes
    version and can only be discovered from AWS
    (`aws eks describe-addon-versions --kubernetes-version 1.32`), so the pins are
    an operator input rather than a hard-coded default.
  EOT
  type        = map(string)
  default     = {}
}

variable "ebs_csi_service_account" {
  description = "namespace/serviceaccount the EBS CSI controller runs as (addon-managed, do not change without checking the addon)."
  type        = string
  default     = "kube-system/ebs-csi-controller-sa"
}

variable "tags" {
  description = "Extra tags. Project-wide tags come from the provider `default_tags`."
  type        = map(string)
  default     = {}
}
