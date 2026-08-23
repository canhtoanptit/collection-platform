# The Terraform state bucket and the CMK that encrypts it.
#
# This is the chicken-and-egg resource: it is created with local state, and then holds the state of
# every stack including its own. It is also the only S3 bucket in the platform that is not created
# by modules/s3-data -- state has different retention (90 days of versions, never expired), never
# gets force_destroy, and must not inherit changes made for the data lake.

data "aws_caller_identity" "current" {}

locals {
  account_id  = data.aws_caller_identity.current.account_id
  bucket_name = coalesce(var.state_bucket_name_override, "colx-tfstate-${local.account_id}")
}

# SSE-KMS with a dedicated CMK rather than SSE-S3, for three reasons:
#
#  1. State is the most sensitive object in the account. It contains RDS endpoints, generated
#     Cognito client secrets, and every resource identifier in the platform. SSE-S3 gives no
#     way to audit or revoke access to that content independently of s3:GetObject.
#  2. A CMK produces a CloudTrail `kms:Decrypt` record naming the principal for every state read,
#     which is the audit trail that makes "who read state" answerable.
#  3. Access can be revoked at the key, instantly and independently of the bucket policy -- the
#     containment step if a CI role is ever compromised.
#
# The cost is $1/month and one genuine risk: destroy the key and every state file is unreadable.
# Mitigations are the 30-day deletion window, bucket versioning, and the fact that this stack only
# manages account-level singletons that can be re-imported (see README, "If you lose the key").
resource "aws_kms_key" "tfstate" {
  description             = "Encrypts the Terraform state bucket ${local.bucket_name}"
  enable_key_rotation     = true
  deletion_window_in_days = 30

  tags = {
    Name = "colx-tfstate"
  }
}

resource "aws_kms_alias" "tfstate" {
  name          = "alias/colx-tfstate"
  target_key_id = aws_kms_key.tfstate.key_id
}

resource "aws_s3_bucket" "tfstate" {
  bucket = local.bucket_name

  # Never true. `terraform destroy` on this stack must fail while state objects exist.
  force_destroy = false

  tags = {
    Name    = local.bucket_name
    purpose = "terraform-state"
  }
}

# Non-negotiable: S3-native locking (`use_lockfile`) relies on conditional writes, and rolling back
# a corrupted state file relies on object versions. Both are silently unavailable if versioning is
# off, and the failure looks like data loss rather than a misconfiguration.
resource "aws_s3_bucket_versioning" "tfstate" {
  bucket = aws_s3_bucket.tfstate.id

  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "tfstate" {
  bucket = aws_s3_bucket.tfstate.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = "aws:kms"
      kms_master_key_id = aws_kms_key.tfstate.arn
    }
    bucket_key_enabled = true
  }
}

resource "aws_s3_bucket_public_access_block" "tfstate" {
  bucket = aws_s3_bucket.tfstate.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "tfstate" {
  bucket = aws_s3_bucket.tfstate.id

  rule {
    object_ownership = "BucketOwnerEnforced"
  }

  depends_on = [aws_s3_bucket_public_access_block.tfstate]
}

data "aws_iam_policy_document" "tfstate" {
  statement {
    sid    = "DenyNonTlsRequests"
    effect = "Deny"

    principals {
      type        = "*"
      identifiers = ["*"]
    }

    actions = ["s3:*"]

    resources = [
      aws_s3_bucket.tfstate.arn,
      "${aws_s3_bucket.tfstate.arn}/*",
    ]

    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }

  # NOT added here: a statement pinning s3:x-amz-server-side-encryption-aws-kms-key-id to this
  # key's ARN. The S3 backend forwards whatever form of key reference backend.hcl contains -- a key
  # id, a key ARN, an alias name, or an alias ARN -- and a StringEquals comparison against one form
  # rejects the other three, breaking every apply with an AccessDenied that reads like a
  # permissions problem. Encryption is enforced instead by the bucket's default SSE-KMS
  # configuration plus `kms_key_id` in envs/dev/backend.hcl.
}

resource "aws_s3_bucket_policy" "tfstate" {
  bucket = aws_s3_bucket.tfstate.id
  policy = data.aws_iam_policy_document.tfstate.json

  depends_on = [aws_s3_bucket_public_access_block.tfstate]
}

resource "aws_s3_bucket_lifecycle_configuration" "tfstate" {
  bucket = aws_s3_bucket.tfstate.id

  # Current state versions are never expired. Only superseded versions age out, and slowly:
  # this is the undo history for every stack in the platform.
  rule {
    id     = "expire-noncurrent-state-versions"
    status = "Enabled"

    filter {}

    noncurrent_version_expiration {
      noncurrent_days = var.state_noncurrent_version_expiration_days
    }
  }

  # No rule for abandoned `.tflock` files: S3 lifecycle filters match on prefix, tag or object
  # size -- never on suffix -- and a lock file's key is `<state key>.tflock`, so no prefix can
  # isolate one from the state file it guards. A lock left behind by a killed CI job is cleared
  # with `terraform force-unlock <id>`; see docs/runbooks/ (FND-13).
  rule {
    id     = "abort-incomplete-multipart-uploads"
    status = "Enabled"

    filter {}

    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }

  depends_on = [aws_s3_bucket_versioning.tfstate]
}
