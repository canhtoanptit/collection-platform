variable "region" {
  description = "AWS region for the storage-integration IAM role."
  type        = string
  default     = "eu-west-1"
}

variable "project" {
  description = "Project tag and AWS resource-name prefix. Named `project` to match envs/<env>/common.tfvars and stacks 00/10/20."
  type        = string
  default     = "colx"
}

variable "env" {
  description = "Environment name."
  type        = string
  default     = "dev"
}

variable "snowflake_object_prefix" {
  description = "Prefix for Snowflake role and monitor names (`COLX_ADMIN`, `COLX_MONTHLY`, ...)."
  type        = string
  default     = "COLX"
}

# ----------------------------------------------------- Snowflake provider auth --

variable "snowflake_organization_name" {
  description = <<-EOT
    Snowflake organization name (the first half of `<org>-<account>`). USER-SUPPLIED
    after the trial account exists — see README "Bootstrap".
  EOT
  type        = string
}

variable "snowflake_account_name" {
  description = "Snowflake account name (the second half of `<org>-<account>`). USER-SUPPLIED."
  type        = string
}

variable "snowflake_terraform_user" {
  description = "Snowflake user Terraform authenticates as. Created by hand in the bootstrap step (README)."
  type        = string
  default     = "TF_SVC"
}

variable "snowflake_terraform_role" {
  description = <<-EOT
    Role Terraform runs with. SYSADMIN cannot create roles or grant them, so the
    bootstrap grants TF_SVC both SYSADMIN and SECURITYADMIN and Terraform runs as
    the one that can do both: SECURITYADMIN for RBAC, SYSADMIN for objects. In
    practice `USERADMIN`-level work plus object creation means ACCOUNTADMIN is the
    only single role that covers everything — so the default is SYSADMIN and the
    README documents running the RBAC-touching applies with SECURITYADMIN, or
    granting both to TF_SVC and setting this to ACCOUNTADMIN for a trial account.
  EOT
  type        = string
  default     = "ACCOUNTADMIN"
}

variable "snowflake_private_key" {
  description = <<-EOT
    PEM private key for `snowflake_terraform_user`, unencrypted PKCS#8.
    USER-SUPPLIED via `TF_VAR_snowflake_private_key` from the `dev` GitHub
    environment. Never committed, never written to a tfvars file — and note it
    still lands in Terraform *state*, which is why the state bucket is SSE-KMS and
    TLS-only (ADR-0010).
  EOT
  type        = string
  sensitive   = true
}

# ------------------------------------------------------------- service users --

variable "service_user_public_keys" {
  description = <<-EOT
    RSA public key bodies (no PEM header/footer, no newlines) for AIRFLOW_SVC,
    DBT_SVC and DBT_CI_SVC. USER-SUPPLIED — generate with the commands in the
    README, store the private halves in Secrets Manager under
    `colx/<env>/snowflake/<user>/private_key`.
  EOT
  type        = map(string)
}

variable "service_user_emails" {
  description = "Optional contact email per service user."
  type        = map(string)
  default     = {}
}

# ----------------------------------------------------- storage integration ----

variable "raw_bucket" {
  description = "Bucket the integration reads. Defaults to `<prefix>-<env>-raw`."
  type        = string
  default     = null
}

variable "raw_bucket_prefixes" {
  description = "Prefixes inside the raw bucket that may be staged. `[\"\"]` = whole bucket."
  type        = list(string)
  default     = [""]
}

variable "storage_integration_external_id" {
  description = <<-EOT
    `sts:ExternalId` tying the Snowflake integration to the AWS role. Defaults to
    a deterministic string derived from the prefix/env/integration name — stable
    across rebuilds, which matters because changing it invalidates every existing
    stage.
  EOT
  type        = string
  default     = null
}

variable "credit_quota" {
  description = "Monthly credit cap (plan §2: 50)."
  type        = number
  default     = 50
}
