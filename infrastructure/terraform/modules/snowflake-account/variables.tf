variable "prefix" {
  description = "Object-name prefix for Snowflake roles and monitors (`COLX`)."
  type        = string
  default     = "COLX"
}

variable "env" {
  description = "Environment name, used in comments and the AWS role name."
  type        = string
  default     = "dev"
}

# ------------------------------------------------------------------ warehouses --

variable "warehouse_size" {
  description = "Size for every warehouse. XSMALL — the credit cap, not the size, is the cost control (ADR-0008)."
  type        = string
  default     = "XSMALL"
}

variable "warehouse_auto_suspend_seconds" {
  description = "Auto-suspend delay. 60s makes idle cost ≈ $0 (plan §2)."
  type        = number
  default     = 60
}

variable "credit_quota" {
  description = "Monthly credit quota on the `<prefix>_MONTHLY` resource monitor. A hard cap: at 100% the warehouses suspend and the day's DAG fails (ADR-0008)."
  type        = number
  default     = 50
}

variable "notify_triggers_percent" {
  description = "Percent-of-quota thresholds that notify."
  type        = list(number)
  default     = [50, 80]
}

variable "suspend_trigger_percent" {
  description = "Percent-of-quota threshold that suspends warehouses after running statements finish."
  type        = number
  default     = 100
}

# ------------------------------------------------------------------- databases --

variable "raw_schemas" {
  description = "Schemas in the RAW database (A§43 source-fidelity layer)."
  type        = list(string)
  default     = ["CDC_COREBANK", "FILES_COREBANK", "WEBHOOKS", "EVENTS", "LEGACY_REPORTS"]
}

variable "analytics_schemas" {
  description = "Schemas in the ANALYTICS database. GOVERNANCE holds the masking policies (A§69)."
  type        = list(string)
  default     = ["STAGING", "INTERMEDIATE", "MARTS", "SNAPSHOTS", "GOVERNANCE"]
}

variable "data_retention_days" {
  description = "Time Travel retention. 1 day in dev; Enterprise allows up to 90 (prod delta in README)."
  type        = number
  default     = 1
}

# -------------------------------------------------------------- service users --

variable "service_user_public_keys" {
  description = <<-EOT
    RSA public keys for the service users, keyed by user name
    (`AIRFLOW_SVC`, `DBT_SVC`, `DBT_CI_SVC`).

    Value is the *body* of the PEM — no `-----BEGIN PUBLIC KEY-----` header, no
    newlines — exactly as `ALTER USER ... SET RSA_PUBLIC_KEY` expects.

    USER-SUPPLIED. Private keys are generated out of band, stored in Secrets
    Manager under `colx/<env>/snowflake/<user>/private_key`, and never enter
    Terraform state or this repo. README has the openssl commands.
  EOT
  type        = map(string)

  validation {
    condition     = length(setsubtract(["AIRFLOW_SVC", "DBT_SVC", "DBT_CI_SVC"], keys(var.service_user_public_keys))) == 0
    error_message = "service_user_public_keys must contain AIRFLOW_SVC, DBT_SVC and DBT_CI_SVC."
  }

  validation {
    condition     = alltrue([for k, v in var.service_user_public_keys : !can(regex("BEGIN (RSA )?PUBLIC KEY", v))])
    error_message = "Strip the PEM header/footer and newlines: pass the base64 body only."
  }
}

variable "service_user_emails" {
  description = "Optional contact email per service user, for Snowflake notifications. Keyed by user name."
  type        = map(string)
  default     = {}
}

# --------------------------------------------------------- storage integration --

variable "storage_integration_name" {
  description = "Name of the S3 storage integration."
  type        = string
  default     = "S3_RAW_INT"
}

variable "raw_bucket" {
  description = "S3 bucket the integration may read (`colx-dev-raw`)."
  type        = string
}

variable "raw_bucket_prefixes" {
  description = <<-EOT
    Prefixes inside `raw_bucket` the integration may read. `[""]` allows the whole
    bucket. Narrowing this is the cheapest way to stop a stage definition reaching
    data it should not (A§35).
  EOT
  type        = list(string)
  default     = [""]
}

variable "aws_role_name" {
  description = <<-EOT
    Name of the AWS IAM role Snowflake assumes. Passed in (rather than derived
    from the integration) on purpose: the integration needs the role ARN and the
    role's trust policy needs the integration's IAM user ARN, so one side has to
    be name-by-convention to break the cycle.
  EOT
  type        = string
}

variable "aws_external_id" {
  description = <<-EOT
    `sts:ExternalId` shared between the integration and the role's trust policy.
    Chosen by us rather than accepted from Snowflake, again to break the
    integration <-> role cycle. Not a secret, but it must be unguessable enough to
    block the confused-deputy attack the condition exists to prevent.
  EOT
  type        = string
}

variable "kms_key_arn" {
  description = "CMK protecting objects in `raw_bucket`, so Snowflake can decrypt what it reads. Null for SSE-S3 buckets."
  type        = string
  default     = null
}

variable "aws_tags" {
  description = "Extra tags for the AWS-side resources."
  type        = map(string)
  default     = {}
}

variable "pii_reader_role_suffix" {
  description = "Role that unmasks PII. The masking policy bodies test `IS_ROLE_IN_SESSION('<prefix>_<this>')`."
  type        = string
  default     = "PII_READER"
}

variable "masked_string_placeholder" {
  description = "Value non-privileged roles see for masked strings. Asserted verbatim by the Phase 6 masking test (ADR-0008)."
  type        = string
  default     = "***MASKED***"
}
