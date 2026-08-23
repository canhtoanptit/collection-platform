# scripts/cost/lib.sh — shared helpers for the cost scripts. Sourced, not executed.
#
# Everything here is read-only or a guard. The point is that stop.sh, start.sh and
# report.sh fail with an actionable message when credentials or a cluster are
# absent, rather than with an AWS stack trace — these scripts are authored in
# Phase 2 and first *run* much later, so the failure messages matter.

# shellcheck shell=bash

: "${AWS_REGION:=${AWS_DEFAULT_REGION:-eu-west-1}}"
: "${CLUSTER_NAME:=colx-dev}"
: "${RDS_INSTANCES:=colx-dev-platform colx-dev-corebank}"
: "${NODEGROUP_MAX_SIZE:=4}"
: "${NODEGROUP_MIN_SIZE:=2}"
: "${NODEGROUP_DESIRED_SIZE:=3}"
: "${COST_TAG_KEY:=stack}"
: "${PROJECT_TAG_VALUE:=colx}"

export AWS_REGION
export AWS_DEFAULT_REGION="$AWS_REGION"

DRY_RUN="${DRY_RUN:-0}"

# run <cmd...> — execute, or print under --dry-run.
run() {
	if [ "${DRY_RUN:-0}" = "1" ]; then
		printf '    [dry-run] %s\n' "$*"
		return 0
	fi
	"$@"
}

require_aws() {
	if ! command -v aws >/dev/null 2>&1; then
		cat >&2 <<-'EOT'
			ERROR: the AWS CLI is not installed.

			These scripts talk to real, metered infrastructure. Install the AWS CLI v2
			and configure credentials (SSO profile or an assumed role) before running
			them. Nothing in this repo ships credentials, and no agent session has any
			(ADR-0010).
		EOT
		return 1
	fi
	if ! aws sts get-caller-identity >/dev/null 2>&1; then
		cat >&2 <<-'EOT'
			ERROR: the AWS CLI is installed but has no usable credentials.

			`aws sts get-caller-identity` failed. Log in (e.g. `aws sso login`) or set
			AWS_PROFILE, then re-run. This script cannot and must not invent
			credentials.
		EOT
		return 1
	fi
}

require_kubectl() {
	if ! command -v kubectl >/dev/null 2>&1; then
		echo "ERROR: kubectl is not installed — needed to verify the cluster after start." >&2
		return 1
	fi
}

require_region() {
	if [ -z "${AWS_REGION:-}" ]; then
		echo "ERROR: AWS_REGION is empty." >&2
		return 1
	fi
	echo "region: $AWS_REGION   cluster: $CLUSTER_NAME"
}

# node_groups — names of the cluster's managed node groups, or nothing if the
# cluster does not exist (destroy-heavy state).
node_groups() {
	aws eks list-nodegroups --cluster-name "$CLUSTER_NAME" \
		--query 'nodegroups[]' --output text 2>/dev/null || true
}

# rds_status <id> — `available` / `stopped` / `stopping` / ... / `missing`.
rds_status() {
	aws rds describe-db-instances --db-instance-identifier "$1" \
		--query 'DBInstances[0].DBInstanceStatus' --output text 2>/dev/null ||
		echo missing
}
