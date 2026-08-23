output "identifier" {
  description = "RDS instance identifier."
  value       = aws_db_instance.this.identifier
}

output "arn" {
  description = "RDS instance ARN."
  value       = aws_db_instance.this.arn
}

output "address" {
  description = "DNS name of the instance, without the port. This is the PGHOST value for scripts/db/provision_databases.sh."
  value       = aws_db_instance.this.address
}

output "endpoint" {
  description = "host:port form of the connection endpoint."
  value       = aws_db_instance.this.endpoint
}

output "port" {
  description = "Postgres port."
  value       = aws_db_instance.this.port
}

output "master_username" {
  description = "Master user name. The password is in the Secrets Manager secret below, never here."
  value       = aws_db_instance.this.username
}

output "master_user_secret_arn" {
  description = "ARN of the RDS-managed master credential secret. Feed this to scripts/db/provision_databases.sh as MASTER_SECRET_ARN; the JSON value holds `username` and `password`."
  value       = try(aws_db_instance.this.master_user_secret[0].secret_arn, null)
}

output "security_group_id" {
  description = "Security group attached to the instance. Grant access by adding ingress_security_group_ids, not by editing this group elsewhere."
  value       = aws_security_group.this.id
}

output "db_subnet_group_name" {
  description = "DB subnet group in use, whether supplied or created here."
  value       = local.subnet_group_name
}

output "parameter_group_name" {
  description = "Parameter group name, or null when no parameters were supplied."
  value       = try(aws_db_parameter_group.this[0].name, null)
}

output "resource_id" {
  description = "The instance's dbi-resource-id, needed to build rds-db:connect IAM policies for IAM database authentication."
  value       = aws_db_instance.this.resource_id
}
