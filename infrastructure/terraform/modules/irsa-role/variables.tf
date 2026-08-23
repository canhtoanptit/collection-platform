variable "name_prefix" {
  description = "Prefix for every role name, e.g. `colx-dev`. Role name is `<name_prefix>-<map key>`."
  type        = string
}

variable "oidc_provider_arn" {
  description = "IAM OIDC provider ARN of the cluster (`module.eks.oidc_provider_arn`)."
  type        = string
}

variable "oidc_provider" {
  description = "OIDC issuer host/path with no scheme (`module.eks.oidc_provider`), used in the `sub`/`aud` conditions."
  type        = string
}

variable "roles" {
  description = <<-EOT
    Map-driven IRSA role set. Key = logical role name (also the role-name suffix
    and, by convention, the workload name).

    Per entry:
      namespace         Kubernetes namespace of the service account.
      service_account   Service account name. Defaults to the map key.
      extra_service_accounts
                        Additional service-account names in the same namespace
                        that may assume this role. Needed where one Helm chart
                        creates a service account per component (Airflow) but the
                        AWS permissions are identical. Still an exact-match list,
                        never a `*` subject.
      policy_json       Inline policy document (usually a rendered
                        `data.aws_iam_policy_document`). `null` means no inline
                        policy — a role that is deliberately created without
                        permissions (see `alb-controller` in stack 30-eks).
      managed_arns      AWS/customer managed policy ARNs to attach.
      description       Human-readable purpose, surfaced in the IAM console.
      max_session_hours Session duration cap; 1 hour is enough for pods.

    Deliberately map-driven: adding a workload is one map entry plus one
    `serviceAccount.annotations` line in a values file, which is what makes
    "who can read which bucket" reviewable in a single diff (plan FND-7).
  EOT
  type = map(object({
    namespace              = string
    service_account        = optional(string)
    extra_service_accounts = optional(list(string), [])
    policy_json            = optional(string)
    managed_arns           = optional(list(string), [])
    description            = optional(string)
    max_session_hours      = optional(number, 1)
  }))

  validation {
    condition = alltrue([
      for k, v in var.roles : v.policy_json != null || length(v.managed_arns) > 0 || can(regex("^alb-controller$", k))
    ])
    error_message = "Every IRSA role needs an inline policy or a managed policy ARN. `alb-controller` is the one documented exception (created without permissions until Phase 12)."
  }
}

variable "tags" {
  description = "Extra tags. Project-wide tags come from the provider `default_tags`."
  type        = map(string)
  default     = {}
}
