# Stack 00-bootstrap -- the only stack a human applies (ADR-0010).
#
# It contains exactly the things that must exist before CI can run Terraform at all:
#   - the state bucket and its CMK (state.tf)
#   - the GitHub OIDC provider and the four CI roles
#   - the alerting sink, the cost budget and the anomaly monitor
#
# Nothing here belongs to the platform's runtime. Every resource is an account-level singleton, so
# this stack is applied once and then changes roughly never. See README.md for the exact sequence.

module "github_oidc" {
  source = "../../modules/github-oidc"

  name_prefix        = var.project
  region             = var.region
  github_repository  = var.github_repository
  github_environment = var.github_environment

  create_oidc_provider       = var.create_github_oidc_provider
  existing_oidc_provider_arn = var.existing_github_oidc_provider_arn

  # The plan role needs read access to state and write access for the S3-native lock file.
  state_bucket_name = aws_s3_bucket.tfstate.id
  state_kms_key_arn = aws_kms_key.tfstate.arn

  ecr_repository_prefix = var.project
}

module "budgets" {
  source = "../../modules/budgets"

  name_prefix = "${var.project}-${var.env}"
  alert_email = var.alert_email

  monthly_budget_usd    = var.monthly_budget_usd
  anomaly_threshold_usd = var.anomaly_threshold_usd
}
