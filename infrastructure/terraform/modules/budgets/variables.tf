variable "name_prefix" {
  description = "Prefix for the topic, budget and anomaly-monitor names, e.g. \"colx-dev\" produces the SNS topic colx-dev-alerts."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,30}$", var.name_prefix))
    error_message = "name_prefix must be lowercase alphanumeric with hyphens, 2-31 characters."
  }
}

variable "alert_email" {
  description = <<-EOT
    Email address that receives budget, anomaly and (from FND-9) Alertmanager notifications.
    No default on purpose: this is a user-supplied value, and an alerting channel that defaults to
    someone else's inbox is worse than no alerting. AWS sends a confirmation email that must be
    clicked before any notification is delivered -- Terraform cannot confirm it.
  EOT
  type        = string

  validation {
    condition     = can(regex("^[^@[:space:]]+@[^@[:space:]]+\\.[A-Za-z]{2,}$", var.alert_email))
    error_message = "alert_email must be a single email address."
  }
}

variable "monthly_budget_usd" {
  description = "Monthly cost budget in USD. The plan's cost model is $540-575/mo with everything running, so $450 is deliberately below the all-on figure: it should fire during a month where nothing was ever stopped."
  type        = number
  default     = 450

  validation {
    condition     = var.monthly_budget_usd > 0
    error_message = "monthly_budget_usd must be positive."
  }
}

variable "budget_actual_thresholds_percent" {
  description = "Percentages of the budget at which an ACTUAL-spend notification fires."
  type        = list(number)
  default     = [50, 80, 100]

  validation {
    condition     = length(var.budget_actual_thresholds_percent) > 0
    error_message = "At least one actual-spend threshold is required."
  }

  validation {
    condition     = alltrue([for t in var.budget_actual_thresholds_percent : t > 0 && t <= 1000])
    error_message = "Each threshold must be between 1 and 1000 percent."
  }
}

variable "budget_forecast_threshold_percent" {
  description = "Percentage of the budget at which a FORECASTED-spend notification fires. This is the only alert that arrives early enough to act on; the 100% actual alert arrives after the money is spent."
  type        = number
  default     = 100

  validation {
    condition     = var.budget_forecast_threshold_percent > 0
    error_message = "budget_forecast_threshold_percent must be positive."
  }
}

variable "enable_cost_anomaly_detection" {
  description = "Create the Cost Anomaly Detection monitor and subscription. Free; catches the class of cost incident a fixed budget misses (one service tripling while the total stays under budget)."
  type        = bool
  default     = true
}

variable "anomaly_threshold_usd" {
  description = "Absolute dollar impact at or above which an anomaly is notified. Low enough to catch a single forgotten NAT gateway or an accidental Serverless MSK cluster."
  type        = number
  default     = 25

  validation {
    condition     = var.anomaly_threshold_usd > 0
    error_message = "anomaly_threshold_usd must be positive."
  }
}

variable "sns_kms_key_arn" {
  description = <<-EOT
    Optional CMK for SNS server-side encryption. Leave null in dev.

    Do NOT set this to `alias/aws/sns`: the AWS-managed key's policy cannot be edited, so the
    `budgets.amazonaws.com` and `costalerts.amazonaws.com` service principals are unable to
    generate a data key and every notification is silently dropped. Encrypting this topic requires
    a customer-managed key whose policy grants those principals `kms:GenerateDataKey*` and
    `kms:Decrypt` -- see the prod deltas in the README.
  EOT
  type        = string
  default     = null
}

variable "include_credits_in_budget" {
  description = "Count credits and refunds against the budget. False means the budget tracks gross spend, so promotional credits (or a Snowflake trial) cannot hide a runaway resource."
  type        = bool
  default     = false
}
