variable "name_prefix" {
  description = "Prefix for the four role names, e.g. \"colx\" produces colx-gha-plan."
  type        = string
  default     = "colx"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,20}$", var.name_prefix))
    error_message = "name_prefix must be lowercase alphanumeric with hyphens, 2-21 characters."
  }
}

variable "region" {
  description = "AWS region, used to scope the ECR and EKS resource ARNs in the inline policies."
  type        = string
}

variable "github_repository" {
  description = "GitHub repository in \"owner/name\" form. Every trust policy is scoped to it; a token from any other repository cannot assume any of these roles."
  type        = string

  validation {
    condition     = can(regex("^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$", var.github_repository))
    error_message = "github_repository must be in owner/name form, e.g. canhtoanptit/collection-platform."
  }
}

variable "github_environment" {
  description = "GitHub Actions environment that gates mutating workflows. The apply and eks-deploy roles trust only `repo:<repo>:environment:<this>`, so the required-reviewer check on the environment is what actually gates an apply."
  type        = string
  default     = "dev"

  validation {
    condition     = can(regex("^[A-Za-z0-9_.-]+$", var.github_environment))
    error_message = "github_environment must not contain path separators or spaces."
  }
}

variable "default_branch" {
  description = "Branch whose pushes may assume the plan and ecr-push roles."
  type        = string
  default     = "main"
}

variable "create_oidc_provider" {
  description = "Create the account's GitHub OIDC provider. An account can hold only one provider per issuer URL, so set this to false and supply `existing_oidc_provider_arn` if one already exists."
  type        = bool
  default     = true
}

variable "existing_oidc_provider_arn" {
  description = "ARN of a pre-existing GitHub OIDC provider. Required when `create_oidc_provider` is false, ignored otherwise."
  type        = string
  default     = null

  validation {
    condition     = var.existing_oidc_provider_arn == null || can(regex("^arn:aws:iam::[0-9]{12}:oidc-provider/", var.existing_oidc_provider_arn))
    error_message = "existing_oidc_provider_arn must be an IAM OIDC provider ARN."
  }
}

variable "oidc_thumbprints" {
  description = "Certificate thumbprints for the GitHub issuer. AWS no longer uses these for token validation on token.actions.githubusercontent.com (it trusts the issuer's CA chain), but the API accepts them and older documentation requires them, so the published values are the default."
  type        = list(string)
  default = [
    "6938fd4d98bab03faadb97b34396831e3780aea1",
    "1c58a3a8518e8759bf075b76b750d4f2df264fcd",
  ]
}

variable "state_bucket_name" {
  description = "Terraform state bucket the plan role must be able to read, and write lock files into."
  type        = string
}

variable "state_kms_key_arn" {
  description = "CMK encrypting the state bucket. `null` when the bucket uses SSE-S3, in which case no KMS statement is added to the plan role."
  type        = string
  default     = null
}

variable "ecr_repository_prefix" {
  description = "Namespace the ecr-push role may push into. \"colx\" scopes it to arn:aws:ecr:<region>:<account>:repository/colx/*."
  type        = string
  default     = "colx"
}

variable "max_session_duration" {
  description = "Maximum assumed-role session length in seconds. One hour is longer than any CI job here needs and is the AWS minimum."
  type        = number
  default     = 3600

  validation {
    condition     = var.max_session_duration >= 3600 && var.max_session_duration <= 43200
    error_message = "max_session_duration must be between 3600 and 43200 seconds."
  }
}

variable "apply_role_policy_arns" {
  description = "Managed policies attached to the apply role. AdministratorAccess in dev because this role creates IAM roles, KMS keys, EKS clusters and RDS instances; see the README for the prod delta."
  type        = list(string)
  default     = ["arn:aws:iam::aws:policy/AdministratorAccess"]
}
