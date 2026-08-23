output "cluster_name" {
  description = "EKS cluster name — `aws eks update-kubeconfig --name <this>`."
  value       = module.eks.cluster_name
}

output "cluster_arn" {
  description = "EKS cluster ARN."
  value       = module.eks.cluster_arn
}

output "cluster_endpoint" {
  description = "Kubernetes API server endpoint."
  value       = module.eks.cluster_endpoint
}

output "cluster_version" {
  description = "Kubernetes version on the control plane."
  value       = module.eks.cluster_version
}

output "cluster_security_group_id" {
  description = "Control-plane security group — the source stack 20-data allows into RDS and MSK."
  value       = module.eks.cluster_security_group_id
}

output "node_security_group_id" {
  description = "Managed node group security group."
  value       = module.eks.node_security_group_id
}

output "oidc_provider_arn" {
  description = "IRSA OIDC provider ARN. Later stacks/WPs that add roles (DEC-6, DEC-14, INF-14) read this."
  value       = module.eks.oidc_provider_arn
}

output "oidc_provider" {
  description = "OIDC issuer host/path with no scheme."
  value       = module.eks.oidc_provider
}

output "node_group_names" {
  description = "Managed node group names — input to `scripts/cost/stop.sh` (scale to 0)."
  value       = keys(module.eks.eks_managed_node_groups)
}

output "irsa_role_arns" {
  description = <<-EOT
    Logical workload name -> IRSA role ARN. Every `serviceAccount.annotations`
    entry in `deployment/values/**` must use a value from this map; nothing else
    should ever put an AWS ARN in a values file by hand.
  EOT
  value       = module.irsa.role_arns
}

output "irsa_service_accounts" {
  description = "Logical workload name -> `namespace/serviceaccount` the role trusts."
  value       = module.irsa.service_accounts
}

output "ebs_csi_irsa_role_arn" {
  description = "IRSA role used by the aws-ebs-csi-driver addon."
  value       = module.eks.ebs_csi_irsa_role_arn
}
