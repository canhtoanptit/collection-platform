# modules/snowflake-account — warehouses, databases/schemas and the credit cap.
#
# RBAC lives in rbac.tf, masking in governance.tf, the S3 integration in
# storage.tf. Nothing in this module loads or transforms data: RAW DDL is
# ANA-owned idempotent SQL (`data/snowflake/`) and models are dbt.

locals {
  warehouses = {
    WH_INGEST    = "COPY INTO loads driven by Airflow"
    WH_TRANSFORM = "dbt runs (dbt_svc and CI)"
    WH_ANALYTICS = "reporting and ad-hoc analysis"
  }

  resource_monitor_name = "${var.prefix}_MONTHLY"

  raw_db          = "RAW"
  analytics_db    = "ANALYTICS"
  analytics_ci_db = "ANALYTICS_CI"

  governance_schema = "GOVERNANCE"

  # `"DB"."SCHEMA"` — the fully qualified, quoted form the provider's `on_schema`
  # / `in_schema` arguments require.
  fq = {
    for k, v in merge(
      { for s in var.raw_schemas : "${local.raw_db}.${s}" => { db = local.raw_db, schema = s } },
      { for s in var.analytics_schemas : "${local.analytics_db}.${s}" => { db = local.analytics_db, schema = s } },
    ) : k => "\"${v.db}\".\"${v.schema}\""
  }
}

# ------------------------------------------------------------ resource monitor --
#
# Created before the warehouses so the cap exists the first time a credit is
# spent. 50 credits/mo with suspend at 100% is a deliberate
# availability-for-cost trade (ADR-0008).

resource "snowflake_resource_monitor" "monthly" {
  name         = local.resource_monitor_name
  credit_quota = var.credit_quota
  frequency    = "MONTHLY"
  # Required by Snowflake whenever `frequency` is set.
  start_timestamp = "IMMEDIATELY"

  notify_triggers = var.notify_triggers_percent
  suspend_trigger = var.suspend_trigger_percent
}

# ------------------------------------------------------------------ warehouses --

resource "snowflake_warehouse" "this" {
  for_each = local.warehouses

  name           = each.key
  comment        = each.value
  warehouse_size = var.warehouse_size

  auto_suspend        = var.warehouse_auto_suspend_seconds
  auto_resume         = "true"
  initially_suspended = true

  min_cluster_count = 1
  max_cluster_count = 1

  resource_monitor = snowflake_resource_monitor.monthly.name
}

# ------------------------------------------------------------------- databases --

resource "snowflake_database" "raw" {
  name                        = local.raw_db
  comment                     = "Source fidelity layer — VARIANT for CDC/webhooks/events, all-VARCHAR for file feeds (A§43)"
  data_retention_time_in_days = var.data_retention_days
}

resource "snowflake_database" "analytics" {
  name                        = local.analytics_db
  comment                     = "dbt-managed staging -> intermediate -> marts, plus SNAPSHOTS and GOVERNANCE"
  data_retention_time_in_days = var.data_retention_days
}

resource "snowflake_database" "analytics_ci" {
  name                        = local.analytics_ci_db
  comment                     = "Throwaway target for dbt CI runs; schemas are created and dropped per run"
  data_retention_time_in_days = 0
  is_transient                = true
}

resource "snowflake_schema" "raw" {
  for_each = toset(var.raw_schemas)

  database = snowflake_database.raw.name
  name     = each.value
  comment  = "RAW.${each.value}"

  data_retention_time_in_days = var.data_retention_days
}

resource "snowflake_schema" "analytics" {
  for_each = toset(var.analytics_schemas)

  database = snowflake_database.analytics.name
  name     = each.value
  comment  = "ANALYTICS.${each.value}"

  data_retention_time_in_days = var.data_retention_days
}
