# S3 -> Snowflake storage integration, wired to its AWS role in the same apply.
#
# The two objects reference each other, so one edge has to be broken by
# convention rather than by reference:
#
#   integration.storage_aws_role_arn  <- constructed from var.aws_role_name
#   integration.storage_aws_external_id <- chosen by us (var.aws_external_id)
#   aws_iam_role.trust                <- integration's IAM user ARN + external id
#
# That makes the graph: integration -> AWS role. Doing it the other way round
# (deriving the external id from Snowflake) is the classic "apply twice" storage
# integration, which is not acceptable for a stack that must rebuild in one CI run.

data "aws_caller_identity" "current" {}

data "aws_partition" "current" {}

locals {
  aws_role_arn = "arn:${data.aws_partition.current.partition}:iam::${data.aws_caller_identity.current.account_id}:role/${var.aws_role_name}"

  raw_bucket_arn = "arn:${data.aws_partition.current.partition}:s3:::${var.raw_bucket}"

  # `s3://bucket/` when no prefix is given, `s3://bucket/<prefix>/` otherwise.
  allowed_locations = [
    for p in var.raw_bucket_prefixes :
    p == "" ? "s3://${var.raw_bucket}/" : "s3://${var.raw_bucket}/${trim(p, "/")}/"
  ]

  allowed_object_arns = [
    for p in var.raw_bucket_prefixes :
    p == "" ? "${local.raw_bucket_arn}/*" : "${local.raw_bucket_arn}/${trim(p, "/")}/*"
  ]

  allowed_list_prefixes = [
    for p in var.raw_bucket_prefixes :
    p == "" ? "*" : "${trim(p, "/")}/*"
  ]
}

resource "snowflake_storage_integration_aws" "raw" {
  name    = var.storage_integration_name
  comment = "Read-only access to s3://${var.raw_bucket} for COPY INTO (A§35)"
  enabled = true

  storage_provider          = "S3"
  storage_aws_role_arn      = local.aws_role_arn
  storage_aws_external_id   = var.aws_external_id
  storage_allowed_locations = local.allowed_locations
}

# ------------------------------------------------------------------- AWS side --

data "aws_iam_policy_document" "snowflake_assume" {
  statement {
    sid     = "SnowflakeAssume"
    effect  = "Allow"
    actions = ["sts:AssumeRole"]

    principals {
      type = "AWS"
      # Populated by DESCRIBE INTEGRATION after the integration is created. This
      # is the only value that genuinely cannot be known in advance.
      identifiers = [snowflake_storage_integration_aws.raw.describe_output[0].iam_user_arn]
    }

    condition {
      test     = "StringEquals"
      variable = "sts:ExternalId"
      values   = [var.aws_external_id]
    }
  }
}

data "aws_iam_policy_document" "snowflake_read_raw" {
  # Read-only, as required: Snowflake loads from raw/, it never writes there. The
  # write side of raw/ belongs to the Kafka Connect S3 sink and the ingestion
  # control plane (stack 30-eks).
  statement {
    sid    = "ReadRawObjects"
    effect = "Allow"
    actions = [
      "s3:GetObject",
      "s3:GetObjectVersion",
    ]
    resources = local.allowed_object_arns
  }

  statement {
    sid    = "ListRawBucket"
    effect = "Allow"
    actions = [
      "s3:ListBucket",
      "s3:GetBucketLocation",
    ]
    resources = [local.raw_bucket_arn]

    condition {
      test     = "StringLike"
      variable = "s3:prefix"
      values   = local.allowed_list_prefixes
    }
  }
}

data "aws_iam_policy_document" "snowflake_kms" {
  count = var.kms_key_arn == null ? 0 : 1

  statement {
    sid       = "DecryptRawObjects"
    effect    = "Allow"
    actions   = ["kms:Decrypt", "kms:DescribeKey"]
    resources = [var.kms_key_arn]
  }
}

resource "aws_iam_role" "snowflake" {
  name               = var.aws_role_name
  description        = "Assumed by Snowflake storage integration ${var.storage_integration_name} (${var.env})"
  assume_role_policy = data.aws_iam_policy_document.snowflake_assume.json

  tags = var.aws_tags
}

resource "aws_iam_role_policy" "snowflake_read_raw" {
  name   = "${var.aws_role_name}-read-raw"
  role   = aws_iam_role.snowflake.id
  policy = data.aws_iam_policy_document.snowflake_read_raw.json
}

resource "aws_iam_role_policy" "snowflake_kms" {
  count = var.kms_key_arn == null ? 0 : 1

  name   = "${var.aws_role_name}-kms"
  role   = aws_iam_role.snowflake.id
  policy = data.aws_iam_policy_document.snowflake_kms[0].json
}

# ------------------------------------------------------- integration grants --
#
# The loader creates the external stages that reference the integration; the
# transformer needs it too for the parity models' legacy-report stage.

resource "snowflake_grant_privileges_to_account_role" "integration_usage" {
  for_each = toset(["loader", "transformer"])

  account_role_name = snowflake_account_role.this[each.value].name
  privileges        = ["USAGE"]

  on_account_object {
    object_type = "INTEGRATION"
    object_name = snowflake_storage_integration_aws.raw.name
  }
}
