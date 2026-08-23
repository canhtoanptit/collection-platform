variable "name_prefix" {
  description = "Prefix for bucket names, e.g. \"colx-dev\" produces colx-dev-landing."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,30}$", var.name_prefix))
    error_message = "name_prefix must be lowercase alphanumeric with hyphens, 2-31 characters."
  }
}

variable "kms_key_arn" {
  description = "ARN of the CMK used for SSE-KMS default encryption on every bucket (the `data` key from modules/kms)."
  type        = string

  validation {
    condition     = can(regex("^arn:aws:kms:", var.kms_key_arn))
    error_message = "kms_key_arn must be a KMS key ARN (arn:aws:kms:...)."
  }
}

variable "buckets" {
  description = <<-EOT
    Buckets to create, keyed by name suffix. The map key becomes `<name_prefix>-<key>`.

    Every bucket gets, unconditionally: public access blocked (all four switches), SSE-KMS default
    encryption with `kms_key_arn` and an S3 Bucket Key, `BucketOwnerEnforced` object ownership
    (ACLs disabled), a TLS-only bucket policy, and a rule aborting incomplete multipart uploads.

    Per-bucket fields:
    - `purpose`                            what the bucket holds; becomes the `purpose` tag.
    - `versioning`                         keep object versions. Costs storage; only worth it where an
                                           overwrite would destroy evidence.
    - `expire_current_after_days`          expire current objects across the whole bucket. `null` = never.
    - `prefix_expiration_days`             map of key prefix -> days, for buckets whose retention differs
                                           per prefix (the `ops` bucket). Prefixes must end with `/`.
    - `noncurrent_version_expiration_days` how long superseded versions survive. Ignored unless
                                           `versioning` is true.
    - `force_destroy`                      allow `terraform destroy` to delete a non-empty bucket.
  EOT

  type = map(object({
    purpose                            = string
    versioning                         = optional(bool, false)
    expire_current_after_days          = optional(number, null)
    prefix_expiration_days             = optional(map(number), {})
    noncurrent_version_expiration_days = optional(number, 7)
    force_destroy                      = optional(bool, false)
  }))

  validation {
    condition     = alltrue([for k, v in var.buckets : can(regex("^[a-z][a-z0-9-]{1,40}$", k))])
    error_message = "Each bucket suffix must be lowercase alphanumeric with hyphens, 2-41 characters."
  }

  validation {
    condition = alltrue(flatten([
      for k, v in var.buckets : [for p, d in v.prefix_expiration_days : endswith(p, "/")]
    ]))
    error_message = "Every prefix in prefix_expiration_days must end with \"/\" so it cannot match sibling prefixes."
  }

  validation {
    condition = alltrue([
      for k, v in var.buckets :
      v.expire_current_after_days == null || v.expire_current_after_days > 0
    ])
    error_message = "expire_current_after_days must be null or a positive number of days."
  }
}

variable "abort_incomplete_multipart_upload_days" {
  description = "Days before an incomplete multipart upload is aborted and its parts stopped billing. Applies to every bucket."
  type        = number
  default     = 7

  validation {
    condition     = var.abort_incomplete_multipart_upload_days >= 1
    error_message = "abort_incomplete_multipart_upload_days must be at least 1."
  }
}
