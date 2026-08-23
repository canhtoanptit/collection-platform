output "repository_urls" {
  description = "Map of repository name to its full push/pull URL (<account>.dkr.ecr.<region>.amazonaws.com/<name>)."
  value       = { for k, v in aws_ecr_repository.this : k => v.repository_url }
}

output "repository_arns" {
  description = "Map of repository name to ARN. Used to scope the colx-gha-ecr-push role and IRSA pull policies."
  value       = { for k, v in aws_ecr_repository.this : k => v.arn }
}

output "repository_names" {
  description = "Sorted list of repository names created, for verification scripts."
  value       = sort([for v in aws_ecr_repository.this : v.name])
}

output "registry_id" {
  description = "The registry (account) id hosting these repositories."
  value       = one(distinct([for v in aws_ecr_repository.this : v.registry_id]))
}
