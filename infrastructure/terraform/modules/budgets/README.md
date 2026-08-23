# module: budgets

The account's cost guard rails and its single alerting sink: SNS topic + email subscription, a
monthly AWS Budget with actual and forecast notifications, and a Cost Anomaly Detection monitor.

Used by `stacks/00-bootstrap` (FND-1).

## What it creates

| Resource | Name | Notes |
|---|---|---|
| SNS topic | `colx-dev-alerts` | Also the target for FND-9 Alertmanager and FND-13 teardown scripts |
| SNS subscription | `alert_email` | Created `PendingConfirmation`; a human must click the link |
| Budget | `colx-dev-monthly` | $450/mo, notifications at 50 / 80 / 100% ACTUAL + 100% FORECASTED |
| Anomaly monitor | `colx-dev-service-monitor` | `DIMENSIONAL` / `SERVICE` |
| Anomaly subscription | `colx-dev-anomaly-alerts` | `IMMEDIATE`, ≥ $25 absolute impact |

## Why $450 when the all-on estimate is $540-575

Deliberately below the everything-running figure (plan §2). The budget is not a spending
authorization, it is a detector for *"a month went by and nothing was ever stopped"*. A budget set
above the all-on cost can only fire after something has gone genuinely wrong.

The forecast notification is the only one that arrives in time to act on. The 100% ACTUAL alert is
a receipt.

## Two service principals, two silent failure modes

Both are guarded by explicit statements in the topic policy:

- `budgets.amazonaws.com` — without it, the budget is created successfully and never delivers.
- `costalerts.amazonaws.com` — same for Cost Anomaly Detection.

Both statements are conditioned on `aws:SourceAccount` so the topic cannot be used as a
notification relay by another account's budget.

## Encryption: why the topic is unencrypted in dev

`kms_master_key_id` defaults to `null`. The obvious fix — `alias/aws/sns` — is a trap: the
AWS-managed key's policy cannot be edited, so `budgets.amazonaws.com` and
`costalerts.amazonaws.com` cannot generate a data key and **every notification is silently
dropped**. Encrypting this topic correctly needs a customer-managed key whose policy grants those
two principals `kms:GenerateDataKey*` and `kms:Decrypt`. The plan enumerates four CMKs
(`colx-dev-{data,db,msk,secrets}`) and none of them is the right home for that policy, so dev ships
the topic unencrypted and carries the payload cost: alert notifications contain a dollar figure, a
service name and an alert name — no customer data.

## Cost Explorer is a global service

`aws_budgets_budget` and the `aws_ce_*` resources are reached through their us-east-1 endpoints
regardless of the provider's region. The SDK routes them there, so no aliased provider is needed
and these resources sit in the same stack as everything else. If a budget notification never
arrives despite the topic policy being correct, that endpoint behaviour — not the region of the SNS
topic — is the first thing to check.

## Usage

```hcl
module "budgets" {
  source             = "../../modules/budgets"
  name_prefix        = "colx-dev"
  alert_email        = var.alert_email
  monthly_budget_usd = 450
}
```

## Inputs

| Name | Type | Default | Description |
|---|---|---|---|
| `name_prefix` | `string` | — | e.g. `colx-dev`. |
| `alert_email` | `string` | — | **User-supplied**, no default. |
| `monthly_budget_usd` | `number` | `450` | |
| `budget_actual_thresholds_percent` | `list(number)` | `[50, 80, 100]` | |
| `budget_forecast_threshold_percent` | `number` | `100` | |
| `enable_cost_anomaly_detection` | `bool` | `true` | |
| `anomaly_threshold_usd` | `number` | `25` | Absolute impact. |
| `sns_kms_key_arn` | `string` | `null` | Read the variable description before setting. |
| `include_credits_in_budget` | `bool` | `false` | Gross spend, so credits cannot hide a runaway. |

## Outputs

`sns_topic_arn`, `sns_topic_name`, `email_subscription_arn`, `budget_name`, `budget_arn`,
`anomaly_monitor_arn`, `anomaly_subscription_arn`.

## Manual step after apply

Confirm the SNS email subscription. Until then `terraform plan` is clean, the console shows
`PendingConfirmation`, and no alert of any kind is delivered. FND-13's acceptance criterion
("budget alert test-fired and evidenced") is what proves the whole chain works.

## Prod deltas

- **Encrypt the topic** with a CMK whose key policy grants `budgets.amazonaws.com` and
  `costalerts.amazonaws.com` `kms:GenerateDataKey*` + `kms:Decrypt`, conditioned on
  `aws:SourceAccount`.
- **Replace email with a paged channel** (PagerDuty/Opsgenie via HTTPS subscription, or Chatbot into
  Slack) and split topics by severity, so a 50%-of-budget notice does not share a channel with a
  production page.
- **Budgets per dimension**, not one account-wide budget: per service (`SERVICE` cost filter), per
  environment tag, and an RI/Savings-Plan coverage budget. One number for a whole account tells you
  something is wrong but never what.
- **`BudgetAction`s** attached to the 100% threshold — apply a deny SCP or stop non-production
  instances automatically — so the budget is a control and not just a notification.
- **Anomaly monitors per linked account** in an Organization, plus a `CUSTOM` monitor on the cost
  categories that map to this platform, since a `SERVICE` monitor cannot distinguish two teams
  sharing EKS.
- **Cost and Usage Report to S3 + Athena** for attribution; budgets tell you the total moved,
  CUR tells you which resource moved it.
