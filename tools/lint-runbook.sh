#!/usr/bin/env bash
#
# lint-runbook.sh — assert every operational runbook carries the D§82 control set.
#
# Usage:
#   tools/lint-runbook.sh <file.md> [<file.md> ...]
#   tools/lint-runbook.sh --list          # print the canonical heading set
#   tools/lint-runbook.sh docs/runbooks/*.md
#
# D§82 ("Operational Controls") requires every pipeline to declare: Owner,
# Support group, SLA, Expected schedule, Alert policy, Retry policy, Escalation,
# Reconciliation, Runbook. This linter requires each of those as a markdown ATX
# heading (`## Owner`, any level 1-6, case-insensitive, optional trailing `:`).
# D§82's "Runbook" item is rendered as the heading "Runbook steps" so the
# actionable step list is unambiguous inside a file that is itself a runbook.
#
# Every ops-facing pipeline/service runbook under docs/runbooks/ must pass.
#
# Exit codes: 0 = all files complete · 1 = missing headings · 2 = usage error
set -euo pipefail

# Canonical heading set — D§82, in the document's order. Do not reorder casually:
# runbook readers and the alert `runbook_url` deep links rely on these names.
HEADINGS=(
	"Owner"
	"Support group"
	"SLA"
	"Expected schedule"
	"Alert policy"
	"Retry policy"
	"Escalation"
	"Reconciliation"
	"Runbook steps"
)

usage() {
	sed -n '3,18p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' >&2
}

if [ "$#" -eq 0 ]; then
	echo "lint-runbook: no files given" >&2
	usage
	exit 2
fi

case "${1:-}" in
-h | --help)
	usage
	exit 0
	;;
--list)
	for h in "${HEADINGS[@]}"; do printf '%s\n' "$h"; done
	exit 0
	;;
-*)
	echo "lint-runbook: unknown option '$1'" >&2
	usage
	exit 2
	;;
esac

# Case-insensitive ATX heading match: `#`..`######`, the exact heading text, an
# optional trailing colon, nothing else on the line.
has_heading() {
	local file="$1" heading="$2" pattern
	pattern="^#{1,6}[[:space:]]+${heading}[[:space:]]*:?[[:space:]]*$"
	grep -qiE "$pattern" "$file"
}

failed=0
checked=0

for file in "$@"; do
	if [ ! -f "$file" ]; then
		echo "lint-runbook: $file: not a file" >&2
		failed=1
		continue
	fi
	checked=$((checked + 1))
	missing=0
	for heading in "${HEADINGS[@]}"; do
		if ! has_heading "$file" "$heading"; then
			echo "$file: missing required D§82 heading: '$heading' (add a line \"## $heading\")" >&2
			missing=$((missing + 1))
		fi
	done
	if [ "$missing" -gt 0 ]; then
		echo "$file: FAIL — $missing of ${#HEADINGS[@]} required headings missing" >&2
		failed=1
	else
		echo "$file: OK — all ${#HEADINGS[@]} D§82 headings present"
	fi
done

if [ "$failed" -ne 0 ]; then
	echo "lint-runbook: FAILED (see errors above); canonical set: tools/lint-runbook.sh --list" >&2
	exit 1
fi

echo "lint-runbook: $checked file(s) OK"
