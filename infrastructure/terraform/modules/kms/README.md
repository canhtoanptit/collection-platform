# module: kms

Customer managed KMS keys plus an alias for each, created from a map so adding a key is one
map entry rather than a copy-pasted resource block.

Used by `stacks/20-data` (FND-3) to create `colx-dev-{data,db,msk,secrets}`.

## Why four keys instead of one

Each key is a separate blast radius and a separate audit stream in CloudTrail:

| Key | Encrypts | Why it is its own key |
|---|---|---|
| `data` | the seven S3 data buckets | the widest-shared key — every IRSA role that writes objects needs `GenerateDataKey` on it |
| `db` | both RDS instances (storage + automated backups + snapshots) | scheduling this key for deletion must not be able to take the buckets with it |
| `msk` | MSK data volumes at rest | MSK creates grants on the key; keeping them off the shared data key keeps `kms:CreateGrant` narrow |
| `secrets` | Secrets Manager secrets (SFTP keys, HMAC secret, Snowflake key-pairs, DB passwords) | the only key protecting material that cannot be regenerated cheaply, so it is the only one with rotation on |

Cost is ~$1/key/month ($4/mo total, inside the §2 "KMS/Secrets $12" line). Automatic rotation
is free; it is enabled only on `secrets` because rotation re-encrypts nothing that already
exists — it only limits how much *new* material any one backing key protects, which matters for
long-lived credentials and not for S3 objects we can re-upload.

## Usage

```hcl
module "kms" {
  source      = "../../modules/kms"
  name_prefix = "colx-dev"

  keys = {
    data    = { description = "S3 data lake buckets" }
    db      = { description = "RDS Postgres storage" }
    msk     = { description = "MSK data at rest" }
    secrets = { description = "Secrets Manager", enable_key_rotation = true }
  }
}
```

## Inputs

| Name | Type | Default | Description |
|---|---|---|---|
| `name_prefix` | `string` | — | Alias prefix, e.g. `colx-dev` → `alias/colx-dev-data`. |
| `keys` | `map(object)` | — | Keys to create. Fields: `description`, `enable_key_rotation` (false), `deletion_window_in_days` (30), `multi_region` (false). |

## Outputs

`key_arns`, `key_ids`, `alias_names`, `alias_arns` — each a map from the short key name.

## Key policy model

No key policy is set, so the AWS default applies: the account root has full access and IAM
policies on consuming principals decide the rest. Grant `kms:Decrypt`/`kms:GenerateDataKey*` on
the specific key ARN in the consumer's IAM policy (IRSA role, `colx-gha-*` role, Snowflake's
storage-integration role). Do not widen the key policy to `Principal: "*"` to "fix" an
AccessDenied — the missing permission is on the caller.

## Deletion is the dangerous operation

`terraform destroy` schedules deletion with a 30-day window; after that every object, snapshot
and secret encrypted with the key is permanently unreadable. `make destroy-heavy` must not touch
this module, and the state bucket's own key lives in `00-bootstrap` for exactly this reason
(see that stack's README).

## Prod deltas

- **Key policies, not just IAM policies.** In prod, restrict each key with an explicit key policy
  (`kms:ViaService` conditions pinning `s3.eu-west-1.amazonaws.com`, `rds.eu-west-1.amazonaws.com`)
  so a stolen role credential cannot use the key against an unrelated service.
- **Rotation on everywhere**, plus a documented manual re-encrypt for objects that predate rotation
  where the compliance requirement is about the data rather than the key.
- **Split the `data` key per classification** (PII vs operational) so masking and access reviews line
  up with a key boundary; the field catalogue already carries the classification.
- **`deletion_window_in_days = 30`** stays, but add an SCP/service-control guard denying
  `kms:ScheduleKeyDeletion` outside a break-glass role, and a CloudWatch alarm on
  `ScheduleKeyDeletion` in CloudTrail.
- **Multi-region keys** if DR is cross-region (see `scripts/dr/`), since a single-region key makes a
  cross-region restore impossible by construction.
