# Stack 20-data -- the platform's stateful substrate: keys, buckets, registries, databases and the
# Kafka cluster. Applied by CI only (ADR-0010), after 10-network.
#
# Everything here is either expensive to recreate or holds state that matters, which is why it is
# its own stack: `make destroy-heavy` (FND-13) can take 30-eks down and leave this in place.

module "kms" {
  source = "../../modules/kms"

  name_prefix = local.name_prefix

  keys = {
    data = {
      description = "S3 data lake buckets (landing, raw, quarantine, archive, ops, decision-audit, batch)"
    }
    db = {
      description = "RDS Postgres storage, automated backups and RDS-managed master passwords"
    }
    msk = {
      description = "MSK broker data at rest"
    }
    secrets = {
      description = "Secrets Manager: SFTP keys, webhook HMAC, Snowflake key-pairs, Keycloak credentials, per-database passwords"
      # The only key protecting material that cannot be cheaply regenerated.
      enable_key_rotation = true
    }
  }
}

module "s3_data" {
  source = "../../modules/s3-data"

  name_prefix = local.name_prefix
  kms_key_arn = module.kms.key_arns["data"]

  # Exactly the seven buckets standardized in plan §6.7. scripts/verify/INF-A.sh parses that
  # section and asserts this list matches it.
  buckets = {
    landing = {
      purpose       = "sftp-landing-files-as-delivered"
      force_destroy = var.s3_force_destroy
    }

    raw = {
      purpose = "canonical-raw-partitions-snowflake-copy-source"
      # Versioned: this is what analytics reconciles against, so an overwrite destroys evidence.
      versioning    = true
      force_destroy = var.s3_force_destroy
    }

    quarantine = {
      purpose       = "files-that-failed-validation"
      force_destroy = var.s3_force_destroy
    }

    archive = {
      purpose = "processed-originals"
      # Versioned: often the only remaining copy of an original file.
      versioning                = true
      expire_current_after_days = var.archive_expiration_days
      force_destroy             = var.s3_force_destroy
    }

    ops = {
      purpose = "operational-artifacts-logs-traces-dbt"
      # Retention per prefix. A backstop, not the primary control: Loki and Tempo enforce their own
      # retention, and this exists so a bad Helm values file cannot accumulate a year of objects.
      prefix_expiration_days = {
        "airflow-logs/"    = 14
        "tempo/"           = 14
        "loki/"            = 30
        "dbt-artifacts/"   = 30
        "msk-broker-logs/" = 14
      }
      force_destroy = var.s3_force_destroy
    }

    decision-audit = {
      purpose       = "decision-snapshots-append-only"
      force_destroy = var.s3_force_destroy
    }

    batch = {
      purpose       = "batch-populations-and-outcome-files"
      force_destroy = var.s3_force_destroy
    }
  }
}

module "ecr" {
  source = "../../modules/ecr"

  repositories = [
    "colx/ingestion",
    "colx/simulator",
    "colx/dbt",
    "colx/connect",
    "colx/airflow",
    "colx/services",
  ]

  scan_on_push     = true
  keep_last_images = 10
  force_delete     = var.ecr_force_delete
}

# --- databases (ADR-0003) ---------------------------------------------------------------------

# Databases `ingestion`, `airflow`, `keycloak` plus one per service, each with its own owner role.
# The databases and roles themselves are created by scripts/db/provision_databases.sh -- Terraform
# owns the instance, not its contents.
module "rds_platform" {
  source = "../../modules/rds-postgres"

  identifier           = "${local.name_prefix}-platform"
  vpc_id               = local.vpc_id
  db_subnet_group_name = local.data_subnet_group_name
  kms_key_arn          = module.kms.key_arns["db"]

  instance_class    = var.rds_platform_instance_class
  allocated_storage = var.rds_allocated_storage

  backup_retention_period = var.rds_backup_retention_days
  multi_az                = false

  ingress_security_group_ids = local.eks_client_security_group_ids
}

# The simulator's legacy-shaped CDC source. The three parameters are what make logical replication
# possible at all; all three are static, hence pending-reboot.
module "rds_corebank" {
  source = "../../modules/rds-postgres"

  identifier           = "${local.name_prefix}-corebank"
  vpc_id               = local.vpc_id
  db_subnet_group_name = local.data_subnet_group_name
  kms_key_arn          = module.kms.key_arns["db"]

  instance_class    = var.rds_corebank_instance_class
  allocated_storage = var.rds_allocated_storage

  # Safety valve, not a growth plan: an unconsumed replication slot retains WAL until the volume
  # fills, and a full volume takes db.t4g.micro down hard (ADR-0003 calls this the most dangerous
  # object in the platform). Autoscaling converts an outage into a cost surprise.
  max_allocated_storage = 50

  backup_retention_period = var.rds_backup_retention_days
  multi_az                = false

  parameters = {
    "rds.logical_replication" = { value = "1" }
    "max_replication_slots"   = { value = "5" }
    "max_wal_senders"         = { value = "10" }
  }

  ingress_security_group_ids = local.eks_client_security_group_ids
}

# --- eventing (ADR-0004) ----------------------------------------------------------------------

module "msk" {
  source = "../../modules/msk"

  cluster_name      = local.name_prefix
  vpc_id            = local.vpc_id
  client_subnet_ids = local.private_subnet_ids
  kms_key_arn       = module.kms.key_arns["msk"]

  kafka_version          = var.msk_kafka_version
  instance_type          = var.msk_instance_type
  number_of_broker_nodes = var.msk_broker_count
  ebs_volume_size        = var.msk_ebs_volume_size

  # auto.create.topics.enable=false is the important one: topics exist only if they are in
  # deployment/kafka/topics.yaml and the apply Job has run.
  server_properties = {
    "auto.create.topics.enable"  = "false"
    "default.replication.factor" = "2"
    "min.insync.replicas"        = "1"
  }

  open_monitoring_enabled = true

  # Broker log delivery is OFF by default (null). Turning it on needs two things this stack cannot
  # verify without an account: a bucket policy statement granting the AWS log-delivery principal
  # PutObject on the ops bucket, and confirmation that MSK's S3 delivery path accepts a bucket
  # encrypted with a customer managed key. Prometheus already covers broker metrics through open
  # monitoring, so the default costs nothing operationally. The ops bucket already carries a
  # 14-day lifecycle rule on the `msk-broker-logs/` prefix, ready for when it is enabled.
  broker_logs_s3_bucket = var.msk_broker_logs_bucket
  broker_logs_s3_prefix = "msk-broker-logs/"

  client_security_group_ids = local.eks_client_security_group_ids
}
