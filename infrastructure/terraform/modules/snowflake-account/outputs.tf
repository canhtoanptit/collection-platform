output "warehouse_names" {
  description = "Warehouse names, all attached to the credit monitor."
  value       = sort([for w in snowflake_warehouse.this : w.name])
}

output "resource_monitor_name" {
  description = "Credit monitor name. `SHOW RESOURCE MONITORS` must report credit_quota == var.credit_quota."
  value       = snowflake_resource_monitor.monthly.name
}

output "database_names" {
  description = "Databases created by this module."
  value = [
    snowflake_database.raw.name,
    snowflake_database.analytics.name,
    snowflake_database.analytics_ci.name,
  ]
}

output "raw_schema_names" {
  description = "Fully qualified RAW schemas."
  value       = sort([for s in snowflake_schema.raw : "${snowflake_database.raw.name}.${s.name}"])
}

output "analytics_schema_names" {
  description = "Fully qualified ANALYTICS schemas."
  value       = sort([for s in snowflake_schema.analytics : "${snowflake_database.analytics.name}.${s.name}"])
}

output "role_names" {
  description = "Logical role key -> Snowflake role name."
  value       = { for k, r in snowflake_account_role.this : k => r.name }
}

output "service_user_names" {
  description = "Service users created with key-pair auth."
  value       = sort([for u in snowflake_service_user.this : u.name])
}

output "service_user_default_roles" {
  description = "Service user -> default role, so a connection profile can be generated rather than typed."
  value       = { for k, u in snowflake_service_user.this : u.name => u.default_role }
}

output "storage_integration_name" {
  description = "Storage integration name, referenced by every external stage."
  value       = snowflake_storage_integration_aws.raw.name
}

output "storage_integration_iam_user_arn" {
  description = "Snowflake-side IAM user the AWS role trusts (from DESCRIBE INTEGRATION)."
  value       = snowflake_storage_integration_aws.raw.describe_output[0].iam_user_arn
}

output "storage_integration_external_id" {
  description = "`sts:ExternalId` enforced by the AWS role's trust policy."
  value       = var.aws_external_id
}

output "aws_role_arn" {
  description = "AWS role Snowflake assumes to read the raw bucket. `DESC INTEGRATION` must show this ARN."
  value       = aws_iam_role.snowflake.arn
}

output "masking_policy_names" {
  description = "Fully qualified masking policies for dbt post-hooks to apply."
  value = {
    string_pii = snowflake_masking_policy.string_pii.fully_qualified_name
    date_pii   = snowflake_masking_policy.date_pii.fully_qualified_name
  }
}
