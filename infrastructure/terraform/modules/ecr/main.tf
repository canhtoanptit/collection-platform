# ECR repositories for the platform's container images.
#
# One repository per image family rather than per deployable: every Go service ships from the
# same multi-arch `colx/services` repository tagged `<service>-<sha>`, because 15 near-identical
# repositories is 15 lifecycle policies to keep in sync for no isolation benefit.

locals {
  repositories = { for r in var.repositories : r => r }
}

resource "aws_ecr_repository" "this" {
  for_each = local.repositories

  name                 = each.value
  image_tag_mutability = var.image_tag_mutability
  force_delete         = var.force_delete

  image_scanning_configuration {
    scan_on_push = var.scan_on_push
  }

  encryption_configuration {
    encryption_type = var.kms_key_arn == null ? "AES256" : "KMS"
    kms_key         = var.kms_key_arn
  }

  tags = {
    Name = each.value
  }
}

# Keep the last N images and drop untagged layers quickly. Without this, ECR storage grows
# monotonically with every CI run: images are ~50-200 MB and a merge-per-day habit turns into
# tens of GB a year of storage nobody looks at.
resource "aws_ecr_lifecycle_policy" "this" {
  for_each = local.repositories

  repository = aws_ecr_repository.this[each.key].name

  policy = jsonencode({
    rules = [
      {
        rulePriority = 1
        description  = "Expire untagged images after ${var.untagged_expire_days} day(s)"
        selection = {
          tagStatus   = "untagged"
          countType   = "sinceImagePushed"
          countUnit   = "days"
          countNumber = var.untagged_expire_days
        }
        action = { type = "expire" }
      },
      {
        rulePriority = 2
        description  = "Keep only the ${var.keep_last_images} most recent images"
        selection = {
          tagStatus   = "any"
          countType   = "imageCountMoreThan"
          countNumber = var.keep_last_images
        }
        action = { type = "expire" }
      },
    ]
  })
}
