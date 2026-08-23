output "oidc_provider_arn" {
  description = "ARN of the GitHub Actions OIDC provider (created here, or the pre-existing one that was supplied)."
  value       = local.oidc_provider_arn
}

output "role_arns" {
  description = "Map of short role key (plan, apply, ecr-push, eks-deploy) to role ARN. These are the values that go into GitHub Actions variables (never secrets — they are not sensitive)."
  value       = { for k, v in aws_iam_role.this : k => v.arn }
}

output "role_names" {
  description = "Map of short role key to role name, e.g. { plan = \"colx-gha-plan\" }."
  value       = { for k, v in aws_iam_role.this : k => v.name }
}

output "eks_deploy_role_arn" {
  description = "Convenience output: the role stacks/30-eks must grant a cluster-admin access entry to."
  value       = aws_iam_role.this["eks-deploy"].arn
}

output "trust_subjects" {
  description = "Map of short role key to the OIDC subject claims that role trusts. Printed by the bootstrap README so the trust scoping can be eyeballed after apply."
  value       = { for k, v in local.roles : k => v.subjects }
}
