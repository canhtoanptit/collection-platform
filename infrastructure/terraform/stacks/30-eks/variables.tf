variable "region" {
  description = "AWS region. eu-west-1 unless overridden (plan §2)."
  type        = string
  default     = "eu-west-1"
}

variable "project" {
  description = "Project tag and resource-name prefix (plan §2). Named `project` to match envs/<env>/common.tfvars and stacks 00/10/20."
  type        = string
  default     = "colx"
}

variable "env" {
  description = "Environment name; part of every resource name and tag."
  type        = string
  default     = "dev"
}

# --------------------------------------------------------------- state reads --

variable "state_bucket" {
  description = <<-EOT
    Terraform state bucket (`colx-tfstate-<account-id>`), used by the
    `terraform_remote_state` reads of stacks 10-network and 20-data. Same value as
    `bucket` in `envs/<env>/backend.hcl`; supplied through
    `envs/<env>/common.tfvars`. No default — an account-specific bucket name must
    never be guessed.
  EOT
  type        = string
}

variable "state_region" {
  description = "Region of the state bucket. Defaults to `region` when null."
  type        = string
  default     = null
}

variable "network_state_key" {
  description = "State key of stack 10-network. Same convention as 20-data's variable of this name."
  type        = string
  default     = "stacks/10-network.tfstate"
}

variable "data_state_key" {
  description = "State key of stack 20-data."
  type        = string
  default     = "stacks/20-data.tfstate"
}

# ------------------------------------------------------------------- cluster --

variable "cluster_name" {
  description = "Cluster name. Defaults to `<project>-<env>` (colx-dev). MUST match `eks_cluster_name` in stack 10-network, which tags the subnets the ALB controller discovers."
  type        = string
  default     = null
}

variable "kubernetes_version" {
  description = "Control-plane Kubernetes version (plan FND-7: 1.32)."
  type        = string
  default     = "1.32"
}

variable "admin_principal_arn" {
  description = <<-EOT
    IAM principal (user or role ARN) that gets `AmazonEKSClusterAdminPolicy` via an
    access entry — i.e. the human operator. USER-SUPPLIED: there is no sensible
    default, and an SSO role ARN must be the *role* ARN
    (`arn:aws:iam::<acct>:role/aws-reserved/sso.amazonaws.com/...`), not the
    assumed-role session ARN.
  EOT
  type        = string

  validation {
    condition     = can(regex("^arn:aws[a-z-]*:iam::[0-9]{12}:(user|role)/", var.admin_principal_arn))
    error_message = "admin_principal_arn must be an IAM user or role ARN."
  }
}

variable "admin_cidrs" {
  description = <<-EOT
    CIDRs allowed to reach the public Kubernetes API endpoint — the operator's
    address, plus any CI egress range that needs `kubectl`. USER-SUPPLIED. There is
    no ingress in this environment (ADR-0011), so this list is the entire public
    attack surface; `0.0.0.0/0` is rejected by the module.
  EOT
  type        = list(string)
}

variable "gha_deploy_role_name" {
  description = "Name of the GitHub Actions deploy role from stack 00-bootstrap that helmfile runs as."
  type        = string
  default     = "colx-gha-eks-deploy"
}

variable "node_instance_types" {
  description = "Managed node group instance types."
  type        = list(string)
  default     = ["t3.large"]
}

variable "node_min_size" {
  description = "Minimum node count (plan FND-7: 2)."
  type        = number
  default     = 2
}

variable "node_desired_size" {
  description = "Desired node count (plan FND-7: 3)."
  type        = number
  default     = 3
}

variable "node_max_size" {
  description = "Maximum node count (plan FND-7: 4)."
  type        = number
  default     = 4
}

variable "addon_versions" {
  description = "Optional exact EKS addon version pins, keyed by addon name. See modules/eks/variables.tf."
  type        = map(string)
  default     = {}
}

# ----------------------------------------------------------------------- MSK --

variable "msk_cluster_arn" {
  description = <<-EOT
    MSK cluster ARN for the `kafka-cluster:*` IRSA policies. Null means "read
    `msk_cluster_arn` from stack 20-data's remote state".
  EOT
  type        = string
  default     = null
}

variable "msk_topic_arn" {
  description = <<-EOT
    ARN (or ARN pattern) scoping topic-level `kafka-cluster:` actions. Null derives
    `arn:aws:kafka:<region>:<acct>:topic/<cluster>/<uuid>/*` from the cluster ARN.
    Narrow this per-workload once the topic inventory in
    `deployment/kafka/topics.yaml` is stable.
  EOT
  type        = string
  default     = null
}

variable "msk_group_arn" {
  description = "ARN (or pattern) scoping consumer-group-level `kafka-cluster:` actions. Null derives it from the cluster ARN."
  type        = string
  default     = null
}

# ------------------------------------------------------------------- toggles --

variable "enable_alb_controller_role" {
  description = <<-EOT
    Create the (permission-less) IRSA role for the AWS Load Balancer Controller.
    Default true: the role and its trust relationship cost nothing, and having
    them in place means Phase 12 (INF-14) turns ingress on with a values change
    plus one policy attachment rather than a new stack apply.
  EOT
  type        = bool
  default     = true
}
