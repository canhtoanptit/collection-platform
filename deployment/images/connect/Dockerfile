# colx/connect — Debezium Kafka Connect + MSK IAM auth + Aiven S3 sink + JMX exporter.
#
# This one image is three things (ADR-0006):
#   1. the CDC runtime (Debezium source connectors against colx-dev-corebank),
#   2. the Kafka -> S3 sink (Aiven's Apache-2.0 connector, so raw/ gets the CDC
#      and webhook streams without a second runtime),
#   3. the "toolbox" the declarative topic-apply Job runs in (FND-5), because it
#      already has kafka-topics.sh *and* the IAM auth jar.
#
# Every artefact URL below was verified reachable (HTTP 200) when this file was
# written. Versions are ARGs so a bump is one build flag, but the defaults are
# exact — nothing here resolves "latest".

# quay.io/debezium/connect:3.6.1.Final — the current stable tag.
ARG DEBEZIUM_VERSION=3.6.1.Final
FROM quay.io/debezium/connect:${DEBEZIUM_VERSION}

# ---------------------------------------------------------------- versions ----
# aws-msk-iam-auth: MSK IAM SASL. Verified:
#   https://github.com/aws/aws-msk-iam-auth/releases/download/v2.3.7/aws-msk-iam-auth-2.3.7-all.jar
ARG MSK_IAM_AUTH_VERSION=2.3.7

# Aiven S3 sink (Apache-2.0 — the reason we use it rather than Confluent's).
# Verified:
#   https://github.com/Aiven-Open/s3-connector-for-apache-kafka/releases/download/v2.15.0/s3-connector-for-apache-kafka-2.15.0.zip
ARG AIVEN_S3_SINK_VERSION=2.15.0

# JMX -> Prometheus, so Connect and Debezium metrics reach the same Prometheus as
# everything else (ADR-0015). Verified:
#   https://repo1.maven.org/maven2/io/prometheus/jmx/jmx_prometheus_javaagent/1.0.1/jmx_prometheus_javaagent-1.0.1.jar
ARG JMX_EXPORTER_VERSION=1.0.1

USER root

RUN set -eux; \
	microdnf install -y unzip || (yum install -y unzip); \
	microdnf clean all 2>/dev/null || yum clean all 2>/dev/null || true

# ------------------------------------------------------------ MSK IAM auth ----
# Goes on the Connect worker classpath (libs), not in a plugin dir: it is a SASL
# login module used by the worker's own producer/consumer as well as by every
# connector, and a plugin-dir jar is invisible to the worker itself.
RUN set -eux; \
	curl -fsSL -o /kafka/libs/aws-msk-iam-auth-${MSK_IAM_AUTH_VERSION}-all.jar \
	"https://github.com/aws/aws-msk-iam-auth/releases/download/v${MSK_IAM_AUTH_VERSION}/aws-msk-iam-auth-${MSK_IAM_AUTH_VERSION}-all.jar"; \
	test -s /kafka/libs/aws-msk-iam-auth-${MSK_IAM_AUTH_VERSION}-all.jar

# ----------------------------------------------------------- Aiven S3 sink ----
RUN set -eux; \
	mkdir -p /kafka/connect/aiven-s3-sink; \
	curl -fsSL -o /tmp/s3-sink.zip \
	"https://github.com/Aiven-Open/s3-connector-for-apache-kafka/releases/download/v${AIVEN_S3_SINK_VERSION}/s3-connector-for-apache-kafka-${AIVEN_S3_SINK_VERSION}.zip"; \
	unzip -q /tmp/s3-sink.zip -d /tmp/s3-sink; \
	find /tmp/s3-sink -name '*.jar' -exec cp {} /kafka/connect/aiven-s3-sink/ \; ; \
	rm -rf /tmp/s3-sink /tmp/s3-sink.zip; \
	ls -1 /kafka/connect/aiven-s3-sink | head; \
	test "$(ls -1 /kafka/connect/aiven-s3-sink | wc -l)" -gt 0

# --------------------------------------------------------- JMX -> Prometheus ---
RUN set -eux; \
	mkdir -p /opt/jmx; \
	curl -fsSL -o /opt/jmx/jmx_prometheus_javaagent.jar \
	"https://repo1.maven.org/maven2/io/prometheus/jmx/jmx_prometheus_javaagent/${JMX_EXPORTER_VERSION}/jmx_prometheus_javaagent-${JMX_EXPORTER_VERSION}.jar"; \
	test -s /opt/jmx/jmx_prometheus_javaagent.jar

# Minimal config: expose everything, relabel nothing. Connect's MBean names are
# already dimensioned by connector and task, and a hand-written rule set here
# would have to be kept in step with two connector versions.
COPY jmx-exporter.yaml /opt/jmx/config.yaml

# The agent is opt-in via KAFKA_OPTS so the same image can run the topic-apply
# Job without also opening a metrics port:
#
#   KAFKA_OPTS=-javaagent:/opt/jmx/jmx_prometheus_javaagent.jar=9404:/opt/jmx/config.yaml
#
EXPOSE 9404

USER 1001
