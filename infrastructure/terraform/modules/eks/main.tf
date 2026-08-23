# modules/eks — thin wrapper over terraform-aws-modules/eks/aws.
#
# The wrapper exists to (a) pin the upstream version in one place, (b) express the
# project's non-negotiable defaults (API-only auth, restricted public endpoint,
# AL2023, one node group) as module defaults rather than as stack copy-paste, and
# (c) own the EBS CSI IRSA role, which the upstream module cannot create for us
# without an input/output cycle.

locals {
  # Addons that need no AWS permissions of their own in this environment. vpc-cni
  # runs before the node group so pods get IPs on first boot.
  base_addons = {
    coredns    = { before_compute = false }
    kube-proxy = { before_compute = true }
    vpc-cni    = { before_compute = true }
  }

  addons = {
    for name, cfg in local.base_addons : name => {
      before_compute = cfg.before_compute
      # An explicit pin wins; otherwise track the newest compatible version.
      addon_version = try(var.addon_versions[name], null)
      most_recent   = !contains(keys(var.addon_versions), name)
    }
  }

  ebs_csi_namespace       = split("/", var.ebs_csi_service_account)[0]
  ebs_csi_service_account = split("/", var.ebs_csi_service_account)[1]
}

module "this" {
  source  = "terraform-aws-modules/eks/aws"
  version = "21.25.0"

  name               = var.name
  kubernetes_version = var.kubernetes_version

  vpc_id                   = var.vpc_id
  subnet_ids               = var.subnet_ids
  control_plane_subnet_ids = coalesce(var.control_plane_subnet_ids, var.subnet_ids)

  # Access is IAM-native: no aws-auth ConfigMap to hand-edit, and every grant is
  # an access entry visible in the plan (ADR-0011).
  authentication_mode                      = var.authentication_mode
  access_entries                           = var.access_entries
  enable_cluster_creator_admin_permissions = var.enable_cluster_creator_admin_permissions

  endpoint_private_access      = var.endpoint_private_access
  endpoint_public_access       = var.endpoint_public_access
  endpoint_public_access_cidrs = var.endpoint_public_access_cidrs

  enabled_log_types                      = var.enabled_log_types
  cloudwatch_log_group_retention_in_days = var.cloudwatch_log_group_retention_in_days
  create_cloudwatch_log_group            = true

  # Reuse the shared `colx-dev-secrets` CMK when given; otherwise let the module
  # create and manage a cluster-scoped key.
  create_kms_key = var.cluster_encryption_kms_key_arn == null
  encryption_config = {
    provider_key_arn = var.cluster_encryption_kms_key_arn
    resources        = ["secrets"]
  }

  # IRSA, not EKS Pod Identity: every pinned chart and every runbook in this repo
  # assumes an OIDC provider plus a service-account annotation (ADR-0011).
  enable_irsa = true

  addons = local.addons

  eks_managed_node_groups = {
    (var.node_group_name) = {
      ami_type       = var.node_ami_type
      instance_types = var.node_instance_types
      capacity_type  = var.node_capacity_type
      disk_size      = var.node_disk_size

      min_size     = var.node_min_size
      max_size     = var.node_max_size
      desired_size = var.node_desired_size

      labels = {
        "colx.io/pool" = var.node_group_name
      }
    }
  }

  tags = var.tags
}

# ---------------------------------------------------------------------------
# EBS CSI driver: IRSA role + addon.
#
# The addon is a standalone resource rather than an entry in the upstream
# `addons` map on purpose. The role's trust policy needs the cluster's OIDC
# provider, and feeding the resulting ARN back into a module input is the kind of
# module-in/module-out wiring that only fails at graph time — which
# `terraform validate` does not exercise. Keeping the addon here makes the
# ordering obvious and provably acyclic.
# ---------------------------------------------------------------------------

data "aws_iam_policy_document" "ebs_csi_assume" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [module.this.oidc_provider_arn]
    }

    condition {
      test     = "StringEquals"
      variable = "${module.this.oidc_provider}:sub"
      values   = ["system:serviceaccount:${local.ebs_csi_namespace}:${local.ebs_csi_service_account}"]
    }

    condition {
      test     = "StringEquals"
      variable = "${module.this.oidc_provider}:aud"
      values   = ["sts.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "ebs_csi" {
  name               = "${var.name}-ebs-csi"
  description        = "IRSA role for the aws-ebs-csi-driver addon on ${var.name}"
  assume_role_policy = data.aws_iam_policy_document.ebs_csi_assume.json
  tags               = var.tags
}

resource "aws_iam_role_policy_attachment" "ebs_csi" {
  role       = aws_iam_role.ebs_csi.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonEBSCSIDriverPolicy"
}

resource "aws_eks_addon" "ebs_csi" {
  cluster_name = module.this.cluster_name
  addon_name   = "aws-ebs-csi-driver"

  addon_version = try(var.addon_versions["aws-ebs-csi-driver"], null)
  # `most_recent` has no equivalent on the bare resource; leaving addon_version
  # null lets EKS pick the default version for the cluster's Kubernetes version.

  service_account_role_arn = aws_iam_role.ebs_csi.arn

  resolve_conflicts_on_create = "NONE"
  resolve_conflicts_on_update = "OVERWRITE"
  # Keep the CSI driver (and therefore attached volumes) when the addon resource
  # is destroyed by a partial teardown.
  preserve = true

  tags = var.tags

  # Controllers need somewhere to run before the addon reports healthy.
  depends_on = [module.this]
}
