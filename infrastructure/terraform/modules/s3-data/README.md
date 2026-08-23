# module: s3-data

The platform's S3 data buckets (plan §6.7), created from one map so the security posture cannot
drift between them.

Used by `stacks/20-data` (FND-3) to create `colx-dev-{landing,raw,quarantine,archive,ops,decision-audit,batch}`.

## Applied to every bucket, with no opt-out

| Control | Resource |
|---|---|
| Public access blocked (all four switches) | `aws_s3_bucket_public_access_block` |
| ACLs disabled, bucket owner owns every object | `aws_s3_bucket_ownership_controls` = `BucketOwnerEnforced` |
| SSE-KMS default encryption with the `data` CMK + S3 Bucket Key | `aws_s3_bucket_server_side_encryption_configuration` |
| TLS-only (`Deny` on `aws:SecureTransport = false`) | `aws_s3_bucket_policy` |
| Incomplete multipart uploads aborted after 7 days | `aws_s3_bucket_lifecycle_configuration` |

`bucket_key_enabled = true` is a cost control, not a nicety: the CDC and file paths write many
small objects, and one data key per bucket/prefix instead of one per object is the difference
between cents and tens of dollars a month in KMS requests.

## The buckets and why each one exists

| Bucket | Holds | Versioned | Retention |
|---|---|---|---|
| `landing` | files as delivered by SFTP, byte-for-byte | no | none (promoted then archived) |
| `raw` | canonicalized raw partitions (`business_date=…`), the Snowflake `COPY INTO` source | **yes** | none |
| `quarantine` | files that failed validation, kept for investigation | no | none |
| `archive` | processed originals | **yes** | 90 days (dev) |
| `ops` | Airflow remote logs, Loki chunks, Tempo blocks, dbt artefacts | no | per prefix, 14–30 days |
| `decision-audit` | decision snapshots (DEC-9) | no | none |
| `batch` | batch populations and outcome files (DEC-10) | no | none |

`raw` and `archive` are versioned because they are the two buckets where an overwrite destroys
evidence: `raw` is what analytics reconciles against, `archive` is the only remaining copy of the
original file. Versioning elsewhere would pay storage for data we can regenerate.

`ops` prefix retention (`airflow-logs/` 14d, `tempo/` 14d, `loki/` 30d, `dbt-artifacts/` 30d) is a
backstop, not the primary control — Loki and Tempo enforce their own retention. It exists so that a
misconfigured retention setting in a Helm values file cannot quietly accumulate object storage for
a year.

## Usage

```hcl
module "s3_data" {
  source      = "../../modules/s3-data"
  name_prefix = "colx-dev"
  kms_key_arn = module.kms.key_arns["data"]

  buckets = {
    landing  = { purpose = "sftp-landing" }
    raw      = { purpose = "raw-partitions", versioning = true }
    archive  = { purpose = "processed-originals", versioning = true, expire_current_after_days = 90 }
    ops      = { purpose = "operational-artifacts", prefix_expiration_days = { "airflow-logs/" = 14 } }
  }
}
```

## Inputs

| Name | Type | Default | Description |
|---|---|---|---|
| `name_prefix` | `string` | — | Bucket name prefix, e.g. `colx-dev`. |
| `kms_key_arn` | `string` | — | CMK for SSE-KMS default encryption. |
| `buckets` | `map(object)` | — | Per bucket: `purpose`, `versioning` (false), `expire_current_after_days` (null), `prefix_expiration_days` ({}), `noncurrent_version_expiration_days` (7), `force_destroy` (false). |
| `abort_incomplete_multipart_upload_days` | `number` | `7` | Applies to every bucket. |

## Outputs

`bucket_ids`, `bucket_arns`, `bucket_regional_domain_names` (maps keyed by suffix) and
`bucket_names` (sorted list, used by `scripts/verify/INF-A.sh`).

## Guard rails worth knowing

- **Prefixes must end with `/`.** A validation enforces it: `loki` would also match `loki-old/`,
  which is how a retention rule silently deletes the wrong data.
- **`force_destroy` defaults to `false`** on every bucket. `terraform destroy` on a bucket holding
  objects therefore *fails* rather than deleting them. `make destroy-all` sets it deliberately; no
  bucket should carry `force_destroy = true` as a convenience.
- Rule ids are derived from the prefix, not from a list index, so reordering the map does not
  rewrite unrelated lifecycle rules.

## Prod deltas

- **Object Lock (WORM) on `decision-audit` and `archive`** in compliance mode with a retention
  period matching the regulatory requirement. Auditability is append-only in the database
  (CLAUDE.md hard rule 4); in prod the object store must say the same thing, and Object Lock can
  only be enabled at bucket creation.
- **Replication** of `raw`, `archive` and `decision-audit` to a second region with its own CMK, plus
  `aws_s3_bucket_replication_configuration` and a replication IAM role.
- **Access logging** (or CloudTrail S3 data events) to a separate, tighter-permissioned log bucket —
  off in dev because data events are billed per request and the dev volume is noise.
- **Storage class transitions**: `raw` and `archive` to Intelligent-Tiering or Glacier IR after 30
  days. Skipped in dev because the volume never justifies the per-object transition charge.
- **Retention increases**: `archive` 90 days → the regulatory retention (typically 7 years) and
  `ops` prefixes → whatever the log-retention policy mandates; the dev numbers are cost choices.
- **Explicit `Deny` on `s3:PutObject` without `x-amz-server-side-encryption: aws:kms`** in the bucket
  policy. Default encryption already covers this, but prod wants the header refusal so a
  misconfigured client fails loudly instead of relying on the bucket default.
