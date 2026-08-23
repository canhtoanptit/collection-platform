# stacks/30-eks — the EKS cluster and the IRSA role set (plan FND-7).
#
# State key: stacks/30-eks.tfstate. Applied by CI only (ADR-0010); agents run
# `make tf-plan STACK=30-eks ENV=dev` at most.
#
# Upstream contract (see README for the full table): stack 10-network must export
# `vpc_id` and `private_subnet_ids`; stack 20-data must export `msk_cluster_arn`.
# Everything else is derived from the naming convention or looked up by alias.

data "aws_caller_identity" "current" {}

data "aws_partition" "current" {}

data "terraform_remote_state" "network" {
  backend = "s3"

  config = {
    bucket = var.state_bucket
    key    = var.network_state_key
    region = coalesce(var.state_region, var.region)
  }
}

data "terraform_remote_state" "data" {
  backend = "s3"

  config = {
    bucket = var.state_bucket
    key    = var.data_state_key
    region = coalesce(var.state_region, var.region)
  }
}

# CMKs are looked up by alias rather than through 20-data's outputs: the alias
# names are part of the naming convention (plan FND-3), so the lookup cannot drift
# when an output is renamed, and an alias that does not exist fails the plan with
# a clear message.
data "aws_kms_alias" "data" {
  name = "alias/${local.name}-data"
}

data "aws_kms_alias" "secrets" {
  name = "alias/${local.name}-secrets"
}

locals {
  account_id = data.aws_caller_identity.current.account_id
  partition  = data.aws_partition.current.partition

  name         = "${var.project}-${var.env}"
  cluster_name = coalesce(var.cluster_name, local.name)

  kms_key_arns = {
    data    = data.aws_kms_alias.data.target_key_arn
    secrets = data.aws_kms_alias.secrets.target_key_arn
  }

  # `kafka-cluster:` resource ARNs. The topic/group ARNs share the cluster ARN's
  # generated UUID, so they are derived from it by substituting the resource type
  # rather than reconstructed from parts.
  msk_cluster_arn = coalesce(var.msk_cluster_arn, try(data.terraform_remote_state.data.outputs.msk_cluster_arn, null))
  msk_topic_arn   = coalesce(var.msk_topic_arn, "${replace(local.msk_cluster_arn, ":cluster/", ":topic/")}/*")
  msk_group_arn   = coalesce(var.msk_group_arn, "${replace(local.msk_cluster_arn, ":cluster/", ":group/")}/*")

  cluster_admin_policy_arn = "arn:${local.partition}:eks::aws:cluster-access-policy/AmazonEKSClusterAdminPolicy"

  access_entries = {
    # The human operator.
    operator = {
      principal_arn = var.admin_principal_arn
      policy_associations = {
        admin = {
          policy_arn   = local.cluster_admin_policy_arn
          access_scope = { type = "cluster" }
        }
      }
    }

    # DEV NOTE: `colx-gha-eks-deploy` is cluster-admin because helmfile installs
    # CRDs (Prometheus operator, external-secrets), creates namespaces and binds
    # cluster roles — all of which need cluster scope. In prod this splits into a
    # namespace-scoped editor plus a separate, human-approved CRD/upgrade path;
    # see README "prod deltas".
    gha_deploy = {
      principal_arn = "arn:${local.partition}:iam::${local.account_id}:role/${var.gha_deploy_role_name}"
      policy_associations = {
        admin = {
          policy_arn   = local.cluster_admin_policy_arn
          access_scope = { type = "cluster" }
        }
      }
    }
  }

  # Namespace/service-account pairs. These MUST match the `serviceAccount.name`
  # set in `deployment/values/<release>/dev.yaml`; scripts/verify/INF-B.sh asserts
  # the two agree, because a mismatch fails at runtime with an opaque AWS error
  # rather than at deploy time.
  irsa_roles = merge(
    {
      external-secrets = {
        namespace   = "platform"
        policy_json = data.aws_iam_policy_document.external_secrets.json
        description = "External Secrets Operator: read colx/${var.env}/* from Secrets Manager"
      }

      ingestion-cp = {
        namespace   = "ingestion"
        policy_json = data.aws_iam_policy_document.ingestion_cp.json
        description = "Ingestion control plane: landing/raw/quarantine/archive + MSK read-write"
      }

      sftp-worker = {
        namespace   = "sftp"
        policy_json = data.aws_iam_policy_document.sftp_worker.json
        description = "SFTP worker: write landed files only"
      }

      webhook-receiver = {
        namespace   = "ingestion"
        policy_json = data.aws_iam_policy_document.webhook_receiver.json
        description = "Webhook receiver: produce to MSK, read the HMAC secret"
      }

      kafka-connect = {
        namespace   = "kafka"
        policy_json = data.aws_iam_policy_document.kafka_connect.json
        description = "Debezium/Kafka Connect: MSK topic admin + Aiven S3 sink to raw/"
      }

      airflow = {
        namespace   = "airflow"
        policy_json = data.aws_iam_policy_document.airflow.json
        description = "Airflow: remote logs + batch artefacts (write), ingested data (read)"
        # The official chart creates one service account per component. All of
        # them need the same AWS permissions; each is listed explicitly rather
        # than trusting `system:serviceaccount:airflow:*`.
        extra_service_accounts = [
          "airflow-scheduler",
          "airflow-webserver",
          "airflow-worker",
          "airflow-triggerer",
          "airflow-migrate-database-job",
          "airflow-create-user-job",
          "airflow-cleanup",
        ]
      }

      simulator = {
        namespace   = "simulator"
        policy_json = data.aws_iam_policy_document.simulator.json
        description = "Source-system simulator: write simulated files only"
      }

      loki = {
        namespace   = "platform"
        policy_json = data.aws_iam_policy_document.loki.json
        description = "Loki: s3://${local.bucket_names.ops}/loki/*"
      }

      tempo = {
        namespace   = "platform"
        policy_json = data.aws_iam_policy_document.tempo.json
        description = "Tempo: s3://${local.bucket_names.ops}/tempo/*"
      }

      alertmanager = {
        namespace   = "platform"
        policy_json = data.aws_iam_policy_document.alertmanager.json
        description = "Alertmanager: sns:Publish to ${local.name}-alerts"
      }
    },
    var.enable_alb_controller_role ? {
      # Deliberately created with NO policy attached. The trust relationship is
      # what takes time to get right; the permissions arrive in Phase 12 (INF-14)
      # together with a domain and the ALB ingress flag (ADR-0011). Until then
      # this role can assume nothing, which is the point.
      alb-controller = {
        namespace       = "kube-system"
        service_account = "aws-load-balancer-controller"
        description     = "AWS Load Balancer Controller — UNATTACHED until Phase 12 ingress (ADR-0011)"
      }
    } : {}
  )
}

module "eks" {
  source = "../../modules/eks"

  name               = local.cluster_name
  kubernetes_version = var.kubernetes_version

  vpc_id     = data.terraform_remote_state.network.outputs.vpc_id
  subnet_ids = data.terraform_remote_state.network.outputs.private_subnet_ids

  authentication_mode = "API"
  access_entries      = local.access_entries

  endpoint_private_access      = true
  endpoint_public_access       = true
  endpoint_public_access_cidrs = var.admin_cidrs

  cluster_encryption_kms_key_arn = local.kms_key_arns.secrets

  node_instance_types = var.node_instance_types
  node_min_size       = var.node_min_size
  node_desired_size   = var.node_desired_size
  node_max_size       = var.node_max_size

  addon_versions = var.addon_versions
}

module "irsa" {
  source = "../../modules/irsa-role"

  name_prefix       = local.name
  oidc_provider_arn = module.eks.oidc_provider_arn
  oidc_provider     = module.eks.oidc_provider

  roles = local.irsa_roles
}
