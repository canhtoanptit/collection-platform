variable "repositories" {
  description = <<-EOT
    Repository names to create, exactly as they appear in an image reference after the registry
    host — e.g. "colx/ingestion" becomes <account>.dkr.ecr.<region>.amazonaws.com/colx/ingestion.
  EOT
  type        = list(string)

  validation {
    condition     = length(var.repositories) > 0
    error_message = "At least one repository is required."
  }

  validation {
    condition     = alltrue([for r in var.repositories : can(regex("^[a-z0-9]+(?:[._/-][a-z0-9]+)*$", r))])
    error_message = "Repository names must match the ECR naming rules: lowercase alphanumeric separated by . _ - or /."
  }
}

variable "scan_on_push" {
  description = "Run a basic vulnerability scan when a tag is pushed. Findings are advisory here; the blocking gate is trivy in the images workflow (FND-12)."
  type        = bool
  default     = true
}

variable "image_tag_mutability" {
  description = "MUTABLE or IMMUTABLE. MUTABLE is required while images are tagged both `<sha>` and `latest` (FND-12), because `latest` has to move."
  type        = string
  default     = "MUTABLE"

  validation {
    condition     = contains(["MUTABLE", "IMMUTABLE"], var.image_tag_mutability)
    error_message = "image_tag_mutability must be MUTABLE or IMMUTABLE."
  }
}

variable "keep_last_images" {
  description = "How many images to retain per repository before the lifecycle policy expires the oldest."
  type        = number
  default     = 10

  validation {
    condition     = var.keep_last_images >= 1 && var.keep_last_images <= 1000
    error_message = "keep_last_images must be between 1 and 1000."
  }
}

variable "untagged_expire_days" {
  description = "Days before an untagged image (a layer orphaned by a re-tag) is expired."
  type        = number
  default     = 1

  validation {
    condition     = var.untagged_expire_days >= 1
    error_message = "untagged_expire_days must be at least 1."
  }
}

variable "kms_key_arn" {
  description = "Optional CMK for repository encryption. `null` uses ECR's AES256 encryption, which is free; a CMK adds a KMS request per layer operation."
  type        = string
  default     = null
}

variable "force_delete" {
  description = "Allow `terraform destroy` to delete a repository that still contains images. Needed for `make destroy-all`; keep false otherwise."
  type        = bool
  default     = false
}
