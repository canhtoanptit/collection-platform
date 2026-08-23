terraform {
  required_version = ">= 1.11"

  required_providers {
    # Note the source: `snowflakedb/snowflake` is the vendor-owned successor to
    # `Snowflake-Labs/snowflake`. Resource names in this module are the v2 names
    # (`snowflake_account_role`, `snowflake_service_user`,
    # `snowflake_storage_integration_aws`) — the v0.x names will not validate.
    snowflake = {
      source  = "snowflakedb/snowflake"
      version = "~> 2.20"
    }

    # Same-stack AWS role for the storage integration.
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.61"
    }
  }
}
