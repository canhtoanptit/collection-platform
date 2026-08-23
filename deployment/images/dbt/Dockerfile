# colx/dbt — dbt Core + the Snowflake adapter, invoked by Airflow (ADR-0008).
#
# dbt Core in our own image rather than dbt Cloud, so CI runs locally and the
# artefacts (manifest.json, run_results.json) land in `s3://colx-dev-ops/dbt-artifacts`
# where slim CI can use them.
#
# No project files in the image: `data/dbt/collections/` is mounted or cloned at
# run time, the same way DAGs are git-synced. A dbt model change must not need an
# image build.

FROM python:3.12-slim

ARG DBT_SNOWFLAKE_VERSION=1.12.0

ENV PYTHONDONTWRITEBYTECODE=1 \
	PYTHONUNBUFFERED=1 \
	DBT_PROFILES_DIR=/dbt/profiles \
	DBT_PROJECT_DIR=/dbt/project

# git: dbt deps fetches packages from git. Nothing else — this container holds a
# Snowflake key-pair at run time, so every extra package is attack surface.
RUN set -eux; \
	apt-get update; \
	apt-get install -y --no-install-recommends git ca-certificates; \
	rm -rf /var/lib/apt/lists/*

# dbt-snowflake pulls dbt-core and dbt-adapters transitively; pinning the adapter
# pins the whole set for a given release, and pinning dbt-core separately is how
# people end up with an adapter/core mismatch.
RUN set -eux; \
	pip install --no-cache-dir "dbt-snowflake==${DBT_SNOWFLAKE_VERSION}"; \
	dbt --version

# Non-root. dbt writes target/ and logs/, so both live under a writable home.
RUN set -eux; \
	useradd --create-home --uid 1001 dbt; \
	mkdir -p /dbt/project /dbt/profiles; \
	chown -R dbt:dbt /dbt

USER dbt
WORKDIR /dbt/project

# `profiles.yml` is supplied at run time from an ExternalSecret: it carries the
# Snowflake account, user and the path to the key-pair private key. There is
# nothing credential-shaped in this image.
ENTRYPOINT ["dbt"]
CMD ["--help"]
