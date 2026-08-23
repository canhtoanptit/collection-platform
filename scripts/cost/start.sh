#!/usr/bin/env bash
#
# scripts/cost/start.sh — the reverse of stop.sh (plan FND-13).
#
#   scripts/cost/start.sh [--dry-run]
#
# Order matters and is the whole reason this is a script rather than two CLI
# calls: RDS starts FIRST, then the nodes. Bring the nodes up first and every
# pod that needs a database — Airflow's scheduler, Keycloak, every service —
# CrashLoopBackOffs for the five to ten minutes RDS takes to come back, and you
# spend the morning reading logs about a problem that fixed itself.
#
# Waits are real waits, not sleeps: `aws rds wait db-instance-available` and a
# node-Ready poll. A "start" that returns before the environment is usable is a
# lie that costs more time than the wait.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=/dev/null
. "$REPO_ROOT/scripts/cost/lib.sh"

DRY_RUN=0
[ "${1:-}" = "--dry-run" ] && DRY_RUN=1
export DRY_RUN

require_aws
require_region

echo "=== start: RDS instances (first — pods need a database) ==="
started=""
for db in $RDS_INSTANCES; do
	status="$(rds_status "$db")"
	case "$status" in
	available)
		echo "  $db already available"
		;;
	stopped)
		echo "  $db: starting"
		run aws rds start-db-instance --db-instance-identifier "$db" >/dev/null
		started="$started $db"
		;;
	starting)
		echo "  $db already starting"
		started="$started $db"
		;;
	missing)
		echo "  $db does not exist — run 'make up-all' (destroy-heavy was used?)" >&2
		;;
	*)
		echo "  $db is '$status' — waiting on it anyway" >&2
		started="$started $db"
		;;
	esac
done

if [ -n "$started" ] && [ "$DRY_RUN" = "0" ]; then
	for db in $started; do
		echo "  waiting for $db to become available (typically 5-10 min)"
		aws rds wait db-instance-available --db-instance-identifier "$db"
		echo "  $db available"
	done
fi

echo
echo "=== start: EKS node groups -> ${NODEGROUP_DESIRED_SIZE} ==="
for ng in $(node_groups); do
	current="$(aws eks describe-nodegroup \
		--cluster-name "$CLUSTER_NAME" --nodegroup-name "$ng" \
		--query 'nodegroup.scalingConfig.desiredSize' --output text)"
	if [ "$current" = "$NODEGROUP_DESIRED_SIZE" ]; then
		echo "  $ng already at $NODEGROUP_DESIRED_SIZE"
		continue
	fi
	echo "  $ng: desired $current -> $NODEGROUP_DESIRED_SIZE"
	run aws eks update-nodegroup-config \
		--cluster-name "$CLUSTER_NAME" \
		--nodegroup-name "$ng" \
		--scaling-config "minSize=${NODEGROUP_MIN_SIZE},maxSize=${NODEGROUP_MAX_SIZE},desiredSize=${NODEGROUP_DESIRED_SIZE}"
done

if [ "$DRY_RUN" = "1" ]; then
	echo
	echo "start: DRY RUN — nothing was changed"
	exit 0
fi

echo
echo "=== start: waiting for nodes to register and become Ready ==="
if ! command -v kubectl >/dev/null 2>&1; then
	echo "  kubectl not installed — skipping the node-Ready check." >&2
	echo "  The node group is scaled up; verify with 'kubectl get nodes' once you have kubectl." >&2
	exit 0
fi

aws eks update-kubeconfig --name "$CLUSTER_NAME" --region "$AWS_REGION" >/dev/null

# Poll rather than `kubectl wait --for=condition=Ready node --all`: with 0 nodes
# the selector matches nothing and `wait` returns success immediately, which is
# exactly the false green this check exists to avoid.
deadline=$((SECONDS + 900))
while [ "$SECONDS" -lt "$deadline" ]; do
	ready="$(kubectl get nodes --no-headers 2>/dev/null | awk '$2=="Ready"' | wc -l | tr -d ' ')"
	echo "  nodes Ready: ${ready}/${NODEGROUP_MIN_SIZE}"
	if [ "${ready:-0}" -ge "$NODEGROUP_MIN_SIZE" ]; then
		break
	fi
	sleep 20
done

ready="$(kubectl get nodes --no-headers 2>/dev/null | awk '$2=="Ready"' | wc -l | tr -d ' ')"
if [ "${ready:-0}" -lt "$NODEGROUP_MIN_SIZE" ]; then
	echo "start: FAILED — only ${ready} node(s) Ready after 15 minutes" >&2
	kubectl get nodes >&2 || true
	exit 1
fi

cat <<-EOT

	start: done. ${ready} node(s) Ready, RDS available.

	Post-start verification (docs/runbooks/cost-and-teardown.md):
	  kubectl get pods -A | grep -v Running | grep -v Completed
	  make grafana   # Grafana answers on :3000
	  make airflow   # trigger the platform_smoke DAG

	Pods that were pending at stop time reschedule on their own; anything still
	CrashLooping after five minutes is a real failure, not a cold start.
EOT
