# `scripts/dr/` — disaster-recovery drills

**Placeholder.** This directory is filled in by **XCT-1** (Phase 14, hardening and production
readiness). Nothing here yet is deliberate, not an oversight.

## Why it is empty now

A DR script that has never been run against real data is worse than no script: it is a promise. XCT-1
owns the drills *and* the evidence that they executed, which is the only thing that makes them count
(D§90).

The pieces DR will depend on exist already, which is why the placeholder is here rather than nowhere:

| Dependency | Where it comes from | Status |
|---|---|---|
| RDS automated backups, 7-day retention | stack 20-data (INF-A) | authored |
| S3 versioning on `raw` and `archive` | stack 20-data (INF-A) | authored |
| Everything else declarative and rebuildable | Terraform + `deployment/helmfile.yaml` | authored |
| Rebuild path exercised end to end | `make destroy-heavy` then `make up-all` | authored, run in Phase 2 acceptance |
| Snowflake Time Travel | stack 40-snowflake, `data_retention_days` | authored, applied Phase 6 |

## What XCT-1 is expected to add

Scripts, each with a runbook under `docs/runbooks/` carrying the D§82 control set:

- **`restore-rds.sh`** — point-in-time restore of `colx-dev-platform` to a new instance, then repoint a
  service at it. The drill that matters is the *repoint*, not the restore.
- **`replay-from-s3.sh`** — rebuild `RAW` in Snowflake from `s3://colx-dev-raw` and re-run dbt, proving
  the warehouse is derived state and not a system of record (ADR-0008).
- **`rebuild-cluster.sh`** — `destroy-heavy` → `up-all` → smoke, timed. ADR-0010 claims ~60 minutes;
  the drill is what turns that into a measured number.
- **`dlq-replay.sh`** — drain `collections.dlq.<service>` back through the consumer after a fix
  (`tools/dlq-replay`, also XCT-1).

## Related, already here

| Path | What it does |
|---|---|
| `scripts/cost/stop.sh`, `start.sh` | The daily cost lever. Not DR, but the same "is this environment rebuildable?" muscle. |
| `docs/runbooks/cost-and-teardown.md` | Teardown and rebuild procedure, including what `destroy-heavy` destroys and what survives it. |
| `docs/cost-model.md` | Cost per teardown level, and the rebuild time each one costs. |
