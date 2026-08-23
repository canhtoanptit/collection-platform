# module: ecr

ECR repositories with scan-on-push and a keep-last-N lifecycle policy.

Used by `stacks/20-data` (FND-3) to create `colx/{ingestion,simulator,dbt,connect,airflow,services}`.

## Repository layout

| Repository | Images |
|---|---|
| `colx/ingestion` | control plane, sftp-worker, webhook-receiver, canonicalizer, recon, dlq |
| `colx/simulator` | corebank seeder, drift tick, filedrop, webhooksim, legacy report |
| `colx/dbt` | dbt runner used by Airflow (`data/dbt/collections`) |
| `colx/connect` | Kafka Connect toolbox: Debezium base + `aws-msk-iam-auth` + Aiven S3 sink + JMX agent. Also the image the Kafka topic-apply Job runs (`deployment/kafka/topic-apply-job.yaml`) |
| `colx/airflow` | `apache/airflow:2.11-python3.12` + providers |
| `colx/services` | every Go domain service, tagged `<service>-<sha>` |

One repository per image *family*, not per deployable: 15 services in 15 repositories means 15
lifecycle policies to keep in sync for no isolation benefit, since they share a base image, a
build workflow and a pull principal.

## Usage

```hcl
module "ecr" {
  source           = "../../modules/ecr"
  repositories     = ["colx/ingestion", "colx/services"]
  keep_last_images = 10
}
```

## Inputs

| Name | Type | Default | Description |
|---|---|---|---|
| `repositories` | `list(string)` | — | Repository names as they appear after the registry host. |
| `scan_on_push` | `bool` | `true` | ECR basic scanning on push. |
| `image_tag_mutability` | `string` | `"MUTABLE"` | See below. |
| `keep_last_images` | `number` | `10` | Images retained per repository. |
| `untagged_expire_days` | `number` | `1` | Untagged layers expire this fast. |
| `kms_key_arn` | `string` | `null` | `null` = ECR AES256 (free). |
| `force_delete` | `bool` | `false` | Allow destroy of a non-empty repository. |

## Outputs

`repository_urls`, `repository_arns` (maps keyed by repository name), `repository_names` (sorted
list, used by `scripts/verify/INF-A.sh`), `registry_id`.

## Why MUTABLE tags in dev

FND-12's images workflow tags every build `<sha>` **and** `latest`. `latest` has to move, which
`IMMUTABLE` forbids. The deployable reference is always the `<sha>` tag — `latest` exists for
`docker run` convenience and nothing in Helmfile resolves it.

## Scanning is advisory here

`scan_on_push` produces findings but blocks nothing. The blocking gate is trivy in
`.github/workflows/images.yml` (FND-12), which fails the build on HIGH and above. ECR basic
scanning is kept on because it re-scans existing images as new CVEs land, which a
build-time-only scan cannot.

## Prod deltas

- **`IMMUTABLE` tags** and no `latest`. Deploys reference digests (`@sha256:…`), so a re-pushed tag
  cannot change what is running.
- **ECR enhanced scanning** (Inspector) registry-wide, with findings routed to the security
  workflow rather than a console page nobody opens.
- **Repository policies** restricting pull to the specific node-group and IRSA roles instead of
  relying on account-level IAM, plus a cross-account pull policy if prod runs in its own account.
- **`keep_last_images` raised** (30–50) so a rollback target survives a busy release week, and
  release tags excluded from expiry via a `tagPrefixList` rule.
- **Pull-through cache rules** for upstream base images so a rebuild does not depend on Docker Hub
  rate limits, plus image signing (cosign) verified by an admission policy.
