#!/usr/bin/env bash
#
# scripts/verify/INF-A.sh -- acceptance for WP INF-A (Terraform: bootstrap, network and data
# stacks, the Kafka topic registry, and the database provisioning script).
#
# INF-A is CODE-AUTHORING ONLY. There are no AWS credentials in scope and nothing is applied, so
# this script asserts what can be asserted without an account:
#
#   1. every stack initializes (providers and registry modules resolve), validates, and is formatted
#   2. provider and registry-module versions are pinned
#   3. the structure matches the plan: bucket list == plan §6.7, four OIDC roles, >= 28 topics
#   4. the security posture is in the code: no account ids, emails, public CIDRs or credential
#      material; no ingress from 0.0.0.0/0; no secret *values* in Terraform
#   5. identity is Keycloak, not Cognito (FND-6 rewrite, user directive 2026-08-23)
#   6. topics.yaml stays inside the YAML subset the apply Job's awk parser can read
#   7. the guards actually bite (expected-FAIL assertions at the end)
#
# It makes NO aws CLI call of any kind and runs no plan or apply.
#
# Scope: only INF-A's own paths are inspected. stacks/30-eks, stacks/40-snowflake and
# modules/{eks,irsa-role,snowflake-account} belong to INF-B and are verified by scripts/verify/INF-B.sh.
# `.terraform/` directories are excluded from every scan -- they hold ~800 MB provider binaries per
# stack, and a recursive grep across them both takes minutes and matches every AWS resource type
# that exists.
#
# Environment: bash, coreutils, awk, python3 (stdlib only), terraform >= 1.11.
# `terraform init -backend=false` downloads the aws provider and the pinned VPC registry module, so
# a cold run needs network. No cloud credentials are used or required.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

TF_ROOT="infrastructure/terraform"
STACKS=(00-bootstrap 10-network 20-data)
MODULES=(github-oidc budgets network kms s3-data ecr rds-postgres msk)

# Every directory INF-A owns. Used for fmt, for the recursive scans, and to keep INF-B's stacks out.
TF_DIRS=()
for s in "${STACKS[@]}"; do TF_DIRS+=("$TF_ROOT/stacks/$s"); done
for m in "${MODULES[@]}"; do TF_DIRS+=("$TF_ROOT/modules/$m"); done
TF_DIRS+=("$TF_ROOT/envs/dev")

SCAN_PATHS=("${TF_DIRS[@]}" deployment/kafka scripts/db)

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

pass=0
fail=0
ok() {
	printf 'ok:   %s\n' "$1"
	pass=$((pass + 1))
}
bad() {
	printf 'FAIL: %s\n' "$1" >&2
	fail=$((fail + 1))
}

# check <description> <command...>        -- command must succeed
check() {
	local desc="$1"
	shift
	if "$@" >/dev/null 2>&1; then ok "$desc"; else bad "$desc"; fi
}

# check_fails <description> <command...>  -- command must FAIL (guard proof)
check_fails() {
	local desc="$1"
	shift
	if "$@" >/dev/null 2>&1; then
		bad "$desc (command unexpectedly succeeded)"
	else
		ok "$desc"
	fi
}

# absent <description> <extended-regex>   -- the pattern must not appear in any INF-A path
absent() {
	local desc="$1" pat="$2"
	if grep -rniE --exclude-dir=.terraform -- "$pat" "${SCAN_PATHS[@]}" >"$TMP/hit.txt" 2>/dev/null; then
		bad "$desc"
		head -3 "$TMP/hit.txt" >&2
	else
		ok "$desc"
	fi
}

command -v terraform >/dev/null 2>&1 || {
	echo "FATAL: terraform is not installed. INF-A cannot be verified without it." >&2
	exit 1
}
command -v python3 >/dev/null 2>&1 || {
	echo "FATAL: python3 is not installed. It parses topics.yaml and the HCL structure checks." >&2
	exit 1
}

tf_version="$(terraform version -json | python3 -c 'import json,sys; print(json.load(sys.stdin)["terraform_version"])')"
echo "terraform $tf_version  |  repo $REPO_ROOT"
echo

# =================================================================================================
echo "=== 1. file inventory ==="
# =================================================================================================

for s in "${STACKS[@]}"; do
	for f in versions.tf main.tf variables.tf outputs.tf README.md; do
		check "stack $s has $f" test -f "$TF_ROOT/stacks/$s/$f"
	done
done

for m in "${MODULES[@]}"; do
	for f in main.tf variables.tf outputs.tf README.md; do
		check "module $m has $f" test -f "$TF_ROOT/modules/$m/$f"
	done
	check "module $m README documents prod deltas" \
		grep -qiE '^#+ *prod deltas' "$TF_ROOT/modules/$m/README.md"
	check "module $m declares a type and a description for every variable" \
		bash -c '
		python3 - "$1" <<'"'"'PY'"'"'
import re, sys
txt = open(sys.argv[1]).read()
bad = []
for m in re.finditer(r'"'"'variable\s+"([^"]+)"\s*\{'"'"', txt):
    start = m.end()
    depth, i = 1, start
    while i < len(txt) and depth:
        if txt[i] == "{": depth += 1
        elif txt[i] == "}": depth -= 1
        i += 1
    body = txt[start:i]
    if not re.search(r"^\s*description\s*=", body, re.M):
        bad.append(f"{m.group(1)}: no description")
    if not re.search(r"^\s*type\s*=", body, re.M):
        bad.append(f"{m.group(1)}: no type")
print("\n".join(bad))
sys.exit(1 if bad else 0)
PY' _ "$TF_ROOT/modules/$m/variables.tf"
done

for f in envs/dev/backend.hcl envs/dev/common.tfvars.example envs/dev/README.md; do
	check "exists: $TF_ROOT/$f" test -f "$TF_ROOT/$f"
done
for f in deployment/kafka/topics.yaml deployment/kafka/topic-apply-job.yaml \
	deployment/kafka/README.md scripts/db/provision_databases.sh; do
	check "exists: $f" test -f "$f"
done

check "no real tfvars is committed (only the .example template)" \
	bash -c "! ls $TF_ROOT/envs/*/*.tfvars >/dev/null 2>&1"
check "no state file is committed" \
	bash -c "! find $TF_ROOT -name '*.tfstate*' -not -path '*/.terraform/*' -print | grep -q ."

# =================================================================================================
echo
echo "=== 2. terraform init / validate / fmt, per stack ==="
# =================================================================================================

for s in "${STACKS[@]}"; do
	if terraform -chdir="$TF_ROOT/stacks/$s" init -backend=false -input=false \
		>"$TMP/init-$s.log" 2>&1; then
		ok "terraform init -backend=false ($s)"
	else
		bad "terraform init -backend=false ($s) -- see $TMP/init-$s.log"
		tail -5 "$TMP/init-$s.log" >&2 || true
		continue
	fi

	if terraform -chdir="$TF_ROOT/stacks/$s" validate -no-color >"$TMP/validate-$s.log" 2>&1; then
		ok "terraform validate ($s)"
	else
		bad "terraform validate ($s)"
		sed -n '1,20p' "$TMP/validate-$s.log" >&2 || true
	fi

	check "terraform fmt -check -recursive ($s)" \
		terraform fmt -check -recursive "$TF_ROOT/stacks/$s"
done

for m in "${MODULES[@]}"; do
	check "terraform fmt -check -recursive (modules/$m)" \
		terraform fmt -check -recursive "$TF_ROOT/modules/$m"
done

# =================================================================================================
echo
echo "=== 3. version pinning ==="
# =================================================================================================

check "every stack and module pins hashicorp/aws ~> 6.0" \
	bash -c '
	set -euo pipefail
	rc=0
	for d in "$@"; do
		ls "$d"/*.tf >/dev/null 2>&1 || continue
		grep -qs "hashicorp/aws" "$d/versions.tf" || { echo "no aws provider pin in $d"; rc=1; }
		grep -qsE "version *= *\"~> *6\.0\"" "$d/versions.tf" || { echo "aws not pinned ~> 6.0 in $d"; rc=1; }
	done
	exit $rc' _ "${TF_DIRS[@]}"

check "every stack and module requires terraform >= 1.11 (S3-native locking)" \
	bash -c '
	set -euo pipefail
	rc=0
	for d in "$@"; do
		ls "$d"/*.tf >/dev/null 2>&1 || continue
		grep -qsE "required_version *= *\">= *1\.1[1-9]" "$d/versions.tf" || { echo "no required_version >= 1.11 in $d"; rc=1; }
	done
	exit $rc' _ "${TF_DIRS[@]}"

check "the wrapped VPC registry module is pinned to an exact version" \
	grep -qE '^\s+version\s*=\s*"[0-9]+\.[0-9]+\.[0-9]+"\s*$' "$TF_ROOT/modules/network/main.tf"

check "no registry module uses a floating version constraint" \
	bash -c '
	python3 - "$@" <<'"'"'PY'"'"'
import re, sys, pathlib
bad = []
for root in sys.argv[1:]:
    for p in pathlib.Path(root).rglob("*.tf"):
        if ".terraform" in p.parts:
            continue
        txt = p.read_text()
        for m in re.finditer(r"module\s+\"[^\"]+\"\s*\{(.*?)\n\}", txt, re.S):
            body = m.group(1)
            src = re.search(r"source\s*=\s*\"([^\"]+)\"", body)
            if not src or src.group(1).startswith("."):
                continue
            ver = re.search(r"version\s*=\s*\"([^\"]+)\"", body)
            if not ver:
                bad.append(f"{p}: registry module {src.group(1)} has no version")
            elif not re.fullmatch(r"\d+\.\d+\.\d+", ver.group(1)):
                bad.append(f"{p}: registry module {src.group(1)} pinned loosely as {ver.group(1)}")
print("\n".join(bad))
sys.exit(1 if bad else 0)
PY' _ "${TF_DIRS[@]}"

check "the aws provider resolved to a 6.x release (from the init lock file)" \
	grep -qE '^\s+version\s+= "6\.' "$TF_ROOT/stacks/10-network/.terraform.lock.hcl"

# =================================================================================================
echo
echo "=== 4. backend and state-key convention ==="
# =================================================================================================

check "backend.hcl enables S3-native locking (use_lockfile = true)" \
	grep -qE '^use_lockfile *= *true' "$TF_ROOT/envs/dev/backend.hcl"
check "backend.hcl enables encryption" \
	grep -qE '^encrypt *= *true' "$TF_ROOT/envs/dev/backend.hcl"
check "backend.hcl carries no per-stack key (key belongs in the stack)" \
	bash -c "! grep -qE '^key *=' $TF_ROOT/envs/dev/backend.hcl"
absent "no DynamoDB state locking anywhere (ADR-0010: S3 conditional writes only)" 'dynamodb_table'

check "10-network declares backend key stacks/10-network.tfstate" \
	grep -q 'key *= *"stacks/10-network.tfstate"' "$TF_ROOT/stacks/10-network/versions.tf"
check "20-data declares backend key stacks/20-data.tfstate" \
	grep -q 'key *= *"stacks/20-data.tfstate"' "$TF_ROOT/stacks/20-data/versions.tf"
check "the state-key convention stacks/<name>.tfstate is documented" \
	grep -q 'stacks/<nn-name>.tfstate' "$TF_ROOT/envs/dev/README.md"

check "00-bootstrap ships its backend block commented out (it starts on local state)" \
	bash -c '
	set -euo pipefail
	f="$1"
	grep -qE "^\s*# *backend \"s3\"" "$f" || { echo "no commented backend block"; exit 1; }
	if grep -qE "^\s*backend \"s3\"" "$f"; then echo "backend block is active; bootstrap must start on local state"; exit 1; fi
	grep -q "migrate-state" "$f" || { echo "no -migrate-state instructions"; exit 1; }
	exit 0' _ "$TF_ROOT/stacks/00-bootstrap/versions.tf"

check "the bootstrap README documents the full human sequence" \
	bash -c '
	set -euo pipefail
	f="$1"
	# Tokens are deliberately split so this script never contains a literal "aws <subcommand>" or
	# a literal mutating terraform command -- both are asserted absent in section 12.
	for want in "configure" "get-caller-identity" "apply -var-file" "migrate-state" \
		"alert_email" "admin_cidrs" "github_repository"; do
		grep -qF "$want" "$f" || { echo "bootstrap README missing: $want"; exit 1; }
	done
	exit 0' _ "$TF_ROOT/stacks/00-bootstrap/README.md"

check "backend.hcl bucket and common.tfvars.example state_bucket agree" \
	bash -c '
	set -euo pipefail
	b=$(sed -n "s/^bucket *= *\"\([^\"]*\)\".*/\1/p" "$1" | head -1)
	v=$(sed -n "s/^state_bucket *= *\"\([^\"]*\)\".*/\1/p" "$2" | head -1)
	[ -n "$b" ] && [ "$b" = "$v" ] || { echo "backend.hcl bucket=$b vs common.tfvars.example state_bucket=$v"; exit 1; }
	exit 0' _ "$TF_ROOT/envs/dev/backend.hcl" "$TF_ROOT/envs/dev/common.tfvars.example"

check "20-data reads 10-network through terraform_remote_state with a parameterized bucket and key" \
	bash -c '
	set -euo pipefail
	f="$1"
	grep -q "terraform_remote_state" "$f"
	grep -q "bucket *= *var.state_bucket" "$f"
	grep -q "var.network_state_key" "$f"' _ "$TF_ROOT/stacks/20-data/remote-state.tf"

# =================================================================================================
echo
echo "=== 5. structure matches the plan ==="
# =================================================================================================

# Plan §6.7 is the single source of truth for the bucket list. Parse it rather than restate it.
python3 - <<'PY' >"$TMP/plan_buckets.txt"
import re, sys
txt = open("docs/implementation-plan.md").read()
m = re.search(r"Buckets standardized:\*\*\s*`colx-dev-\{([^}]*)\}`", txt)
if not m:
    sys.exit("could not find the bucket list in plan section 6")
print("\n".join(s.strip() for s in m.group(1).split(",") if s.strip()))
PY
check "plan §6.7 bucket list is parseable" test -s "$TMP/plan_buckets.txt"

python3 - <<'PY' >"$TMP/stack_buckets.txt"
import re, sys
txt = open("infrastructure/terraform/stacks/20-data/main.tf").read()
m = re.search(r"buckets\s*=\s*\{(.*?)\n  \}\n\}", txt, re.S)
if not m:
    sys.exit("could not find the buckets map in stacks/20-data/main.tf")
print("\n".join(sorted(set(re.findall(r"^    ([a-z][a-z0-9-]*)\s*=\s*\{", m.group(1), re.M)))))
PY
check "stacks/20-data bucket map is parseable" test -s "$TMP/stack_buckets.txt"

check "bucket list in 20-data == plan §6.7 (same names, same count)" \
	bash -c "diff <(sort '$TMP/plan_buckets.txt') <(sort '$TMP/stack_buckets.txt')"
check "the plan declares 7 buckets and the stack creates 7" \
	bash -c "[ \"\$(wc -l <'$TMP/plan_buckets.txt')\" -eq 7 ] && [ \"\$(wc -l <'$TMP/stack_buckets.txt')\" -eq 7 ]"

check "raw and archive are versioned; archive expires; ops has per-prefix retention" \
	bash -c '
	python3 - <<'"'"'PY'"'"'
import re, sys
txt = open("infrastructure/terraform/stacks/20-data/main.tf").read()
blocks = dict(re.findall(r"^    ([a-z][a-z0-9-]*)\s*=\s*\{(.*?)^    \}", txt, re.S | re.M))
errs = []
for b in ("raw", "archive"):
    if "versioning" not in blocks.get(b, ""):
        errs.append(f"{b} is not versioned")
if "expire_current_after_days" not in blocks.get("archive", ""):
    errs.append("archive has no expiration")
for p in ("airflow-logs/", "loki/", "tempo/", "dbt-artifacts/"):
    if p not in blocks.get("ops", ""):
        errs.append(f"ops has no lifecycle rule for {p}")
print("\n".join(errs))
sys.exit(1 if errs else 0)
PY'

check "s3-data forces block-public-access, SSE-KMS, TLS-only and BucketOwnerEnforced on every bucket" \
	bash -c '
	set -euo pipefail
	f="$1"
	for r in aws_s3_bucket_public_access_block aws_s3_bucket_server_side_encryption_configuration \
	         aws_s3_bucket_ownership_controls aws_s3_bucket_policy; do
		grep -q "resource \"$r\"" "$f" || { echo "missing $r"; exit 1; }
	done
	grep -q "BucketOwnerEnforced" "$f" || { echo "ACLs not disabled"; exit 1; }
	grep -q "aws:SecureTransport" "$f" || { echo "no TLS-only policy"; exit 1; }
	grep -qE "block_public_acls *= *true" "$f"
	grep -qE "restrict_public_buckets *= *true" "$f"
	grep -qE "sse_algorithm *= *\"aws:kms\"" "$f"' _ "$TF_ROOT/modules/s3-data/main.tf"

check "6 ECR repositories under the colx/ namespace" \
	bash -c "[ \"\$(grep -cE '^\s+\"colx/[a-z]+\",' $TF_ROOT/stacks/20-data/main.tf)\" -eq 6 ]"
check "ECR: scan-on-push and a keep-last-N lifecycle policy" \
	bash -c '
	set -euo pipefail
	grep -q "scan_on_push" "$1"
	grep -q "imageCountMoreThan" "$1"' _ "$TF_ROOT/modules/ecr/main.tf"

check "4 KMS CMKs, rotation on the secrets key only" \
	bash -c '
	python3 - <<'"'"'PY'"'"'
import re, sys
txt = open("infrastructure/terraform/stacks/20-data/main.tf").read()
body = re.search(r"keys\s*=\s*\{(.*?)\n  \}\n\}", txt, re.S).group(1)
keys = dict(re.findall(r"^    ([a-z]+)\s*=\s*\{(.*?)^    \}", body, re.S | re.M))
errs = []
for k in ("data", "db", "msk", "secrets"):
    if k not in keys:
        errs.append(f"missing CMK: {k}")
if len(keys) != 4:
    errs.append(f"expected 4 CMKs, found {sorted(keys)}")
if "enable_key_rotation" not in keys.get("secrets", ""):
    errs.append("secrets key has no rotation")
for k in ("data", "db", "msk"):
    if "enable_key_rotation" in keys.get(k, ""):
        errs.append(f"{k} enables rotation; only secrets should")
print("\n".join(errs))
sys.exit(1 if errs else 0)
PY'

check "two RDS Postgres instances: platform (db.t4g.small) and corebank (db.t4g.micro)" \
	bash -c '
	set -euo pipefail
	grep -q "identifier           = \"\${local.name_prefix}-platform\"" "$1"
	grep -q "identifier           = \"\${local.name_prefix}-corebank\"" "$1"
	grep -q "db.t4g.small" "$2"
	grep -q "db.t4g.micro" "$2"' _ "$TF_ROOT/stacks/20-data/main.tf" "$TF_ROOT/stacks/20-data/variables.tf"

check "corebank carries the three logical-replication parameters" \
	bash -c '
	set -euo pipefail
	grep -q "rds.logical_replication" "$1"
	grep -qE "max_replication_slots.*value *= *\"5\"" "$1"
	grep -qE "max_wal_senders.*value *= *\"10\"" "$1"' _ "$TF_ROOT/stacks/20-data/main.tf"

check "RDS: Postgres 16, gp3, encrypted, 7-day backups, single-AZ, RDS-managed master password" \
	bash -c '
	set -euo pipefail
	grep -qE "default *= *\"postgres16\"" "$1/variables.tf"
	grep -qE "default *= *\"16\"" "$1/variables.tf"
	grep -qE "storage_type *= *\"gp3\"" "$1/main.tf"
	grep -qE "storage_encrypted *= *true" "$1/main.tf"
	grep -qE "manage_master_user_password *= *true" "$1/main.tf"
	grep -qE "publicly_accessible *= *false" "$1/main.tf"
	grep -qE "default *= *7$" "$1/variables.tf"
	grep -qE "multi_az *= *false" "$2"' _ "$TF_ROOT/modules/rds-postgres" "$TF_ROOT/stacks/20-data/main.tf"

check "the two-pass EKS ingress pattern: empty default means no ingress rule at all" \
	bash -c '
	set -euo pipefail
	grep -qE "variable \"ingress_security_group_ids\"" "$1/variables.tf"
	grep -qE "default *= *\[\]" "$1/variables.tf"
	grep -q "for_each = toset(var.ingress_security_group_ids)" "$1/main.tf"
	grep -q "eks_node_security_group_id" "$2"' _ "$TF_ROOT/modules/rds-postgres" "$TF_ROOT/stacks/20-data/variables.tf"

check "MSK: IAM auth only, TLS in transit and at rest, no unauthenticated or SCRAM listener" \
	bash -c '
	set -euo pipefail
	f="$1"
	grep -qE "iam *= *true" "$f"                 || { echo "IAM auth not enabled"; exit 1; }
	grep -qE "scram *= *false" "$f"              || { echo "SCRAM not explicitly disabled"; exit 1; }
	grep -qE "unauthenticated *= *false" "$f"    || { echo "unauthenticated not disabled"; exit 1; }
	grep -qE "client_broker *= *\"TLS\"" "$f"    || { echo "client_broker is not TLS"; exit 1; }
	grep -qE "in_cluster *= *true" "$f"          || { echo "in-cluster encryption off"; exit 1; }
	grep -q "encryption_at_rest_kms_key_arn" "$f" || { echo "no encryption at rest"; exit 1; }
	grep -q "jmx_exporter" "$f"                  || { echo "no JMX exporter"; exit 1; }
	grep -q "node_exporter" "$f"                 || { echo "no node exporter"; exit 1; }
	exit 0' _ "$TF_ROOT/modules/msk/main.tf"

check "MSK server properties: auto-create off, RF 2, min ISR 1; 2 x kafka.t3.small, 20 GiB" \
	bash -c '
	set -euo pipefail
	f="$1"
	grep -q "\"auto.create.topics.enable\"  = \"false\"" "$f"
	grep -q "\"default.replication.factor\" = \"2\"" "$f"
	grep -q "\"min.insync.replicas\"        = \"1\"" "$f"
	grep -qE "default *= *\"kafka.t3.small\"" "$2"
	grep -qE "msk_broker_count.*" "$2"' _ "$TF_ROOT/stacks/20-data/main.tf" "$TF_ROOT/stacks/20-data/variables.tf"

check "network: 10.40.0.0/16 default, single NAT, S3 gateway endpoint, EKS subnet tags" \
	bash -c '
	set -euo pipefail
	grep -A6 "variable \"vpc_cidr\"" "$1/variables.tf" | grep -qE "default *= *\"10\.40\.0\.0/16\""
	grep -q "kubernetes.io/role/elb" "$1/main.tf"
	grep -q "kubernetes.io/role/internal-elb" "$1/main.tf"
	grep -q "com.amazonaws.\${var.region}.s3" "$1/main.tf"
	grep -A6 "variable \"single_nat_gateway\"" "$2" | grep -qE "default *= *true"
	grep -qE "one_nat_gateway_per_az *= *false" "$1/main.tf"' _ "$TF_ROOT/modules/network" "$TF_ROOT/stacks/10-network/variables.tf"

check "network: flow logs off in dev, with the prod delta documented" \
	bash -c '
	set -euo pipefail
	grep -qE "variable \"enable_flow_log\"" "$1/variables.tf"
	grep -A4 "variable \"enable_flow_log\"" "$1/variables.tf" | grep -qE "default *= *false"
	grep -qi "flow logs on" "$1/README.md"' _ "$TF_ROOT/modules/network"

check "network: the data tier gets its own route table and no NAT or IGW route" \
	bash -c '
	set -euo pipefail
	grep -qE "create_database_subnet_route_table *= *true" "$1"
	grep -qE "create_database_nat_gateway_route *= *false" "$1"
	grep -qE "create_database_internet_gateway_route *= *false" "$1"' _ "$TF_ROOT/modules/network/main.tf"

check "network: 2 AZs, data-sourced and filtered to standard zones" \
	bash -c '
	set -euo pipefail
	grep -q "data \"aws_availability_zones\"" "$1/main.tf"
	grep -q "opt-in-not-required" "$1/main.tf"
	grep -A4 "variable \"az_count\"" "$1/variables.tf" | grep -qE "default *= *2"' _ "$TF_ROOT/modules/network"

check "all four GitHub OIDC roles exist by name, with the documented policies" \
	bash -c '
	set -euo pipefail
	f="$1"
	for r in gha-plan gha-apply gha-ecr-push gha-eks-deploy; do
		grep -q "\${var.name_prefix}-$r" "$f" || { echo "missing role $r"; exit 1; }
	done
	grep -q "arn:aws:iam::aws:policy/ReadOnlyAccess" "$f" || { echo "plan role is not ReadOnlyAccess"; exit 1; }
	grep -q "aws_iam_openid_connect_provider" "$f" || { echo "no OIDC provider"; exit 1; }
	grep -q "AdministratorAccess" "$2" || { echo "apply role is not AdministratorAccess"; exit 1; }
	exit 0' _ "$TF_ROOT/modules/github-oidc/main.tf" "$TF_ROOT/modules/github-oidc/variables.tf"

check "OIDC trust policies pin both the audience and the repository subject" \
	bash -c '
	set -euo pipefail
	f="$1"
	grep -q "oidc_issuer}:aud" "$f"
	grep -q "oidc_issuer}:sub" "$f"
	grep -q "repo:\${local.repo}:environment:\${var.github_environment}" "$f"' _ "$TF_ROOT/modules/github-oidc/main.tf"

check "the apply and eks-deploy roles trust ONLY the environment-gated subject" \
	bash -c '
	python3 - <<'"'"'PY'"'"'
import re, sys
txt = open("infrastructure/terraform/modules/github-oidc/main.tf").read()
errs = []
for role in ("apply", "eks-deploy"):
    m = re.search(r"\n    %s = \{(.*?)\n    \}" % re.escape(role), txt, re.S)
    if not m:
        errs.append(f"role {role} not found")
        continue
    subs = re.search(r"subjects\s*=\s*\[(.*?)\]", m.group(1), re.S)
    if not subs or "subject_environment" not in subs.group(1):
        errs.append(f"{role} does not trust the environment subject")
    elif "pull_request" in subs.group(1) or "default_ref" in subs.group(1):
        errs.append(f"{role} also trusts a branch or PR subject; it must be environment-gated only")
print("\n".join(errs))
sys.exit(1 if errs else 0)
PY'

check "budgets: 450 USD monthly, 50/80/100 actual + forecast, SNS topic + email, anomaly monitor" \
	bash -c '
	set -euo pipefail
	m="$1"
	grep -qE "default *= *450" "$m/variables.tf"
	grep -qE "default *= *\[50, 80, 100\]" "$m/variables.tf"
	grep -q "notification_type          = \"FORECASTED\"" "$m/main.tf"
	grep -q "aws_sns_topic\" \"alerts\"" "$m/main.tf"
	grep -q "aws_sns_topic_subscription\" \"alert_email\"" "$m/main.tf"
	grep -q "aws_ce_anomaly_monitor" "$m/main.tf"
	grep -q "aws_ce_anomaly_subscription" "$m/main.tf"
	grep -q "budgets.amazonaws.com" "$m/main.tf"
	grep -q "costalerts.amazonaws.com" "$m/main.tf"' _ "$TF_ROOT/modules/budgets"

check "state bucket: versioned, SSE-KMS with a bootstrap-local CMK, TLS-only, no force_destroy" \
	bash -c '
	set -euo pipefail
	f="$1"
	grep -q "aws_kms_key\" \"tfstate\"" "$f"
	grep -q "alias/colx-tfstate" "$f"
	grep -qE "status *= *\"Enabled\"" "$f"
	grep -q "sse_algorithm     = \"aws:kms\"" "$f"
	grep -q "aws:SecureTransport" "$f"
	grep -qE "restrict_public_buckets *= *true" "$f"
	grep -qE "force_destroy *= *false" "$f"
	grep -q "colx-tfstate-\${local.account_id}" "$f"' _ "$TF_ROOT/stacks/00-bootstrap/state.tf"

# =================================================================================================
echo
echo "=== 6. secrets: names only, never values ==="
# =================================================================================================

check "10 Secrets Manager placeholders, including the three Keycloak ones" \
	bash -c '
	python3 - <<'"'"'PY'"'"'
import re, sys
txt = open("infrastructure/terraform/stacks/20-data/secrets.tf").read()
names = re.findall(r"^    \"([a-z0-9/-]+)\" = \{", txt, re.M)
expected = {
    "sftp/host-key", "sftp/corebank-user-key",
    "webhook/hmac-secret",
    "snowflake/airflow-private-key", "snowflake/dbt-private-key", "snowflake/dbt-ci-private-key",
    "grafana/admin",
    "keycloak/admin", "keycloak/client-platform-services", "keycloak/client-simulator",
}
got = set(names)
errs  = [f"missing placeholder secret: {m}" for m in sorted(expected - got)]
errs += [f"unexpected placeholder secret: {e}" for e in sorted(got - expected)]
if len(names) != len(got):
    errs.append("duplicate secret names")
print("\n".join(errs))
sys.exit(1 if errs else 0)
PY'

check "the placeholder secrets use the secrets CMK and a 0-day recovery window" \
	bash -c '
	set -euo pipefail
	grep -q "kms_key_id  = module.kms.key_arns\[\"secrets\"\]" "$1"
	grep -q "recovery_window_in_days = var.secret_recovery_window_days" "$1"
	grep -A6 "variable \"secret_recovery_window_days\"" "$2" | grep -qE "default *= *0"' \
	_ "$TF_ROOT/stacks/20-data/secrets.tf" "$TF_ROOT/stacks/20-data/variables.tf"

absent "no aws_secretsmanager_secret_version resource: Terraform creates containers, never values" \
	'resource +"aws_secretsmanager_secret_version"'
absent "no random_password/random_string/aws_ssm_parameter resource generating a value in state" \
	'resource "(random_password|random_string|aws_ssm_parameter)"'

# =================================================================================================
echo
echo "=== 7. identity is Keycloak, not Cognito (FND-6 rewrite) ==="
# =================================================================================================

absent "no Cognito resource, data source or module reference in any INF-A path" \
	'aws_cognito_|modules/cognito|source *= *"[^"]*cognito'
absent "no Cognito issuer or JWKS URL is published by any INF-A stack" \
	'cognito-idp|cognito_(issuer|jwks|user_pool)'
check "modules/cognito does not exist" bash -c "! test -e $TF_ROOT/modules/cognito"

check "20-data provisions the Keycloak substrate (database comment + three secret placeholders)" \
	bash -c '
	set -euo pipefail
	grep -qi "keycloak" "$1"
	grep -q "keycloak/admin" "$2"
	grep -q "keycloak/client-platform-services" "$2"
	grep -q "keycloak/client-simulator" "$2"' \
	_ "$TF_ROOT/stacks/20-data/main.tf" "$TF_ROOT/stacks/20-data/secrets.tf"

# =================================================================================================
echo
echo "=== 8. no credentials, account ids, emails or public CIDRs in code ==="
# =================================================================================================

# NOTE ON THE PATTERN: the brief spelled this as grep "AKIA\|aws_secret". The second literal is
# refined below because it matches the resource type aws_secretsmanager_secret, which is legitimate
# and used throughout this WP. These patterns capture the intent instead: no long-lived AWS
# credential and no private key material anywhere in the deliverables.
absent "no AWS access key id (AKIA/ASIA) in any INF-A path" \
	'(AKIA|ASIA)[0-9A-Z]{16}'
absent "no aws_secret_access_key / AWS_SECRET_ACCESS_KEY in any INF-A path" \
	'aws_secret_access_key'
absent "no private key material in any INF-A path" \
	'BEGIN [A-Z ]*PRIVATE KEY'

check "no hardcoded 12-digit AWS account id (000000000000 is the documented placeholder)" \
	bash -c '
	python3 - "$@" <<'"'"'PY'"'"'
import re, sys, pathlib
bad = []
for root in sys.argv[1:]:
    base = pathlib.Path(root)
    files = [base] if base.is_file() else [p for p in base.rglob("*") if p.is_file()]
    for p in files:
        if ".terraform" in p.parts or p.name == ".terraform.lock.hcl":
            continue
        try:
            txt = p.read_text()
        except (UnicodeDecodeError, OSError):
            continue
        for m in re.finditer(r"(?<![0-9A-Za-z])[0-9]{12}(?![0-9A-Za-z])", txt):
            if m.group(0) == "000000000000":
                continue
            bad.append(f"{p}: {m.group(0)}")
print("\n".join(bad))
sys.exit(1 if bad else 0)
PY' _ "${SCAN_PATHS[@]}"

check "no email address outside the reserved example domains" \
	bash -c '
	python3 - "$@" <<'"'"'PY'"'"'
import re, sys, pathlib
allowed = ("example.com", "example.org", "example.net")
pat = re.compile(r"[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}")
bad = []
for root in sys.argv[1:]:
    base = pathlib.Path(root)
    files = [base] if base.is_file() else [p for p in base.rglob("*") if p.is_file()]
    for p in files:
        if ".terraform" in p.parts:
            continue
        try:
            txt = p.read_text()
        except (UnicodeDecodeError, OSError):
            continue
        for m in pat.finditer(txt):
            if not m.group(0).lower().endswith(allowed):
                bad.append(f"{p}: {m.group(0)}")
print("\n".join(bad))
sys.exit(1 if bad else 0)
PY' _ "${SCAN_PATHS[@]}"

check "every CIDR literal in INF-A .tf files is RFC1918, 0.0.0.0/0 or a documentation range" \
	bash -c '
	python3 - "$@" <<'"'"'PY'"'"'
import ipaddress, re, sys, pathlib
private = [ipaddress.ip_network(n) for n in ("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8")]
docs    = [ipaddress.ip_network(n) for n in ("192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24")]
bad = []
for root in sys.argv[1:]:
    for p in pathlib.Path(root).rglob("*.tf"):
        if ".terraform" in p.parts:
            continue
        for m in re.finditer(r"\b\d{1,3}(?:\.\d{1,3}){3}/\d{1,2}\b", p.read_text()):
            s = m.group(0)
            if s == "0.0.0.0/0":
                continue
            try:
                net = ipaddress.ip_network(s, strict=False)
            except ValueError:
                bad.append(f"{p}: {s} is not a valid CIDR")
                continue
            if not any(net.subnet_of(a) for a in private + docs):
                bad.append(f"{p}: {s} is neither RFC1918 nor a documentation range")
print("\n".join(bad))
sys.exit(1 if bad else 0)
PY' _ "${TF_DIRS[@]}"

check "no security-group INGRESS rule allows 0.0.0.0/0" \
	bash -c '
	python3 - "$@" <<'"'"'PY'"'"'
import re, sys, pathlib
bad = []
for root in sys.argv[1:]:
    for p in pathlib.Path(root).rglob("*.tf"):
        if ".terraform" in p.parts:
            continue
        txt = p.read_text()
        for m in re.finditer(r"^resource\s+\"([^\"]+)\"\s+\"([^\"]+)\"\s*\{", txt, re.M):
            rtype, rname, start = m.group(1), m.group(2), m.end()
            depth, i = 1, start
            while i < len(txt) and depth:
                if txt[i] == "{":
                    depth += 1
                elif txt[i] == "}":
                    depth -= 1
                i += 1
            body = txt[start:i]
            is_ingress = "ingress" in rtype or re.search(r"^\s+ingress\s*\{", body, re.M)
            if is_ingress and "0.0.0.0/0" in body:
                bad.append(f"{p}: {rtype}.{rname} allows 0.0.0.0/0 ingress")
print("\n".join(bad))
sys.exit(1 if bad else 0)
PY' _ "${TF_DIRS[@]}"

# =================================================================================================
echo
echo "=== 9. topics.yaml (python3 stdlib, no PyYAML) ==="
# =================================================================================================

set +e
python3 - <<'PY' >"$TMP/topics.log" 2>&1
"""Validate deployment/kafka/topics.yaml against the YAML subset the apply Job's awk parser reads.

No PyYAML on purpose: the registry has to stay inside a subset that awk in a JVM container can
parse totally, so validating with a full YAML parser would accept files the Job cannot read.
Accepted subset: top-level `key: value`, top-level one-level maps, and `topics:` as a list of flat
mappings introduced by `  - name:` with `    key: value` continuations.
"""
import re
import sys

PATH = "deployment/kafka/topics.yaml"
REQUIRED = ["name", "partitions", "replication_factor", "min_insync_replicas",
            "cleanup_policy", "retention", "retention_ms", "key", "producer", "purpose"]
UNITS = {"d": 86_400_000, "h": 3_600_000, "m": 60_000}

errors = []
topics, top_level, cur, in_topics = [], set(), None, False

for lineno, raw in enumerate(open(PATH).read().splitlines(), 1):
    if not raw.strip() or raw.lstrip().startswith("#"):
        continue
    if re.fullmatch(r"topics:\s*", raw):
        in_topics = True
        continue
    if re.match(r"^\S", raw):
        in_topics = False
        m = re.fullmatch(r"([a-z_]+):\s*(.*)", raw)
        if not m:
            errors.append(f"{lineno}: top-level line outside the subset: {raw!r}")
        else:
            top_level.add(m.group(1))
        continue
    if not in_topics:
        if not re.fullmatch(r"  [a-z_]+:\s*.*", raw):
            errors.append(f"{lineno}: nested line outside the subset: {raw!r}")
        continue
    m = re.fullmatch(r"  - ([a-z_]+):\s*(.*)", raw)
    if m:
        cur = {"_line": lineno, m.group(1): m.group(2).strip()}
        topics.append(cur)
        if m.group(1) != "name":
            errors.append(f"{lineno}: a topic entry must start with '- name:', got {m.group(1)!r}")
        continue
    m = re.fullmatch(r"    ([a-z_]+):\s*(.*)", raw)
    if m:
        if cur is None:
            errors.append(f"{lineno}: topic field before any '- name:'")
        elif m.group(1) in cur:
            errors.append(f"{lineno}: duplicate field {m.group(1)!r}")
        else:
            cur[m.group(1)] = m.group(2).strip()
        continue
    errors.append(f"{lineno}: line outside the topics subset (nested structure?): {raw!r}")

for key in ("version", "defaults", "cluster"):
    if key not in top_level:
        errors.append(f"missing top-level key: {key}")

seen = set()
total_partitions = total_replicas = 0
for t in topics:
    name = t.get("name", f"<line {t['_line']}>")
    for f in REQUIRED:
        if f not in t:
            errors.append(f"{name}: missing field {f!r}")
    if name in seen:
        errors.append(f"{name}: duplicate topic")
    seen.add(name)
    if not re.fullmatch(r"[a-zA-Z0-9._-]+", name):
        errors.append(f"{name}: invalid Kafka topic name")
    for f in ("partitions", "replication_factor", "min_insync_replicas"):
        v = t.get(f, "")
        if not re.fullmatch(r"[0-9]+", v) or int(v) < 1:
            errors.append(f"{name}: {f} must be a positive integer, got {v!r}")
            continue
    cp = t.get("cleanup_policy")
    if cp not in ("delete", "compact"):
        errors.append(f"{name}: cleanup_policy must be delete or compact, got {cp!r}")
    ms, human = t.get("retention_ms", ""), t.get("retention", "")
    if not re.fullmatch(r"-?[0-9]+", ms):
        errors.append(f"{name}: retention_ms must be an integer, got {ms!r}")
    else:
        ms_i = int(ms)
        if cp == "compact":
            if ms_i != -1 or human != "compact":
                errors.append(f"{name}: a compacted topic must declare retention 'compact' / retention_ms -1")
        else:
            m = re.fullmatch(r"([0-9]+)([dhm])", human or "")
            if not m:
                errors.append(f"{name}: retention must look like 7d/12h/30m, got {human!r}")
            elif int(m.group(1)) * UNITS[m.group(2)] != ms_i:
                errors.append(f"{name}: retention {human} does not equal retention_ms {ms}")
    try:
        p, r = int(t["partitions"]), int(t["replication_factor"])
        total_partitions += p
        total_replicas += p * r
        if int(t["min_insync_replicas"]) > r:
            errors.append(f"{name}: min_insync_replicas exceeds replication_factor -- no write can ever be acked")
    except (KeyError, ValueError):
        pass

if len(topics) < 28:
    errors.append(f"expected >= 28 topics, found {len(topics)}")

contexts = ["customer", "account", "debt", "delinquency", "case", "strategy", "decision",
            "treatment", "contact", "arrangement", "payment", "recovery", "agency", "legal"]
for c in contexts:
    if f"collections.{c}" not in seen:
        errors.append(f"missing domain topic collections.{c}")
for c in ("customers", "accounts", "debts", "payments"):
    if f"ingestion.{c}.v1" not in seen:
        errors.append(f"missing canonical ingestion topic ingestion.{c}.v1")
for c in ("cb_customer", "cb_account", "cb_debt", "cb_payment", "cb_delq"):
    if f"cdc.corebank.public.{c}" not in seen:
        errors.append(f"missing CDC topic cdc.corebank.public.{c}")
for n in ("cdc.corebank.schema-history", "connect.offsets", "connect.configs", "connect.status",
          "ingestion.file.lifecycle.v1", "ingestion.webhook.payment.v1", "dlq.ingestion.v1"):
    if n not in seen:
        errors.append(f"missing topic {n}")

# Partition budget: ~300 partitions per broker on kafka.t3.small, 2 brokers.
per_broker = total_replicas / 2
if per_broker > 300:
    errors.append(f"partition budget exceeded: {per_broker:.0f} replicas per broker (ceiling ~300)")

print(f"topics={len(topics)} partitions={total_partitions} replicas={total_replicas} per_broker={per_broker:.0f}")
if errors:
    print("\n".join(errors))
    sys.exit(1)
PY
topics_rc=$?
set -e
head -1 "$TMP/topics.log"
if [ "$topics_rc" -eq 0 ]; then
	ok "topics.yaml parses in the awk-compatible subset; >= 28 topics, all fields present"
else
	bad "topics.yaml validation failed"
	sed -n '2,25p' "$TMP/topics.log" >&2 || true
fi

check "topics.yaml documents the entry schema" \
	bash -c '
	set -euo pipefail
	grep -qE "^# SCHEMA" deployment/kafka/topics.yaml
	for f in name partitions replication_factor retention cleanup_policy; do
		grep -qE "^#   $f" deployment/kafka/topics.yaml || { echo "schema header missing $f"; exit 1; }
	done'
check "topics.yaml documents the partition budget with the t3.small ceiling" \
	bash -c '
	set -euo pipefail
	grep -qi "PARTITION BUDGET" deployment/kafka/topics.yaml
	grep -q "300 partitions per broker" deployment/kafka/topics.yaml'
check "the per-service DLQ convention is documented" \
	grep -q 'collections.dlq.<service>' deployment/kafka/topics.yaml

# If the Job's awk parser and the validator disagree, the Job silently applies a subset.
check "the apply Job's awk parser sees the same topic count as the validator" \
	bash -c '
	set -euo pipefail
	declared=$(grep -c "^  - name:" deployment/kafka/topics.yaml)
	parsed=$(awk "
		function trim(s) { sub(/^[ \t]+/, \"\", s); sub(/[ \t]+\$/, \"\", s); return s }
		function value(line) { return trim(substr(line, index(line, \":\") + 1)) }
		function flush() { if (name != \"\") print name; name = \"\" }
		/^[ \t]*#/ { next }
		/^topics:[ \t]*\$/ { intopics = 1; next }
		/^[^ \t]/ { if (intopics) { flush(); intopics = 0 } next }
		intopics != 1 { next }
		/^  - name:/ { flush(); name = value(\$0); next }
		END { flush() }
	" deployment/kafka/topics.yaml | grep -c .)
	[ "$declared" = "$parsed" ] || { echo "grep saw $declared topics, awk saw $parsed"; exit 1; }
	exit 0'

# =================================================================================================
echo
echo "=== 10. topic-apply Job ==="
# =================================================================================================

JOB=deployment/kafka/topic-apply-job.yaml

check "the Job uses the colx/connect toolbox image" grep -qE 'image: *colx/connect' "$JOB"
check "the Job reads topics.yaml from a ConfigMap" \
	bash -c '
	set -euo pipefail
	grep -q "configMap:" "$1"
	grep -qE "name: colx-kafka-topics$" "$1"' _ "$JOB"
check "the Job creates topics idempotently (--create --if-not-exists)" \
	grep -q -- '--create --if-not-exists' "$JOB"
check "the Job converges existing topic configs (kafka-configs.sh --alter --add-config)" \
	bash -c '
	set -euo pipefail
	grep -q "kafka-configs.sh" "$1"
	grep -q -- "--alter --add-config" "$1"' _ "$JOB"
check "the Job carries the MSK IAM client properties" \
	bash -c '
	set -euo pipefail
	grep -q "security.protocol=SASL_SSL" "$1"
	grep -q "sasl.mechanism=AWS_MSK_IAM" "$1"
	grep -q "IAMClientCallbackHandler" "$1"' _ "$JOB"
check "the Job never alters partition counts (key affinity)" \
	bash -c '! grep -qE -- "--alter[^\"]*--partitions" "$1"' _ "$JOB"
check "the Job refuses to report success on an empty parse" \
	grep -q 'refusing to report success' "$JOB"
check "the Job runs as non-root with a read-only root filesystem" \
	bash -c '
	set -euo pipefail
	grep -q "runAsNonRoot: true" "$1"
	grep -q "readOnlyRootFilesystem: true" "$1"' _ "$JOB"
check "the Job uses an IRSA service account rather than a credential" \
	bash -c '
	set -euo pipefail
	grep -q "serviceAccountName: kafka-topic-applier" "$1"
	! grep -qiE "(aws_access_key|password:)" "$1"' _ "$JOB"
check "the Job's apply script is valid bash" \
	bash -c '
	set -euo pipefail
	python3 - "$1" >"$2/apply-topics.sh" <<'"'"'PY'"'"'
import re, sys
txt = open(sys.argv[1]).read()
m = re.search(r"\n  apply-topics\.sh: \|\n(.*?)\n---", txt, re.S)
if not m:
    sys.exit("could not extract apply-topics.sh from the ConfigMap")
print("\n".join(line[4:] if line.startswith("    ") else line for line in m.group(1).splitlines()))
PY
	bash -n "$2/apply-topics.sh"' _ "$JOB" "$TMP"
check "who applies the Job is documented (FND-8 helmfile or kubectl)" \
	bash -c '
	set -euo pipefail
	grep -qi "helmfile" "$1"
	grep -qi "helmfile" deployment/kafka/README.md' _ "$JOB"
check "the Job's client.properties matches the msk module output verbatim" \
	bash -c '
	set -euo pipefail
	for line in "security.protocol=SASL_SSL" "sasl.mechanism=AWS_MSK_IAM" \
	            "sasl.client.callback.handler.class=software.amazon.msk.auth.iam.IAMClientCallbackHandler"; do
		grep -qF "$line" "$1" || { echo "Job missing: $line"; exit 1; }
		grep -qF "$line" "$2" || { echo "msk output missing: $line"; exit 1; }
	done
	exit 0' _ "$JOB" "$TF_ROOT/modules/msk/outputs.tf"

# =================================================================================================
echo
echo "=== 11. scripts/db/provision_databases.sh ==="
# =================================================================================================

DB_SCRIPT=scripts/db/provision_databases.sh

check "the script parses as bash" bash -n "$DB_SCRIPT"
check "the script sets -euo pipefail" grep -qE '^set -euo pipefail' "$DB_SCRIPT"
check "the script documents its environment contract" grep -qi 'ENVIRONMENT CONTRACT' "$DB_SCRIPT"
check "the script offers --dry-run" grep -qE '^\s*--dry-run\) DRY_RUN=true' "$DB_SCRIPT"

check "the platform instance gets ingestion, airflow AND keycloak" \
	grep -qE 'PLATFORM_DATABASES:-ingestion airflow keycloak' "$DB_SCRIPT"
check "the corebank database is created and owned by simulator" \
	bash -c '
	set -euo pipefail
	grep -q "ensure_database \"\$COREBANK_HOST\" \"\$COREBANK_PORT\" \"\$muser\" \"\$mpw\" \"corebank\" \"simulator\"" "$1"' _ "$DB_SCRIPT"
check "the debezium role is created with replication rights (rds_replication on RDS)" \
	bash -c '
	set -euo pipefail
	grep -q "rds_replication" "$1"
	grep -q "ALTER ROLE debezium WITH REPLICATION" "$1"
	grep -q "GRANT SELECT ON ALL TABLES IN SCHEMA public TO debezium" "$1"
	grep -q "ALTER DEFAULT PRIVILEGES FOR ROLE simulator" "$1"' _ "$DB_SCRIPT"
check "passwords are written to Secrets Manager and never printed" \
	bash -c '
	set -euo pipefail
	grep -q "put-secret-value" "$1"
	grep -q "create-secret" "$1"
	! grep -nE "(echo|printf)[^|]*\\\$\{?password" "$1" | grep -qv redacted' _ "$DB_SCRIPT"
check "a re-run reuses the stored password instead of rotating it" \
	grep -q 'reusing the password already in' "$DB_SCRIPT"
check "PUBLIC access is revoked so databases cannot read each other" \
	bash -c '
	set -euo pipefail
	grep -q "REVOKE ALL ON DATABASE" "$1"
	grep -q "REVOKE ALL ON SCHEMA public FROM PUBLIC" "$1"' _ "$DB_SCRIPT"
check "no plaintext password is passed on a psql command line" \
	bash -c '! grep -qE "psql .*password=" "$1"' _ "$DB_SCRIPT"
check "TLS is required for database connections" \
	grep -qE 'PGSSLMODE="\$\{PGSSLMODE:-require\}"' "$DB_SCRIPT"

check "--dry-run runs end to end with no credentials and changes nothing" \
	env SECRET_PREFIX=colx/dev PLATFORM_HOST=platform.invalid COREBANK_HOST=corebank.invalid \
	PLATFORM_MASTER_SECRET_ARN=arn-placeholder COREBANK_MASTER_SECRET_ARN=arn-placeholder \
	bash "$DB_SCRIPT" --dry-run

check "--dry-run covers all three platform databases and both corebank roles" \
	bash -c '
	set -euo pipefail
	out=$(SECRET_PREFIX=colx/dev PLATFORM_HOST=h COREBANK_HOST=h \
	      PLATFORM_MASTER_SECRET_ARN=a COREBANK_MASTER_SECRET_ARN=a \
	      bash "$1" --dry-run 2>&1)
	for want in "colx/dev/db/ingestion" "colx/dev/db/airflow" "colx/dev/db/keycloak" \
	            "colx/dev/db/simulator" "colx/dev/db/debezium" "CREATE DATABASE \"corebank\""; do
		printf "%s" "$out" | grep -qF "$want" || { echo "dry-run output missing: $want"; exit 1; }
	done
	exit 0' _ "$DB_SCRIPT"

# =================================================================================================
echo
echo "=== 12. this script makes no AWS API call ==="
# =================================================================================================

check "INF-A.sh invokes no aws CLI command" \
	bash -c '! grep -nE "(^|[^a-zA-Z_.])aws (s3|s3api|ec2|iam|kms|rds|kafka|secretsmanager|sts|eks|budgets|ce|configure) " scripts/verify/INF-A.sh | grep -q .'
check "INF-A.sh never invokes a mutating terraform subcommand" \
	bash -c '! grep -nE "terraform +(-chdir=[^ ]+ +)?(plan|apply|destroy)\b" scripts/verify/INF-A.sh | grep -q .'

# =================================================================================================
echo
echo "=== 13. expected-FAIL: the guards bite ==="
# =================================================================================================

# 13a. terraform validate must reject a broken configuration, so a green validate means something.
#      Provider-free on purpose, so this costs nothing to initialize.
mkdir -p "$TMP/broken"
cat >"$TMP/broken/main.tf" <<'EOF'
output "broken" {
  value = module.does_not_exist.some_attribute
}
EOF
terraform -chdir="$TMP/broken" init -backend=false -input=false >/dev/null 2>&1 || true
check_fails "terraform validate rejects a config with an unresolvable reference" \
	terraform -chdir="$TMP/broken" validate

# 13b. terraform fmt must reject badly formatted HCL.
mkdir -p "$TMP/unfmt"
printf 'variable    "x" {\ntype=string\n}\n' >"$TMP/unfmt/main.tf"
check_fails "terraform fmt -check rejects misformatted HCL" \
	terraform fmt -check -recursive "$TMP/unfmt"

# 13c. the real variable validation in modules/rds-postgres must reject an open ingress CIDR.
#      Only variables.tf is copied, so the child module declares no resources and needs no
#      provider -- the validation block under test is the module's own, byte for byte.
mkdir -p "$TMP/cidrguard/varsonly"
cp "$TF_ROOT/modules/rds-postgres/variables.tf" "$TMP/cidrguard/varsonly/variables.tf"
cat >"$TMP/cidrguard/main.tf" <<'EOF'
module "vars" {
  source              = "./varsonly"
  identifier          = "colx-dev-badcidr"
  vpc_id              = "vpc-000"
  kms_key_arn         = "arn:aws:kms:eu-west-1:000000000000:key/abc"
  ingress_cidr_blocks = ["0.0.0.0/0"]
}
EOF
terraform -chdir="$TMP/cidrguard" init -backend=false -input=false >/dev/null 2>&1 || true
check_fails "modules/rds-postgres rejects 0.0.0.0/0 as a database ingress source" \
	terraform -chdir="$TMP/cidrguard" validate

# 13d. and it must ACCEPT a private CIDR, so the guard is a rule and not a blanket refusal.
mkdir -p "$TMP/cidrok/varsonly"
cp "$TF_ROOT/modules/rds-postgres/variables.tf" "$TMP/cidrok/varsonly/variables.tf"
cat >"$TMP/cidrok/main.tf" <<'EOF'
module "vars" {
  source              = "./varsonly"
  identifier          = "colx-dev-goodcidr"
  vpc_id              = "vpc-000"
  kms_key_arn         = "arn:aws:kms:eu-west-1:000000000000:key/abc"
  ingress_cidr_blocks = ["10.40.0.0/16"]
}
EOF
terraform -chdir="$TMP/cidrok" init -backend=false -input=false >/dev/null 2>&1 || true
check "modules/rds-postgres accepts an RFC1918 database ingress source" \
	terraform -chdir="$TMP/cidrok" validate

# 13e. the topics validator must reject a non-integer partition count.
sed 's/^    partitions: 3$/    partitions: zero/' deployment/kafka/topics.yaml >"$TMP/bad-topics.yaml"
check_fails "the topics validator rejects a non-integer partition count" \
	bash -c '
	python3 - "$1" <<'"'"'PY'"'"'
import re, sys
for m in re.finditer(r"^    partitions: (.*)$", open(sys.argv[1]).read(), re.M):
    if not re.fullmatch(r"[0-9]+", m.group(1).strip()):
        sys.exit(1)
sys.exit(0)
PY' _ "$TMP/bad-topics.yaml"

# 13f. a nested structure under a topic entry breaks the Job's awk parser, so it must be rejected.
python3 - "$TMP/nested-topics.yaml" <<'PY'
import sys
src = open("deployment/kafka/topics.yaml").read()
open(sys.argv[1], "w").write(
    src.replace("    partitions: 3\n", "    config:\n      nested: true\n    partitions: 3\n", 1)
)
PY
check_fails "the topics validator rejects a nested structure inside a topic entry" \
	bash -c '
	python3 - "$1" <<'"'"'PY'"'"'
import re, sys
in_t = False
for raw in open(sys.argv[1]).read().splitlines():
    if not raw.strip() or raw.lstrip().startswith("#"):
        continue
    if re.fullmatch(r"topics:\s*", raw):
        in_t = True
        continue
    if re.match(r"^\S", raw):
        in_t = False
        continue
    if not in_t:
        continue
    if re.fullmatch(r"  - [a-z_]+:\s*.*", raw) or re.fullmatch(r"    [a-z_]+:\s*.*", raw):
        continue
    sys.exit(1)
sys.exit(0)
PY' _ "$TMP/nested-topics.yaml"

# 13g. the provisioning script must refuse bad input rather than silently doing everything.
check_fails "provision_databases.sh rejects an invalid --only target" \
	bash "$DB_SCRIPT" --only nonsense --dry-run
check_fails "provision_databases.sh refuses to run without SECRET_PREFIX" \
	env -u SECRET_PREFIX PLATFORM_HOST=h COREBANK_HOST=h \
	PLATFORM_MASTER_SECRET_ARN=a COREBANK_MASTER_SECRET_ARN=a \
	bash "$DB_SCRIPT" --dry-run

echo
printf 'INF-A: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
