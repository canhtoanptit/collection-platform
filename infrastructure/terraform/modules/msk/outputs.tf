output "cluster_arn" {
  description = "MSK cluster ARN. Needed in IAM policies for kafka-cluster:* actions."
  value       = aws_msk_cluster.this.arn
}

output "cluster_name" {
  description = "MSK cluster name."
  value       = aws_msk_cluster.this.cluster_name
}

output "cluster_uuid" {
  description = "Cluster UUID, the wildcard segment in kafka-cluster IAM resource ARNs (arn:aws:kafka:<region>:<acct>:topic/<name>/<uuid>/*)."
  value       = aws_msk_cluster.this.cluster_uuid
}

output "bootstrap_brokers_sasl_iam" {
  description = "Bootstrap servers for SASL/IAM over TLS (port 9098). This is the only endpoint clients should ever use; it is delivered to workloads as an ExternalSecret, never committed."
  value       = aws_msk_cluster.this.bootstrap_brokers_sasl_iam
}

output "zookeeper_connect_string_tls" {
  description = "ZooKeeper TLS connect string. Clients must not use it -- kafka-topics.sh takes --bootstrap-server. Exported only because some operational tooling still asks."
  value       = aws_msk_cluster.this.zookeeper_connect_string_tls
}

output "security_group_id" {
  description = "Broker security group. Grant access by adding client_security_group_ids, not by editing this group from another stack."
  value       = aws_security_group.brokers.id
}

output "configuration_arn" {
  description = "ARN of the MSK cluster configuration."
  value       = aws_msk_configuration.this.arn
}

output "configuration_revision" {
  description = "Current revision of the cluster configuration. Increments whenever server_properties changes."
  value       = aws_msk_configuration.this.latest_revision
}

output "server_properties" {
  description = "The rendered cluster configuration, for eyeballing in a plan."
  value       = local.server_properties
}

output "client_properties" {
  description = <<-EOT
    Contents of the client.properties file every IAM-authenticated client needs. Used verbatim by
    deployment/kafka/topic-apply-job.yaml and by anything running kafka-topics.sh in-cluster.
  EOT
  value       = <<-EOT
    security.protocol=SASL_SSL
    sasl.mechanism=AWS_MSK_IAM
    sasl.jaas.config=software.amazon.msk.auth.iam.IAMLoginModule required;
    sasl.client.callback.handler.class=software.amazon.msk.auth.iam.IAMClientCallbackHandler
  EOT
}
