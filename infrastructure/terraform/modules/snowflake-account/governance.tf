# Dynamic masking (A§69, D§45).
#
# Terraform owns the POLICY objects; dbt post-hooks own the column APPLICATIONS.
# That split is deliberate: policies must survive a `dbt run --full-refresh` that
# recreates the table, and a policy that only exists because a model ran is not a
# control.
#
# The verifiable assertion these exist for: the same query run as COLX_REPORTER
# returns `***MASKED***` and as COLX_PII_READER returns the value.

locals {
  pii_reader_role = local.role_names.pii_reader

  governance_fq = "\"${local.analytics_db}\".\"${local.governance_schema}\""
}

resource "snowflake_masking_policy" "string_pii" {
  database = snowflake_database.analytics.name
  schema   = local.governance_schema
  name     = "MASK_STRING_PII"

  argument {
    name = "VAL"
    type = "STRING"
  }

  return_data_type = "VARCHAR"

  # IS_ROLE_IN_SESSION (not CURRENT_ROLE) so the check honours the role
  # hierarchy: a session using a role that *inherits* COLX_PII_READER unmasks,
  # which is how a human analyst gets access without being granted the role
  # directly.
  body = "CASE WHEN IS_ROLE_IN_SESSION('${local.pii_reader_role}') THEN VAL ELSE '${var.masked_string_placeholder}' END"

  comment = "Names, emails, phones, addresses. Non-privileged roles see ${var.masked_string_placeholder}"

  depends_on = [snowflake_schema.analytics]
}

resource "snowflake_masking_policy" "date_pii" {
  database = snowflake_database.analytics.name
  schema   = local.governance_schema
  name     = "MASK_DATE_PII"

  argument {
    name = "VAL"
    type = "DATE"
  }

  return_data_type = "DATE"

  # Truncating to the year keeps age-band analysis working while removing the
  # identifying precision of a date of birth. Returning NULL instead would break
  # every downstream NOT NULL test for no privacy gain.
  body = "CASE WHEN IS_ROLE_IN_SESSION('${local.pii_reader_role}') THEN VAL ELSE DATE_TRUNC('YEAR', VAL) END"

  comment = "Dates of birth and similar. Non-privileged roles see the year only"

  depends_on = [snowflake_schema.analytics]
}

# The transformer applies the policies from dbt post-hooks, so it needs to
# reference them (APPLY on the policy object) and see the schema they live in.
resource "snowflake_grant_privileges_to_account_role" "transformer_governance_usage" {
  account_role_name = snowflake_account_role.this["transformer"].name
  privileges        = ["USAGE"]

  on_schema {
    schema_name = local.governance_fq
  }

  depends_on = [snowflake_schema.analytics]
}

resource "snowflake_grant_privileges_to_account_role" "transformer_apply_masking" {
  for_each = {
    string = snowflake_masking_policy.string_pii.fully_qualified_name
    date   = snowflake_masking_policy.date_pii.fully_qualified_name
  }

  account_role_name = snowflake_account_role.this["transformer"].name
  privileges        = ["APPLY"]

  on_schema_object {
    object_type = "MASKING POLICY"
    object_name = each.value
  }
}
