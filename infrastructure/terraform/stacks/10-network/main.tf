# Stack 10-network -- the VPC every other stack builds inside.
#
# Applied by CI only (ADR-0010). It has no dependency on any other stack, and stacks 20-data and
# 30-eks read its outputs through terraform_remote_state, so it must be applied first.
#
# The stack is deliberately thin: all the substance is in modules/network, which wraps
# terraform-aws-modules/vpc at an exact version. That keeps the upstream module's surface behind
# one reviewable boundary and lets the rest of the repo speak our vocabulary ("data subnets")
# rather than the module's ("database subnets").

module "network" {
  source = "../../modules/network"

  name     = "${var.project}-${var.env}"
  region   = var.region
  vpc_cidr = var.vpc_cidr
  az_count = var.az_count

  enable_nat_gateway = true
  single_nat_gateway = var.single_nat_gateway
  enable_flow_log    = var.enable_flow_log

  enable_s3_gateway_endpoint = true
  eks_cluster_name           = var.eks_cluster_name
}
