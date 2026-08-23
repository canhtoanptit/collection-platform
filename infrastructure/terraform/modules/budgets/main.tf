# Cost guard rails and the account's single alerting sink.
#
# The SNS topic created here is not only for cost: FND-9's Alertmanager receiver and FND-13's
# teardown tooling publish to the same topic, so there is exactly one place to subscribe and one
# place to check when "we got no alert" turns out to be true.
#
# Cost Explorer and Budgets are global services reached through their us-east-1 endpoints. The AWS
# SDK routes them there regardless of the provider's configured region, so no aliased provider is
# needed and the resources below live in the same stack as everything else.

data "aws_caller_identity" "current" {}

locals {
  account_id = data.aws_caller_identity.current.account_id
  topic_name = "${var.name_prefix}-alerts"
}

resource "aws_sns_topic" "alerts" {
  name              = local.topic_name
  display_name      = "${var.name_prefix} alerts"
  kms_master_key_id = var.sns_kms_key_arn

  tags = {
    Name = local.topic_name
  }
}

data "aws_iam_policy_document" "alerts" {
  # Preserve the default owner grant. Replacing a topic policy discards it, and while same-account
  # IAM policies still work without it, tooling that reads the policy (and any future cross-account
  # subscriber) expects the owner statement to be there.
  statement {
    sid    = "AllowAccountOwnerFullAccess"
    effect = "Allow"

    principals {
      type        = "AWS"
      identifiers = ["*"]
    }

    actions = [
      "SNS:Publish",
      "SNS:Subscribe",
      "SNS:GetTopicAttributes",
      "SNS:SetTopicAttributes",
      "SNS:AddPermission",
      "SNS:RemovePermission",
      "SNS:DeleteTopic",
      "SNS:ListSubscriptionsByTopic",
      "SNS:Receive",
    ]

    resources = [aws_sns_topic.alerts.arn]

    condition {
      test     = "StringEquals"
      variable = "AWS:SourceOwner"
      values   = [local.account_id]
    }
  }

  # AWS Budgets publishes as a service principal, so without this statement the budget is created
  # successfully and then never delivers anything.
  statement {
    sid    = "AllowBudgetsToPublish"
    effect = "Allow"

    principals {
      type        = "Service"
      identifiers = ["budgets.amazonaws.com"]
    }

    actions   = ["SNS:Publish"]
    resources = [aws_sns_topic.alerts.arn]

    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [local.account_id]
    }
  }

  # Same for Cost Anomaly Detection, which publishes as costalerts.amazonaws.com.
  statement {
    sid    = "AllowCostAnomalyDetectionToPublish"
    effect = "Allow"

    principals {
      type        = "Service"
      identifiers = ["costalerts.amazonaws.com"]
    }

    actions   = ["SNS:Publish"]
    resources = [aws_sns_topic.alerts.arn]

    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [local.account_id]
    }
  }
}

resource "aws_sns_topic_policy" "alerts" {
  arn    = aws_sns_topic.alerts.arn
  policy = data.aws_iam_policy_document.alerts.json
}

# Terraform creates the subscription in `PendingConfirmation`. AWS emails a confirmation link that
# a human must click; until then nothing is delivered and `terraform plan` stays clean, which is
# exactly the failure mode the bootstrap README calls out as a manual step.
resource "aws_sns_topic_subscription" "alert_email" {
  topic_arn = aws_sns_topic.alerts.arn
  protocol  = "email"
  endpoint  = var.alert_email
}

resource "aws_budgets_budget" "monthly" {
  name         = "${var.name_prefix}-monthly"
  budget_type  = "COST"
  limit_amount = tostring(var.monthly_budget_usd)
  limit_unit   = "USD"
  time_unit    = "MONTHLY"

  cost_types {
    include_credit = var.include_credits_in_budget
    include_refund = var.include_credits_in_budget
    include_tax    = true
  }

  dynamic "notification" {
    for_each = var.budget_actual_thresholds_percent

    content {
      comparison_operator        = "GREATER_THAN"
      notification_type          = "ACTUAL"
      threshold                  = notification.value
      threshold_type             = "PERCENTAGE"
      subscriber_sns_topic_arns  = [aws_sns_topic.alerts.arn]
      subscriber_email_addresses = []
    }
  }

  # The forecast notification is the only one that arrives in time to do something about it; the
  # 100% ACTUAL alert is a receipt, not a warning.
  notification {
    comparison_operator        = "GREATER_THAN"
    notification_type          = "FORECASTED"
    threshold                  = var.budget_forecast_threshold_percent
    threshold_type             = "PERCENTAGE"
    subscriber_sns_topic_arns  = [aws_sns_topic.alerts.arn]
    subscriber_email_addresses = []
  }

  depends_on = [aws_sns_topic_policy.alerts]
}

# A fixed budget cannot see "MSK tripled while EKS was torn down". Anomaly detection is per
# service and free, which makes it the better of the two signals for this environment.
resource "aws_ce_anomaly_monitor" "service" {
  count = var.enable_cost_anomaly_detection ? 1 : 0

  name              = "${var.name_prefix}-service-monitor"
  monitor_type      = "DIMENSIONAL"
  monitor_dimension = "SERVICE"
}

resource "aws_ce_anomaly_subscription" "alerts" {
  count = var.enable_cost_anomaly_detection ? 1 : 0

  name = "${var.name_prefix}-anomaly-alerts"
  # SNS subscribers require IMMEDIATE; DAILY and WEEKLY are only valid for EMAIL subscribers.
  frequency        = "IMMEDIATE"
  monitor_arn_list = [aws_ce_anomaly_monitor.service[0].arn]

  subscriber {
    type    = "SNS"
    address = aws_sns_topic.alerts.arn
  }

  threshold_expression {
    dimension {
      key           = "ANOMALY_TOTAL_IMPACT_ABSOLUTE"
      match_options = ["GREATER_THAN_OR_EQUAL"]
      values        = [tostring(var.anomaly_threshold_usd)]
    }
  }

  depends_on = [aws_sns_topic_policy.alerts]
}
