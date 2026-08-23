output "account_id" {
  description = "The AWS account this stack was applied into. Every other stack's state key lives in the bucket below, in this account."
  value       = local.account_id
}

output "state_bucket_name" {
  description = "Terraform state bucket. Copy this into envs/dev/backend.hcl before running `terraform init -migrate-state`."
  value       = aws_s3_bucket.tfstate.id
}

output "state_kms_key_arn" {
  description = "CMK encrypting the state bucket. Use this if the backend rejects the `alias/colx-tfstate` form of kms_key_id in backend.hcl."
  value       = aws_kms_key.tfstate.arn
}

output "state_kms_alias" {
  description = "Alias of the state CMK, which is what envs/dev/backend.hcl references so the file carries no account id."
  value       = aws_kms_alias.tfstate.name
}

output "backend_config" {
  description = "The exact envs/dev/backend.hcl contents for this account. Print it with `terraform output -raw backend_config` and diff it against the committed file."
  value       = <<-EOT
    bucket       = "${aws_s3_bucket.tfstate.id}"
    region       = "${var.region}"
    encrypt      = true
    kms_key_id   = "${aws_kms_alias.tfstate.name}"
    use_lockfile = true
  EOT
}

output "github_oidc_provider_arn" {
  description = "ARN of the GitHub Actions OIDC provider."
  value       = module.github_oidc.oidc_provider_arn
}

output "gha_role_arns" {
  description = "Map of CI role ARNs (plan, apply, ecr-push, eks-deploy). Store these as GitHub Actions *variables*, never secrets: they are useless without a matching OIDC token."
  value       = module.github_oidc.role_arns
}

output "gha_trust_subjects" {
  description = "Which OIDC subject claims each CI role trusts. Eyeball this after apply -- it is the whole security boundary."
  value       = module.github_oidc.trust_subjects
}

output "alerts_sns_topic_arn" {
  description = "SNS topic for budgets, cost anomalies, Alertmanager (FND-9) and teardown tooling (FND-13)."
  value       = module.budgets.sns_topic_arn
}

output "budget_name" {
  description = "Name of the monthly cost budget, for `aws budgets describe-budget`."
  value       = module.budgets.budget_name
}

output "next_steps" {
  description = "Human checklist printed after apply. Terraform cannot do any of these."
  value = join("\n", [
    "1. Confirm the SNS subscription emailed to ${var.alert_email} -- until then nothing is delivered.",
    "2. Uncomment the backend block in versions.tf, then: terraform init -backend-config=../../envs/dev/backend.hcl -migrate-state",
    "3. Delete the local terraform.tfstate* files once migration succeeds.",
    "4. Set the GitHub Actions variables AWS_REGION and the four role ARNs from gha_role_arns.",
    "5. Create the GitHub environment '${var.github_environment}' with yourself as a required reviewer -- this is the apply gate.",
  ])
}
