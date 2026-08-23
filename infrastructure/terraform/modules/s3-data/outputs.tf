output "bucket_ids" {
  description = "Map of bucket suffix to bucket name, e.g. { landing = \"colx-dev-landing\" }."
  value       = { for k, v in aws_s3_bucket.this : k => v.id }
}

output "bucket_arns" {
  description = "Map of bucket suffix to bucket ARN. Use these when writing IAM policies for IRSA roles."
  value       = { for k, v in aws_s3_bucket.this : k => v.arn }
}

output "bucket_regional_domain_names" {
  description = "Map of bucket suffix to regional domain name, for clients that must address the bucket directly."
  value       = { for k, v in aws_s3_bucket.this : k => v.bucket_regional_domain_name }
}

output "bucket_names" {
  description = "Sorted list of every bucket name created, for verification scripts."
  value       = sort([for v in aws_s3_bucket.this : v.id])
}
