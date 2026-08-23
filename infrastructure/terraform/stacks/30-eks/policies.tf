# IRSA policy documents (plan FND-7).
#
# One document per workload, least privilege by bucket *prefix* wherever the
# workload only owns a prefix. Bucket names are the standardized set from plan
# §6.7 (`colx-dev-{landing,raw,quarantine,archive,ops,decision-audit,batch}`) and
# are therefore constructed from `project`/`env` rather than read out of another
# stack's outputs — the naming is a fixed convention, and constructing the ARN
# keeps this stack from breaking every time 20-data renames an output.
#
# The values that genuinely cannot be derived (VPC/subnets, CMK ARNs, the MSK
# cluster ARN with its generated UUID) come from remote state or a data source.

locals {
  # ------------------------------------------------------------------ buckets --
  bucket_names = {
    landing        = "${local.name}-landing"
    raw            = "${local.name}-raw"
    quarantine     = "${local.name}-quarantine"
    archive        = "${local.name}-archive"
    ops            = "${local.name}-ops"
    decision_audit = "${local.name}-decision-audit"
    batch          = "${local.name}-batch"
  }

  bucket_arns = { for k, v in local.bucket_names : k => "arn:${local.partition}:s3:::${v}" }

  # ------------------------------------------------------------------ secrets --
  # Every Kubernetes secret is an ExternalSecret under `colx/dev/*` (D§66); no
  # workload may read outside its environment's namespace of the secret store.
  secrets_arn_pattern = "arn:${local.partition}:secretsmanager:${var.region}:${local.account_id}:secret:${var.project}/${var.env}/*"

  sns_alerts_arn = "arn:${local.partition}:sns:${var.region}:${local.account_id}:${local.name}-alerts"

  s3_object_write_actions = [
    "s3:PutObject",
    "s3:PutObjectTagging",
    "s3:GetObject",
    "s3:GetObjectTagging",
    "s3:DeleteObject",
    "s3:AbortMultipartUpload",
  ]

  s3_object_read_actions = [
    "s3:GetObject",
    "s3:GetObjectTagging",
  ]
}

# --------------------------------------------------------------------- shared --

# Read the platform's own secrets, and decrypt them with the secrets CMK.
data "aws_iam_policy_document" "secrets_read" {
  statement {
    sid    = "ReadColxSecrets"
    effect = "Allow"
    actions = [
      "secretsmanager:GetSecretValue",
      "secretsmanager:DescribeSecret",
    ]
    resources = [local.secrets_arn_pattern]
  }

  statement {
    sid       = "ListSecretsForDiscovery"
    effect    = "Allow"
    actions   = ["secretsmanager:ListSecrets"]
    resources = ["*"]
  }

  statement {
    sid    = "DecryptSecrets"
    effect = "Allow"
    actions = [
      "kms:Decrypt",
      "kms:DescribeKey",
    ]
    resources = [local.kms_key_arns.secrets]
  }
}

# Encrypt/decrypt SSE-KMS objects in the data buckets.
data "aws_iam_policy_document" "kms_data" {
  statement {
    sid    = "DataBucketEnvelopeKeys"
    effect = "Allow"
    actions = [
      "kms:Decrypt",
      "kms:Encrypt",
      "kms:GenerateDataKey",
      "kms:DescribeKey",
    ]
    resources = [local.kms_key_arns.data]
  }
}

# -------------------------------------------------------------- external-secrets

data "aws_iam_policy_document" "external_secrets" {
  source_policy_documents = [data.aws_iam_policy_document.secrets_read.json]

  # ESO also resolves SSM parameters in some provider configurations; this stack
  # uses the Secrets Manager provider only, so nothing else is granted.
}

# ------------------------------------------------------------------ ingestion --

data "aws_iam_policy_document" "ingestion_cp" {
  source_policy_documents = [
    data.aws_iam_policy_document.secrets_read.json,
    data.aws_iam_policy_document.kms_data.json,
    data.aws_iam_policy_document.msk_read_write.json,
  ]

  statement {
    sid       = "ListIngestionBuckets"
    effect    = "Allow"
    actions   = ["s3:ListBucket", "s3:GetBucketLocation"]
    resources = [for k in ["landing", "raw", "quarantine", "archive"] : local.bucket_arns[k]]
  }

  statement {
    sid       = "ReadWriteIngestionObjects"
    effect    = "Allow"
    actions   = local.s3_object_write_actions
    resources = [for k in ["landing", "raw", "quarantine", "archive"] : "${local.bucket_arns[k]}/*"]
  }
}

data "aws_iam_policy_document" "sftp_worker" {
  source_policy_documents = [
    data.aws_iam_policy_document.secrets_read.json,
    data.aws_iam_policy_document.kms_data.json,
  ]

  statement {
    sid       = "ListLanding"
    effect    = "Allow"
    actions   = ["s3:ListBucket", "s3:GetBucketLocation"]
    resources = [local.bucket_arns.landing]
  }

  # The worker downloads from SFTP and lands the byte-identical file. It never
  # reads back what it wrote and never touches raw/ — the control plane promotes.
  statement {
    sid       = "WriteLandedFiles"
    effect    = "Allow"
    actions   = ["s3:PutObject", "s3:PutObjectTagging", "s3:AbortMultipartUpload"]
    resources = ["${local.bucket_arns.landing}/*"]
  }
}

data "aws_iam_policy_document" "webhook_receiver" {
  source_policy_documents = [
    data.aws_iam_policy_document.secrets_read.json,
    data.aws_iam_policy_document.msk_write_only.json,
  ]
}

# --------------------------------------------------------------- kafka-connect --

data "aws_iam_policy_document" "kafka_connect" {
  source_policy_documents = [
    data.aws_iam_policy_document.secrets_read.json,
    data.aws_iam_policy_document.kms_data.json,
    data.aws_iam_policy_document.msk_admin.json,
  ]

  statement {
    sid       = "ListSinkBucket"
    effect    = "Allow"
    actions   = ["s3:ListBucket", "s3:GetBucketLocation"]
    resources = [local.bucket_arns.raw]
  }

  # The Aiven S3 sink writes CDC/webhook/event partitions under raw/.
  statement {
    sid       = "WriteSinkObjects"
    effect    = "Allow"
    actions   = local.s3_object_write_actions
    resources = ["${local.bucket_arns.raw}/*"]
  }
}

# -------------------------------------------------------------------- airflow --

data "aws_iam_policy_document" "airflow" {
  source_policy_documents = [
    data.aws_iam_policy_document.secrets_read.json,
    data.aws_iam_policy_document.kms_data.json,
  ]

  statement {
    sid       = "ListOrchestrationBuckets"
    effect    = "Allow"
    actions   = ["s3:ListBucket", "s3:GetBucketLocation"]
    resources = [for k in ["ops", "raw", "batch", "quarantine", "archive"] : local.bucket_arns[k]]
  }

  # Remote task logs, dbt artefacts and batch populations/outcomes are Airflow's
  # own data.
  statement {
    sid       = "WriteAirflowOwnedObjects"
    effect    = "Allow"
    actions   = local.s3_object_write_actions
    resources = ["${local.bucket_arns.ops}/*", "${local.bucket_arns.batch}/*"]
  }

  # Loads and reconciliation read raw/quarantine/archive; Airflow must not be able
  # to rewrite the bytes it is reconciling against.
  statement {
    sid       = "ReadIngestedObjects"
    effect    = "Allow"
    actions   = local.s3_object_read_actions
    resources = ["${local.bucket_arns.raw}/*", "${local.bucket_arns.quarantine}/*", "${local.bucket_arns.archive}/*"]
  }
}

# ------------------------------------------------------------------ simulator --

data "aws_iam_policy_document" "simulator" {
  source_policy_documents = [
    data.aws_iam_policy_document.secrets_read.json,
    data.aws_iam_policy_document.kms_data.json,
  ]

  # The simulator drops files onto the containerised SFTP server and calls the
  # webhook endpoint. It gets a landing-bucket write path for the legacy-report
  # extract only, and never reads platform data — it must stay ignorant of the
  # ingestion side (CLAUDE.md hard rule 1).
  statement {
    sid       = "ListLanding"
    effect    = "Allow"
    actions   = ["s3:ListBucket", "s3:GetBucketLocation"]
    resources = [local.bucket_arns.landing]
  }

  statement {
    sid       = "WriteSimulatedFiles"
    effect    = "Allow"
    actions   = ["s3:PutObject", "s3:PutObjectTagging", "s3:AbortMultipartUpload"]
    resources = ["${local.bucket_arns.landing}/*"]
  }
}

# -------------------------------------------------------------- observability --

# Loki and Tempo share the ops bucket, separated by prefix. The `s3:prefix`
# condition on ListBucket is what stops one from enumerating the other's chunks
# (and Airflow's logs).
data "aws_iam_policy_document" "loki" {
  source_policy_documents = [data.aws_iam_policy_document.kms_data.json]

  statement {
    sid       = "ListLokiPrefix"
    effect    = "Allow"
    actions   = ["s3:ListBucket", "s3:GetBucketLocation"]
    resources = [local.bucket_arns.ops]

    condition {
      test     = "StringLike"
      variable = "s3:prefix"
      values   = ["loki/*", "loki"]
    }
  }

  statement {
    sid       = "ReadWriteLokiObjects"
    effect    = "Allow"
    actions   = local.s3_object_write_actions
    resources = ["${local.bucket_arns.ops}/loki/*"]
  }
}

data "aws_iam_policy_document" "tempo" {
  source_policy_documents = [data.aws_iam_policy_document.kms_data.json]

  statement {
    sid       = "ListTempoPrefix"
    effect    = "Allow"
    actions   = ["s3:ListBucket", "s3:GetBucketLocation"]
    resources = [local.bucket_arns.ops]

    condition {
      test     = "StringLike"
      variable = "s3:prefix"
      values   = ["tempo/*", "tempo"]
    }
  }

  statement {
    sid       = "ReadWriteTempoObjects"
    effect    = "Allow"
    actions   = local.s3_object_write_actions
    resources = ["${local.bucket_arns.ops}/tempo/*"]
  }
}

data "aws_iam_policy_document" "alertmanager" {
  statement {
    sid       = "PublishAlerts"
    effect    = "Allow"
    actions   = ["sns:Publish"]
    resources = [local.sns_alerts_arn]
  }
}

# ------------------------------------------------------------------------ MSK --
#
# `kafka-cluster:` actions, scoped to the cluster / topic / group ARNs. Not
# `kafka:*` — that is the control plane (create/delete clusters), which no pod
# needs.

data "aws_iam_policy_document" "msk_write_only" {
  statement {
    sid       = "ConnectToCluster"
    effect    = "Allow"
    actions   = ["kafka-cluster:Connect", "kafka-cluster:DescribeCluster"]
    resources = [local.msk_cluster_arn]
  }

  statement {
    sid       = "ProduceOnly"
    effect    = "Allow"
    actions   = ["kafka-cluster:DescribeTopic", "kafka-cluster:WriteData"]
    resources = [local.msk_topic_arn]
  }
}

data "aws_iam_policy_document" "msk_read_write" {
  source_policy_documents = [data.aws_iam_policy_document.msk_write_only.json]

  statement {
    sid       = "Consume"
    effect    = "Allow"
    actions   = ["kafka-cluster:ReadData"]
    resources = [local.msk_topic_arn]
  }

  statement {
    sid       = "JoinConsumerGroups"
    effect    = "Allow"
    actions   = ["kafka-cluster:AlterGroup", "kafka-cluster:DescribeGroup"]
    resources = [local.msk_group_arn]
  }
}

# Connect owns its three internal topics and the CDC topics, so it needs topic
# creation and dynamic-config rights that no service gets. Deletion is withheld:
# a Connect misconfiguration should fail, not silently drop a CDC topic.
data "aws_iam_policy_document" "msk_admin" {
  source_policy_documents = [data.aws_iam_policy_document.msk_read_write.json]

  statement {
    sid    = "ManageTopics"
    effect = "Allow"
    actions = [
      "kafka-cluster:CreateTopic",
      "kafka-cluster:AlterTopic",
      "kafka-cluster:AlterTopicDynamicConfiguration",
      "kafka-cluster:DescribeTopicDynamicConfiguration",
    ]
    resources = [local.msk_topic_arn]
  }

  statement {
    sid       = "DescribeClusterConfig"
    effect    = "Allow"
    actions   = ["kafka-cluster:DescribeClusterDynamicConfiguration"]
    resources = [local.msk_cluster_arn]
  }
}
