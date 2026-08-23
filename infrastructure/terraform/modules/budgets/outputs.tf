output "sns_topic_arn" {
  description = "ARN of the alerts topic. FND-9's Alertmanager receiver and FND-13's teardown scripts publish to this same topic."
  value       = aws_sns_topic.alerts.arn
}

output "sns_topic_name" {
  description = "Name of the alerts topic (<name_prefix>-alerts)."
  value       = aws_sns_topic.alerts.name
}

output "email_subscription_arn" {
  description = "ARN of the email subscription. Note that a subscription in PendingConfirmation delivers nothing until the confirmation link in the AWS email is clicked."
  value       = aws_sns_topic_subscription.alert_email.arn
}

output "budget_name" {
  description = "Name of the monthly cost budget."
  value       = aws_budgets_budget.monthly.name
}

output "budget_arn" {
  description = "ARN of the monthly cost budget."
  value       = aws_budgets_budget.monthly.arn
}

output "anomaly_monitor_arn" {
  description = "ARN of the Cost Anomaly Detection monitor, or null when disabled."
  value       = try(aws_ce_anomaly_monitor.service[0].arn, null)
}

output "anomaly_subscription_arn" {
  description = "ARN of the Cost Anomaly Detection subscription, or null when disabled."
  value       = try(aws_ce_anomaly_subscription.alerts[0].arn, null)
}
