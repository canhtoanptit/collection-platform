# module: msk

Amazon MSK **Provisioned**, IAM authentication only, TLS in transit and at rest.

Used by `stacks/20-data` (FND-5). Topics are declared separately in
[`deployment/kafka/topics.yaml`](../../../../deployment/kafka/topics.yaml) and applied by
[`topic-apply-job.yaml`](../../../../deployment/kafka/topic-apply-job.yaml) — this module creates the
cluster and nothing inside it.

## Shape

| | Dev |
|---|---|
| Deployment | Provisioned (not Serverless) |
| Brokers | 2 × `kafka.t3.small`, one per AZ |
| Storage | 20 GiB EBS per broker |
| Kafka version | `3.7.x` (see below) |
| Auth | SASL/IAM only, port 9098 |
| In transit | TLS client↔broker, encrypted in-cluster |
| At rest | `colx-dev-msk` CMK |
| Replication factor | 2, `min.insync.replicas = 1` |
| Auto-create topics | **off** |
| Monitoring | open monitoring (JMX + node exporters); CloudWatch at `DEFAULT` granularity |

Provisioned rather than Serverless on cost alone: ~$80/mo against ~$550/mo at this shape
(ADR-0004). The trade is a fixed partition budget — roughly 300 partitions per broker on
`t3.small` — which `topics.yaml` documents and stays well inside (108 partitions, 216 replicas,
~108 per broker).

## One authentication path, deliberately

No unauthenticated listener, no SCRAM, no mutual TLS. Every client needs `aws-msk-iam-auth` and an
IRSA role, and there is no password in the system to leak or rotate. The cost is that a client
without the IAM library gets a TLS handshake failure rather than an auth error, which is a
confusing first ten minutes for anyone new to MSK IAM.

`client_properties` is exported so the exact client configuration lives in one place:

```properties
security.protocol=SASL_SSL
sasl.mechanism=AWS_MSK_IAM
sasl.jaas.config=software.amazon.msk.auth.iam.IAMLoginModule required;
sasl.client.callback.handler.class=software.amazon.msk.auth.iam.IAMClientCallbackHandler
```

## `kafka_version` is an MSK identifier, not a Kafka version

MSK publishes its own version strings, and which ones are valid depends on the region and on
whether the cluster is ZooKeeper- or KRaft-based (`3.7.x` vs `3.7.x.kraft`). A wrong string fails at
**apply** with `BadRequestException`, not at plan, so confirm against the account before changing
it:

```bash
aws kafka list-kafka-versions --query 'KafkaVersions[?Status==`ACTIVE`].Version'
```

The default is `3.7.x`, matching plan §2's "Kafka 3.7". Version upgrades are in-place and one-way —
MSK cannot downgrade.

## Security group rules, and the two that are easy to miss

| Rule | Why |
|---|---|
| 9098 from `client_security_group_ids` | The only client listener |
| 11001, 11002 from `client_security_group_ids` | Open-monitoring JMX + node exporters. Without these FND-9's MSK dashboards are permanently empty and `up{job=~".*msk.*"}` returns nothing |
| all protocols from **itself** | Inter-broker replication. Broker ENIs all sit in this group; without a self-referencing rule a two-broker cluster never reaches an in-sync state |
| egress `0.0.0.0/0` | Brokers are a managed service on customer ENIs and reach KMS, CloudWatch and the MSK control plane outbound |

`client_security_group_ids` defaults to `[]`, and empty means *no client ingress rules at all* — a
freshly applied cluster is unreachable rather than open. In dev the value is the EKS node security
group, supplied on a second apply once `stacks/30-eks` exists. Same two-pass pattern as
[`modules/rds-postgres`](../rds-postgres/README.md#the-two-pass-eks-ingress-pattern).

## `min.insync.replicas = 1` is a dev-only choice

With RF 2, requiring 2 in-sync replicas means a single broker restart — including MSK's own
patching — stops every `acks=all` producer. Dev accepts the durability trade because Kafka here is
transport, not a system of record (D§47), and the outbox in Postgres is what makes a publication
durable. Prod does not accept it; see below.

## Usage

```hcl
module "msk" {
  source = "../../modules/msk"

  cluster_name      = "colx-dev"
  vpc_id            = local.vpc_id
  client_subnet_ids = local.private_subnet_ids
  kms_key_arn       = module.kms.key_arns["msk"]

  number_of_broker_nodes = 2
  instance_type          = "kafka.t3.small"
  ebs_volume_size        = 20

  client_security_group_ids = compact([var.eks_node_security_group_id])
}
```

`number_of_broker_nodes` must be an exact multiple of `length(client_subnet_ids)`; MSK places
brokers evenly across AZs and rejects anything else.

## Inputs

| Name | Type | Default | Description |
|---|---|---|---|
| `cluster_name` | `string` | — | |
| `kafka_version` | `string` | `"3.7.x"` | Read the section above. |
| `vpc_id` | `string` | — | |
| `client_subnet_ids` | `list(string)` | — | ≥ 2, one per AZ. |
| `number_of_broker_nodes` | `number` | `2` | Multiple of the subnet count. |
| `instance_type` | `string` | `"kafka.t3.small"` | |
| `ebs_volume_size` | `number` | `20` | Growable in place, never shrinkable. |
| `kms_key_arn` | `string` | — | Encryption at rest. |
| `client_security_group_ids` | `list(string)` | `[]` | Empty = unreachable. |
| `client_cidr_blocks` | `list(string)` | `[]` | `0.0.0.0/0` rejected. |
| `server_properties` | `map(string)` | the three below | Rendered sorted into an MSK configuration. |
| `enhanced_monitoring` | `string` | `"DEFAULT"` | |
| `open_monitoring_enabled` | `bool` | `true` | |
| `broker_logs_s3_bucket` | `string` | `null` | e.g. `colx-dev-ops`. See the caveat below. |
| `broker_logs_s3_prefix` | `string` | `"msk-broker-logs/"` | |

Default `server_properties`: `auto.create.topics.enable=false`, `default.replication.factor=2`,
`min.insync.replicas=1`.

## Outputs

`cluster_arn`, `cluster_name`, `cluster_uuid`, `bootstrap_brokers_sasl_iam`,
`zookeeper_connect_string_tls`, `security_group_id`, `configuration_arn`,
`configuration_revision`, `server_properties`, `client_properties`.

`cluster_uuid` matters for IAM: topic and group ARNs are
`arn:aws:kafka:<region>:<account>:topic/<cluster-name>/<cluster-uuid>/<topic>`.

## Broker log delivery is opt-in

`broker_logs_s3_bucket` defaults to `null` and `stacks/20-data` leaves it that way. Two
prerequisites cannot be verified without an account, and getting either wrong fails the apply:

1. the target bucket needs a policy statement allowing the AWS log-delivery principal to
   `s3:PutObject` under the prefix;
2. MSK's S3 delivery path has to accept a bucket whose default encryption is a customer managed
   key — every bucket in `modules/s3-data` is SSE-KMS with the `data` CMK.

Nothing operational is lost meanwhile: broker and JVM metrics reach Prometheus through open
monitoring, which is the signal the FND-9 dashboards and alerts are built on. The `ops` bucket
already carries a 14-day lifecycle rule on `msk-broker-logs/`, so enabling delivery later is one
variable plus a bucket policy statement.

## Teardown

**MSK Provisioned cannot be stopped.** Brokers bill ~$80/mo whether or not anything is running, so
the only lever is destroying the cluster — which is safe by design (Kafka is transport, not a system
of record) but means every rebuild re-runs the topic-apply Job and every consumer restarts from its
configured offset reset. `make destroy-heavy` (FND-13) destroys it; `make stop` does not, because
there is nothing to stop.

## Prod deltas

- **3 brokers, `default.replication.factor=3`, `min.insync.replicas=2`** across 3 AZs. This is the
  headline delta: it is the difference between "a broker restart pauses producers" and "a broker
  restart is invisible". It needs `az_count = 3` in the network stack.
- **`unclean.leader.election.enable=false`** explicitly, so an out-of-sync replica can never be
  elected leader and silently truncate committed records.
- **`kafka.m7g.large` or larger.** `t3.small` brokers are burstable: sustained throughput exhausts
  CPU credits and the symptom is producer latency with no obvious cause.
- **Tiered storage** (`storage_mode = "TIERED"`) for long-retention topics, plus provisioned EBS
  throughput. Not available on `t3.small`.
- **Broker logs to S3 or CloudWatch on**, and `enhanced_monitoring = "PER_TOPIC_PER_BROKER"` so a hot
  partition is diagnosable from metrics rather than from a JMX dashboard.
- **Interface endpoint for `kafka`** and egress narrowed from `0.0.0.0/0` to those endpoints.
- **Kafka quotas per client id** so one runaway consumer cannot starve the cluster, and topic-level
  `retention.bytes` so a producer bug cannot fill a broker volume.
- **MSK Connect or a dedicated Connect cluster** rather than Debezium on EKS with one replica; the
  dev choice trades availability for iteration speed and log access (ADR-0006).
