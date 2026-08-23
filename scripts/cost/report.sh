#!/usr/bin/env bash
#
# scripts/cost/report.sh — what did this environment actually cost? (plan FND-13)
#
#   scripts/cost/report.sh [--month YYYY-MM] [--out docs/cost-model.md.actuals]
#
# Produces a markdown table: AWS cost by `stack` tag from Cost Explorer, plus
# Snowflake credits from ACCOUNT_USAGE, for one month. The output is meant to be
# pasted into the "actuals" column of docs/cost-model.md — the estimates there are
# guesses until this has been run at least once.
#
# WRITTEN NOW, RUN LATER. Cost Explorer only has data after the account has been
# billed for a period, and stack 40-snowflake is not applied until Phase 6
# (plan §6.10). Both halves degrade independently: no Snowflake CLI means the AWS
# table still prints, with the Snowflake section marked unavailable.
#
# Two things worth knowing before trusting the numbers:
#   * Cost Explorer is T-1 at best and the current month is always partial.
#   * `UnblendedCost` grouped by the `stack` tag leaves an untagged bucket —
#     data transfer and some support charges carry no resource tag. That row is
#     printed rather than hidden, because a large untagged number is itself the
#     finding.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=/dev/null
. "$REPO_ROOT/scripts/cost/lib.sh"

MONTH="$(date -u +%Y-%m)"
OUT=""

while [ "$#" -gt 0 ]; do
	case "$1" in
	--month)
		MONTH="${2:?--month needs YYYY-MM}"
		shift 2
		;;
	--out)
		OUT="${2:?--out needs a path}"
		shift 2
		;;
	-h | --help)
		sed -n '3,25p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	*)
		echo "report.sh: unknown argument '$1'" >&2
		exit 2
		;;
	esac
done

if ! printf '%s' "$MONTH" | grep -qE '^[0-9]{4}-(0[1-9]|1[0-2])$'; then
	echo "report.sh: --month must be YYYY-MM (got '$MONTH')" >&2
	exit 2
fi

start="${MONTH}-01"
# First day of the next month; Cost Explorer's End is exclusive.
end="$(python3 - "$MONTH" <<-'PY'
	import sys, datetime
	y, m = (int(x) for x in sys.argv[1].split('-'))
	nxt = datetime.date(y + (m == 12), 1 if m == 12 else m + 1, 1)
	print(nxt.isoformat())
PY
)"

emit() { printf '%s\n' "$1" >>"$REPORT"; }

REPORT="$(mktemp)"
trap 'rm -f "$REPORT"' EXIT

emit "# Cost report — ${MONTH}"
emit ""
emit "Generated $(date -u +%Y-%m-%dT%H:%M:%SZ) by \`scripts/cost/report.sh\`."
emit "Period: ${start} .. ${end} (end exclusive). Cost Explorer lags ~24h; a"
emit "current-month run is partial by definition."
emit ""

# --------------------------------------------------------------------- AWS ----
emit "## AWS — unblended cost by \`stack\` tag"
emit ""

if ! command -v aws >/dev/null 2>&1 || ! aws sts get-caller-identity >/dev/null 2>&1; then
	emit "_Unavailable: no AWS credentials. See scripts/cost/lib.sh for what this needs._"
else
	ce_json="$(mktemp)"
	if aws ce get-cost-and-usage \
		--time-period "Start=${start},End=${end}" \
		--granularity MONTHLY \
		--metrics UnblendedCost \
		--group-by "Type=TAG,Key=${COST_TAG_KEY}" \
		--region us-east-1 \
		>"$ce_json" 2>/dev/null; then

		emit "| stack | cost (USD) |"
		emit "|---|---:|"
		python3 - "$ce_json" <<-'PY' >>"$REPORT"
			import json, sys
			d = json.load(open(sys.argv[1]))
			total = 0.0
			rows = []
			for period in d.get("ResultsByTime", []):
			    for g in period.get("Groups", []):
			        key = g["Keys"][0]
			        # Cost Explorer returns "stack$" for untagged resources.
			        label = key.split("$", 1)[1] or "(untagged)"
			        amount = float(g["Metrics"]["UnblendedCost"]["Amount"])
			        total += amount
			        rows.append((label, amount))
			for label, amount in sorted(rows, key=lambda r: -r[1]):
			    print(f"| {label} | {amount:,.2f} |")
			print(f"| **total** | **{total:,.2f}** |")
		PY
		emit ""
		emit "Note: \`(untagged)\` collects data transfer and other charges that carry no"
		emit "resource tag. A large value there is a finding, not noise."
	else
		emit "_Unavailable: \`aws ce get-cost-and-usage\` failed. Cost Explorer must be"
		emit "enabled on the account (once, in the console) and the caller needs"
		emit "\`ce:GetCostAndUsage\`. Cost Explorer is us-east-1 only._"
	fi
	rm -f "$ce_json"
fi

emit ""

# --------------------------------------------------------------- Snowflake ----
emit "## Snowflake — credits by warehouse"
emit ""

if ! command -v snow >/dev/null 2>&1; then
	emit "_Unavailable: the Snowflake CLI (\`snow\`) is not installed._"
	emit ""
	emit "Run this query in a worksheet instead — it is the same one:"
	emit ""
	emit '```sql'
	emit "SELECT warehouse_name,"
	emit "       ROUND(SUM(credits_used), 2) AS credits"
	emit "FROM   SNOWFLAKE.ACCOUNT_USAGE.WAREHOUSE_METERING_HISTORY"
	emit "WHERE  start_time >= '${start}'"
	emit "  AND  start_time <  '${end}'"
	emit "GROUP BY warehouse_name"
	emit "ORDER BY credits DESC;"
	emit '```'
elif [ ! -f "${SNOWFLAKE_HOME:-$HOME/.snowflake}/config.toml" ] && [ -z "${SNOWFLAKE_ACCOUNT:-}" ]; then
	emit "_Unavailable: \`snow\` is installed but has no connection configured."
	emit "See infrastructure/terraform/stacks/40-snowflake/README.md, step 5._"
else
	sql="SELECT warehouse_name, ROUND(SUM(credits_used),2) AS credits
	     FROM SNOWFLAKE.ACCOUNT_USAGE.WAREHOUSE_METERING_HISTORY
	     WHERE start_time >= '${start}' AND start_time < '${end}'
	     GROUP BY warehouse_name ORDER BY credits DESC"
	if snow sql -q "$sql" --format csv >"$REPORT.sf" 2>/dev/null; then
		emit "| warehouse | credits |"
		emit "|---|---:|"
		# CSV -> markdown, skipping the header row.
		tail -n +2 "$REPORT.sf" | tr -d '"' | awk -F, 'NF>=2 {printf "| %s | %s |\n", $1, $2}' >>"$REPORT"
		emit ""
		emit "The \`COLX_MONTHLY\` resource monitor caps the account at 50 credits/mo and"
		emit "suspends every warehouse at 100% (ADR-0008). Credits above ~40 are the"
		emit "documented trigger for the \"drop to Standard + secure views\" escape hatch."
		rm -f "$REPORT.sf"
	else
		emit "_Unavailable: the metering query failed. ACCOUNT_USAGE needs a role with"
		emit "\`IMPORTED PRIVILEGES ON DATABASE SNOWFLAKE\` — ACCOUNTADMIN has it by default._"
	fi
fi

emit ""
emit "---"
emit ""
emit "Estimates to compare against: \`docs/cost-model.md\`."

if [ -n "$OUT" ]; then
	cp "$REPORT" "$OUT"
	echo "report.sh: wrote $OUT"
else
	cat "$REPORT"
fi
