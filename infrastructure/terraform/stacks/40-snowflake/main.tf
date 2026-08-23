# stacks/40-snowflake — the Snowflake account objects and their AWS wiring.
#
# State key: stacks/40-snowflake.tfstate.
#
# *** APPLY AT PHASE 6 KICKOFF, NOT NOW (plan §6.10). ***
#
# This stack is authored in Phase 2 and deliberately left un-applied. Applying it
# starts the 30-day Enterprise trial, and Phase 6 (the analytical platform) is the
# phase that actually needs the account. Idle cost after conversion is ≈ $0 because
# every warehouse is XSMALL with 60s auto-suspend — the trial clock, not the bill,
# is what is being conserved.
#
# The stack is therefore excluded from the `main` auto-apply path in
# `.github/workflows/terraform.yml` (see the `apply` job's stack list) and is
# applied through a deliberate workflow_dispatch.

data "aws_kms_alias" "data" {
  name = "alias/${local.name}-data"
}

locals {
  name = "${var.project}-${var.env}"

  raw_bucket = coalesce(var.raw_bucket, "${local.name}-raw")

  # Deterministic and stable across rebuilds; not a secret, but not guessable
  # from the bucket name alone either.
  external_id = coalesce(
    var.storage_integration_external_id,
    upper("${var.project}_${var.env}_S3_RAW_INT"),
  )

  aws_role_name = "${local.name}-snowflake-s3-raw"
}

module "snowflake" {
  source = "../../modules/snowflake-account"

  prefix = var.snowflake_object_prefix
  env    = var.env

  credit_quota = var.credit_quota

  service_user_public_keys = var.service_user_public_keys
  service_user_emails      = var.service_user_emails

  storage_integration_name = "S3_RAW_INT"
  raw_bucket               = local.raw_bucket
  raw_bucket_prefixes      = var.raw_bucket_prefixes
  aws_role_name            = local.aws_role_name
  aws_external_id          = local.external_id
  kms_key_arn              = data.aws_kms_alias.data.target_key_arn
}
