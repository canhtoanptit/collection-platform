# RBAC (plan FND-11, D§45, A§69).
#
# Four functional roles plus an admin role, and the grants are *future* grants
# wherever objects are created by something other than Terraform (dbt creates
# every table in ANALYTICS; loads create tables in RAW). Future grants are what
# make "REPORTER can read every mart" true after tomorrow's `dbt run` as well as
# today's.
#
# The one rule to keep in mind while reading: COLX_REPORTER has NO grant on RAW at
# all. Not SELECT, not USAGE. Reporting reads marts; PII protection that can be
# routed around by querying RAW is not protection.

locals {
  role_names = {
    admin       = "${var.prefix}_ADMIN"
    loader      = "${var.prefix}_LOADER"
    transformer = "${var.prefix}_TRANSFORMER"
    reporter    = "${var.prefix}_REPORTER"
    pii_reader  = "${var.prefix}_${var.pii_reader_role_suffix}"
  }

  role_comments = {
    admin       = "Owns the COLX objects; granted to SYSADMIN so account admins inherit it"
    loader      = "Airflow: COPY INTO on RAW, stage usage on the S3 integration"
    transformer = "dbt: read RAW, own ANALYTICS and ANALYTICS_CI"
    reporter    = "Read the marts. Deliberately has no grant of any kind on RAW (D§45)"
    pii_reader  = "Unmasks PII via IS_ROLE_IN_SESSION. Granted to no user by default (A§69)"
  }

  # user -> (role key, default warehouse)
  service_users = {
    AIRFLOW_SVC = { role = "loader", warehouse = "WH_INGEST", comment = "Airflow: triggers COPY INTO and reconciliation queries" }
    DBT_SVC     = { role = "transformer", warehouse = "WH_TRANSFORM", comment = "dbt Core, invoked by Airflow" }
    DBT_CI_SVC  = { role = "transformer", warehouse = "WH_TRANSFORM", comment = "dbt slim CI; targets ANALYTICS_CI" }
  }

  raw_schema_keys = [for s in var.raw_schemas : "${local.raw_db}.${s}"]
}

resource "snowflake_account_role" "this" {
  for_each = local.role_names

  name    = each.value
  comment = local.role_comments[each.key]
}

# COLX_ADMIN -> SYSADMIN, so the standard account hierarchy can see and manage
# everything this module creates without anyone using ACCOUNTADMIN day to day.
resource "snowflake_grant_account_role" "admin_to_sysadmin" {
  role_name        = snowflake_account_role.this["admin"].name
  parent_role_name = "SYSADMIN"
}

# Functional roles roll up into COLX_ADMIN.
resource "snowflake_grant_account_role" "functional_to_admin" {
  for_each = toset(["loader", "transformer", "reporter"])

  role_name        = snowflake_account_role.this[each.value].name
  parent_role_name = snowflake_account_role.this["admin"].name
}

# PII_READER *inherits* REPORTER (not the other way round): a session that can
# unmask can also read the marts, but holding REPORTER never grants unmasking.
# Getting this direction wrong silently unmasks PII for every reporting user.
resource "snowflake_grant_account_role" "reporter_to_pii_reader" {
  role_name        = snowflake_account_role.this["reporter"].name
  parent_role_name = snowflake_account_role.this["pii_reader"].name
}

# ---------------------------------------------------------------- warehouses --

locals {
  warehouse_grants = {
    "loader:WH_INGEST"         = { role = "loader", warehouse = "WH_INGEST" }
    "transformer:WH_TRANSFORM" = { role = "transformer", warehouse = "WH_TRANSFORM" }
    "reporter:WH_ANALYTICS"    = { role = "reporter", warehouse = "WH_ANALYTICS" }
    "transformer:WH_ANALYTICS" = { role = "transformer", warehouse = "WH_ANALYTICS" }
  }
}

resource "snowflake_grant_privileges_to_account_role" "warehouse_usage" {
  for_each = local.warehouse_grants

  account_role_name = snowflake_account_role.this[each.value.role].name
  privileges        = ["USAGE", "OPERATE", "MONITOR"]

  on_account_object {
    object_type = "WAREHOUSE"
    object_name = snowflake_warehouse.this[each.value.warehouse].name
  }
}

# ------------------------------------------------------------- database usage --

locals {
  database_usage = {
    "loader:RAW"               = { role = "loader", database = local.raw_db }
    "transformer:RAW"          = { role = "transformer", database = local.raw_db }
    "transformer:ANALYTICS"    = { role = "transformer", database = local.analytics_db }
    "transformer:ANALYTICS_CI" = { role = "transformer", database = local.analytics_ci_db }
    "reporter:ANALYTICS"       = { role = "reporter", database = local.analytics_db }
  }
}

resource "snowflake_grant_privileges_to_account_role" "database_usage" {
  for_each = local.database_usage

  account_role_name = snowflake_account_role.this[each.value.role].name
  privileges        = ["USAGE"]

  on_account_object {
    object_type = "DATABASE"
    object_name = each.value.database
  }

  depends_on = [
    snowflake_database.raw,
    snowflake_database.analytics,
    snowflake_database.analytics_ci,
  ]
}

# dbt creates and drops schemas in both ANALYTICS and ANALYTICS_CI.
resource "snowflake_grant_privileges_to_account_role" "transformer_create_schema" {
  for_each = toset([local.analytics_db, local.analytics_ci_db])

  account_role_name = snowflake_account_role.this["transformer"].name
  privileges        = ["CREATE SCHEMA"]

  on_account_object {
    object_type = "DATABASE"
    object_name = each.value
  }

  depends_on = [snowflake_database.analytics, snowflake_database.analytics_ci]
}

# --------------------------------------------------------------- RAW: loading --

# The loader creates the tables/stages/file formats that COPY INTO writes into.
resource "snowflake_grant_privileges_to_account_role" "loader_raw_schema" {
  for_each = toset(local.raw_schema_keys)

  account_role_name = snowflake_account_role.this["loader"].name
  privileges        = ["USAGE", "CREATE TABLE", "CREATE VIEW", "CREATE STAGE", "CREATE FILE FORMAT"]

  on_schema {
    schema_name = local.fq[each.value]
  }

  depends_on = [snowflake_schema.raw]
}

resource "snowflake_grant_privileges_to_account_role" "loader_raw_future_tables" {
  account_role_name = snowflake_account_role.this["loader"].name
  privileges        = ["SELECT", "INSERT", "TRUNCATE"]

  on_schema_object {
    future {
      object_type_plural = "TABLES"
      in_database        = local.raw_db
    }
  }

  depends_on = [snowflake_database.raw]
}

resource "snowflake_grant_privileges_to_account_role" "loader_raw_all_tables" {
  account_role_name = snowflake_account_role.this["loader"].name
  privileges        = ["SELECT", "INSERT", "TRUNCATE"]

  on_schema_object {
    all {
      object_type_plural = "TABLES"
      in_database        = local.raw_db
    }
  }

  # `all` grants are a point-in-time snapshot; re-running the apply is what keeps
  # them current, which is why `always_apply` is on here and nowhere else.
  always_apply = true

  depends_on = [snowflake_schema.raw]
}

# ------------------------------------------------------- RAW: transformer read --

resource "snowflake_grant_privileges_to_account_role" "transformer_raw_schema_usage" {
  account_role_name = snowflake_account_role.this["transformer"].name
  privileges        = ["USAGE"]

  on_schema {
    all_schemas_in_database = local.raw_db
  }

  depends_on = [snowflake_schema.raw]
}

resource "snowflake_grant_privileges_to_account_role" "transformer_raw_future_schema_usage" {
  account_role_name = snowflake_account_role.this["transformer"].name
  privileges        = ["USAGE"]

  on_schema {
    future_schemas_in_database = local.raw_db
  }

  depends_on = [snowflake_database.raw]
}

resource "snowflake_grant_privileges_to_account_role" "transformer_raw_future_read" {
  for_each = toset(["TABLES", "VIEWS"])

  account_role_name = snowflake_account_role.this["transformer"].name
  privileges        = ["SELECT"]

  on_schema_object {
    future {
      object_type_plural = each.value
      in_database        = local.raw_db
    }
  }

  depends_on = [snowflake_database.raw]
}

resource "snowflake_grant_privileges_to_account_role" "transformer_raw_all_read" {
  for_each = toset(["TABLES", "VIEWS"])

  account_role_name = snowflake_account_role.this["transformer"].name
  privileges        = ["SELECT"]

  on_schema_object {
    all {
      object_type_plural = each.value
      in_database        = local.raw_db
    }
  }

  always_apply = true

  depends_on = [snowflake_schema.raw]
}

# ------------------------------------------------- ANALYTICS: transformer owns --

resource "snowflake_grant_privileges_to_account_role" "transformer_analytics_schemas" {
  for_each = toset([local.analytics_db, local.analytics_ci_db])

  account_role_name = snowflake_account_role.this["transformer"].name
  privileges = [
    "USAGE",
    "CREATE TABLE",
    "CREATE VIEW",
    "CREATE MATERIALIZED VIEW",
    "CREATE STAGE",
    "CREATE FILE FORMAT",
    "CREATE FUNCTION",
    "CREATE PROCEDURE",
    "CREATE DYNAMIC TABLE",
    "CREATE TASK",
  ]

  on_schema {
    all_schemas_in_database = each.value
  }

  depends_on = [snowflake_schema.analytics, snowflake_database.analytics_ci]
}

resource "snowflake_grant_privileges_to_account_role" "transformer_analytics_future_schemas" {
  for_each = toset([local.analytics_db, local.analytics_ci_db])

  account_role_name = snowflake_account_role.this["transformer"].name
  privileges = [
    "USAGE",
    "CREATE TABLE",
    "CREATE VIEW",
    "CREATE MATERIALIZED VIEW",
    "CREATE STAGE",
    "CREATE FILE FORMAT",
    "CREATE FUNCTION",
    "CREATE PROCEDURE",
    "CREATE DYNAMIC TABLE",
    "CREATE TASK",
  ]

  on_schema {
    future_schemas_in_database = each.value
  }

  depends_on = [snowflake_database.analytics, snowflake_database.analytics_ci]
}

# --------------------------------------------------------- ANALYTICS: reporter --
#
# MARTS and SNAPSHOTS only, and read-only. `future` grants mean tomorrow's model
# is readable without a Terraform change; the masking policies (governance.tf) are
# what stop the read from returning PII.

locals {
  reporter_schemas = [
    for s in ["MARTS", "SNAPSHOTS"] : "${local.analytics_db}.${s}"
    if contains(var.analytics_schemas, s)
  ]
}

resource "snowflake_grant_privileges_to_account_role" "reporter_schema_usage" {
  for_each = toset(local.reporter_schemas)

  account_role_name = snowflake_account_role.this["reporter"].name
  privileges        = ["USAGE"]

  on_schema {
    schema_name = local.fq[each.value]
  }

  depends_on = [snowflake_schema.analytics]
}

resource "snowflake_grant_privileges_to_account_role" "reporter_future_read" {
  for_each = {
    for pair in setproduct(local.reporter_schemas, ["TABLES", "VIEWS"]) :
    "${pair[0]}:${pair[1]}" => { schema = pair[0], plural = pair[1] }
  }

  account_role_name = snowflake_account_role.this["reporter"].name
  privileges        = ["SELECT"]

  on_schema_object {
    future {
      object_type_plural = each.value.plural
      in_schema          = local.fq[each.value.schema]
    }
  }

  depends_on = [snowflake_schema.analytics]
}

resource "snowflake_grant_privileges_to_account_role" "reporter_all_read" {
  for_each = {
    for pair in setproduct(local.reporter_schemas, ["TABLES", "VIEWS"]) :
    "${pair[0]}:${pair[1]}" => { schema = pair[0], plural = pair[1] }
  }

  account_role_name = snowflake_account_role.this["reporter"].name
  privileges        = ["SELECT"]

  on_schema_object {
    all {
      object_type_plural = each.value.plural
      in_schema          = local.fq[each.value.schema]
    }
  }

  always_apply = true

  depends_on = [snowflake_schema.analytics]
}

# --------------------------------------------------------------- service users --

resource "snowflake_service_user" "this" {
  for_each = local.service_users

  name         = each.key
  display_name = each.key
  comment      = each.value.comment
  email        = try(var.service_user_emails[each.key], null)

  # Key-pair auth only: a service user with a password is a password to rotate,
  # store and leak (ADR-0008). `snowflake_service_user` cannot have one at all.
  rsa_public_key = var.service_user_public_keys[each.key]

  default_role      = snowflake_account_role.this[each.value.role].name
  default_warehouse = snowflake_warehouse.this[each.value.warehouse].name

  # Secondary roles off: a session gets exactly the role it asked for, so a
  # QUERY_HISTORY row unambiguously names the privileges that ran it.
  default_secondary_roles_option = "NONE"
}

resource "snowflake_grant_account_role" "service_user_roles" {
  for_each = local.service_users

  role_name = snowflake_account_role.this[each.value.role].name
  user_name = snowflake_service_user.this[each.key].name
}
