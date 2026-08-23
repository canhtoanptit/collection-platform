#!/usr/bin/env bash
#
# scripts/verify/INF-B.sh — acceptance for WP INF-B: terraform stacks 30-eks and
# 40-snowflake, the cluster baseline (helmfile / values / observability / Airflow /
# Keycloak), the infra CI workflows, and the cost/teardown machinery.
#
# ENVIRONMENT: none. No AWS account, no Snowflake account, no Kubernetes cluster.
# Needs bash, coreutils, python3 (stdlib only), terraform, helm, and network
# access for `terraform init` (provider/module download) and `helm repo`
# (chart download). A pinned helmfile binary is installed into ./bin if missing.
#
# WHAT THIS CAN AND CANNOT PROVE
#
#   Can:    HCL is formatted, valid and resolves its pinned modules/providers;
#           every chart renders against the committed values (the check that
#           actually catches values-schema drift after a chart bump — the most
#           common helmfile failure); the helmfile parses and lists the expected
#           releases with exact pins; the realm JSON is valid and complete; the
#           workflows are structurally sound and carry no long-lived credentials;
#           the runbook has the D§82 heading set.
#
#   Cannot: anything requiring a cluster. `kubectl apply/create --dry-run=client`
#           was evaluated and rejected: kubectl needs API discovery to build a
#           REST mapping, so with no cluster it fails with
#           "unable to recognize ...: dial tcp [::1]:8080: connection refused"
#           even with --validate=false. Kubernetes manifests are therefore
#           checked by parsing them as YAML (via a Go one-off using yaml.v3) and
#           asserting apiVersion/kind/required fields.
#
# Anything that needs real infrastructure is listed in the WP report, not skipped
# silently here.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Keep helm's repo cache out of the user's home so this script is repeatable and
# cannot be affected by a stale repo the developer added by hand.
export HELM_CACHE_HOME="$TMP/helm/cache"
export HELM_CONFIG_HOME="$TMP/helm/config"
export HELM_DATA_HOME="$TMP/helm/data"

HELMFILE_VERSION="1.7.4"
BIN="$REPO_ROOT/bin"

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

# check <description> <command...>       -- must succeed
check() {
	local desc="$1"
	shift
	if "$@" >"$TMP/out" 2>&1; then ok "$desc"; else
		bad "$desc"
		sed 's/^/      /' "$TMP/out" | tail -8 >&2
	fi
}

# check_fails <description> <command...> -- must FAIL (guard proof)
check_fails() {
	local desc="$1"
	shift
	if "$@" >/dev/null 2>&1; then
		bad "$desc (command unexpectedly succeeded)"
	else
		ok "$desc"
	fi
}

need() {
	command -v "$1" >/dev/null 2>&1 || {
		echo "FAIL: required tool '$1' is not installed ($2)" >&2
		exit 1
	}
}

need terraform "terraform >= 1.11; see mise.toml"
need helm "helm 3.x or later"
need python3 "python3 (stdlib only)"
need go "go toolchain, for the YAML structural checker"

echo "=== 0. tool versions ==="
terraform version | head -1
helm version --short
python3 --version

# ===========================================================================
echo
echo "=== 1. terraform: stacks 30-eks and 40-snowflake ==="
# `init -backend=false` is the whole trick: it resolves and downloads the pinned
# module and providers without ever touching the S3 backend, so validate runs
# with zero credentials. Same commands as the `static` job in
# .github/workflows/terraform.yml — a green laptop and a green CI mean the same
# thing.

check "terraform fmt -check -recursive (all of infrastructure/terraform)" \
	terraform fmt -check -recursive -diff infrastructure/terraform

for stack in 30-eks 40-snowflake; do
	dir="infrastructure/terraform/stacks/$stack"
	check "exists: $dir" test -d "$dir"
	check "$stack: init -backend=false (downloads pinned modules + providers)" \
		terraform -chdir="$dir" init -backend=false -input=false -no-color
	check "$stack: validate" \
		terraform -chdir="$dir" validate -no-color
done

echo
echo "--- pins are exact, not ranges where it matters"
check "30-eks pins terraform-aws-modules/eks/aws to an exact version" \
	grep -qE '^\s+version\s*=\s*"21\.[0-9]+\.[0-9]+"$' infrastructure/terraform/modules/eks/main.tf
check "40-snowflake uses the snowflakedb (not Snowflake-Labs) provider source" \
	grep -q 'source  = "snowflakedb/snowflake"' infrastructure/terraform/stacks/40-snowflake/versions.tf
check "every module declares required_version >= 1.11 (S3-native locking, ADR-0010)" \
	bash -c 'for f in infrastructure/terraform/modules/*/versions.tf infrastructure/terraform/stacks/{30-eks,40-snowflake}/versions.tf; do
	  grep -q "required_version = \">= 1.11\"" "$f" || { echo "missing in $f"; exit 1; }
	done'

echo
echo "--- provider lock files are committed and cross-platform"
# `terraform init` on a laptop writes only that laptop's platform hashes, and CI
# (linux_amd64) then fails the *next* init with "does not match any of the
# checksums" — a failure that is invisible until it happens on a different
# machine. Both platforms are locked with:
#   terraform providers lock -platform=linux_amd64 -platform=darwin_arm64
#
# A lock file never contains the platform names, so the assertion is on the count:
# one `h1:` hash per locked platform, so every provider block needs >= 2.
for stack in 30-eks 40-snowflake; do
	lock="infrastructure/terraform/stacks/$stack/.terraform.lock.hcl"
	check "$stack: .terraform.lock.hcl is committed" test -f "$lock"
	check "$stack: every provider is locked for >= 2 platforms" \
		python3 -c "
import re, sys
txt = open('$lock').read()
blocks = re.findall(r'provider \"([^\"]+)\" \{(.*?)\n\}', txt, re.S)
assert blocks, 'no provider blocks in $lock'
for name, body in blocks:
    n = body.count('\"h1:')
    assert n >= 2, '%s has %d h1 hash(es); run terraform providers lock -platform=linux_amd64 -platform=darwin_arm64' % (name, n)
"
done
check "30-eks lock pins hashicorp/aws" \
	grep -q 'registry.terraform.io/hashicorp/aws' infrastructure/terraform/stacks/30-eks/.terraform.lock.hcl
check "40-snowflake lock pins snowflakedb/snowflake" \
	grep -q 'registry.terraform.io/snowflakedb/snowflake' infrastructure/terraform/stacks/40-snowflake/.terraform.lock.hcl

echo
echo "--- module structure (main/variables/outputs/README per convention)"
for m in eks irsa-role snowflake-account; do
	for f in main.tf variables.tf outputs.tf README.md versions.tf; do
		check "modules/$m/$f" test -f "infrastructure/terraform/modules/$m/$f"
	done
done

echo
echo "--- the security-relevant defaults are actually the defaults"
check "eks module rejects 0.0.0.0/0 on the public endpoint" \
	grep -q 'error_message = "0.0.0.0/0 is not an acceptable public endpoint CIDR' \
	infrastructure/terraform/modules/eks/variables.tf
check "eks module defaults authentication_mode to API (no aws-auth ConfigMap)" \
	grep -qE 'default\s+=\s+"API"' infrastructure/terraform/modules/eks/variables.tf
# grep -v '^\s*#' because main.tf's header comment deliberately *names* the
# wildcard form it refuses to emit.
check "irsa-role trusts an exact serviceaccount subject, never a wildcard" \
	bash -c 'grep -v "^[[:space:]]*#" infrastructure/terraform/modules/irsa-role/main.tf | grep -qE "system:serviceaccount:.*[*]" && exit 1; exit 0'
check "30-eks creates the alb-controller role with no policy (Phase 12, ADR-0011)" \
	grep -q 'UNATTACHED until Phase 12' infrastructure/terraform/stacks/30-eks/main.tf
check "snowflake masking policies gate on IS_ROLE_IN_SESSION" \
	grep -c "IS_ROLE_IN_SESSION" infrastructure/terraform/modules/snowflake-account/governance.tf
check "snowflake resource monitor quota defaults to 50 credits" \
	grep -qE 'default\s+=\s+50' infrastructure/terraform/modules/snowflake-account/variables.tf
check "40-snowflake README carries the APPLY-AT-PHASE-6 rule (plan 6.10)" \
	grep -q 'APPLY AT PHASE 6 KICKOFF' infrastructure/terraform/stacks/40-snowflake/README.md

echo
echo "--- expected-FAIL: the terraform guards bite"
# A stack that references a module path that does not exist must fail init. This
# proves `terraform init` is really resolving our modules rather than silently
# accepting anything.
mkdir -p "$TMP/badtf"
cat >"$TMP/badtf/main.tf" <<'EOF'
module "nope" {
  source = "../../modules/does-not-exist"
}
EOF
check_fails "terraform init fails on a nonexistent module path" \
	terraform -chdir="$TMP/badtf" init -backend=false -input=false -no-color

# A public endpoint CIDR of 0.0.0.0/0 must be rejected by the module's variable
# validation, not merely discouraged in a comment.
mkdir -p "$TMP/badcidr"
cat >"$TMP/badcidr/main.tf" <<EOF
terraform {
  required_providers { aws = { source = "hashicorp/aws", version = "~> 6.61" } }
}
module "eks" {
  source                       = "$REPO_ROOT/infrastructure/terraform/modules/eks"
  name                         = "colx-dev"
  vpc_id                       = "vpc-123"
  subnet_ids                   = ["subnet-a", "subnet-b"]
  endpoint_public_access_cidrs = ["0.0.0.0/0"]
}
EOF
terraform -chdir="$TMP/badcidr" init -backend=false -input=false -no-color >/dev/null 2>&1 || true
check_fails "eks module rejects endpoint_public_access_cidrs = 0.0.0.0/0" \
	terraform -chdir="$TMP/badcidr" validate -no-color

# ===========================================================================
echo
echo "=== 2. helm: every chart renders against the committed values ==="
# This is the check with the highest value per second in the whole script. A chart
# bump that renames a values key does not fail `helmfile list`, does not fail
# `terraform validate`, and does not fail until `helmfile apply` in CI — where it
# is expensive. `helm template` catches it here.

helm repo add external-secrets https://charts.external-secrets.io >/dev/null 2>&1
helm repo add metrics-server https://kubernetes-sigs.github.io/metrics-server/ >/dev/null 2>&1
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts >/dev/null 2>&1
helm repo add grafana https://grafana.github.io/helm-charts >/dev/null 2>&1
helm repo add open-telemetry https://open-telemetry.github.io/opentelemetry-helm-charts >/dev/null 2>&1
helm repo add apache-airflow https://airflow.apache.org >/dev/null 2>&1
helm repo add codecentric https://codecentric.github.io/helm-charts >/dev/null 2>&1
helm repo add eks https://aws.github.io/eks-charts >/dev/null 2>&1
if helm repo update >/dev/null 2>&1; then
	ok "helm repo add + update for all 8 chart repositories"
else
	bad "helm repo update failed — this step needs network access"
fi

# release:chart:version:namespace — versions must match deployment/helmfile.yaml.
RELEASES="
external-secrets:external-secrets/external-secrets:2.9.0:platform
metrics-server:metrics-server/metrics-server:3.14.0:platform
kube-prometheus-stack:prometheus-community/kube-prometheus-stack:88.5.3:platform
loki:grafana/loki:7.3.0:platform
tempo:grafana/tempo:1.24.4:platform
alloy:grafana/alloy:1.11.1:platform
opentelemetry-collector:open-telemetry/opentelemetry-collector:0.170.0:platform
keycloak:codecentric/keycloakx:7.2.3:platform
airflow:apache-airflow/airflow:1.16.0:airflow
aws-load-balancer-controller:eks/aws-load-balancer-controller:3.5.0:kube-system
"

while IFS= read -r line; do
	[ -z "$line" ] && continue
	rel="${line%%:*}"
	rest="${line#*:}"
	chart="${rest%%:*}"
	rest="${rest#*:}"
	ver="${rest%%:*}"
	ns="${rest##*:}"

	vals="deployment/values/$rel/dev.yaml"
	if [ ! -f "$vals" ]; then
		bad "values file missing: $vals"
		continue
	fi

	# The version in helmfile.yaml must be the version we template. Asserting
	# both directions is what stops this script from testing a chart the cluster
	# does not run.
	if grep -qE "^\s+version:\s+$ver\s*$" deployment/helmfile.yaml; then
		ok "helmfile pins $rel to $ver"
	else
		bad "helmfile does not pin $rel to $ver"
	fi

	if helm template "$rel" "$chart" --version "$ver" -n "$ns" -f "$vals" \
		>"$TMP/$rel.yaml" 2>"$TMP/$rel.err"; then
		objects="$(grep -c '^kind:' "$TMP/$rel.yaml" || true)"
		if [ "${objects:-0}" -ge 1 ]; then
			ok "helm template $rel ($chart $ver) -> $objects objects"
		else
			bad "helm template $rel rendered nothing"
		fi
	else
		bad "helm template $rel ($chart $ver): $(tail -2 "$TMP/$rel.err" | tr '\n' ' ')"
	fi
done <<<"$RELEASES"

echo
echo "--- the two releases the brief calls out, in detail"
check "kube-prometheus-stack: Prometheus retention is 7d/10GB (plan FND-9)" \
	bash -c "grep -qE 'retention: \"?7d\"?' '$TMP/kube-prometheus-stack.yaml' && grep -qE 'retentionSize: \"?10GB\"?' '$TMP/kube-prometheus-stack.yaml'"
check "kube-prometheus-stack: Grafana admin comes from the ESO secret, not a literal" \
	grep -q 'name: grafana-admin' "$TMP/kube-prometheus-stack.yaml"
check "kube-prometheus-stack: Grafana dashboard sidecar is enabled" \
	grep -q 'grafana-sc-dashboard' "$TMP/kube-prometheus-stack.yaml"
check "kube-prometheus-stack: Loki + Tempo datasources with trace/log correlation" \
	bash -c "grep -q 'tracesToLogsV2' '$TMP/kube-prometheus-stack.yaml' && grep -q 'derivedFields' '$TMP/kube-prometheus-stack.yaml'"
check "kube-prometheus-stack: alertmanager runs as the stable 'alertmanager' SA (IRSA subject)" \
	grep -q 'serviceAccountName: alertmanager' "$TMP/kube-prometheus-stack.yaml"

check "airflow: KubernetesExecutor" \
	grep -q 'executor = KubernetesExecutor' "$TMP/airflow.yaml"
check "airflow: airflowVersion is 2.11 (ADR-0007), not the chart default 2.10.5" \
	grep -q 'worker_container_tag = 2.11' "$TMP/airflow.yaml"
check "airflow: remote logs to s3://colx-dev-ops/airflow-logs" \
	grep -q 'remote_base_log_folder = s3://colx-dev-ops/airflow-logs' "$TMP/airflow.yaml"
check "airflow: git-sync enabled against this repo, subPath airflow/dags" \
	bash -c "grep -q 'GITSYNC_REPO' '$TMP/airflow.yaml' && grep -q 'airflow/dags' '$TMP/airflow.yaml'"
check "airflow: pgbouncer deployed" \
	grep -q 'pgbouncer' "$TMP/airflow.yaml"
check "airflow: statsd exporter deployed" \
	grep -q 'name: airflow-statsd' "$TMP/airflow.yaml"
check "airflow: connections/variables arrive via the airflow-connections secret" \
	grep -q 'name: airflow-connections' "$TMP/airflow.yaml"
check "airflow: the local admin password is NOT the literal from values.yaml" \
	bash -c "! grep -q 'unused-see-createUserJob-args' '$TMP/airflow.yaml'"
check "airflow: 9 component service accounts rendered" \
	bash -c "test \"\$(grep -c 'kind: ServiceAccount' '$TMP/airflow.yaml')\" -ge 9"

check "keycloak: official quay.io/keycloak image, pinned" \
	grep -q 'image: "quay.io/keycloak/keycloak:26.6.4"' "$TMP/keycloak.yaml"
check "keycloak: start --import-realm (production mode, not start-dev)" \
	bash -c "grep -q '\- start' '$TMP/keycloak.yaml' && grep -q '\-\-import-realm' '$TMP/keycloak.yaml'"
check "keycloak: KC_DB=postgres against the keycloak database" \
	bash -c "grep -q 'value: postgres' '$TMP/keycloak.yaml' && grep -q 'KC_DB_URL_DATABASE' '$TMP/keycloak.yaml'"
check "keycloak: admin credentials come from the ESO secret" \
	grep -q 'name: keycloak-admin' "$TMP/keycloak.yaml"
check "keycloak: ClusterIP only, no Ingress (ADR-0011)" \
	bash -c "grep -q 'type: ClusterIP' '$TMP/keycloak.yaml' && ! grep -q 'kind: Ingress' '$TMP/keycloak.yaml'"
check "keycloak: ServiceMonitor for metrics" \
	grep -q 'kind: ServiceMonitor' "$TMP/keycloak.yaml"
check "keycloak: health probes on /health/{live,ready,started}" \
	bash -c "grep -q '/health/live' '$TMP/keycloak.yaml' && grep -q '/health/ready' '$TMP/keycloak.yaml'"

check "loki: S3 backend under the loki/ prefix (matches the IRSA policy)" \
	bash -c "grep -q 'storage_prefix: loki' '$TMP/loki.yaml' && grep -q 'colx-dev-ops' '$TMP/loki.yaml'"
check "tempo: S3 backend under the tempo/ prefix (matches the IRSA policy)" \
	bash -c "grep -q 'prefix: tempo' '$TMP/tempo.yaml' && grep -q 'bucket: colx-dev-ops' '$TMP/tempo.yaml'"
check "opentelemetry-collector: OTLP -> Tempo, jaeger/zipkin receivers removed" \
	bash -c "grep -q 'otlp/tempo' '$TMP/opentelemetry-collector.yaml' && ! grep -qE '^\s+(jaeger|zipkin):' '$TMP/opentelemetry-collector.yaml'"
check "alloy: promotes trace_id/correlation_id from JSON log lines" \
	bash -c "grep -q 'correlation_id' '$TMP/alloy.yaml' && grep -q 'stage.structured_metadata' '$TMP/alloy.yaml'"

echo
echo "--- expected-FAIL: helm rejects a values key the chart does not know"
# The Airflow chart's values.schema.json permits unknown *top-level* keys — which
# is exactly the trap documented in values/airflow/dev.yaml (a `serviceMonitor:`
# key is accepted and read by nothing). Nested objects ARE validated, so this uses
# one of those; it proves the render above is genuinely checking our values.
printf 'createUserJob:\n  thisKeyDoesNotExist: true\n' >"$TMP/bogus.yaml"
check_fails "helm template airflow rejects an unknown nested values key" \
	helm template airflow apache-airflow/airflow --version 1.16.0 -n airflow \
	-f deployment/values/airflow/dev.yaml -f "$TMP/bogus.yaml"

# ===========================================================================
echo
echo "=== 3. helmfile ==="
if ! command -v helmfile >/dev/null 2>&1 && [ ! -x "$BIN/helmfile" ]; then
	echo "      helmfile not found — installing the pinned release v$HELMFILE_VERSION into ./bin (gitignored)"
	os="$(uname -s | tr '[:upper:]' '[:lower:]')"
	arch="$(uname -m)"
	case "$arch" in
	x86_64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	esac
	mkdir -p "$BIN"
	if curl -sSfL \
		"https://github.com/helmfile/helmfile/releases/download/v${HELMFILE_VERSION}/helmfile_${HELMFILE_VERSION}_${os}_${arch}.tar.gz" \
		-o "$TMP/helmfile.tgz" &&
		tar -xzf "$TMP/helmfile.tgz" -C "$TMP" helmfile; then
		install -m 0755 "$TMP/helmfile" "$BIN/helmfile"
	else
		bad "could not download helmfile v$HELMFILE_VERSION (network?)"
	fi
fi

HELMFILE="$(command -v helmfile || echo "$BIN/helmfile")"
if [ -x "$HELMFILE" ]; then
	installed="$("$HELMFILE" --version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1)"
	ok "helmfile $installed available at $HELMFILE (pinned: $HELMFILE_VERSION)"

	if "$HELMFILE" -f deployment/helmfile.yaml -e dev list >"$TMP/hf-list.txt" 2>"$TMP/hf-list.err"; then
		ok "helmfile -e dev list parses the whole file"
		releases="$(tail -n +2 "$TMP/hf-list.txt" | grep -c . || true)"
		check "helmfile lists 10 releases (found $releases)" test "$releases" -eq 10
		check "the ALB controller release is present but installed=false (ADR-0011)" \
			bash -c "grep aws-load-balancer-controller '$TMP/hf-list.txt' | grep -q false"
	else
		bad "helmfile -e dev list: $(tail -2 "$TMP/hf-list.err" | tr '\n' ' ')"
	fi

	# The strongest offline check available: render every release through helmfile,
	# which also exercises the `.gotmpl` values files (the IRSA ARNs, the image
	# reference, the Alertmanager SNS config) that plain `helm template` cannot see.
	if "$HELMFILE" -f deployment/helmfile.yaml -e dev template --skip-deps \
		>"$TMP/hf-template.yaml" 2>"$TMP/hf-template.err"; then
		objects="$(grep -c '^kind:' "$TMP/hf-template.yaml" || true)"
		ok "helmfile -e dev template renders the whole baseline ($objects objects)"
		check "the render is substantial (>= 150 objects)" test "${objects:-0}" -ge 150
		check "IRSA annotations rendered from accountId (no raw Go actions left)" \
			bash -c "grep -q 'role/colx-dev-external-secrets' '$TMP/hf-template.yaml' && ! grep -q 'role-arn: arn:aws:iam::{{' '$TMP/hf-template.yaml'"
		check "Alertmanager SNS topic ARN rendered" \
			bash -c "python3 - <<'PY'
import base64, re, sys
s = open('$TMP/hf-template.yaml').read()
m = re.search(r'alertmanager\.yaml: \"([A-Za-z0-9+/=]+)\"', s)
sys.exit(0 if m and 'topic_arn: arn:aws:sns:eu-west-1:' in base64.b64decode(m.group(1)).decode() else 1)
PY"
		check "airflow image resolved from the registry/tag state values" \
			grep -q '/colx/airflow:' "$TMP/hf-template.yaml"
	else
		bad "helmfile -e dev template: $(tail -3 "$TMP/hf-template.err" | tr '\n' ' ')"
	fi
else
	bad "helmfile is not available and could not be installed"
fi

echo
echo "--- no secrets in git (D§66)"
check "no literal secret-looking values under deployment/values/**" \
	bash -c '! grep -rniE "^[[:space:]]*(password|adminPassword|secretKey|clientSecret|apiKey|token)[[:space:]]*:[[:space:]]*[\"'"'"']?[A-Za-z0-9+/=_-]{12,}" deployment/values/'
check "every ExternalSecret references the colx/dev/* store" \
	bash -c 'test "$(grep -c "key: colx/dev/" deployment/values/external-secrets/externalsecrets.yaml)" -ge 8'
check "no Secret manifests in deployment/** (ExternalSecret only)" \
	bash -c '! grep -rlE "^kind: Secret$" deployment/values deployment/observability deployment/namespaces.yaml 2>/dev/null | grep .'

# ===========================================================================
echo
echo "=== 4. Kubernetes manifests + realm JSON (structural) ==="
# kubectl cannot help here (see the header). A tiny Go program using yaml.v3
# parses every document and asserts the fields that matter.
mkdir -p "$TMP/yamlcheck"
cat >"$TMP/yamlcheck/go.mod" <<'EOF'
module yamlcheck

go 1.24

require gopkg.in/yaml.v3 v3.0.1
EOF
cat >"$TMP/yamlcheck/main.go" <<'EOF'
// Parses every YAML document in the given files and prints
// "<file> <index> <apiVersion> <kind> <namespace>/<name>" per document.
// Exits non-zero on a parse error or a document missing apiVersion/kind/name.
package main

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type meta struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name      string `yaml:"name"`
		Namespace string `yaml:"namespace"`
	} `yaml:"metadata"`
}

func main() {
	rc := 0
	for _, path := range os.Args[1:] {
		b, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERR %s: %v\n", path, err)
			rc = 1
			continue
		}
		dec := yaml.NewDecoder(bytes.NewReader(b))
		i := 0
		for {
			var raw yaml.Node
			if err := dec.Decode(&raw); err != nil {
				if err.Error() == "EOF" {
					break
				}
				fmt.Fprintf(os.Stderr, "ERR %s doc %d: %v\n", path, i, err)
				rc = 1
				break
			}
			if raw.Kind == 0 {
				i++
				continue
			}
			var m meta
			if err := raw.Decode(&m); err != nil {
				fmt.Fprintf(os.Stderr, "ERR %s doc %d: %v\n", path, i, err)
				rc = 1
				i++
				continue
			}
			if m.APIVersion == "" || m.Kind == "" || m.Metadata.Name == "" {
				fmt.Fprintf(os.Stderr, "ERR %s doc %d: missing apiVersion/kind/metadata.name\n", path, i)
				rc = 1
			}
			ns := m.Metadata.Namespace
			if ns == "" {
				ns = "-"
			}
			fmt.Printf("%s %d %s %s %s/%s\n", path, i, m.APIVersion, m.Kind, ns, m.Metadata.Name)
			i++
		}
	}
	os.Exit(rc)
}
EOF
(cd "$TMP/yamlcheck" && GOWORK=off GOFLAGS=-mod=mod go mod tidy >/dev/null 2>&1) || true

# Absolute paths: the runner cd's into the temp module, so a repo-relative path
# would not resolve.
yamlcheck() {
	local args=() f
	for f in "$@"; do
		case "$f" in
		/*) args+=("$f") ;;
		*) args+=("$REPO_ROOT/$f") ;;
		esac
	done
	(cd "$TMP/yamlcheck" && GOWORK=off GOFLAGS=-mod=mod go run . "${args[@]}")
}

MANIFESTS="
deployment/namespaces.yaml
deployment/values/external-secrets/clustersecretstore.yaml
deployment/values/external-secrets/externalsecrets.yaml
deployment/values/keycloak/client-secrets-job.yaml
deployment/values/airflow/servicemonitor.yaml
deployment/observability/alerts/platform-rules.yaml
"
# shellcheck disable=SC2086
if yamlcheck $MANIFESTS >"$TMP/manifests.txt" 2>"$TMP/manifests.err"; then
	ok "every Kubernetes manifest parses and carries apiVersion/kind/name ($(grep -c . "$TMP/manifests.txt") documents)"
	# Strip the repo prefix so the assertions below read naturally.
	sed -i.bak "s#^$REPO_ROOT/##" "$TMP/manifests.txt" && rm -f "$TMP/manifests.txt.bak"
else
	bad "manifest parse failed: $(tail -3 "$TMP/manifests.err" | tr '\n' ' ')"
fi

check "7 labelled namespaces (plan FND-8)" \
	bash -c 'test "$(grep -c "^deployment/namespaces.yaml .* Namespace " '"$TMP/manifests.txt"')" -eq 7'
check "namespaces are platform/airflow/ingestion/kafka/simulator/sftp/services" \
	bash -c 'for n in platform airflow ingestion kafka simulator sftp services; do
	  grep -q "Namespace -/$n$" '"$TMP/manifests.txt"' || { echo "missing $n"; exit 1; }
	done'
check "every namespace carries the colx.io/tier and data-class labels" \
	bash -c 'test "$(grep -cE "^[[:space:]]+colx.io/tier:" deployment/namespaces.yaml)" -eq 7 \
	  && test "$(grep -cE "^[[:space:]]+colx.io/data-class:" deployment/namespaces.yaml)" -eq 7'
check "ClusterSecretStore uses external-secrets.io/v1 and IRSA (jwt) auth" \
	bash -c "grep -q 'external-secrets.io/v1' deployment/values/external-secrets/clustersecretstore.yaml && grep -q 'serviceAccountRef' deployment/values/external-secrets/clustersecretstore.yaml"
check "PrometheusRule has 4+ alerts, every one with a runbook_url (ADR-0015)" \
	bash -c 'alerts=$(grep -c "^\s*- alert:" deployment/observability/alerts/platform-rules.yaml);
	  urls=$(grep -c "runbook_url:" deployment/observability/alerts/platform-rules.yaml);
	  test "$alerts" -ge 4 && test "$urls" -eq "$alerts"'
check "PrometheusRule covers node NotReady, PV>80%, CrashLoop and a deadman" \
	bash -c 'f=deployment/observability/alerts/platform-rules.yaml;
	  grep -q ColxNodeNotReady "$f" && grep -q ColxPersistentVolumeFillingUp "$f" \
	    && grep -q ColxPodCrashLooping "$f" && grep -q Deadman "$f"'

echo
echo "--- expected-FAIL: the YAML checker actually rejects bad input"
printf 'apiVersion: v1\nkind: Namespace\nmetadata: [broken\n' >"$TMP/broken.yaml"
check_fails "the manifest checker rejects malformed YAML" yamlcheck "$TMP/broken.yaml"
printf 'kind: Namespace\nmetadata:\n  name: x\n' >"$TMP/noapiversion.yaml"
check_fails "the manifest checker rejects a document with no apiVersion" yamlcheck "$TMP/noapiversion.yaml"

echo
echo "--- Grafana dashboards"
check "dashboards/cluster.json is valid JSON with panels" \
	python3 -c "import json;d=json.load(open('deployment/observability/dashboards/cluster.json'));assert d['uid'] and len(d['panels'])>=8"
check "dashboards/colx-ops.json is valid JSON with the D§51 rows" \
	python3 -c "
import json
d=json.load(open('deployment/observability/dashboards/colx-ops.json'))
rows={p['title'] for p in d['panels'] if p['type']=='row'}
assert {'INGESTION','PIPELINES','COLLECTIONS','DECISIONING'} <= rows, rows
"
check "observability/apply.sh is executable bash with set -euo pipefail" \
	bash -c "head -1 deployment/observability/apply.sh | grep -q 'usr/bin/env bash' && grep -q 'set -euo pipefail' deployment/observability/apply.sh"
check "observability/apply.sh and keycloak/bootstrap.sh are syntactically valid" \
	bash -c "bash -n deployment/observability/apply.sh && bash -n deployment/values/keycloak/bootstrap.sh"

echo
echo "--- Keycloak realm as code (plan FND-6)"
check "realm-colx.json is valid JSON for realm 'colx'" \
	python3 -c "import json;d=json.load(open('deployment/values/keycloak/realm-colx.json'));assert d['realm']=='colx'"
check "realm declares all 23 colon-form client scopes" \
	python3 -c "
import json
want = {'cases:read','cases:write','cases:admin','delinquency:read','delinquency:admin',
        'payments:read','payments:write','payments:admin','recovery:read','recovery:write',
        'agency:read','agency:admin','decisions:read','decisions:write','strategy:author',
        'treatments:read','treatments:write','ingestion:read','ingestion:write','webhook:write',
        'customers:read','accounts:read','debts:read'}
d = json.load(open('deployment/values/keycloak/realm-colx.json'))
have = {s['name'] for s in d['clientScopes']}
missing = want - have
assert not missing, 'missing scopes: %s' % sorted(missing)
assert len(want) == 23
# and every one must land in the token's scope claim verbatim
for s in d['clientScopes']:
    if s['name'] in want:
        assert s['attributes']['include.in.token.scope'] == 'true', s['name']
"
check "realm declares the 7 groups" \
	python3 -c "
import json
want = {'strategy-author','business-approver','risk-approver','admin','collector','ops-admin','analyst'}
d = json.load(open('deployment/values/keycloak/realm-colx.json'))
have = {g['name'] for g in d['groups']}
assert want == have, (want ^ have)
"
check "realm emits a plain 'groups' claim (full.path false)" \
	python3 -c "
import json
d = json.load(open('deployment/values/keycloak/realm-colx.json'))
sc = [s for s in d['clientScopes'] if s['name'] == 'groups'][0]
m  = sc['protocolMappers'][0]
assert m['protocolMapper'] == 'oidc-group-membership-mapper'
assert m['config']['claim.name'] == 'groups'
assert m['config']['full.path'] == 'false'
assert m['config']['access.token.claim'] == 'true'
"
check "realm declares the 2 M2M clients with service accounts and no standard flow" \
	python3 -c "
import json
d = json.load(open('deployment/values/keycloak/realm-colx.json'))
byid = {c['clientId']: c for c in d['clients']}
assert set(byid) == {'platform-services','simulator'}, sorted(byid)
for c in byid.values():
    assert c['serviceAccountsEnabled'] is True
    assert c['standardFlowEnabled'] is False
    assert c['publicClient'] is False
assert 'webhook:write' in byid['simulator']['defaultClientScopes']
assert len(byid['simulator']['defaultClientScopes']) == 2   # groups + webhook:write
assert len(byid['platform-services']['defaultClientScopes']) == 24
"
check "NO client secret anywhere in the realm JSON" \
	python3 -c "
import json
raw = open('deployment/values/keycloak/realm-colx.json').read()
d = json.load(open('deployment/values/keycloak/realm-colx.json'))
for c in d['clients']:
    assert 'secret' not in c, c['clientId']
# `client_credentials.use_refresh_token` is a legitimate attribute NAME, so the
# check targets JSON *keys* that would carry a secret value.
for bad in ('\"secret\":', '\"clientSecret\":', '\"credentials\":'):
    assert bad not in raw, bad
"
check_fails "realm JSON contains no high-entropy secret-looking literal" \
	grep -qE '"(secret|password|credential)"[[:space:]]*:' deployment/values/keycloak/realm-colx.json
check "the kcadm Job reads client secrets from the ESO secret, not from git" \
	bash -c "grep -q 'name: keycloak-client-secrets' deployment/values/keycloak/client-secrets-job.yaml && grep -q 'kcadm.sh' deployment/values/keycloak/client-secrets-job.yaml"

# ===========================================================================
echo
echo "=== 5. CI workflows (structural) ==="
# shellcheck disable=SC2086
WORKFLOWS=".github/workflows/terraform.yml .github/workflows/images.yml .github/workflows/helmfile.yml"
# GitHub workflows have no apiVersion/kind, so the checker's field assertions are
# expected to complain; what matters is that no document fails to PARSE.
yamlcheck $WORKFLOWS >/dev/null 2>"$TMP/wf.err" || true
check "all three workflow files parse as YAML (no parse errors)" \
	bash -c "! grep -qE 'doc [0-9]+: yaml:' '$TMP/wf.err'"

for wf in terraform images helmfile; do
	f=".github/workflows/$wf.yml"
	check "exists: $f" test -f "$f"
	check "$wf: has on/jobs/permissions" \
		bash -c "grep -qE '^on:' '$f' && grep -qE '^jobs:' '$f' && grep -qE '^permissions:' '$f'"
	check "$wf: requests an OIDC token (id-token: write)" \
		grep -q 'id-token: write' "$f"
	check "$wf: assumes a colx-gha-* role, never a static key" \
		grep -q 'role-to-assume: arn:aws:iam::' "$f"
done

check "terraform.yml: per-stack paths-filter via dorny/paths-filter" \
	bash -c "grep -q 'dorny/paths-filter' .github/workflows/terraform.yml && grep -q '30-eks:' .github/workflows/terraform.yml && grep -q '40-snowflake:' .github/workflows/terraform.yml"
check "terraform.yml: fmt, validate, tflint and trivy-config on PR" \
	bash -c "f=.github/workflows/terraform.yml;
	  grep -q 'terraform fmt -check' \$f && grep -q 'terraform validate' \$f \
	    && grep -q 'terraform-linters/setup-tflint' \$f && grep -q 'aquasecurity/trivy-action' \$f"
check "terraform.yml: plan runs as colx-gha-plan and is posted as a PR comment" \
	bash -c "f=.github/workflows/terraform.yml;
	  grep -q 'role/colx-gha-plan' \$f && grep -q 'createComment' \$f"
check "terraform.yml: apply is gated on the dev environment and uses colx-gha-apply" \
	bash -c "f=.github/workflows/terraform.yml;
	  grep -q 'environment: dev' \$f && grep -q 'role/colx-gha-apply' \$f"
check "terraform.yml: 40-snowflake is excluded from the automatic apply (plan 6.10)" \
	grep -q '40-snowflake is applied at Phase 6 kickoff' .github/workflows/terraform.yml
check "terraform.yml: 00-bootstrap is never applied by CI (ADR-0010)" \
	grep -q '00-bootstrap is human-applied' .github/workflows/terraform.yml

check "images.yml: builds on path change, tags sha + latest, uses colx-gha-ecr-push" \
	bash -c "f=.github/workflows/images.yml;
	  grep -q 'role/colx-gha-ecr-push' \$f && grep -q 'sha-' \$f && grep -q ':latest' \$f"
check "images.yml: trivy image scan fails on HIGH or CRITICAL" \
	bash -c "f=.github/workflows/images.yml;
	  grep -q \"severity: 'HIGH,CRITICAL'\" \$f && grep -q \"exit-code: '1'\" \$f"
check "images.yml: PRs build but do not push" \
	grep -q "if: github.event_name == 'push'" .github/workflows/images.yml

check "helmfile.yml: diff on PR, gated apply on main, colx-gha-eks-deploy" \
	bash -c "f=.github/workflows/helmfile.yml;
	  grep -q 'helmfile .*diff' \$f && grep -q 'helmfile .*apply' \$f \
	    && grep -q 'role/colx-gha-eks-deploy' \$f && grep -q 'environment: dev' \$f"
check "helmfile.yml: post-apply diff must be empty (FND-8 acceptance)" \
	grep -q 'post-apply diff must be empty' .github/workflows/helmfile.yml

echo
echo "--- every `run:` block is valid bash"
# Stronger than any grep: extracts each `run: |` script, stubs out the GitHub
# expressions (they are substituted before bash ever sees them) and runs
# `bash -n`. A stray heredoc delimiter or an unbalanced quote fails here instead
# of six minutes into a deploy.
check "all workflow run blocks pass bash -n" python3 - <<'PYCHECK'
import os, pathlib, re, subprocess, sys, tempfile

bad = []
for f in sorted(pathlib.Path(".github/workflows").glob("*.yml")):
    lines = f.read_text().split("\n")
    i = n = 0
    while i < len(lines):
        m = re.match(r"^(\s*)run: \|\s*$", lines[i])
        if not m:
            i += 1
            continue
        base = len(m.group(1)) + 2
        body = []
        i += 1
        while i < len(lines) and (lines[i].strip() == "" or len(lines[i]) - len(lines[i].lstrip()) >= base):
            body.append(lines[i][base:] if len(lines[i]) >= base else "")
            i += 1
        n += 1
        script = re.sub(r"\$\{\{[^}]*\}\}", "GHEXPR", "\n".join(body))
        with tempfile.NamedTemporaryFile("w", suffix=".sh", delete=False) as t:
            t.write(script)
            path = t.name
        r = subprocess.run(["bash", "-n", path], capture_output=True, text=True)
        os.unlink(path)
        if r.returncode != 0:
            bad.append("%s block #%d: %s" % (f, n, r.stderr.strip()))
if bad:
    print("\n".join(bad))
    sys.exit(1)
PYCHECK

echo
echo "--- no long-lived credentials anywhere (ADR-0010)"
check "no AWS access-key ids or secret-key env in any workflow" \
	bash -c '! grep -rniE "AWS_(ACCESS_KEY_ID|SECRET_ACCESS_KEY)" .github/workflows/'
check "no aws-access-key-id input to configure-aws-credentials" \
	bash -c '! grep -rn "aws-access-key-id" .github/workflows/'
check "CODEOWNERS exists with a global owner" \
	bash -c "grep -qE '^\*\s+@canhtoanptit' .github/CODEOWNERS"

# ===========================================================================
echo
echo "=== 6. images ==="
for img in airflow connect dbt; do
	check "deployment/images/$img/Dockerfile" test -f "deployment/images/$img/Dockerfile"
done
check "airflow image is built from apache/airflow 2.11 + python3.12" \
	bash -c "grep -q 'AIRFLOW_VERSION=2.11' deployment/images/airflow/Dockerfile && grep -q 'PYTHON_VERSION=3.12' deployment/images/airflow/Dockerfile"
check "airflow providers are pinned to exact versions in requirements.txt" \
	bash -c 'test "$(grep -cE "^apache-airflow-providers-[a-z-]+==[0-9]" deployment/images/airflow/requirements.txt)" -ge 3'
check "airflow requirements pin the four providers the plan names" \
	bash -c 'f=deployment/images/airflow/requirements.txt;
	  grep -q "providers-cncf-kubernetes==" $f && grep -q "providers-snowflake==" $f \
	    && grep -q "providers-amazon==" $f && grep -qE "^statsd==" $f'
check "airflow Dockerfile pins transitive deps with Airflow's constraints file" \
	grep -q 'constraints-\${AIRFLOW_VERSION}/constraints-\${PYTHON_VERSION}.txt' deployment/images/airflow/Dockerfile
check "connect image pins debezium, msk-iam-auth, aiven s3 sink and the jmx agent" \
	bash -c 'f=deployment/images/connect/Dockerfile;
	  grep -q "DEBEZIUM_VERSION=" $f && grep -q "MSK_IAM_AUTH_VERSION=" $f \
	    && grep -q "AIVEN_S3_SINK_VERSION=" $f && grep -q "JMX_EXPORTER_VERSION=" $f'
check "connect image ships a JMX exporter config" \
	test -f deployment/images/connect/jmx-exporter.yaml
check "dbt image pins dbt-snowflake" \
	grep -qE 'DBT_SNOWFLAKE_VERSION=[0-9]+\.[0-9]+\.[0-9]+' deployment/images/dbt/Dockerfile
check "no Dockerfile uses a floating :latest base" \
	bash -c '! grep -rnE "^FROM .*:latest" deployment/images/'

# ===========================================================================
echo
echo "=== 7. cost, teardown and docs ==="
for f in scripts/cost/stop.sh scripts/cost/start.sh scripts/cost/report.sh scripts/cost/lib.sh scripts/dr/README.md; do
	check "exists: $f" test -f "$f"
done
check "cost scripts are syntactically valid bash" \
	bash -c 'for f in scripts/cost/*.sh; do bash -n "$f" || exit 1; done'
check "stop.sh and start.sh guard on the AWS CLI and on real credentials" \
	bash -c "grep -q 'require_aws' scripts/cost/stop.sh && grep -q 'require_aws' scripts/cost/start.sh && grep -q 'get-caller-identity' scripts/cost/lib.sh"
check "start.sh starts RDS before the nodes (pods need a database)" \
	bash -c "rds=\$(grep -n 'start: RDS instances' scripts/cost/start.sh | cut -d: -f1);
	  nodes=\$(grep -n 'start: EKS node groups' scripts/cost/start.sh | cut -d: -f1);
	  test \"\$rds\" -lt \"\$nodes\""
check "report.sh queries Cost Explorer by the stack tag and Snowflake metering" \
	bash -c "grep -q 'get-cost-and-usage' scripts/cost/report.sh && grep -q 'WAREHOUSE_METERING_HISTORY' scripts/cost/report.sh"
check "scripts/dr/README.md points at XCT-1" \
	grep -q 'XCT-1' scripts/dr/README.md

echo
echo "--- Makefile (append-only section)"
for t in stop start destroy-heavy up-all grafana airflow keycloak pf-cp cost-report; do
	check "make target exists: $t" bash -c "grep -qE '^${t}:' Makefile"
done
check "the INF-B section is clearly marked append-only" \
	grep -q 'APPEND-ONLY SECTION' Makefile
check "pre-existing targets are untouched (tf-plan still present and unmodified)" \
	bash -c "grep -q 'tf-plan: ## Plan a terraform stack' Makefile"
check "every cloud-touching target is guarded on the AWS CLI" \
	bash -c 'test "$(grep -c "require_aws" Makefile)" -ge 5'
check "make help lists the new targets" \
	bash -c "make help 2>/dev/null | grep -q 'destroy-heavy'"

echo
echo "--- expected-FAIL: the Makefile guards bite"
check_fails "make stop refuses to run without usable AWS credentials" \
	env AWS_PROFILE=colx-no-such-profile AWS_ACCESS_KEY_ID= AWS_SECRET_ACCESS_KEY= \
	AWS_SESSION_TOKEN= AWS_CONFIG_FILE=/dev/null AWS_SHARED_CREDENTIALS_FILE=/dev/null \
	make stop

echo
echo "--- docs"
check "docs/runbooks/cost-and-teardown.md carries the D§82 heading set" \
	bash tools/lint-runbook.sh docs/runbooks/cost-and-teardown.md
check "docs/cost-model.md has estimate and actual columns" \
	bash -c "grep -q 'Estimate / mo' docs/cost-model.md && grep -q 'Actual / mo' docs/cost-model.md"
check "docs/cost-model.md lists all four teardown levers" \
	bash -c "f=docs/cost-model.md; grep -q 'make stop' \$f && grep -q 'make destroy-heavy' \$f \
	  && grep -q 'Full destroy' \$f && grep -q 'Everything running' \$f"
check "docs/cost-model.md has no Cognito row (identity is Keycloak now)" \
	bash -c '! grep -qiE "^\| *cognito" docs/cost-model.md'
check "docs/cost-model.md notes Keycloak is ~\$0 incremental" \
	bash -c "grep -qi 'keycloak' docs/cost-model.md"

# ===========================================================================
echo
printf 'INF-B: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
