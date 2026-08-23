# Amazon MSK Provisioned, IAM authentication only, TLS everywhere.
#
# Provisioned rather than Serverless on cost alone: ~$80/mo for two kafka.t3.small brokers against
# ~$550/mo for Serverless at this shape (ADR-0004). The trade is a fixed partition budget --
# roughly 300 partitions per broker on t3.small -- which deployment/kafka/topics.yaml documents and
# stays well inside.
#
# There is exactly one authentication path: SASL/IAM over TLS on port 9098. No SCRAM, no mutual
# TLS, no unauthenticated listener. That means every client needs `aws-msk-iam-auth` and an IRSA
# role, and there is no password anywhere in the system to leak or rotate.

locals {
  # Rendered as a Java properties document, sorted so an unrelated map reordering cannot produce a
  # new configuration revision.
  server_properties = join("\n", [
    for k in sort(keys(var.server_properties)) : "${k}=${var.server_properties[k]}"
  ])

  # Ports clients need to reach. 9098 is SASL/IAM over TLS -- the only broker listener enabled.
  # 11001/11002 are the open-monitoring JMX and node exporters, which Prometheus scrapes; without
  # rules for them FND-9's MSK dashboards are permanently empty.
  client_ports = merge(
    {
      kafka_iam_tls = { from = 9098, to = 9098, description = "Kafka SASL/IAM over TLS" }
    },
    var.open_monitoring_enabled ? {
      prometheus_jmx  = { from = 11001, to = 11001, description = "Open monitoring: JMX exporter" }
      prometheus_node = { from = 11002, to = 11002, description = "Open monitoring: node exporter" }
    } : {}
  )

  sg_ingress_rules = merge([
    for port_key, port in local.client_ports : {
      for sg in var.client_security_group_ids : "${port_key}|${sg}" => {
        port_key    = port_key
        from        = port.from
        to          = port.to
        description = "${port.description} from ${sg}"
        source_sg   = sg
      }
    }
  ]...)

  cidr_ingress_rules = merge([
    for port_key, port in local.client_ports : {
      for cidr in var.client_cidr_blocks : "${port_key}|${cidr}" => {
        port_key    = port_key
        from        = port.from
        to          = port.to
        description = "${port.description} from ${cidr}"
        source_cidr = cidr
      }
    }
  ]...)
}

resource "aws_security_group" "brokers" {
  name        = "${var.cluster_name}-msk"
  description = "MSK broker access for ${var.cluster_name}"
  vpc_id      = var.vpc_id

  tags = {
    Name = "${var.cluster_name}-msk"
  }

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_vpc_security_group_ingress_rule" "clients" {
  for_each = local.sg_ingress_rules

  security_group_id = aws_security_group.brokers.id
  description       = each.value.description

  referenced_security_group_id = each.value.source_sg
  ip_protocol                  = "tcp"
  from_port                    = each.value.from
  to_port                      = each.value.to
}

resource "aws_vpc_security_group_ingress_rule" "client_cidrs" {
  for_each = local.cidr_ingress_rules

  security_group_id = aws_security_group.brokers.id
  description       = each.value.description

  cidr_ipv4   = each.value.source_cidr
  ip_protocol = "tcp"
  from_port   = each.value.from
  to_port     = each.value.to
}

# Brokers talk to each other on the replication listener. Their ENIs all sit in this group, so a
# self-referencing rule is what permits inter-broker replication; without it a two-broker cluster
# never reaches an in-sync state.
resource "aws_vpc_security_group_ingress_rule" "inter_broker" {
  security_group_id = aws_security_group.brokers.id
  description       = "Inter-broker replication within the cluster"

  referenced_security_group_id = aws_security_group.brokers.id
  ip_protocol                  = "-1"
}

# MSK brokers are a managed service on customer ENIs: they reach KMS, CloudWatch and the MSK
# control plane outbound. Restricting this to interface endpoints is a prod delta; in dev the
# brokers are in private subnets behind a NAT gateway with no inbound path.
resource "aws_vpc_security_group_egress_rule" "all" {
  security_group_id = aws_security_group.brokers.id
  description       = "Broker egress to AWS services and to peer brokers"

  cidr_ipv4   = "0.0.0.0/0"
  ip_protocol = "-1"
}

resource "aws_msk_configuration" "this" {
  name              = "${var.cluster_name}-config"
  description       = "Cluster configuration for ${var.cluster_name}"
  kafka_versions    = [var.kafka_version]
  server_properties = local.server_properties
}

resource "aws_msk_cluster" "this" {
  cluster_name           = var.cluster_name
  kafka_version          = var.kafka_version
  number_of_broker_nodes = var.number_of_broker_nodes
  enhanced_monitoring    = var.enhanced_monitoring

  broker_node_group_info {
    instance_type   = var.instance_type
    client_subnets  = var.client_subnet_ids
    security_groups = [aws_security_group.brokers.id]

    storage_info {
      ebs_storage_info {
        volume_size = var.ebs_volume_size
      }
    }
  }

  client_authentication {
    # No unauthenticated listener, no SCRAM, no mutual TLS: IAM is the only path in.
    unauthenticated = false

    sasl {
      iam   = true
      scram = false
    }
  }

  encryption_info {
    encryption_at_rest_kms_key_arn = var.kms_key_arn

    encryption_in_transit {
      # TLS-only client connections and encrypted replication traffic. "TLS_PLAINTEXT" would leave
      # a plaintext listener available, which defeats the point of IAM auth.
      client_broker = "TLS"
      in_cluster    = true
    }
  }

  configuration_info {
    arn = aws_msk_configuration.this.arn
    # Tracking latest_revision means a server_properties change is picked up by the cluster in the
    # same apply that creates the new revision.
    revision = aws_msk_configuration.this.latest_revision
  }

  open_monitoring {
    prometheus {
      jmx_exporter {
        enabled_in_broker = var.open_monitoring_enabled
      }

      node_exporter {
        enabled_in_broker = var.open_monitoring_enabled
      }
    }
  }

  dynamic "logging_info" {
    for_each = var.broker_logs_s3_bucket == null ? [] : [var.broker_logs_s3_bucket]

    content {
      broker_logs {
        s3 {
          enabled = true
          bucket  = logging_info.value
          prefix  = var.broker_logs_s3_prefix
        }
      }
    }
  }

  tags = {
    Name = var.cluster_name
  }
}
