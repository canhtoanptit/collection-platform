# These outputs are the stack's public interface. stacks/30-eks reads them through
# terraform_remote_state to build IRSA policies; scripts/db/provision_databases.sh takes the RDS
# ones as environment variables; FND-8's helmfile reads the MSK bootstrap servers.
#
# Treat them as a contract: add rather than rename.

# --- keys ---

output "kms_key_arns" {
  description = "Map of CMK ARNs keyed by short name (data, db, msk, secrets). IRSA policies grant kms:Decrypt/GenerateDataKey on the specific ARN."
  value       = module.kms.key_arns
}

output "kms_alias_names" {
  description = "Map of CMK alias names keyed by short name."
  value       = module.kms.alias_names
}

# --- buckets ---

output "bucket_ids" {
  description = "Map of bucket suffix to bucket name, e.g. { raw = \"colx-dev-raw\" }."
  value       = module.s3_data.bucket_ids
}

output "bucket_arns" {
  description = "Map of bucket suffix to bucket ARN, for IRSA and Snowflake storage-integration policies."
  value       = module.s3_data.bucket_arns
}

output "bucket_names" {
  description = "Sorted list of every bucket created. Asserted against plan §6.7 by scripts/verify/INF-A.sh."
  value       = module.s3_data.bucket_names
}

# --- registries ---

output "ecr_repository_urls" {
  description = "Map of ECR repository name to push/pull URL."
  value       = module.ecr.repository_urls
}

output "ecr_repository_arns" {
  description = "Map of ECR repository name to ARN."
  value       = module.ecr.repository_arns
}

# --- databases ---

output "rds_platform" {
  description = "Connection details for colx-dev-platform (databases ingestion, airflow, keycloak, plus one per service). The password lives only in the Secrets Manager secret named here."
  value = {
    identifier             = module.rds_platform.identifier
    address                = module.rds_platform.address
    endpoint               = module.rds_platform.endpoint
    port                   = module.rds_platform.port
    master_username        = module.rds_platform.master_username
    master_user_secret_arn = module.rds_platform.master_user_secret_arn
    security_group_id      = module.rds_platform.security_group_id
    resource_id            = module.rds_platform.resource_id
  }
}

output "rds_corebank" {
  description = "Connection details for colx-dev-corebank, the CDC source with logical replication enabled."
  value = {
    identifier             = module.rds_corebank.identifier
    address                = module.rds_corebank.address
    endpoint               = module.rds_corebank.endpoint
    port                   = module.rds_corebank.port
    master_username        = module.rds_corebank.master_username
    master_user_secret_arn = module.rds_corebank.master_user_secret_arn
    security_group_id      = module.rds_corebank.security_group_id
    parameter_group_name   = module.rds_corebank.parameter_group_name
    resource_id            = module.rds_corebank.resource_id
  }
}

# --- eventing ---

output "msk_bootstrap_brokers_sasl_iam" {
  description = "MSK bootstrap servers for SASL/IAM over TLS. Delivered to workloads as an ExternalSecret or a ConfigMap by FND-8; never committed."
  value       = module.msk.bootstrap_brokers_sasl_iam
}

output "msk_cluster_arn" {
  description = "MSK cluster ARN, needed for kafka-cluster:* IAM policies."
  value       = module.msk.cluster_arn
}

output "msk_cluster_name" {
  description = "MSK cluster name."
  value       = module.msk.cluster_name
}

output "msk_cluster_uuid" {
  description = "Cluster UUID -- the middle segment of kafka-cluster topic/group ARNs."
  value       = module.msk.cluster_uuid
}

output "msk_security_group_id" {
  description = "MSK broker security group id."
  value       = module.msk.security_group_id
}

output "msk_client_properties" {
  description = "The client.properties content every IAM-authenticated Kafka client needs. Kept as an output so deployment/kafka/topic-apply-job.yaml and the connect toolbox cannot drift from the cluster."
  value       = module.msk.client_properties
}

# --- secrets ---

output "secret_arns" {
  description = "Map of short secret name (e.g. \"keycloak/admin\") to ARN. IRSA and ExternalSecret policies reference these; every one is created without a value on purpose."
  value       = { for k, v in aws_secretsmanager_secret.placeholders : k => v.arn }
}

output "secret_names" {
  description = "Sorted list of placeholder secret names created (colx/dev/...)."
  value       = sort([for v in aws_secretsmanager_secret.placeholders : v.name])
}

# --- operational hints ---

output "provision_databases_env" {
  description = <<-EOT
    The environment scripts/db/provision_databases.sh expects, filled in from this stack's outputs.
    Print with `terraform output -raw provision_databases_env`; run the script from a pod or a
    bastion inside the VPC -- the data subnets have no route in from outside.
  EOT
  value       = <<-EOT
    export AWS_REGION="${var.region}"
    export SECRET_PREFIX="${local.secret_prefix}"
    export SECRETS_KMS_KEY_ARN="${module.kms.key_arns["secrets"]}"

    export PLATFORM_HOST="${module.rds_platform.address}"
    export PLATFORM_PORT="${module.rds_platform.port}"
    export PLATFORM_MASTER_SECRET_ARN="${module.rds_platform.master_user_secret_arn}"

    export COREBANK_HOST="${module.rds_corebank.address}"
    export COREBANK_PORT="${module.rds_corebank.port}"
    export COREBANK_MASTER_SECRET_ARN="${module.rds_corebank.master_user_secret_arn}"
  EOT
}

output "second_pass_required" {
  description = "True while no EKS node security group has been supplied, meaning the databases and brokers exist but have no ingress rules and nothing can connect. Set eks_node_security_group_id and re-apply after stacks/30-eks."
  value       = length(local.eks_client_security_group_ids) == 0
}
