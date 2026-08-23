terraform {
  required_version = ">= 1.11"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.61"
    }
  }

  # Partial backend configuration: `key` is per stack and lives here, everything
  # else (bucket, region, encrypt, kms_key_id, use_lockfile) comes from
  # envs/<env>/backend.hcl. Same convention as stacks 00/10/20.
  #
  # `terraform init -backend=false` still works offline, which is what makes
  # fmt/validate credential-free (scripts/verify/INF-B.sh, and the `static` job in
  # .github/workflows/terraform.yml).
  backend "s3" {
    key = "stacks/30-eks.tfstate"
  }
}

provider "aws" {
  region = var.region

  default_tags {
    tags = {
      project      = var.project
      env          = var.env
      stack        = "30-eks"
      "managed-by" = "terraform"
    }
  }
}
