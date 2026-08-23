# stack 20-data

The platform's stateful substrate: CMKs, S3 buckets, ECR repositories, both RDS instances, the MSK
cluster and the Secrets Manager placeholders. **CI-applied** (ADR-0010) — locally,
`make tf-plan STACK=20-data ENV=dev`.

Covers FND-3 (KMS + S3 + ECR + secrets baseline), FND-4 (RDS ×2) and FND-5 (MSK).

## Position in the apply order

```
00-bootstrap (human)  ->  10-network  ->  20-data  ->  30-eks  ->  40-snowflake
                                             ^            |
                                             +------------+  second pass: EKS node SG id
```

Reads `10-network`'s outputs through `terraform_remote_state` (`stacks/10-network.tfstate`).
State key: `stacks/20-data.tfstate`.

Its own outputs are read by `30-eks` (INF-B) to build IRSA policies against the bucket, secret and
MSK ARNs.

## What it creates

| Group | Resources |
|---|---|
| KMS | `colx-dev-{data,db,msk,secrets}` + aliases; rotation on `secrets` only |
| S3 | `colx-dev-{landing,raw,quarantine,archive,ops,decision-audit,batch}` — plan §6.7 exactly |
| ECR | `colx/{ingestion,simulator,dbt,connect,airflow,services}`, scan-on-push, keep-10 |
| RDS | `colx-dev-platform` (`db.t4g.small`), `colx-dev-corebank` (`db.t4g.micro`, logical replication) |
| MSK | `colx-dev`, 2 × `kafka.t3.small`, IAM auth only, auto-create off |
| Secrets | 10 placeholder secrets under `colx/dev/*`, **containers only, no values** |

Module-level detail lives in each module's README:
[kms](../../modules/kms/README.md) ·
[s3-data](../../modules/s3-data/README.md) ·
[ecr](../../modules/ecr/README.md) ·
[rds-postgres](../../modules/rds-postgres/README.md) ·
[msk](../../modules/msk/README.md).

## The second pass

`eks_node_security_group_id` defaults to `null`, and null means **no client ingress rules are
created at all**. On the first apply the databases and brokers exist and nothing can reach them —
which is correct for infrastructure with no clients yet, and is why FND-4/FND-5 do not depend on
FND-7.

```bash
# pass 1: 20-data applied with eks_node_security_group_id unset
terraform output second_pass_required    # true
# pass 2: 30-eks applied, node security group now exists
# pass 3: 20-data re-applied with the id -> adds ingress rules and nothing else
```

Because the rules are separate `aws_vpc_security_group_ingress_rule` resources, pass 3 is an
additive plan: no instance modification, no MSK update, no downtime.

Making `20-data` read `30-eks`'s state instead would create a cycle — `30-eks` needs the network and
will eventually want database endpoints. An explicit variable keeps the stack graph acyclic and
makes the pending step visible in `terraform output`.

## Values you must supply

| Variable | Why |
|---|---|
| `state_bucket` | **Required, account-specific.** `colx-tfstate-<account_id>`. Must equal `bucket` in `envs/dev/backend.hcl`; the backend config and the remote-state lookup are separate mechanisms and nothing reconciles them. `scripts/verify/INF-A.sh` asserts the two files agree. |
| `eks_node_security_group_id` | After `30-eks` exists. Until then, leave unset. |
| `msk_kafka_version` | Only if `3.7.x` is not offered in the account/region. Check with `aws kafka list-kafka-versions`. |

Everything else has a working default.

## Secrets: containers without values, on purpose

There is no `aws_secretsmanager_secret_version` in this stack. A value in HCL is a value in git; a
value passed through tfvars is a value in state and in every plan output. Values arrive out of band:

| Secret | Written by |
|---|---|
| `colx/dev/sftp/{host-key,corebank-user-key}` | operator, once (SIM-3 / ING-4) |
| `colx/dev/webhook/hmac-secret` | operator, once (shared by simulator and receiver) |
| `colx/dev/snowflake/{airflow,dbt,dbt-ci}-private-key` | FND-11 bootstrap (key-pair auth) |
| `colx/dev/grafana/admin` | operator (FND-9) |
| `colx/dev/keycloak/admin` | operator (FND-6) |
| `colx/dev/keycloak/client-{platform-services,simulator}` | FND-6's `kcadm.sh` Job, after realm import |
| per-database owner passwords | `scripts/db/provision_databases.sh` (creates its own secrets) |
| RDS master passwords | RDS itself (`manage_master_user_password`) |

**A secret with no version cannot be read.** An ExternalSecret referencing one stays `NotReady` and
the pod that needs it will not start. That is the intended failure: a missing credential should stop
a deploy, not produce a service running with an empty string.

`recovery_window_in_days = 0` because a 7–30 day window keeps the *name* reserved after deletion,
and the re-apply after a teardown then fails with "already scheduled for deletion".

## Identity is Keycloak, not Cognito

Per the FND-6 rewrite (user directive 2026-08-23), identity is **Keycloak on EKS** backed by a
`keycloak` database on `colx-dev-platform`. This stack's only involvement is:

1. the `keycloak` database and its owner role — created by `scripts/db/provision_databases.sh`;
2. the three `colx/dev/keycloak/*` secret placeholders.

There are no Cognito resources in this stack, and no OIDC issuer output: the issuer is an in-cluster
Keycloak URL that INF-B's Helm values define. `scripts/verify/INF-A.sh` greps for Cognito resources
and fails if any reappear.

## After apply

```bash
terraform output -raw provision_databases_env    # environment for the DB provisioning script
terraform output second_pass_required            # true until the EKS node SG is wired
terraform output msk_bootstrap_brokers_sasl_iam  # for FND-8's Kafka client config
```

`scripts/db/provision_databases.sh` must run from inside the VPC — the data subnets have no inbound
route from outside — so from a pod, a bastion, or an Airflow task.

Kafka topics are **not** created by this stack. `deployment/kafka/topics.yaml` plus
`topic-apply-job.yaml` do that, applied by FND-8's helmfile after the cluster is up.

## Teardown

| Lever | Effect on this stack |
|---|---|
| `make stop` | RDS instances stopped (~$37/mo saved). MSK cannot be stopped. |
| `make destroy-heavy` | MSK destroyed (~$80/mo). Buckets, keys, ECR and secrets kept. |
| `make destroy-all` | Everything, and only with `s3_force_destroy`/`ecr_force_delete` set. |

A stopped RDS instance still bills for storage and backups, and AWS restarts it automatically after
7 days. MSK Provisioned has no stop: the brokers bill whether or not anything is running, which is
why `destroy-heavy` targets it and why every rebuild re-runs the topic-apply Job.

## Prod deltas

Per-module lists are in the module READMEs. The stack-level ones:

- **Split this stack.** In prod, buckets/keys, databases and Kafka belong in separate stacks with
  separate apply approvals: a bucket lifecycle change should not sit in the same plan as an MSK
  version upgrade.
- **`multi_az = true` on both instances**, or one instance per service — ADR-0003 is explicit that
  the shared instance is a cost compromise, not the target architecture.
- **3 MSK brokers, RF 3, `min.insync.replicas = 2`** across 3 AZs, which also needs `az_count = 3`
  in the network stack.
- **`deletion_protection = true`** on RDS, Object Lock on `decision-audit` and `archive`, and
  `force_destroy` unavailable in the module rather than merely defaulted off.
- **Secrets rotation**: rotation Lambdas for the database owner passwords and the webhook HMAC
  secret, with `recovery_window_in_days` back to 30.
- **Cross-region replication** for `raw`, `archive` and `decision-audit`, with their own CMK, plus
  cross-region automated RDS backups.
