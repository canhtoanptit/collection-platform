#!/usr/bin/env bash
#
# scripts/ci/check-contract-immutability.sh — released contract files never change
# (CON-7, contracts/README.md §1, CLAUDE.md §7).
#
# Baseline = the latest `contracts-v*` tag. Every file that exists at the baseline
# and differs in the working tree fails the check: a consumer compiled against the
# frozen tag would silently get different meaning. A change ships as a NEW `vN`
# file alongside the old one, never as an edit.
#
# Before the freeze tag exists there is nothing to protect, so the check prints a
# skip notice and exits 0 — deliberately, so this can be wired into CI now and
# start biting the moment the lead creates `contracts-v1.0`.
#
# Usage:
#   bash scripts/ci/check-contract-immutability.sh [--baseline <ref>]
#
#   --baseline <ref>   compare against <ref> instead of the latest contracts-v*
#                      tag (used by scripts/verify/CON-7.sh to prove the gate)
#
# Environment: none (git + coreutils). Exit 0 = pass or skip, 1 = violation.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

CONTRACTS_DIR="contracts"
TAG_GLOB="contracts-v*"

baseline=""
while [ $# -gt 0 ]; do
	case "$1" in
	--baseline)
		[ $# -ge 2 ] || {
			echo "check-contract-immutability: --baseline needs a git ref" >&2
			exit 2
		}
		baseline="$2"
		shift 2
		;;
	-h | --help)
		sed -n '2,22p' "${BASH_SOURCE[0]}"
		exit 0
		;;
	*)
		echo "check-contract-immutability: unknown argument '$1' (see --help)" >&2
		exit 2
		;;
	esac
done

if [ -z "$baseline" ]; then
	baseline="$(git tag -l "$TAG_GLOB" | sort -V | tail -1)"
	if [ -z "$baseline" ]; then
		echo "immutability: SKIP — no $TAG_GLOB tag in this repository yet, so no contract is released."
		echo "immutability: the gate activates automatically once the lead tags contracts-v1.0."
		echo "immutability: (CI must check out with fetch-depth: 0, or tags are invisible and this skips wrongly.)"
		exit 0
	fi
fi

if ! git rev-parse --verify --quiet "$baseline^{commit}" >/dev/null; then
	echo "immutability: FAIL — baseline ref '$baseline' does not exist in this repository." >&2
	exit 1
fi

released="$(git ls-tree -r --name-only "$baseline" -- "$CONTRACTS_DIR" | wc -l | tr -d ' ')"
echo "immutability: baseline $baseline ($(git rev-parse --short "$baseline^{commit}")), $released released files under $CONTRACTS_DIR/"

# --no-renames keeps the report simple: a renamed released file shows up as a
# deletion (a violation) plus an addition (allowed on its own).
# Statuses: M modified, D deleted, T type changed — all mean "a released file no
# longer says what it said". A means a new file, which is exactly how a change is
# supposed to ship.
violations=""
while IFS=$'\t' read -r status file; do
	[ -n "${file:-}" ] || continue
	case "$status" in
	A) continue ;;
	esac
	# `case` globs match `/` too, so */README.md covers any depth.
	case "$file" in
	"$CONTRACTS_DIR"/README.md | "$CONTRACTS_DIR"/*/README.md)
		echo "immutability: exempt — $file ($status): READMEs document the contracts, they are not the contract."
		continue
		;;
	"$CONTRACTS_DIR"/vacuum-ruleset.yaml)
		echo "immutability: exempt — $file ($status): CI lint config, not a contract (lead ruling, freeze review)."
		continue
		;;
	esac
	violations+="  $status  $file"$'\n'
done < <(git diff --no-renames --name-status "$baseline" -- "$CONTRACTS_DIR")

if [ -n "$violations" ]; then
	echo
	echo "immutability: FAIL — released contract files were modified or deleted:" >&2
	printf '%s' "$violations" >&2
	cat >&2 <<EOF

Released files under $CONTRACTS_DIR/ are immutable after the freeze tag
(contracts/README.md §1, CLAUDE.md §7). A consumer pinned to $baseline must keep
getting exactly what it compiled against.

To change a contract:
  * ship a NEW versioned file next to the old one — EventEnvelope.v2.json,
    case.v2.yaml, PaymentReceived.v2 — and serve both until every consumer has
    migrated (D§29);
  * for events, that also means a new (eventType, eventVersion) pair, a new
    schema path and a new entry in contracts/asyncapi/collections.v1.yaml;
  * restore the released file with: git checkout $baseline -- <file>

Only contracts/**/README.md is exempt. A typo fix in a description is the one
exception a reviewer may wave through — by reverting the file and re-landing it
as part of a documented, reviewed change, never by weakening this gate.
EOF
	exit 1
fi

echo "immutability: OK — no released file under $CONTRACTS_DIR/ differs from $baseline."
