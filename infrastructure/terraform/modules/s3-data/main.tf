# The platform's S3 data buckets (plan §6.7). One map entry per bucket; every security
# control below is applied to every bucket with no per-bucket opt-out, because "which
# bucket forgot public-access-block" is not a question anyone should have to ask.

locals {
  # Flatten the per-bucket prefix retention maps into a single set of rules so the lifecycle
  # configuration can use one dynamic block per bucket. Key shape: "<bucket>|<prefix>".
  prefix_rules = merge([
    for bucket_key, bucket in var.buckets : {
      for prefix, days in bucket.prefix_expiration_days :
      "${bucket_key}|${prefix}" => {
        bucket_key = bucket_key
        prefix     = prefix
        days       = days
        # Rule ids must be unique per bucket and stable across applies: derive from the prefix
        # rather than an index so reordering the map cannot rewrite unrelated rules.
        rule_id = "expire-${replace(trimsuffix(prefix, "/"), "/", "-")}"
      }
    }
  ]...)

  versioned_buckets = { for k, v in var.buckets : k => v if v.versioning }
}

resource "aws_s3_bucket" "this" {
  for_each = var.buckets

  bucket        = "${var.name_prefix}-${each.key}"
  force_destroy = each.value.force_destroy

  tags = {
    Name    = "${var.name_prefix}-${each.key}"
    purpose = each.value.purpose
  }
}

resource "aws_s3_bucket_public_access_block" "this" {
  for_each = var.buckets

  bucket = aws_s3_bucket.this[each.key].id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# ACLs disabled entirely: every object is owned by the bucket owner, so access is decided by
# bucket policy and IAM only. This is what makes "block public access" a complete statement.
resource "aws_s3_bucket_ownership_controls" "this" {
  for_each = var.buckets

  bucket = aws_s3_bucket.this[each.key].id

  rule {
    object_ownership = "BucketOwnerEnforced"
  }

  depends_on = [aws_s3_bucket_public_access_block.this]
}

resource "aws_s3_bucket_server_side_encryption_configuration" "this" {
  for_each = var.buckets

  bucket = aws_s3_bucket.this[each.key].id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = "aws:kms"
      kms_master_key_id = var.kms_key_arn
    }
    # One data key per bucket+prefix instead of one per object: cuts KMS request charges by
    # orders of magnitude on the CDC/file paths that write thousands of small objects.
    bucket_key_enabled = true
  }
}

resource "aws_s3_bucket_versioning" "this" {
  for_each = local.versioned_buckets

  bucket = aws_s3_bucket.this[each.key].id

  versioning_configuration {
    status = "Enabled"
  }
}

data "aws_iam_policy_document" "tls_only" {
  for_each = var.buckets

  statement {
    sid    = "DenyNonTlsRequests"
    effect = "Deny"

    principals {
      type        = "*"
      identifiers = ["*"]
    }

    actions = ["s3:*"]

    resources = [
      aws_s3_bucket.this[each.key].arn,
      "${aws_s3_bucket.this[each.key].arn}/*",
    ]

    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }
}

resource "aws_s3_bucket_policy" "this" {
  for_each = var.buckets

  bucket = aws_s3_bucket.this[each.key].id
  policy = data.aws_iam_policy_document.tls_only[each.key].json

  # A bucket policy that denies everything unencrypted must not land before public access is
  # blocked, or there is a window where the bucket is neither.
  depends_on = [aws_s3_bucket_public_access_block.this]
}

resource "aws_s3_bucket_lifecycle_configuration" "this" {
  for_each = var.buckets

  bucket = aws_s3_bucket.this[each.key].id

  # Unconditional: parts of a failed multipart upload are invisible in the console and bill
  # forever. Every bucket gets this rule.
  rule {
    id     = "abort-incomplete-multipart-uploads"
    status = "Enabled"

    filter {}

    abort_incomplete_multipart_upload {
      days_after_initiation = var.abort_incomplete_multipart_upload_days
    }
  }

  dynamic "rule" {
    for_each = each.value.expire_current_after_days == null ? [] : [each.value.expire_current_after_days]

    content {
      id     = "expire-current-objects"
      status = "Enabled"

      filter {}

      expiration {
        days = rule.value
      }
    }
  }

  dynamic "rule" {
    for_each = each.value.versioning ? [each.value.noncurrent_version_expiration_days] : []

    content {
      id     = "expire-noncurrent-versions"
      status = "Enabled"

      filter {}

      noncurrent_version_expiration {
        noncurrent_days = rule.value
      }
    }
  }

  dynamic "rule" {
    for_each = { for k, v in local.prefix_rules : k => v if v.bucket_key == each.key }

    content {
      id     = rule.value.rule_id
      status = "Enabled"

      filter {
        prefix = rule.value.prefix
      }

      expiration {
        days = rule.value.days
      }
    }
  }

  # Lifecycle rules that reference noncurrent versions are rejected until versioning exists.
  depends_on = [aws_s3_bucket_versioning.this]
}
