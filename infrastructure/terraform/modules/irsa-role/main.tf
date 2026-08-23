# modules/irsa-role — one IAM role per Kubernetes service account, driven by a map.
#
# Trust is pinned to the exact `system:serviceaccount:<ns>:<sa>` subject and the
# `sts.amazonaws.com` audience. A wildcard subject (`system:serviceaccount:*:*`)
# would let any pod in the cluster assume any role, which is the whole point of
# not using node-role permissions (ADR-0011).

locals {
  roles = {
    for name, cfg in var.roles : name => merge(cfg, {
      service_account = coalesce(cfg.service_account, name)
      role_name       = "${var.name_prefix}-${name}"
      description     = coalesce(cfg.description, "IRSA role for ${cfg.namespace}/${coalesce(cfg.service_account, name)}")
      subjects = [
        for sa in distinct(concat([coalesce(cfg.service_account, name)], cfg.extra_service_accounts)) :
        "system:serviceaccount:${cfg.namespace}:${sa}"
      ]
    })
  }

  # Flattened (role, managed policy ARN) pairs — for_each needs a stable, flat key.
  managed_attachments = merge([
    for name, cfg in local.roles : {
      for arn in cfg.managed_arns : "${name}:${arn}" => {
        role_name  = cfg.role_name
        policy_arn = arn
      }
    }
  ]...)
}

data "aws_iam_policy_document" "assume" {
  for_each = local.roles

  statement {
    sid     = "AllowIRSAAssume"
    effect  = "Allow"
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [var.oidc_provider_arn]
    }

    condition {
      test     = "StringEquals"
      variable = "${var.oidc_provider}:sub"
      values   = each.value.subjects
    }

    condition {
      test     = "StringEquals"
      variable = "${var.oidc_provider}:aud"
      values   = ["sts.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "this" {
  for_each = local.roles

  name                 = each.value.role_name
  description          = each.value.description
  assume_role_policy   = data.aws_iam_policy_document.assume[each.key].json
  max_session_duration = each.value.max_session_hours * 3600

  tags = merge(var.tags, {
    "colx.io/k8s-namespace"      = each.value.namespace
    "colx.io/k8s-serviceaccount" = each.value.service_account
  })
}

resource "aws_iam_role_policy" "this" {
  for_each = { for name, cfg in local.roles : name => cfg if cfg.policy_json != null }

  name   = "${each.value.role_name}-inline"
  role   = aws_iam_role.this[each.key].id
  policy = each.value.policy_json
}

resource "aws_iam_role_policy_attachment" "managed" {
  for_each = local.managed_attachments

  role       = each.value.role_name
  policy_arn = each.value.policy_arn

  depends_on = [aws_iam_role.this]
}
