# deployment/kafka

Declarative Kafka topics for the MSK cluster created by `infrastructure/terraform/stacks/20-data`.

| File | What it is |
|---|---|
| `topics.yaml` | The topic registry: every topic on the platform, with partitions, replication, retention and cleanup policy. |
| `topic-apply-job.yaml` | An idempotent Kubernetes Job that converges the cluster onto `topics.yaml`. |

## Auto-create is off

`auto.create.topics.enable=false` on the cluster (ADR-0004). A topic that is not in `topics.yaml`
does not exist. This is the point: with auto-create on, a typo in a topic name creates a brand new
topic with default settings, the producer succeeds, and the consumer waits forever on an empty
topic. With it off, the producer fails immediately.

## Two files, one topic, no overlap

| Question | Answered by |
|---|---|
| Which topic carries this event, keyed by what, validated against which schema? | `contracts/asyncapi/collections.v1.yaml` (CON-2, normative, frozen) |
| How many partitions, what replication, how long is it kept? | `topics.yaml` (this directory, operational) |

The split is deliberate so the two can never disagree: the AsyncAPI index says nothing about
partitions, and `topics.yaml` says nothing about schemas. A topic has to appear in both to be real.

## Applying

FND-8's helmfile applies the Job as part of the cluster baseline, after the namespaces and
external-secrets exist. By hand:

```bash
# 1. topics.yaml becomes a ConfigMap -- generated, never hand-written
kubectl -n kafka create configmap colx-kafka-topics \
  --from-file=topics.yaml=deployment/kafka/topics.yaml \
  --dry-run=client -o yaml | kubectl apply -f -

# 2. a Job's pod template is immutable, so replace rather than update
kubectl -n kafka delete job colx-kafka-topics-apply --ignore-not-found
kubectl -n kafka apply -f deployment/kafka/topic-apply-job.yaml
kubectl -n kafka wait --for=condition=complete job/colx-kafka-topics-apply --timeout=300s
kubectl -n kafka logs job/colx-kafka-topics-apply
```

Step 2's `delete` is not optional. Re-applying a changed Job under the same name is rejected by the
API server; helmfile or kustomize should either delete-then-apply or suffix the Job name with a hash
of `topics.yaml`, so each version of the registry is a distinct Job.

Set `DRY_RUN=true` in the Job's env to print every command without executing it.

## What the Job does, and what it refuses to do

| Step | Behaviour |
|---|---|
| Create | `kafka-topics.sh --create --if-not-exists` per topic — safe to re-run |
| Converge | `kafka-configs.sh --alter` on `cleanup.policy`, `retention.ms`, `min.insync.replicas`, so a topic that already existed with different settings is corrected |
| Partitions | `--describe`, then **WARN and continue** if the live count differs |
| Empty parse | Fails loudly rather than reporting success |

**Partition counts are never changed.** Growing partitions rehashes keys to different partitions, so
per-key ordering is lost across the change — and per-key ordering is the only ordering guarantee the
platform has (A§26). The fix for "we need more partitions" is a new versioned topic and a consumer
migration, not an `--alter`.

Running it twice is a no-op, which matters because `make destroy-heavy` destroys the MSK cluster
(MSK Provisioned cannot be stopped) and every rebuild re-applies topics.

## Why awk and not a YAML parser

The `colx/connect` toolbox image is a JVM image: no guaranteed `python3`, no `yq`. So `topics.yaml`
is written in a fixed two-level subset of YAML — top-level scalars and maps, then
`topics:` as a list of flat mappings — and parsed with awk. `scripts/verify/INF-A.sh` validates that
the file stays inside that subset and that every entry carries all ten fields, so the parse is total
rather than best-effort. Adding a nested structure under a topic entry breaks the Job; the verify
script fails first.

## Prerequisites (owned by INF-B)

| Thing | Provided by |
|---|---|
| namespace `kafka` | FND-8 |
| ServiceAccount `kafka-topic-applier` with IRSA | FND-7's IRSA map |
| ConfigMap `colx-kafka-bootstrap` (`bootstrap_servers`) | FND-8, from `terraform output -raw msk_bootstrap_brokers_sasl_iam` |
| image `colx/connect` | ECR repository from stack 20-data; built by FND-12's images workflow |

The IRSA role needs `kafka-cluster:Connect`, `DescribeCluster`, `AlterCluster`, `*Topic*` on the
cluster ARN — `msk_cluster_arn` and `msk_cluster_uuid` are outputs of stack 20-data, and topic ARNs
are `arn:aws:kafka:<region>:<account>:topic/colx-dev/<uuid>/*`.

Bootstrap servers are not secret, so a ConfigMap rather than an ExternalSecret. Everything that *is*
secret goes through ESO referencing `colx/dev/*` (D§66) — and MSK IAM auth means there is no Kafka
password anywhere in the system.

## Partition budget

108 partitions today → 216 replicas → ~108 per broker, against a ~300-per-broker ceiling on
`kafka.t3.small`. With all 14 per-service DLQs added it becomes ~150 per broker, half the budget.
The full reasoning, and the rules for adding a topic, are in the header of `topics.yaml`. Read it
before adding one.
