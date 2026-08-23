# stacks/40-snowflake

The Snowflake account objects and their AWS wiring (plan FND-11, ADR-0008).

State key `stacks/40-snowflake.tfstate`.

---

## ⚠ APPLY AT PHASE 6 KICKOFF — NOT NOW

Plan §6.10 is explicit: **this stack is written in Phase 2 and applied at the start of Phase 6.**

Applying it starts the 30-day Enterprise trial clock. Phase 6 (the analytical platform) is the phase that
actually needs a warehouse; every phase of trial burned before then is a phase of trial not available
when dbt, parity and masking are being built. Idle cost after conversion is ≈ $0 anyway — three XSMALL
warehouses with 60 s auto-suspend and a 50-credit monthly cap (ADR-0008) — so the trial *clock*, not the
bill, is the thing being conserved.

Mechanically: the stack is excluded from the `main`-branch auto-apply list in
`.github/workflows/terraform.yml`. PRs still run `fmt` / `validate` / `tflint` / `trivy-config` against
it, and `plan` is skipped because there is no account to plan against yet. When Phase 6 starts, run the
workflow's `workflow_dispatch` with `stack=40-snowflake`.

---

## Bootstrap (one-time, human, before the first apply)

Terraform cannot create the account it authenticates to, so five steps happen by hand.

**1. Create the trial account.** <https://signup.snowflake.com> — **Enterprise** edition, AWS,
`eu-west-1` (same region as the rest of the platform: cross-region `COPY INTO` egress is billable and
slow). Note the organization name and account name from the URL — they are `var.snowflake_organization_name`
and `var.snowflake_account_name`.

**2. Generate a key pair for `TF_SVC`.**

```bash
# Unencrypted PKCS#8 — the Terraform provider cannot prompt for a passphrase.
openssl genrsa 2048 | openssl pkcs8 -topk8 -inform PEM -out tf_svc_key.p8 -nocrypt
openssl rsa -in tf_svc_key.p8 -pubout -out tf_svc_key.pub

# The body only: no header/footer, no newlines. This is what Snowflake wants.
grep -v -- ----- tf_svc_key.pub | tr -d '\n'; echo
```

`*.p8` and `*_rsa*` are gitignored. Store the private key in the `dev` GitHub environment as
`SNOWFLAKE_TF_PRIVATE_KEY` and delete the local copy.

**3. Create the Terraform user, as `ACCOUNTADMIN` in a worksheet.**

```sql
USE ROLE ACCOUNTADMIN;

CREATE USER TF_SVC
  TYPE           = SERVICE
  RSA_PUBLIC_KEY = '<body from step 2>'
  DEFAULT_ROLE   = SYSADMIN
  COMMENT        = 'Terraform (stack 40-snowflake). Key-pair only.';

-- Object creation needs SYSADMIN; roles, grants and users need SECURITYADMIN.
GRANT ROLE SYSADMIN     TO USER TF_SVC;
GRANT ROLE SECURITYADMIN TO USER TF_SVC;

-- Resource monitors are ACCOUNTADMIN-only, which is why var.snowflake_terraform_role
-- defaults to ACCOUNTADMIN for this dev account. See "Least privilege" below.
GRANT ROLE ACCOUNTADMIN TO USER TF_SVC;
```

**4. Generate the three service-user key pairs** the same way as step 2, for `AIRFLOW_SVC`, `DBT_SVC` and
`DBT_CI_SVC`:

```bash
for u in airflow_svc dbt_svc dbt_ci_svc; do
  openssl genrsa 2048 | openssl pkcs8 -topk8 -inform PEM -out "${u}.p8" -nocrypt
  openssl rsa -in "${u}.p8" -pubout | grep -v -- ----- | tr -d '\n' > "${u}.pub.body"
done
```

Put each **private** key in Secrets Manager under `colx/dev/snowflake/<USER>/private_key` (the path ESO
and the Airflow connection ExternalSecrets expect), then delete the local `.p8` files. The public key
bodies go into `var.service_user_public_keys` — they are not secret, so `envs/dev/common.tfvars` is a fine
home for them.

**5. Sanity-check auth before running Terraform.**

```bash
snow connection add --connection-name colx-tf \
  --account "<org>-<account>" --user TF_SVC --private-key tf_svc_key.p8 --role ACCOUNTADMIN
snow sql -c colx-tf -q "select current_account(), current_role(), current_version()"
```

## Inputs (user-supplied)

| Variable | Source |
|---|---|
| `snowflake_organization_name` | Step 1 |
| `snowflake_account_name` | Step 1 |
| `snowflake_private_key` | `TF_VAR_snowflake_private_key`, from the `dev` GitHub environment secret. Never a tfvars file. |
| `service_user_public_keys` | Step 4 — map with keys `AIRFLOW_SVC`, `DBT_SVC`, `DBT_CI_SVC` |
| `raw_bucket` | Defaults to `colx-dev-raw` (stack 20-data) |

AWS side: the stack looks up `alias/colx-dev-data` for the CMK protecting objects in the raw bucket, and
needs `iam:CreateRole` — so the apply runs with `colx-gha-apply`, not the read-only plan role.

Note that `snowflake_private_key` lands in Terraform **state**, not just in memory. That is why the state
bucket is versioned, SSE-KMS and TLS-only (ADR-0010), and why the key is rotated rather than reused if
the bucket's access policy ever changes.

## Least privilege for `TF_SVC`

`var.snowflake_terraform_role` defaults to `ACCOUNTADMIN`, which is not least privilege and is a
deliberate, documented dev choice. The reason: this stack spans three privilege domains — objects
(`SYSADMIN`), roles/users/grants (`SECURITYADMIN`), and resource monitors (`ACCOUNTADMIN` only). No single
non-`ACCOUNTADMIN` role covers all three, and splitting a single stack across three provider aliases with
three key pairs buys real security in prod and mostly buys confusion in a one-developer dev account.

The prod split is in the module README's prod-deltas table. If you want the split here, run the stack with
`-var snowflake_terraform_role=SECURITYADMIN` for the RBAC resources and `SYSADMIN` for the rest via
`-target`, and create the resource monitor by hand.

## Verifying an apply

```bash
snow sql -q "show warehouses like 'WH_%'"                 # 3, all SUSPENDED, XSMALL
snow sql -q "show resource monitors like 'COLX_MONTHLY'"   # credit_quota = 50
snow sql -q "desc integration S3_RAW_INT"                  # STORAGE_AWS_ROLE_ARN + STORAGE_AWS_IAM_USER_ARN
snow sql -q "show masking policies in schema ANALYTICS.GOVERNANCE"

# each service user can authenticate with its own key
snow sql -c colx-airflow -q "select current_user(), current_role()"
```

## Prod deltas

See `../../modules/snowflake-account/README.md`.
