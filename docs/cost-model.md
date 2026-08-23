# Cost model

Estimates from plan §2 / ADR-0010, with an empty **actuals** column to be filled from
`make cost-report`. Until that column has numbers in it, everything here is a projection — the point of
keeping the two side by side is that an uncorrected model quietly stops being used.

Region `eu-west-1`, single environment `dev`, resource prefix `colx`.

Hard limits: **AWS Budget $450/mo** (50/80/100% actual + forecast → SNS) and the **Snowflake
`COLX_MONTHLY` resource monitor at 50 credits/mo**, which suspends warehouses at 100%.

Identity note: this table has **no Cognito row**. Identity moved to self-hosted Keycloak on 2026-08-23
(plan FND-6), which costs ≈ $0 incremental — it runs on nodes that are already paid for and a database
on the existing `colx-dev-platform` instance. The ~$12/mo Cognito M2M line is gone.

---

## Everything running

| Component | What drives the cost | Estimate / mo | Actual / mo |
|---|---|---:|---:|
| EKS control plane | $0.10/h, fixed, cannot be stopped | $73 | |
| EKS data plane | 3 × `t3.large` on-demand (~$0.0928/h each) | $200 | |
| MSK Provisioned | 2 × `kafka.t3.small` + storage | $80 | |
| RDS Postgres | `colx-dev-platform` db.t4g.small + `colx-dev-corebank` db.t4g.micro, single-AZ, 7-day backups | $50 | |
| NAT gateway | 1 NAT, hourly + data processing | $40 | |
| KMS + Secrets Manager | 4 CMKs @ $1 + ~8 secrets @ $0.40 + API calls | $12 | |
| S3 | 7 buckets, dev volumes, versioning on `raw`/`archive` | $5 | |
| ECR | 6 repos, keep-10 lifecycle | $3 | |
| CloudWatch | EKS control-plane logs (3 types, 7-day retention) | $5 | |
| EBS | Prometheus 20Gi + Loki 10Gi + Tempo 5Gi gp3 | $4 | |
| Data transfer | S3 gateway endpoint keeps ingestion off the NAT | $3 | |
| **AWS subtotal** | | **$475** | |
| Keycloak | Runs on existing nodes; `keycloak` DB on the existing RDS instance | **$0** | |
| Snowflake | Enterprise, 3 × XSMALL @ 60 s auto-suspend, 50-credit cap. $0 idle | $60–90 active | |
| **Total** | | **$535–565** | |

The plan quotes $540–575; this table lands slightly lower because Cognito is gone and Keycloak is free
at the margin.

### What is actually irreducible

Three line items ignore every lever short of destroying them, and together they are the $230 floor:

| Component | Why it cannot be stopped |
|---|---|
| EKS control plane ($73) | AWS runs it; there is no stop, only delete |
| MSK brokers ($80) | Provisioned brokers bill whether or not a topic is written |
| NAT gateway ($40) | Hourly charge regardless of traffic |

Everything else — the ~$200 of node capacity and the $50 of RDS — is switchable, which is why `stop`
targets exactly those two.

---

## Teardown levers

| Lever | Command | Cost / mo | Rebuild | What it does |
|---|---|---:|---|---|
| Everything running | — | $535–565 | — | |
| **stop** | `make stop` | **~$230** | minutes | Node group → 0, both RDS stopped |
| **destroy-heavy** | `make destroy-heavy` | **~$60** | ~60 min | Destroys stack `30-eks` and the MSK module in `20-data` |
| Full destroy | manual, reverse order | **<$5** | ~60 min | Everything except `00-bootstrap` (state bucket, OIDC roles, budgets) |

| Lever | Actual measured cost | Actual measured rebuild |
|---|---:|---|
| stop | | |
| destroy-heavy | | |
| full destroy | | |

Two caveats that change behaviour:

- **`stop` expires.** AWS force-starts a stopped RDS instance after **7 days**. A pause longer than
  that must be `destroy-heavy`, or the savings silently end mid-week.
- **`destroy-heavy` is cheap because it is honest.** It destroys the two most expensive things and
  keeps every piece of state: S3, both RDS instances, CMKs, ECR, the Keycloak database, Terraform
  state, Secrets Manager. What is lost is compute and MSK topic *data*, both of which are rebuilt
  declaratively.

---

## Snowflake

| Item | Estimate | Actual |
|---|---:|---:|
| Credits/mo, active development | 20–30 | |
| Credits/mo, idle (auto-suspend 60 s) | < 1 | |
| $/credit, Enterprise on AWS eu-west-1 | ~$3 | |
| Storage (compressed, dev volumes) | < $5 | |
| **Cap** | **50 credits (hard, suspends at 100%)** | |

The cap is a hard limit, not an alarm: at 100% the warehouses suspend and the day's DAG fails
(ADR-0008). The documented escape hatch, if sustained usage exceeds ~40 credits/mo, is to drop to
Standard edition and replace native masking with secure views — cheaper per credit, and materially
harder to verify mechanically, which is why it is a hatch and not the default.

Trial timing: the account is created and stack `40-snowflake` applied at **Phase 6 kickoff**
(plan §6.10). Applying earlier burns trial days on phases that do not need a warehouse.

---

## Cost per phase (rough)

Useful for deciding when to leave things running. Assumes `stop` overnight and at weekends, i.e. roughly
50 running hours a week.

| Phase | Weeks | What must be up | ~Cost |
|---|---|---|---|
| 2 — cloud foundation | 2 | Everything, most of the time | $300 |
| 3 — platform libraries | 2 | Nothing (compose stack is local) | $60 |
| 4 — simulator | 1 | EKS + RDS + MSK | $130 |
| 5 — ingestion | 3 | Everything | $450 |
| 6 — analytics | 2 | Everything + Snowflake active | $400 |
| 7–11 — services & decisioning | 8 | EKS + RDS, MSK part-time | $900 |
| 12 — UI | 2 | Everything + CloudFront + a domain | $350 |
| 13–14 — ML & hardening | 3 | Everything | $500 |

That totals roughly **$3,100 across the build** with disciplined `stop` usage, against roughly
**$12,000** if everything ran continuously for a year. The discipline, not the architecture, is what
saves the money — which is why `make stop` is a habit in `docs/conventions.md` §7 and not a nice idea.

---

## How to fill in the actuals

```bash
make cost-report MONTH=2026-08                                    # prints markdown
make cost-report MONTH=2026-08 --out /tmp/aug.md                  # or write a file
```

`scripts/cost/report.sh` groups AWS `UnblendedCost` by the `stack` tag and queries
`SNOWFLAKE.ACCOUNT_USAGE.WAREHOUSE_METERING_HISTORY` for credits. Paste the rows into the actuals
columns above.

Reading the output honestly:

- Cost Explorer lags ~24 hours, and the current month is partial by definition.
- There is always an `(untagged)` row — data transfer and some charges carry no resource tag. A large
  value there is a finding, not noise.
- A lever's effect appears T+1 day in Cost Explorer and T+1 month in the invoice. If the daily figure
  has not moved 48 hours after a `make stop`, something did not stop; the reconciliation commands are
  in `docs/runbooks/cost-and-teardown.md`.
