variable "name_prefix" {
  description = "Prefix for key aliases, e.g. \"colx-dev\" produces alias/colx-dev-data."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,30}$", var.name_prefix))
    error_message = "name_prefix must be lowercase alphanumeric with hyphens, 2-31 characters."
  }
}

variable "keys" {
  description = <<-EOT
    Customer managed keys to create, keyed by short name. The map key becomes the alias suffix
    (`alias/<name_prefix>-<key>`) and the key's `Name` tag.

    - `description`             human-readable purpose, shown in the KMS console.
    - `enable_key_rotation`     annual automatic rotation of the backing key material. Free, but it
                                only helps keys that encrypt long-lived material we cannot re-encrypt
                                cheaply, so it is opt-in per key rather than blanket-on.
    - `deletion_window_in_days` grace period after `terraform destroy` before the key is unrecoverable.
                                Anything encrypted with the key is unreadable once it elapses.
    - `multi_region`            create a multi-region primary. Not needed in dev.
  EOT

  type = map(object({
    description             = string
    enable_key_rotation     = optional(bool, false)
    deletion_window_in_days = optional(number, 30)
    multi_region            = optional(bool, false)
  }))

  validation {
    condition     = alltrue([for k, v in var.keys : can(regex("^[a-z][a-z0-9-]{1,30}$", k))])
    error_message = "Each key name must be lowercase alphanumeric with hyphens, 2-31 characters."
  }

  validation {
    condition     = alltrue([for k, v in var.keys : v.deletion_window_in_days >= 7 && v.deletion_window_in_days <= 30])
    error_message = "deletion_window_in_days must be between 7 and 30 (AWS limit)."
  }
}
