# `contracts/files/SPEC.md` — file-feed contract meta-schema (v1)

Every batch file the platform ingests is described by exactly one contract file in this directory:

```text
contracts/files/<feed_id>.v<N>.yaml
```

The contract is the **single source of truth** for that feed. Three independent consumers read it, and
none of them may re-declare the format in code:

| Consumer | WP | Uses |
|---|---|---|
| `ingestion/internal/feedspec` (validation library) | ING-3 | everything below |
| ingestion SFTP/CSV pipeline worker | ING-4 | `filename_regex`, validation order, quarantine reasons, control total |
| ingestion control plane `bootstrap` | ING-2 | `feed_id`, `source_id`, `filename_regex`, `sla` → `source`/`feed` rows |
| `simulator/corebank` file-drop generator | SIM-4 | `header`, `trailer`, `columns` order, `control_total_column` |
| analytics parity harness | ANA-7 | `legacy_daily_summary.v1.yaml` columns |

> **Hard rule (plan risk 6, CLAUDE.md §10.1).** The simulator re-implements the D§21 line format from
> this contract; it must never import `ingestion/` or `platform/` code. If writer and validator shared
> code, a green reconciliation would prove only that the code agrees with itself.

This document is the meta-schema: what keys a contract file may contain, what they mean, and what
`feedspec.Load` rejects. A contract that violates any MUST here fails to load — loudly, at start-up,
not per row.

---

## 1. Physical file format (D§19, D§21)

A feed file is a **line-oriented RFC 4180 CSV** in **UTF-8**, with three record kinds distinguished by
the first field:

```text
HEADER,<FEED_CODE>,<YYYYMMDD>,<record_count>
DATA,<col1>,<col2>,...,<colN>
DATA,<col1>,<col2>,...,<colN>
TRAILER,<record_count>,<control_total>
```

Rules:

1. Exactly one `HEADER` record, and it is the **first** record in the file.
2. Zero or more `DATA` records, in any order, each with exactly `1 + len(columns)` fields — the literal
   `DATA` plus one field per entry of `columns[]`, **in declared order**. Column order is part of the
   contract: reordering `columns[]` is a breaking change (§7).
3. Exactly one `TRAILER` record, and it is the **last** record in the file. Nothing may follow it.
4. `<FEED_CODE>` is the literal `header.feed_code` of the contract (D§21's example uses `LOAN`).
5. `<YYYYMMDD>` is the file's business date, a real calendar date, equal to the `business_date`
   captured from the file name by `filename_regex`.
6. Both `record_count` fields are the number of `DATA` records — a non-negative decimal integer, no
   thousands separators, no sign.
7. `<control_total>` is the decimal sum of `trailer.control_total_column` over all `DATA` records, at
   **exactly 2 decimal places** (`1801.00`, not `1801` or `1801.000`). It is compared as an exact
   decimal, never as a float (CLAUDE.md §3: money never touches a float).
8. Empty fields mean "absent". A column with `required: true` rejects an empty field; a column with
   `required: false` treats it as an absent value (SQL `NULL`), never as `0` or `""`.
9. No BOM, no trailing junk, `LF` or `CRLF` line endings (`encoding/csv` accepts both), fields quoted
   per RFC 4180 when they contain `,`, `"` or a newline.

Two structural details worth stating, because they decide which reason code an operator sees:

- A `HEADER` record that is not the first record means the file has **no header where one is
  required**: it is reported as `HEADER_MISSING` (the offending line is named in the detail), and a
  *second* `HEADER` is `HEADER_DUPLICATE`. One problem, one failure, either way.
- The trailer's `control_total` must be written with exactly `scale` decimal places. `1801` for a
  `scale: 2` column is `TRAILER_CONTROL_TOTAL_INVALID`, not a match — these files are machine
  generated, so a formatting drift is a signal that the producer changed, and it is cheaper to catch
  here than in a mart six weeks later. The *value* comparison against the computed sum is a decimal
  comparison, never string equality.

### Validation order (normative — D§21, A§32)

`feedspec` validates in exactly this order, because a later check is meaningless if an earlier one
failed:

```text
header valid → schema valid (field count) → mandatory fields present → data types correct
            → business rules valid → trailer valid → record count correct → control totals correct
```

Consequences the pipeline (ING-4) depends on:

- A row with **any `ERROR`** failure quarantines the **whole file**. Once one row is rejected the
  declared control total can no longer be reconciled against the parsed rows, so partial loads are not
  offered (documented policy, ING-4 step 4).
- `WARN` failures are recorded per row (`quarantine_row`, `rejects.jsonl`) and the file continues.
- Business rules are evaluated **only** for rows where every column produced a usable typed value
  (no parse failure, no missing required field). Evaluating `od_amt <= curr_bal` against an
  unparseable `curr_bal` would be noise.
- A rule that cannot be evaluated (null dereference, wrong type at run time) is an **`ERROR`
  regardless of the rule's declared severity** — fail closed, because an unevaluated rule proves
  nothing.

---

## 2. Top-level keys

Unknown keys are rejected (`yaml.Decoder.KnownFields(true)`): a typo like `filename_regexp` must be a
load error, not a silently unvalidated feed.

| Key | Type | Req | Rules |
|---|---|:--:|---|
| `feed_id` | string | ✔ | `^[a-z][a-z0-9_]{2,63}$`. Stable identity of the feed; the `feed.feed_id` primary key in the control plane (ING-1). MUST equal the file-name stem: `<feed_id>.v<version>.yaml`. |
| `version` | int | ✔ | ≥ 1. Major contract version. MUST equal the `vN` in the file name. |
| `source_id` | string | ✔ | `^[A-Z][A-Z0-9_]{2,63}$`. The `source.source_id` this feed arrives from (ING-1). |
| `description` | string | – | One or two sentences: what the feed is, who produces it. |
| `filename_regex` | string | ✔ | Go/RE2 syntax, MUST compile, MUST contain the named group `(?P<business_date>\d{8})`. Matched against the **base name** only, and must match the name in full. |
| `encoding` | string | ✔ | `UTF-8` — the only accepted value in v1. |
| `format` | string | ✔ | `RFC4180` — the only accepted value in v1. |
| `header` | object | ✔ | See §3. |
| `trailer` | object | ✔ | See §4. |
| `columns` | list | ✔ | ≥ 1 entry, see §5. Order is the physical `DATA` field order. |
| `business_rules` | list | – | May be omitted or empty, see §6. |
| `sla` | object | ✔ | See §8. |
| `reconciliation` | object | ✔ | See §9. |

## 3. `header`

| Key | Type | Req | Rules |
|---|---|:--:|---|
| `feed_code` | string | ✔ | `^[A-Z][A-Z0-9_]{1,31}$`. The literal that MUST appear as field 2 of the `HEADER` record. Source systems use their own short codes, so this is declared, never derived from `feed_id`. |
| `fields` | list | ✔ | MUST be exactly `[feed_code, business_date, record_count]`. The list documents the D§21 line shape; it is fixed in v1 so header parsing is not configurable code. |

## 4. `trailer`

| Key | Type | Req | Rules |
|---|---|:--:|---|
| `fields` | list | ✔ | MUST be exactly `[record_count, control_total]`. |
| `control_total_column` | string | ✔ | Name of a `columns[]` entry of type `decimal`. Its decimal sum over all `DATA` rows must equal the trailer's `control_total` to 2dp exactly. |

## 5. `columns[]`

| Key | Type | Req | Rules |
|---|---|:--:|---|
| `name` | string | ✔ | `^[a-z][a-z0-9_]{0,62}$`, unique within the feed, not a CEL reserved word (`in`, `for`, `null`, …) — column names become rule variables (§6). |
| `type` | enum | ✔ | `string` \| `integer` \| `decimal` \| `date_yyyymmdd` \| `enum`. |
| `required` | bool | ✔ | `true` → an empty field is `REQUIRED_FIELD_MISSING`. Must be stated explicitly; there is no default. |
| `description` | string | – | What the column means, and its unit where not obvious. |
| `pattern` | string | – | `string` only. RE2, anchored implicitly (the whole field must match). |
| `enum` | list | – | `enum` only, and **required** for `enum`. ≥ 1 distinct non-empty value. Closed vocabulary; case sensitive. |
| `scale` | int | – | `decimal` only, and **required** for `decimal`. 0–9. Exact number of decimal places allowed; more places is `DECIMAL_SCALE_EXCEEDED`. Money columns are always `2`. |
| `min` | number | – | `integer`/`decimal` only. **Inclusive** lower bound. |
| `max` | number | – | `integer`/`decimal` only. **Inclusive** upper bound, MUST be ≥ `min`. |

`min`/`max` are read as exact decimal text (never a float), so `min: 0.01` is exactly one cent. Because
they are inclusive, a strict `> 0` constraint is expressed as `min: 0.01` on a `scale: 2` column
**and** a business rule (`amount > 0`) — deliberate defence in depth: the bound rejects the value, the
rule names the intent in the reject record.

### Type semantics

| `type` | Accepted field text | Rejected with |
|---|---|---|
| `string` | any non-empty text; `pattern` applied when declared | `PATTERN_MISMATCH` |
| `integer` | `^-?\d+$`, fits in `int64`; `min`/`max` applied | `INVALID_INTEGER`, `MIN_VIOLATION`, `MAX_VIOLATION` |
| `decimal` | plain decimal, optional sign, no thousands separator, no exponent (`-1200.50`); ≤ `scale` decimal places; `min`/`max` applied | `INVALID_DECIMAL`, `DECIMAL_SCALE_EXCEEDED`, `MIN_VIOLATION`, `MAX_VIOLATION` |
| `date_yyyymmdd` | exactly 8 digits **and** a real calendar date (`20260231` is rejected, `20240229` is accepted) | `INVALID_DATE` |
| `enum` | one of `enum[]`, exactly | `ENUM_INVALID` |

Dates are `YYYYMMDD` because the source systems emit them that way (`cb_account.open_dt int`);
everything downstream of the canonicalizer is ISO-8601 (contracts README §5).

## 6. `business_rules[]`

Row predicates that are true for valid data, expressed in **CEL** (`github.com/google/cel-go`) over the
row's columns.

| Key | Type | Req | Rules |
|---|---|:--:|---|
| `id` | string | ✔ | `^[a-z][a-z0-9_]{2,63}$`, unique within the feed. Appears verbatim in reject records (`rule_id`) and metrics, so it is part of the contract. |
| `expr` | string | ✔ | CEL expression, MUST compile at load time against the column declarations, MUST have result type `bool`. `true` = the row satisfies the rule. |
| `severity` | enum | ✔ | `ERROR` (quarantines the file) \| `WARN` (recorded, file continues). |
| `description` | string | – | Why the rule exists, in business language. |

Rule variables are the column names, typed as follows:

| Column `type` | CEL type | Absent optional value |
|---|---|---|
| `string`, `enum`, `date_yyyymmdd` | `string` (dates as the raw `YYYYMMDD` — lexical order equals chronological order) | `null` |
| `integer` | `int` | `null` |
| `decimal` | `double` | `null` |

Optional columns are declared as `dyn`, so a rule that touches one MUST null-check it
(`last_pay_dt != null && last_pay_dt >= open_dt`). An unguarded null comparison is a run-time rule
error, which is reported as an `ERROR` failure (fail closed) — never silently skipped.

### Why `decimal` is exposed to CEL as `double`

Business rules are **comparative predicates** (`od_amt <= curr_bal`, `amount > 0`), not accounting
arithmetic. CEL has no decimal type; the alternatives were `double`, a `string` (which makes `<=`
lexical and wrong), or a custom decimal extension type (a non-standard rule dialect nobody else can
read). `double` is chosen because for two 2dp values converted from the same decimal text, IEEE-754
conversion is deterministic and order-preserving as long as the scaled integer fits exactly in a
float64 mantissa. `feedspec` **enforces** that boundary instead of documenting a hope: a decimal whose
value scaled by `10^scale` exceeds 2^52 (for `scale: 2`, about ±4.5×10¹³ — 45 trillion) is reported as
`DECIMAL_EXCEEDS_RULE_PRECISION` (`ERROR`) rather than silently compared at reduced precision.

**Control totals never use this path.** They are summed with `shopspring/decimal` and compared to the
trailer as exact decimals (§4). No monetary value is ever *computed* in a float — a float only ever
carries one already-validated value into one comparison.

## 7. Versioning, immutability, duplicates

**Released contracts are immutable** (contracts README §1, CLAUDE.md §7). A contract on `main` never
changes meaning; a change ships as `<feed_id>.v<N+1>.yaml` next to the old file, and both are served
until every consumer has migrated. CI fails a PR that modifies a released file
(`scripts/ci/check-contract-immutability.sh`, CON-7).

Breaking — requires a new `vN` **and** a new `schema_version` on the `feed` row:

- adding a column, removing a column, or **reordering `columns[]`** (the `DATA` field positions move);
- adding `required: true` to an existing column, or making a column required;
- narrowing a `type`, `pattern`, `scale`, `min`/`max`, or removing an `enum` value;
- changing `header.feed_code`, `filename_regex`, `trailer.control_total_column`, or
  `reconciliation`;
- adding a rule with `severity: ERROR`, or promoting `WARN` → `ERROR` (files that used to load now
  quarantine).

Non-breaking, allowed in place: `description` text, and (before the `contracts-v1.0` tag only) purely
additive optional metadata. Adding a `WARN` rule is a judgement call: it changes reject records but not
file outcomes, so it ships as a new `vN` once the feed is live.

**Duplicate files** are a pipeline concern, not a row concern (A§33, ING-4): SHA-256 of the file plus
`UNIQUE(source_id, checksum_sha256)` and `UNIQUE(feed_id, file_name)` in the file registry. Same
content twice → the second file is `DUPLICATE` (terminal, audited). Same file name with different
content → `QUARANTINED` / `FILENAME_REUSED`. The business date is *not* a uniqueness key by itself: a
corrected re-drop for the same business date is a legitimate new file with a new checksum, and it is
reprocessed through `QUARANTINED → VALIDATING`.

**Duplicate rows inside a file are not detected in v1.** No contract declares a natural key, so
`feedspec` cannot know that two `loan_accounts` rows for the same `acct_no` are a defect. Row-level
uniqueness is asserted downstream, in the analytics layer (dbt `unique` tests on the staging models,
D§35). A future `natural_key: [acct_no]` field would move that check into ingestion — and, being a new
required check, would ship as a new `vN`.

## 8. `sla`

| Key | Type | Req | Rules |
|---|---|:--:|---|
| `expected_by` | string | ✔ | `HH:MM`, 24h. The file is expected to have arrived by this wall time (D§81). |
| `late_by` | string | ✔ | `HH:MM`, strictly after `expected_by`. Past this, lateness is an alertable breach (`colx_ingestion_file_lateness_seconds`, ING-2). |
| `timezone` | string | ✔ | `UTC` — the only accepted value. D§81 says "local time"; the platform is UTC-only (CLAUDE.md §3), so contracts state UTC and the source system's local schedule is converted once, here. |

## 9. `reconciliation`

Explicit controls, because pipeline success is never evidence of correctness (D§38, CLAUDE.md §10.5).

| Key | Type | Req | Rules |
|---|---|:--:|---|
| `count` | enum | ✔ | `declared_equals_parsed` — the trailer's (and header's) `record_count` must equal the number of parsed `DATA` rows (A§37: `declared = rejected + loaded`). |
| `amount.column` | string | ✔ | A `decimal` column; MUST be the same column as `trailer.control_total_column` (a contract that reconciles a different column than it totals is inconsistent and fails to load). |
| `amount.vs` | enum | ✔ | `trailer_control_total` — the declared side of the amount check. |

## 10. Worked example

```yaml
feed_id: loan_accounts
version: 1
source_id: COREBANK_SFTP
filename_regex: '^loan_accounts_(?P<business_date>\d{8})\.csv$'
encoding: UTF-8
format: RFC4180
header:
  feed_code: LOAN_ACCOUNTS
  fields: [feed_code, business_date, record_count]
trailer:
  fields: [record_count, control_total]
  control_total_column: curr_bal
columns:
  - name: acct_no
    type: string
    required: true
    pattern: '^[A-Z0-9]{6,12}$'
  - name: curr_bal
    type: decimal
    required: true
    scale: 2
  - name: od_amt
    type: decimal
    required: true
    scale: 2
business_rules:
  - id: od_amt_within_balance
    expr: od_amt <= curr_bal
    severity: ERROR
sla:
  expected_by: "02:00"
  late_by: "03:00"
  timezone: UTC
reconciliation:
  count: declared_equals_parsed
  amount:
    column: curr_bal
    vs: trailer_control_total
```

A conforming file for business date `2026-08-22`:

```text
HEADER,LOAN_ACCOUNTS,20260822,2
DATA,ACC0000001,1200.50,0.00
DATA,ACC0000002,600.50,100.00
TRAILER,2,1801.00
```

## 11. Failure reasons

Stable `SCREAMING_SNAKE` codes. `feedspec` emits them; ING-4 persists them on `quarantine_row` and in
`rejects.jsonl`; dashboards and runbooks branch on them. They are contract surface: renaming one is a
breaking change.

| Scope | Reason |
|---|---|
| file | `HEADER_MISSING`, `HEADER_MALFORMED`, `HEADER_RECORD_TYPE`, `HEADER_FEED_CODE_MISMATCH`, `HEADER_BUSINESS_DATE_INVALID`, `HEADER_BUSINESS_DATE_MISMATCH`, `HEADER_RECORD_COUNT_INVALID`, `HEADER_DUPLICATE` |
| file | `TRAILER_MISSING`, `TRAILER_MALFORMED`, `TRAILER_RECORD_TYPE`, `TRAILER_RECORD_COUNT_INVALID`, `TRAILER_CONTROL_TOTAL_INVALID`, `ROW_AFTER_TRAILER` |
| file | `RECORD_COUNT_MISMATCH`, `HEADER_TRAILER_COUNT_MISMATCH`, `CONTROL_TOTAL_MISMATCH` |
| row | `RECORD_TYPE_UNKNOWN`, `ROW_UNPARSEABLE`, `ROW_FIELD_COUNT`, `ENCODING_INVALID` |
| cell | `REQUIRED_FIELD_MISSING`, `PATTERN_MISMATCH`, `INVALID_INTEGER`, `INVALID_DECIMAL`, `DECIMAL_SCALE_EXCEEDED`, `MIN_VIOLATION`, `MAX_VIOLATION`, `INVALID_DATE`, `ENUM_INVALID`, `DECIMAL_EXCEEDS_RULE_PRECISION` |
| rule | `BUSINESS_RULE_FAILED`, `RULE_EVALUATION_ERROR` |

## 12. Contracts in this directory

| File | Feed | Source | Delivery | Control total |
|---|---|---|---|---|
| `loan_accounts.v1.yaml` | `loan_accounts` | `COREBANK_SFTP` | SFTP | `sum(curr_bal)` |
| `payments.v1.yaml` | `payments` | `COREBANK_SFTP` | SFTP | `sum(amount)` |
| `delinquency_snapshot.v1.yaml` | `delinquency_snapshot` | `COREBANK_SFTP` | SFTP | `sum(od_amt)` |
| `legacy_daily_summary.v1.yaml` | `legacy_daily_summary` | `LEGACY_MI_S3` | S3 (not SFTP) | `sum(total_overdue)` |
