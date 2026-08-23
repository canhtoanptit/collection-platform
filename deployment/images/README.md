# `deployment/images/` — the three images this platform builds

Built and pushed by `.github/workflows/images.yml` on a change under the matching directory. Tagged
`sha-<short>` **and** `latest`; the SHA tag is the one a Helm release should reference, `latest` exists
so a `make up-all` on a fresh cluster has something to pull.

| Image | Base | Purpose |
|---|---|---|
| `colx/airflow` | `apache/airflow:2.11.2-python3.12` | Airflow with the cncf-kubernetes / snowflake / amazon providers + statsd |
| `colx/connect` | `quay.io/debezium/connect:3.6.1.Final` | Debezium CDC, the Aiven S3 sink, the JMX exporter, and the topic-apply toolbox |
| `colx/dbt` | `python:3.12-slim` | dbt Core + `dbt-snowflake` |

Three images and no more: `colx/ingestion`, `colx/simulator` and the service images are built by their
own WPs from the distroless template in `deployment/charts/`.

## Pinning

Every version is exact. Nothing resolves `latest`, and nothing uses a floating tag — a rebuild in three
weeks has to install the same bytes or `destroy-heavy` stops being a safe lever (ADR-0010).

**Artefact URLs were verified reachable (HTTP 200) when these files were written:**

| Artefact | URL |
|---|---|
| `aws-msk-iam-auth` 2.3.7 | `https://github.com/aws/aws-msk-iam-auth/releases/download/v2.3.7/aws-msk-iam-auth-2.3.7-all.jar` |
| Aiven S3 sink 2.15.0 | `https://github.com/Aiven-Open/s3-connector-for-apache-kafka/releases/download/v2.15.0/s3-connector-for-apache-kafka-2.15.0.zip` |
| `jmx_prometheus_javaagent` 1.0.1 | `https://repo1.maven.org/maven2/io/prometheus/jmx/jmx_prometheus_javaagent/1.0.1/jmx_prometheus_javaagent-1.0.1.jar` |
| `apache/airflow:2.11.2-python3.12` | Docker Hub tag exists |
| `quay.io/debezium/connect:3.6.1.Final` | Quay tag exists |
| Airflow constraints for 2.11.2 / py3.12 | `https://raw.githubusercontent.com/apache/airflow/constraints-2.11.2/constraints-3.12.txt` |

All of them are `ARG`s, so a bump is a build flag rather than an edit — but the defaults are the
verified versions, not placeholders.

## The Airflow provider trap

`deployment/images/airflow/requirements.txt` pins providers to the versions in **Airflow's own
constraints file for 2.11.2 / Python 3.12**, not to the newest release. The newest release of each
provider targets Airflow 3:

| Provider | Newest | Pinned (2.11.2 constraints) |
|---|---|---|
| `apache-airflow-providers-amazon` | 9.34.0 | **9.22.0** |
| `apache-airflow-providers-cncf-kubernetes` | 10.21.0 | **10.13.0** |
| `apache-airflow-providers-snowflake` | 6.16.0 | **6.10.0** |

Installing a newest-release provider on 2.11 either fails the resolver or — worse — installs cleanly
and breaks at DAG-parse time in the cluster. The Dockerfile also passes the constraints file to pip as
`--constraint`, so transitive dependencies are pinned too.

There is no `apache-airflow-providers-statsd`. StatsD support is the `statsd` library plus
`[metrics] statsd_on = True` (set in `deployment/values/airflow/dev.yaml`).

## Local build

The verify script does not build these — a Docker build is minutes and network, and `scripts/verify/INF-B.sh`
is meant to be runnable in seconds. Build by hand when changing one:

```bash
docker build -t colx/airflow:dev deployment/images/airflow
docker build -t colx/connect:dev deployment/images/connect
docker build -t colx/dbt:dev     deployment/images/dbt
```

Each Dockerfile ends with an assertion step (`python -c "import ..."`, `test -s <jar>`,
`dbt --version`) so a missing provider or a moved artefact fails the **build** rather than the cluster.

## First-build checklist

If a build fails on a download, the URL moved. Do not switch to `latest`:

1. Find the new version on the project's releases page.
2. `curl -sIL -o /dev/null -w '%{http_code}'` the new URL.
3. Bump the `ARG` default, note the verification in this table, rebuild.
