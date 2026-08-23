# colx/airflow — Airflow 2.11 with the four providers this platform needs.
#
# Built and pushed by .github/workflows/images.yml; deployed by the `airflow`
# release in deployment/helmfile.yaml.
#
# ADR-0007 pins Airflow **2.11**, the last 2.x line. The 3.x migration is deferred
# work, and this image is where that decision is enforced — the Helm chart follows
# the image, not the other way round.
#
# No DAGs in the image: DAGs are git-synced from `airflow/dags` (ADR-0007), so a
# DAG fix does not need a build. The image carries providers and nothing else.

ARG AIRFLOW_VERSION=2.11.2
ARG PYTHON_VERSION=3.12

FROM apache/airflow:${AIRFLOW_VERSION}-python${PYTHON_VERSION}

# Redeclared: ARGs before FROM are not visible after it.
ARG AIRFLOW_VERSION
ARG PYTHON_VERSION

USER root

# git is needed by nothing at runtime (git-sync is a sidecar with its own image),
# so nothing is installed. Kept as a deliberate note rather than a hopeful
# `apt-get install` list: every package here is attack surface in a pod that can
# read Secrets Manager.

USER airflow

COPY requirements.txt /tmp/requirements.txt

# --constraint against Airflow's own constraints file for this exact
# version/python pair. Without it pip is free to upgrade a transitive dependency
# (or Airflow itself) to something 2.11 was never tested against — the single
# most common way a working Airflow image stops working.
RUN set -eux; \
	pip install --no-cache-dir \
	--constraint "https://raw.githubusercontent.com/apache/airflow/constraints-${AIRFLOW_VERSION}/constraints-${PYTHON_VERSION}.txt" \
	-r /tmp/requirements.txt; \
	rm -f /tmp/requirements.txt

# Fail the build rather than the cluster if a provider did not actually land.
RUN set -eux; \
	python -c "import airflow; print('airflow', airflow.__version__)"; \
	python -c "import airflow.providers.cncf.kubernetes as m; print('cncf-kubernetes ok')"; \
	python -c "import airflow.providers.snowflake as m; print('snowflake ok')"; \
	python -c "import airflow.providers.amazon as m; print('amazon ok')"; \
	python -c "import statsd; print('statsd ok')"

# S3 remote logging and Snowflake key-pair auth both use the pod's IRSA identity
# or an ESO-supplied secret; there is nothing credential-shaped to bake in.
