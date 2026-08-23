terraform {
  # S3-native state locking (`use_lockfile`) needs >= 1.11 — ADR-0010.
  required_version = ">= 1.11"

  required_providers {
    aws = {
      source = "hashicorp/aws"
      # terraform-aws-modules/eks/aws v21.25.0 requires >= 6.59.
      version = "~> 6.61"
    }
  }
}
