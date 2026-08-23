# GitHub Actions -> AWS via OIDC federation. Four roles, no access keys anywhere
# (ADR-0010: `gh secret list | grep -c AWS_SECRET` == 0 is an acceptance check).
#
# The security property that matters is in the trust policies, not the permission policies:
# `sub` is pinned to this repository, and the two roles that can change anything are pinned to
# `environment:<github_environment>`. A GitHub environment with a required reviewer is therefore
# the human approval gate, enforced by AWS rather than by workflow YAML that a PR could edit.

data "aws_caller_identity" "current" {}

locals {
  oidc_issuer = "token.actions.githubusercontent.com"

  oidc_provider_arn = var.create_oidc_provider ? aws_iam_openid_connect_provider.github[0].arn : var.existing_oidc_provider_arn

  account_id = data.aws_caller_identity.current.account_id
  repo       = var.github_repository

  # Subject claims, by workflow shape:
  #   pull_request                      -> a plan posted as a PR comment
  #   ref:refs/heads/<default_branch>   -> post-merge plan and image push
  #   environment:<github_environment>  -> the gated apply and the gated helmfile deploy
  subject_pull_request = "repo:${local.repo}:pull_request"
  subject_default_ref  = "repo:${local.repo}:ref:refs/heads/${var.default_branch}"
  subject_environment  = "repo:${local.repo}:environment:${var.github_environment}"

  roles = {
    plan = {
      name        = "${var.name_prefix}-gha-plan"
      description = "Terraform plan: read-only account access plus state-bucket read/write for the lock file."
      subjects    = [local.subject_pull_request, local.subject_default_ref]
      # ReadOnlyAccess covers describe/list/get across every service. It deliberately does NOT
      # include kms:Decrypt or s3:PutObject, both of which a plan against encrypted remote state
      # needs, so the inline policy below supplies exactly those.
      managed_policy_arns = ["arn:aws:iam::aws:policy/ReadOnlyAccess"]
      inline_policy       = data.aws_iam_policy_document.state_access.json
    }

    apply = {
      name                = "${var.name_prefix}-gha-apply"
      description         = "Terraform apply. Environment-gated: only a run in the '${var.github_environment}' GitHub environment can assume it."
      subjects            = [local.subject_environment]
      managed_policy_arns = var.apply_role_policy_arns
      inline_policy       = null
    }

    ecr-push = {
      name                = "${var.name_prefix}-gha-ecr-push"
      description         = "Push container images to the ${var.ecr_repository_prefix}/* ECR namespace."
      subjects            = [local.subject_default_ref]
      managed_policy_arns = []
      inline_policy       = data.aws_iam_policy_document.ecr_push.json
    }

    eks-deploy = {
      name                = "${var.name_prefix}-gha-eks-deploy"
      description         = "Helmfile deploys. IAM side is describe-only; cluster authorization comes from an EKS access entry created by stacks/30-eks."
      subjects            = [local.subject_environment]
      managed_policy_arns = []
      inline_policy       = data.aws_iam_policy_document.eks_deploy.json
    }
  }

  # Flattened for a single for_each over role/policy pairs.
  managed_policy_attachments = merge([
    for key, role in local.roles : {
      for arn in role.managed_policy_arns : "${key}|${arn}" => {
        role_key   = key
        policy_arn = arn
      }
    }
  ]...)

  inline_policies = { for key, role in local.roles : key => role.inline_policy if role.inline_policy != null }
}

resource "aws_iam_openid_connect_provider" "github" {
  count = var.create_oidc_provider ? 1 : 0

  url             = "https://${local.oidc_issuer}"
  client_id_list  = ["sts.amazonaws.com"]
  thumbprint_list = length(var.oidc_thumbprints) > 0 ? var.oidc_thumbprints : null

  tags = {
    Name = "github-actions-oidc"
  }
}

data "aws_iam_policy_document" "assume_role" {
  for_each = local.roles

  statement {
    sid     = "GitHubActionsWebIdentity"
    effect  = "Allow"
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [local.oidc_provider_arn]
    }

    # Both conditions are load-bearing. Without the `aud` check the role trusts any audience;
    # without the `sub` check it trusts every repository on GitHub.
    condition {
      test     = "StringEquals"
      variable = "${local.oidc_issuer}:aud"
      values   = ["sts.amazonaws.com"]
    }

    condition {
      test     = "StringLike"
      variable = "${local.oidc_issuer}:sub"
      values   = each.value.subjects
    }
  }
}

resource "aws_iam_role" "this" {
  for_each = local.roles

  name                 = each.value.name
  description          = each.value.description
  assume_role_policy   = data.aws_iam_policy_document.assume_role[each.key].json
  max_session_duration = var.max_session_duration

  tags = {
    Name = each.value.name
  }
}

resource "aws_iam_role_policy_attachment" "managed" {
  for_each = local.managed_policy_attachments

  role       = aws_iam_role.this[each.value.role_key].name
  policy_arn = each.value.policy_arn
}

resource "aws_iam_role_policy" "inline" {
  for_each = local.inline_policies

  name   = "${local.roles[each.key].name}-inline"
  role   = aws_iam_role.this[each.key].id
  policy = each.value
}

# --- inline policy documents ------------------------------------------------------------------

# S3-native state locking writes and deletes a `<key>.tflock` object beside the state file, so
# even a plan-only role needs PutObject and DeleteObject on the state prefix. This is the whole
# reason `colx-gha-plan` is not literally read-only.
data "aws_iam_policy_document" "state_access" {
  statement {
    sid    = "ListStateBucket"
    effect = "Allow"
    actions = [
      "s3:ListBucket",
      "s3:GetBucketVersioning",
      "s3:GetBucketLocation",
    ]
    resources = ["arn:aws:s3:::${var.state_bucket_name}"]
  }

  statement {
    sid    = "ReadWriteStateObjectsAndLocks"
    effect = "Allow"
    actions = [
      "s3:GetObject",
      "s3:GetObjectVersion",
      "s3:PutObject",
      "s3:DeleteObject",
    ]
    resources = ["arn:aws:s3:::${var.state_bucket_name}/*"]
  }

  dynamic "statement" {
    for_each = var.state_kms_key_arn == null ? [] : [var.state_kms_key_arn]

    content {
      sid    = "UseStateEncryptionKey"
      effect = "Allow"
      actions = [
        "kms:Decrypt",
        "kms:GenerateDataKey",
        "kms:DescribeKey",
      ]
      resources = [statement.value]
    }
  }
}

data "aws_iam_policy_document" "ecr_push" {
  # GetAuthorizationToken has no resource scope in ECR's authorization model: it is either
  # allowed on "*" or the docker login cannot happen at all.
  statement {
    sid       = "EcrLogin"
    effect    = "Allow"
    actions   = ["ecr:GetAuthorizationToken"]
    resources = ["*"]
  }

  statement {
    sid    = "PushAndPullColxImages"
    effect = "Allow"
    actions = [
      "ecr:BatchCheckLayerAvailability",
      "ecr:BatchGetImage",
      "ecr:CompleteLayerUpload",
      "ecr:DescribeImages",
      "ecr:DescribeRepositories",
      "ecr:GetDownloadUrlForLayer",
      "ecr:InitiateLayerUpload",
      "ecr:ListImages",
      "ecr:PutImage",
      "ecr:UploadLayerPart",
    ]
    resources = ["arn:aws:ecr:${var.region}:${local.account_id}:repository/${var.ecr_repository_prefix}/*"]
  }
}

data "aws_iam_policy_document" "eks_deploy" {
  # Deliberately describe-only. `aws eks update-kubeconfig` needs DescribeCluster; everything the
  # deploy actually does inside the cluster is authorized by the EKS access entry that
  # stacks/30-eks creates for this role, not by IAM. Widening this policy would not widen
  # cluster access, and narrowing the access entry is how cluster permissions get reduced.
  statement {
    sid    = "DescribeClustersForKubeconfig"
    effect = "Allow"
    actions = [
      "eks:DescribeCluster",
      "eks:ListClusters",
      "eks:DescribeNodegroup",
      "eks:ListNodegroups",
      "eks:DescribeAddon",
      "eks:ListAddons",
      "eks:DescribeAccessEntry",
      "eks:ListAccessEntries",
    ]
    resources = ["*"]
  }
}
