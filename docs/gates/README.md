# Phase gates

A phase gate is the only thing that lets the next phase start. **No phase starts on a red gate**, and
no gate is passed by argument — only by commands that exited 0 in front of a verifier agent.

---

## 1. Where things live

```text
docs/gates/README.md              this procedure
docs/gates/gate-<n>.md            the gate for phase <n> — every line a runnable command
docs/gates/evidence/<n>/          committed output of the run that passed
```

`gate-<n>.md` is written by the lead agent when phase `<n>` is planned (its exit criteria come from the
plan's phase map), reviewed before the phase starts, and executed by a verifier agent when the phase's
WPs are all merged and reviewed.

---

## 2. Rules

1. **Every line is runnable.** A gate is a checklist of commands with expected results — `make e2e-wave1`,
   `oasdiff breaking contracts-v1.0..HEAD` returning empty, a `psql` query returning an exact count, a
   Grafana API call returning non-empty series, `bash tools/lint-runbook.sh docs/runbooks/*.md`,
   `bash scripts/verify/<WP-ID>.sh` for each WP in the phase. No line may be a judgement call like
   "dashboards look right"; state which panel, which query, which value.
2. **The verifier did not implement the WPs.** Gate verification is run by an agent that wrote none of
   the code under test (plan §4.7), using the strongest available model. It re-runs the commands itself
   rather than reading someone's summary.
3. **Evidence is committed.** Every command's actual output goes to `docs/gates/evidence/<n>/` —
   one file per command or logical group, named after it (e.g. `evidence/5/ING-8-recon-day3.txt`), plus
   `evidence/<n>/summary.md` listing each gate line, the evidence file, and PASS/FAIL. Screenshots only
   where a UI/dashboard assertion genuinely cannot be a command; the query behind the panel goes in the
   evidence file too.
4. **Expected-fail assertions are part of the gate.** Guards must be shown rejecting bad input — a
   tampered control total failing reconciliation, an `UPDATE` on `decision_audit` erroring, a mutated
   released contract failing CI, a duplicate event producing no second side effect. A gate with no
   negative test is not a gate.
5. **Partial passes do not exist.** One red line = red gate. Fix it (new commit, new review) and re-run
   the whole gate; keep the failed run's evidence alongside the passing run so the history is honest.
6. **Reproducibility.** Commands state their environment (local compose / dev EKS) and any fixture or
   business date they need. `<n>` days of simulated data means exactly that: named dates, re-runnable.
7. **Open findings block.** No unresolved code-review or adversarial finding (`docs/review-policy.md`)
   may be open on any WP in the phase.
8. **Record the decision.** Where the plan calls for it, the gate result is also written up as an ADR
   (`docs/adr/`) — e.g. the analytics gate and the MVP gate.

---

## 3. Procedure

```text
1. all WPs in the phase merged, reviewed, adversarial findings closed
2. verifier agent (did not implement) opens docs/gates/gate-<n>.md
3. runs every line top to bottom, capturing stdout/stderr to docs/gates/evidence/<n>/
4. writes evidence/<n>/summary.md: line -> evidence file -> PASS/FAIL
5. all PASS  -> report gate green; next phase may start
   any FAIL  -> report the failing line + evidence; phase stays open; no next-phase work begins
```

The verifier reports facts: what was run, what came back, what it means. A gate report that
paraphrases instead of quoting output is not accepted.

---

## 4. Key gates

| Phase | Gate asserts |
|---|---|
| 0 | Repo scaffold, conventions pack, CI skeleton green. |
| 1 | `contracts-v1.0` tagged; contracts CI green; mutated released schema fails CI (negative test). |
| 2 | Infra live, observability live, one full teardown/rebuild cycle proven. |
| 3 | `platform/` coverage ≥85%; compose stack boots; smoke E2E green. |
| 4 | Three consecutive simulated days green across tick/filedrop/legacyreport. |
| 5 | Reconciliation PASS three consecutive days; CDC lag p95 <60s; every fault-injection mode quarantined/alerted; canonical topics flowing. |
| 6 | dbt build + tests green; parity `abs_diff ≤ 0.01` over ≥5 dates; masking verified (REPORTER masked, PII_READER not). |
| 7 | E2E-1 ingestion → delinquency → case chain green with one correlation id. |
| 8 | E2E-2 decision → audit → events → explanation green; `UPDATE decision_audit` provably rejected. |
| 9 | E2E-3 promise → pay → allocate → recover green. |
| 10 | **MVP (D§88)**: A§106 steps 1–23 end to end with no harness shortcuts, ×2 consecutive days, replay safety proven, batch control-total identity holds, correlation chain sampled file → case. |
| 11 | E2E-5 escalation branches + agency/legal mutual-exclusion invariant (both directions). |
| 12 | Playwright smoke ×3 consecutive runs on the deployed UI, zero console errors, Lighthouse a11y ≥90. |
| 14 | D§90 production-readiness checklist fully evidenced; performance thresholds met; DR drills executed. |

Continuous gates that must stay green between phases: contracts CI (immutability + breaking change),
ownership CI, security CI, `e2e.yml` on `main`, `oasdiff` empty against `contracts-v1.0`.
