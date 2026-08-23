output "warehouse_names" {
  description = "WH_INGEST / WH_TRANSFORM / WH_ANALYTICS."
  value       = module.snowflake.warehouse_names
}

output "resource_monitor_name" {
  description = "Credit monitor guarding every warehouse."
  value       = module.snowflake.resource_monitor_name
}

output "database_names" {
  description = "RAW / ANALYTICS / ANALYTICS_CI."
  value       = module.snowflake.database_names
}

output "raw_schema_names" {
  description = "RAW schemas — the load targets referenced by `data/snowflake/` DDL."
  value       = module.snowflake.raw_schema_names
}

output "analytics_schema_names" {
  description = "ANALYTICS schemas — dbt target schemas."
  value       = module.snowflake.analytics_schema_names
}

output "role_names" {
  description = "Logical key -> Snowflake role name."
  value       = module.snowflake.role_names
}

output "service_user_names" {
  description = "Key-pair service users."
  value       = module.snowflake.service_user_names
}

output "service_user_default_roles" {
  description = "Service user -> default role."
  value       = module.snowflake.service_user_default_roles
}

output "storage_integration_name" {
  description = "Name every external stage must reference."
  value       = module.snowflake.storage_integration_name
}

output "storage_integration_aws_role_arn" {
  description = "AWS role Snowflake assumes. `DESC INTEGRATION S3_RAW_INT` must show this."
  value       = module.snowflake.aws_role_arn
}

output "storage_integration_iam_user_arn" {
  description = "Snowflake-side IAM user trusted by the AWS role."
  value       = module.snowflake.storage_integration_iam_user_arn
}

output "storage_integration_external_id" {
  description = "External id enforced by the role's trust policy."
  value       = module.snowflake.storage_integration_external_id
}

output "masking_policy_names" {
  description = "Fully qualified masking policies for the dbt post-hooks (ANA WPs)."
  value       = module.snowflake.masking_policy_names
}

output "raw_bucket" {
  description = "Bucket the integration is scoped to."
  value       = local.raw_bucket
}
