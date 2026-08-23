# Shared S3 backend configuration for every CI-applied stack (dev).
#
# Used as a partial configuration:
#
#     terraform -chdir=infrastructure/terraform/stacks/<nn-name> \
#       init -backend-config=infrastructure/terraform/envs/dev/backend.hcl
#
# `key` is deliberately NOT here -- it is per stack and lives in that stack's `backend "s3"` block,
# following the convention `stacks/<nn-name>.tfstate`. See README.md in this directory.
#
# ---------------------------------------------------------------------------------------------
# REPLACE 000000000000 WITH YOUR AWS ACCOUNT ID.
#
# The bucket name is `colx-tfstate-<account_id>`, created by stack 00-bootstrap. After applying
# that stack, `terraform output -raw backend_config` prints this file's exact contents for your
# account; diff it against this file rather than editing from memory.
#
# The zeros are a deliberate placeholder: scripts/verify/INF-A.sh fails on any *other* literal
# 12-digit account id anywhere under infrastructure/terraform, so leaving a real one here (or
# copying it into a module) is a red build.
# ---------------------------------------------------------------------------------------------

bucket = "colx-tfstate-000000000000"
region = "eu-west-1"

# Server-side encryption. `kms_key_id` names the bootstrap CMK by alias so this file carries no
# account id beyond the bucket name. If `terraform init` rejects the alias form, substitute the
# full ARN from `terraform output -raw state_kms_key_arn` in stack 00-bootstrap.
#
# Without `kms_key_id`, `encrypt = true` makes the backend request SSE-S3 (AES256), which overrides
# the bucket's SSE-KMS default for the state objects themselves -- state would be encrypted, but not
# with the CMK whose CloudTrail trail is the audit record for state reads.
encrypt    = true
kms_key_id = "alias/colx-tfstate"

# S3-native state locking via conditional writes (Terraform >= 1.11, ADR-0010). No DynamoDB table.
#
# This line is load-bearing: an older CLI, or this line removed, means there is no locking at all
# and concurrent applies silently corrupt state rather than failing.
use_lockfile = true
