terraform {
  required_version = ">= 1.11"

  required_providers {
    snowflake = {
      source  = "snowflakedb/snowflake"
      version = "~> 2.20"
    }

    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.61"
    }
  }

  # Partial backend configuration: `key` is per stack and lives here, everything
  # else (bucket, region, encrypt, kms_key_id, use_lockfile) comes from
  # envs/<env>/backend.hcl. Same convention as stacks 00/10/20.
  backend "s3" {
    key = "stacks/40-snowflake.tfstate"
  }
}

provider "aws" {
  region = var.region

  default_tags {
    tags = {
      project      = var.project
      env          = var.env
      stack        = "40-snowflake"
      "managed-by" = "terraform"
    }
  }
}

provider "snowflake" {
  organization_name = var.snowflake_organization_name
  account_name      = var.snowflake_account_name

  # Key-pair auth for Terraform too. The private key is injected as
  # TF_VAR_snowflake_private_key from the `dev` GitHub environment secret;
  # `authenticator` must be set explicitly or the provider tries password auth.
  user          = var.snowflake_terraform_user
  authenticator = "SNOWFLAKE_JWT"
  private_key   = var.snowflake_private_key
  role          = var.snowflake_terraform_role
}
