output "cluster_name" {
  description = "EKS cluster name."
  value       = module.this.cluster_name
}

output "cluster_arn" {
  description = "EKS cluster ARN."
  value       = module.this.cluster_arn
}

output "cluster_endpoint" {
  description = "Kubernetes API server endpoint."
  value       = module.this.cluster_endpoint
}

output "cluster_version" {
  description = "Kubernetes version actually running on the control plane."
  value       = module.this.cluster_version
}

output "cluster_certificate_authority_data" {
  description = "Base64 cluster CA, for kubeconfig generation."
  value       = module.this.cluster_certificate_authority_data
}

output "cluster_security_group_id" {
  description = "Cluster (control-plane) security group id — the id stack 20-data allows into RDS and MSK."
  value       = module.this.cluster_security_group_id
}

output "node_security_group_id" {
  description = "Managed node group security group id."
  value       = module.this.node_security_group_id
}

output "oidc_provider_arn" {
  description = "IAM OIDC provider ARN for IRSA trust policies."
  value       = module.this.oidc_provider_arn
}

output "oidc_provider" {
  description = "OIDC issuer host/path (no scheme) used in IRSA `sub`/`aud` conditions."
  value       = module.this.oidc_provider
}

output "node_iam_role_arn" {
  description = "Managed node group instance role ARN."
  value       = module.this.node_iam_role_arn
}

output "eks_managed_node_groups" {
  description = "Managed node group attributes, keyed by node group name (autoscaling group names live under `.node_group_autoscaling_group_names`)."
  value       = module.this.eks_managed_node_groups
}

output "ebs_csi_irsa_role_arn" {
  description = "IRSA role assumed by the aws-ebs-csi-driver addon."
  value       = aws_iam_role.ebs_csi.arn
}
