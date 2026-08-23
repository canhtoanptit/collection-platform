#!/usr/bin/env bash
#
# scripts/cost/stop.sh — the daily cost lever (plan FND-13, ADR-0010).
#
#   scripts/cost/stop.sh [--dry-run]
#
# Takes the environment from ~$540/mo to ~$230/mo in about five minutes:
#   * EKS managed node group -> 0 desired/min (the ~$220/mo of t3.large capacity)
#   * both RDS instances stopped (~$50/mo)
#
# What it does NOT stop, and why the floor is $230 and not $5:
#   * the EKS control plane ($73/mo) — cannot be stopped, only destroyed
#   * MSK brokers ($80/mo) — same
#   * the NAT gateway ($40/mo) — same
# For longer pauses use `make destroy-heavy` (~$60/mo); see
# docs/runbooks/cost-and-teardown.md.
#
# Safe to re-run: scaling a node group that is already 0 and stopping an RDS
# instance that is already stopped are both no-ops here.
#
# RDS caveat that bites: AWS force-starts a stopped instance after **7 days**. A
# week-long pause needs destroy-heavy, not stop.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=/dev/null
. "$REPO_ROOT/scripts/cost/lib.sh"

DRY_RUN=0
[ "${1:-}" = "--dry-run" ] && DRY_RUN=1

require_aws
require_region

echo "=== stop: EKS node groups -> 0 ==="
for ng in $(node_groups); do
	current="$(aws eks describe-nodegroup \
		--cluster-name "$CLUSTER_NAME" --nodegroup-name "$ng" \
		--query 'nodegroup.scalingConfig.desiredSize' --output text)"
	if [ "$current" = "0" ]; then
		echo "  $ng already at 0"
		continue
	fi
	echo "  $ng: desired $current -> 0"
	run aws eks update-nodegroup-config \
		--cluster-name "$CLUSTER_NAME" \
		--nodegroup-name "$ng" \
		--scaling-config "minSize=0,maxSize=${NODEGROUP_MAX_SIZE},desiredSize=0"
done

echo
echo "=== stop: RDS instances ==="
for db in $RDS_INSTANCES; do
	status="$(rds_status "$db")"
	case "$status" in
	stopped | stopping)
		echo "  $db already $status"
		;;
	available)
		echo "  $db: stopping"
		run aws rds stop-db-instance --db-instance-identifier "$db" >/dev/null
		;;
	missing)
		# Not an error: destroy-heavy or a fresh account legitimately has no
		# instance. Saying so is better than a confusing AWS error.
		echo "  $db does not exist — skipping"
		;;
	*)
		echo "  $db is '$status' — not a state stop can act on; skipping" >&2
		;;
	esac
done

echo
if [ "$DRY_RUN" = "1" ]; then
	echo "stop: DRY RUN — nothing was changed"
else
	cat <<-'EOT'
		stop: done.

		Expect ~$230/mo while stopped (EKS control plane + MSK + NAT + storage).
		Pods are gone, not paused: everything is declarative, and `make start`
		brings the nodes back and lets the DaemonSets/Deployments reschedule.

		RDS auto-starts after 7 days. If the pause is longer than that, run
		`make destroy-heavy` instead.
	EOT
fi
