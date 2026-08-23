terraform {
  required_version = ">= 1.11"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }

  # Partial backend configuration: `key` is per stack and lives here, everything else (bucket,
  # region, encrypt, kms_key_id, use_lockfile) comes from envs/<env>/backend.hcl. See
  # infrastructure/terraform/envs/dev/README.md for the key convention.
  backend "s3" {
    key = "stacks/10-network.tfstate"
  }
}

provider "aws" {
  region = var.region

  default_tags {
    tags = {
      project      = var.project
      env          = var.env
      stack        = "10-network"
      "managed-by" = "terraform"
    }
  }
}
