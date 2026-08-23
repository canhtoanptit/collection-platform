terraform {
  required_version = ">= 1.11"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }

  # ---------------------------------------------------------------------------------------------
  # THIS STACK BOOTSTRAPS ITS OWN BACKEND, SO IT STARTS WITH LOCAL STATE.
  #
  # First apply: leave the block below commented out. Terraform writes terraform.tfstate next to
  # this file, creating the state bucket and its CMK.
  #
  # Second step: uncomment the block, then run
  #     terraform init -backend-config=../../envs/dev/backend.hcl -migrate-state
  # and answer "yes". Terraform copies the local state into the bucket it just created.
  #
  # After migration, delete the local terraform.tfstate* files -- .gitignore already refuses to
  # commit them, but a stale local state file is the most likely way this stack gets applied twice
  # against different truths.
  #
  # `key` is set here rather than in backend.hcl because it is per stack; backend.hcl carries only
  # the values every stack shares (bucket, region, use_lockfile, encrypt).
  # ---------------------------------------------------------------------------------------------
  # backend "s3" {
  #   key = "stacks/00-bootstrap.tfstate"
  # }
}

provider "aws" {
  region = var.region

  default_tags {
    tags = {
      project      = var.project
      env          = var.env
      stack        = "00-bootstrap"
      "managed-by" = "terraform"
    }
  }
}
