# modules/snowflake-account

Warehouses, databases/schemas, RBAC, key-pair service users, the credit cap, the S3 storage integration
with its AWS role, and the two masking policies (plan FND-11, A§43/§69, ADR-0008).

Provider: **`snowflakedb/snowflake` pinned `~> 2.20`** (probed against 2.20.0). Note the source — this is
the vendor-owned successor to `Snowflake-Labs/snowflake`, and the resource names differ. If you copy
snippets from an older tutorial they will not validate:

| v0.x name | v2.x name used here |
|---|---|
| `snowflake_role` | `snowflake_account_role` |
| `snowflake_user` (for services) | `snowflake_service_user` |
| `snowflake_storage_integration` | `snowflake_storage_integration_aws` (the generic one is deprecated) |
| `snowflake_role_grants` | `snowflake_grant_account_role` |
| `snowflake_*_grant` | `snowflake_grant_privileges_to_account_role` |

## File map

| File | Contents |
|---|---|
| `main.tf` | Resource monitor, three warehouses, `RAW` / `ANALYTICS` / `ANALYTICS_CI` + schemas |
| `rbac.tf` | Five roles, the grant graph, three service users |
| `governance.tf` | `MASK_STRING_PII`, `MASK_DATE_PII`, and the transformer's `APPLY` grants |
| `storage.tf` | `S3_RAW_INT` + the AWS IAM role it assumes |

## The grant graph

```
SYSADMIN
  └── COLX_ADMIN
        ├── COLX_LOADER        WH_INGEST     RAW: create + insert + select (future & all)
        ├── COLX_TRANSFORMER   WH_TRANSFORM  RAW: select   ANALYTICS(+_CI): own
        └── COLX_REPORTER      WH_ANALYTICS  ANALYTICS.{MARTS,SNAPSHOTS}: select

COLX_PII_READER  ──grants──►  COLX_REPORTER        (granted to no user)
```

Three things in that picture are decisions, not defaults:

1. **`COLX_REPORTER` has no grant of any kind on `RAW`** — not `SELECT`, not `USAGE` (D§45). Masking
   protects the marts; a reporting role that can read `RAW` walks around it.
2. **`COLX_PII_READER` is the *parent* of `COLX_REPORTER`, not the child.** A session holding
   `COLX_PII_READER` inherits mart access and unmasks; holding `COLX_REPORTER` never unmasks. Inverting
   this edge silently unmasks PII for every reporting user, and no test would fail.
3. **Future grants everywhere dbt or a load creates the object.** `COLX_REPORTER` can read tomorrow's
   mart without a Terraform change. The `all`-grant siblings carry `always_apply = true` because an
   `ALL` grant is a point-in-time snapshot — re-running the apply is what keeps existing objects
   covered.

## Masking

Terraform owns the **policy objects**; dbt post-hooks own the **column applications** (ANA WPs). The
split matters: `dbt run --full-refresh` recreates a table, and a policy that only exists because a model
ran is not a control.

```sql
MASK_STRING_PII(VAL STRING) -> CASE WHEN IS_ROLE_IN_SESSION('COLX_PII_READER') THEN VAL ELSE '***MASKED***' END
MASK_DATE_PII(VAL DATE)     -> CASE WHEN IS_ROLE_IN_SESSION('COLX_PII_READER') THEN VAL ELSE DATE_TRUNC('YEAR', VAL) END
```

`IS_ROLE_IN_SESSION` rather than `CURRENT_ROLE` so the check honours the role hierarchy. Dates truncate
to the year instead of returning `NULL` — a year keeps age-band analysis working, and `NULL` would break
every downstream `not_null` test for no privacy gain.

The acceptance assertion is one query run twice: as `COLX_REPORTER` it returns `***MASKED***`, as
`COLX_PII_READER` it returns the value.

## The storage integration cycle

`S3_RAW_INT` needs the AWS role ARN; the AWS role's trust policy needs the integration's IAM user ARN
and external id. Something has to break the loop, and the usual answer — apply twice, copy the values
from `DESC INTEGRATION` — is not acceptable for a stack that must rebuild in one CI run.

So two of the three values are chosen by us rather than read back:

- `storage_aws_role_arn` is **constructed** from `var.aws_role_name` + the caller's account id.
- `storage_aws_external_id` is **chosen** (`var.aws_external_id`), not accepted from Snowflake.
- Only `iam_user_arn` is read back, from `describe_output[0].iam_user_arn`, into the role's trust policy.

Graph: integration → AWS role. One apply.

The AWS policy is **read-only** on the raw bucket. Snowflake loads from `raw/`; the write side belongs to
the Kafka Connect S3 sink and the ingestion control plane (stack 30-eks).

Changing `aws_external_id` invalidates every existing stage, which is why the stack derives a stable
default instead of, say, a random id.

## Service users

`snowflake_service_user` cannot hold a password at all — key-pair only, which is the point. Pass the
**body** of the public key (no PEM header/footer, no newlines) in `service_user_public_keys`; the variable
validates both the key set and the absence of a PEM header, because passing the whole PEM fails at apply
time with an unhelpful Snowflake error.

`default_secondary_roles_option = "NONE"`: a session gets exactly the role it asked for, so a
`QUERY_HISTORY` row unambiguously names the privileges that ran it.

## Prod deltas

| Dev | Prod |
|---|---|
| `data_retention_days = 1` | 7–90 days Time Travel on `ANALYTICS`; `RAW` can stay short since S3 is the source of truth |
| `credit_quota = 50`, suspend at 100% | Higher quota, suspend replaced by an alert — suspending a prod warehouse is an outage |
| Warehouses `XSMALL`, single cluster | Sized per workload; multi-cluster on the reporting warehouse |
| Network policy: none | Account-level network policy allowing only NAT egress + CI |
| `ANALYTICS_CI` transient, retention 0 | Same |
| `TF_SVC` runs as `ACCOUNTADMIN` | Split: `SECURITYADMIN` for RBAC applies, `SYSADMIN` for objects, separate keys |
| `COLX_PII_READER` granted to nobody | Granted just-in-time with an approval trail |
