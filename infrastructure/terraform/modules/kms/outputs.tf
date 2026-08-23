output "key_arns" {
  description = "Map of short key name to KMS key ARN. Use these in resource arguments that expect an ARN (S3 SSE, RDS storage encryption, MSK encryption at rest)."
  value       = { for k, v in aws_kms_key.this : k => v.arn }
}

output "key_ids" {
  description = "Map of short key name to KMS key id (UUID)."
  value       = { for k, v in aws_kms_key.this : k => v.key_id }
}

output "alias_names" {
  description = "Map of short key name to alias name (alias/<name_prefix>-<key>)."
  value       = { for k, v in aws_kms_alias.this : k => v.name }
}

output "alias_arns" {
  description = "Map of short key name to alias ARN."
  value       = { for k, v in aws_kms_alias.this : k => v.arn }
}
