output "role_arns" {
  description = "Logical role name -> IAM role ARN. This is what goes in a service account's `eks.amazonaws.com/role-arn` annotation."
  value       = { for k, r in aws_iam_role.this : k => r.arn }
}

output "role_names" {
  description = "Logical role name -> IAM role name."
  value       = { for k, r in aws_iam_role.this : k => r.name }
}

output "service_accounts" {
  description = "Logical role name -> primary `namespace/serviceaccount` the role trusts. Verify scripts assert the annotation matches this."
  value       = { for k, v in local.roles : k => "${v.namespace}/${v.service_account}" }
}

output "trusted_subjects" {
  description = "Logical role name -> every `system:serviceaccount:<ns>:<sa>` subject in the role's trust policy."
  value       = { for k, v in local.roles : k => v.subjects }
}

output "annotations" {
  description = <<-EOT
    Ready-made service-account annotation maps, keyed by logical role name:
    `{ "eks.amazonaws.com/role-arn" = "<arn>" }`. Helmfile values reference these
    via the stack outputs so an ARN is never hand-copied into a values file.
  EOT
  value = {
    for k, r in aws_iam_role.this : k => { "eks.amazonaws.com/role-arn" = r.arn }
  }
}
