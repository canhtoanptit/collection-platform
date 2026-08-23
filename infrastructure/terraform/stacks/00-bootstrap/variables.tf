variable "region" {
  description = "AWS region for every regional resource in this stack (the state bucket, its CMK and the SNS topic)."
  type        = string
  default     = "eu-west-1"
}

variable "project" {
  description = "Project tag applied to every resource via the provider's default_tags."
  type        = string
  default     = "colx"
}

variable "env" {
  description = "Environment tag applied to every resource via the provider's default_tags."
  type        = string
  default     = "dev"
}

variable "github_repository" {
  description = "GitHub repository in owner/name form. Every CI role's trust policy is scoped to it. USER-SUPPLIED: change this if the repository is forked or renamed, or CI cannot authenticate."
  type        = string
  default     = "canhtoanptit/collection-platform"

  validation {
    condition     = can(regex("^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$", var.github_repository))
    error_message = "github_repository must be in owner/name form."
  }
}

variable "github_environment" {
  description = "GitHub Actions environment that gates applies. Its required-reviewer setting is the human approval gate for every infrastructure change (ADR-0010)."
  type        = string
  default     = "dev"
}

variable "alert_email" {
  description = "USER-SUPPLIED, no default: the address subscribed to the colx-dev-alerts SNS topic. AWS emails a confirmation link that must be clicked before any notification is delivered."
  type        = string

  validation {
    condition     = can(regex("^[^@[:space:]]+@[^@[:space:]]+\\.[A-Za-z]{2,}$", var.alert_email))
    error_message = "alert_email must be a single email address."
  }
}

variable "monthly_budget_usd" {
  description = "Monthly AWS cost budget in USD (plan §2: $450 against an all-on estimate of $540-575)."
  type        = number
  default     = 450
}

variable "anomaly_threshold_usd" {
  description = "Absolute dollar impact at or above which a cost anomaly is notified."
  type        = number
  default     = 25
}

variable "create_github_oidc_provider" {
  description = "Create the account's GitHub OIDC provider. Set false and supply existing_github_oidc_provider_arn if the account already has one (IAM permits only one per issuer URL)."
  type        = bool
  default     = true
}

variable "existing_github_oidc_provider_arn" {
  description = "ARN of a pre-existing GitHub Actions OIDC provider, used when create_github_oidc_provider is false."
  type        = string
  default     = null
}

variable "state_bucket_name_override" {
  description = "Full state bucket name, overriding the default colx-tfstate-<account_id>. Only needed if that name is already taken globally."
  type        = string
  default     = null
}

variable "state_noncurrent_version_expiration_days" {
  description = "How long superseded state versions are kept. This is the undo history for every stack, so it is deliberately longer than the data buckets' 7 days."
  type        = number
  default     = 90

  validation {
    condition     = var.state_noncurrent_version_expiration_days >= 30
    error_message = "Keep at least 30 days of state history; recovering from a bad apply discovered a week later needs it."
  }
}
