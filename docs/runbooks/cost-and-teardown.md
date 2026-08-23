# Runbook — cost control and teardown

The environment is real, metered AWS plus a real Snowflake account. This runbook is how it gets turned
down, turned back up, and rebuilt from nothing. Levers and prices: `docs/cost-model.md`.

Related: ADR-0010 (five stacks, CI-only applies, cost levers), `docs/conventions.md` §7,
`scripts/cost/{stop,start,report}.sh`, `scripts/dr/README.md` (XCT-1).

---

## Owner

Platform / infrastructure owner — `@canhtoanptit`.

There is one owner, which is the actual risk this runbook manages: nothing here reminds anyone. The
AWS Budget and the Snowflake resource monitor are the only automated backstops, and they are alarms
(and one hard cap), not brakes.

## Support group

Self-supported. Escalation is to AWS Support (Basic — no technical support, so a broken control plane
is a rebuild, not a ticket) and Snowflake trial support.

CI failures on `terraform` / `helmfile` / `images` go to the same owner via the GitHub `dev`
environment's required-reviewer notification.

## SLA

Not a production system; no availability SLA. The commitments that matter here are *time* commitments,
because they are what make the cost levers usable:

| Action | Target | Measured |
|---|---|---|
| `make stop` completes | 5 min | node group scale-down is immediate; RDS stop ~2-3 min |
| `make start` to usable environment | 15 min | RDS 5-10 min, then nodes ~3 min, then pods ~2 min |
| `make destroy-heavy` completes | 25 min | EKS destroy ~15 min, MSK ~10 min |
| `make up-all` after destroy-heavy | 60 min | EKS create ~15 min, MSK ~25 min, helmfile ~10 min |
| Budget alert to owner's inbox | < 24 h of threshold | AWS Budgets evaluates daily |

Anything materially slower than the table is a finding: record the real number in
`docs/cost-model.md` rather than leaving the estimate in place.

## Expected schedule

| When | Do |
|---|---|
| End of any day a work stream pauses | `make stop` |
| Start of the next working session | `make start` |
| Pause longer than ~3 days | `make destroy-heavy` |
| Pause longer than ~7 days | `make destroy-heavy` — **required**, not preferred: AWS force-starts a stopped RDS instance after 7 days, so `stop` silently stops saving |
| Monthly, first working day | `make cost-report MONTH=<previous>` and paste into `docs/cost-model.md` |
| Before any long backfill or dbt full refresh | Check Snowflake credits used this month against the 50-credit cap |

## Alert policy

| Alert | Source | Threshold | Goes to |
|---|---|---|---|
| AWS Budget actual | AWS Budgets → SNS `colx-dev-alerts` | 50% / 80% / 100% of $450/mo | owner email |
| AWS Budget forecast | AWS Budgets → SNS | forecast > 100% | owner email |
| Cost anomaly | AWS Cost Anomaly Detection → SNS | service-level anomaly | owner email |
| Snowflake credits | `COLX_MONTHLY` resource monitor | notify 50% / 80%, **suspend at 100%** | Snowflake notification + suspended warehouses |
| Node NotReady, PV > 80%, CrashLoopBackOff | `deployment/observability/alerts/platform-rules.yaml` → Alertmanager → SNS | see the rule file | owner email |
| Deadman (`ColxAlertingPipelineDeadman`, `Watchdog`) | same | always firing | owner email hourly |

Two of these are load-bearing in opposite directions:

- The **Snowflake monitor is a hard cap**, not an alert. At 100% the warehouses suspend and the day's
  DAG fails. That is a deliberate availability-for-cost trade (ADR-0008).
- The **deadman alerts are only useful by their absence.** If the hourly email stops arriving, the
  alerting path is broken — and a silent alerting path looks exactly like good news. Do not silence
  them, do not "fix" them.

Expected alert noise: during `make stop`, `ColxNodeUnreachable` and `ColxPodCrashLooping` will fire.
Silence them for the duration or accept the mail; do not delete the rules.

## Retry policy

| Failure | Retry |
|---|---|
| `make stop` partially applied (nodes scaled, RDS stop failed) | Re-run. Both scripts are idempotent: scaling a node group already at 0 and stopping an already-stopped instance are no-ops. |
| `make start` times out waiting for nodes | Re-run. If it fails twice, check the node group's health in the EKS console — a full AZ or an instance-type shortage looks identical to a slow start. |
| `terraform apply` fails mid-way | Re-run the workflow. Terraform is idempotent; the state lock (`use_lockfile`, S3 conditional write) prevents a concurrent second attempt. **Never** run `terraform apply` locally to "unstick" it (ADR-0010). |
| State lock held by a dead run | Confirm no workflow is running, then `terraform force-unlock <id>` — in CI, with the apply role. This is the one destructive-adjacent command in this runbook; getting it wrong while a real apply is in flight corrupts state. |
| `helmfile apply` leaves a release `pending-upgrade` | `helm -n <ns> rollback <release>`, then re-run `helmfile apply`. Caused by cancelling a run mid-`helm upgrade`, which is why the workflow's concurrency group has `cancel-in-progress: false`. |
| `up-all` fails at the helmfile step | Re-run just that step. Releases are ordered by `needs:`; a failure part-way leaves the earlier releases installed and healthy. |
| ESO Secret never appears | Check the ClusterSecretStore is `Ready` and that the Secrets Manager entry exists with the exact keys listed in `deployment/values/external-secrets/externalsecrets.yaml`. A missing *key* fails the same way as a missing secret. |

Do not retry: a `destroy-heavy` that failed part-way. Read the plan, then decide — a partial destroy
plus a blind retry is how the RDS instance holding the Airflow metadata DB gets deleted.

## Escalation

1. **Cost above budget with no explanation** → `make cost-report`, find the stack tag, then
   `make stop` first and investigate second. An unexplained bill is not a debugging session, it is a
   stop-loss.
2. **Snowflake suspended at 100% credits mid-phase** → do not raise the quota reflexively. Check
   `WAREHOUSE_METERING_HISTORY` for the query that burned them (usually a `--full-refresh` or a
   missing incremental filter). The escape hatch of last resort is documented in ADR-0008: drop to
   Standard edition plus secure views.
3. **EKS control plane or MSK unrecoverable** → `destroy-heavy` then `up-all`. This is supported and
   exercised; do not spend hours repairing something whose whole design assumption is that it is
   rebuildable.
4. **Terraform state lost or corrupted** → the state bucket is versioned. Restore the previous object
   version, then plan before anything else. Recreating state with `terraform import` across five
   stacks is a multi-day job, so treat versioning as the actual control.
5. **Secrets Manager values lost** → they are out-of-band by design and not in git. Regenerate
   (Snowflake key pairs, Keycloak client secrets, Grafana/Airflow admins), update the entries, restart
   the consuming pods. `infrastructure/terraform/stacks/40-snowflake/README.md` has the key-generation
   commands.

## Reconciliation

Cost work has the same rule as pipeline work: success is not evidence (D§38). After every lever, assert
the observable outcome.

**After `make stop`:**

```bash
aws eks describe-nodegroup --cluster-name colx-dev --nodegroup-name default \
  --query 'nodegroup.scalingConfig'                       # desiredSize == 0
for db in colx-dev-platform colx-dev-corebank; do
  aws rds describe-db-instances --db-instance-identifier "$db" \
    --query 'DBInstances[0].DBInstanceStatus'             # "stopped"
done
kubectl get nodes                                         # no resources found
```

**After `make start`:**

```bash
kubectl get nodes                                         # >= 2 Ready
kubectl get pods -A | grep -Ev 'Running|Completed'        # empty
kubectl -n platform get clustersecretstore colx-dev       # Ready
make grafana                                              # answers on :3000
make airflow                                              # trigger platform_smoke -> success
```

**After `make up-all`:**

```bash
helmfile -f deployment/helmfile.yaml -e dev diff           # empty (FND-8 acceptance)
kubectl get ns                                             # 7 colx namespaces
kubectl get sa -A -o json | jq -r '.items[]
  | select(.metadata.annotations["eks.amazonaws.com/role-arn"])
  | "\(.metadata.namespace)/\(.metadata.name)"'            # >= 6 annotated
```

**Monthly:** `make cost-report MONTH=<prev>`, then compare each row against the estimate in
`docs/cost-model.md`. A row more than 25% off its estimate gets the estimate corrected — the table is
a model, and an uncorrected model stops being used.

The one identity worth checking by hand: `stop` should move the *next* month's bill toward $230, not
this month's. Savings show up T+1 day in Cost Explorer and T+1 month in the invoice; a bill that has
not moved 48 hours after a stop means something did not actually stop.

## Runbook steps

### Daily: stop

```bash
make stop                    # ~5 min, ~$230/mo floor
```

Scales the managed node group to 0 and stops both RDS instances. Pods are deleted, not paused —
everything is declarative, so `make start` lets them reschedule.

### Daily: start

```bash
make start                   # ~15 min
```

RDS **first** (waited to `available`), then the node group, then a poll until at least 2 nodes are
Ready. The order is the point: nodes first means every database-dependent pod CrashLoopBackOffs for
the ten minutes RDS takes, and you spend the morning reading logs about a problem that fixes itself.

### Longer pause: destroy-heavy

```bash
make destroy-heavy           # ~25 min, ~$60/mo floor, prompts for the cluster name
```

Destroys stack `30-eks` entirely and the MSK module in `20-data` by `-target`. Targeted rather than a
full `20-data` destroy because the RDS instances, buckets and CMKs in that stack must survive.

**Survives:** S3 (`raw` and `archive` versioned), both RDS instances, KMS CMKs, ECR images, the
Keycloak database, Terraform state, Secrets Manager values, the Snowflake account.

**Lost:** every pod, every Helm release, MSK topic *data* (topic definitions are re-applied from
`deployment/kafka/topics.yaml`), Prometheus metrics history, Loki/Tempo local WALs (their S3 blocks
survive).

### Rebuild

```bash
make up-all                  # ~60 min
```

Order: `10-network` → `20-data` → `30-eks` (plan only from a laptop — applies are CI-only, ADR-0010),
then kubeconfig, namespaces, `helmfile apply`, Kafka topics, smoke.

Two things need a human afterwards:

1. **MSK broker targets.** `deployment/values/kube-prometheus-stack/dev.yaml` has empty
   `additionalScrapeConfigs` targets — the broker DNS names only exist after `20-data` applies. Fill
   them in and re-apply; the acceptance check is `up{job=~".*msk.*"}` returning at least 2 series.
2. **Keycloak realm.** `--import-realm` skips a realm that already exists, so a rebuilt cluster
   pointing at a *surviving* Keycloak database will not re-import. The kcadm Job detects the drift and
   fails the deploy; the fix is in `deployment/values/keycloak/README.md`.

### Full destroy

Not a make target, on purpose — there is no one-command path to deleting the RDS instances and the
buckets. Destroy in reverse order (`30-eks`, `20-data`, `10-network`), leaving `00-bootstrap` (state
bucket, OIDC roles, budgets) alone. Under $5/mo afterwards, essentially S3 storage.

### Snowflake

Nothing to stop: three XSMALL warehouses with 60 s auto-suspend are ≈ $0 idle, and the 50-credit
monitor is the cap (ADR-0008). Stack `40-snowflake` is applied at Phase 6 kickoff, not before — see
its README for why the trial clock is the thing being conserved.

### Checking what a lever actually saved

```bash
make cost-report MONTH=2026-08              # or --out to write a file
```

Cost Explorer is T-1 and the current month is partial. Compare like-for-like months, and remember the
`(untagged)` row: data transfer carries no resource tag, and a large value there is the finding.
