#!/usr/bin/env bash
#
# deployment/observability/apply.sh — push the dashboards and alert rules that
# live in git into the cluster.
#
#   apply.sh [namespace]        (default: platform)
#
# Called by deployment/helmfile.yaml as a postsync hook on the
# kube-prometheus-stack release, and by `make up-all`. Idempotent.
#
# Dashboards become ConfigMaps labelled `grafana_dashboard=1`, which is what the
# Grafana sidecar watches. Alert rules are a PrometheusRule, which needs the
# Prometheus operator CRDs — hence "after kube-prometheus-stack", not before.
#
# Needs: bash, kubectl, and a kubeconfig pointing at colx-dev.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ns="${1:-platform}"

if ! command -v kubectl >/dev/null 2>&1; then
	echo "observability/apply.sh: kubectl not found — this step needs cluster credentials" >&2
	exit 1
fi

# One ConfigMap per dashboard file. Separate rather than combined so a broken
# dashboard cannot stop the others loading, and so `kubectl describe` names the
# dashboard you are looking for.
shopt -s nullglob
dashboards=("$HERE"/dashboards/*.json)
if [ "${#dashboards[@]}" -eq 0 ]; then
	echo "observability/apply.sh: no dashboards found in $HERE/dashboards" >&2
	exit 1
fi

for f in "${dashboards[@]}"; do
	base="$(basename "$f" .json)"
	name="colx-dashboard-${base}"
	echo "observability/apply.sh: dashboard $base -> ConfigMap $name"
	kubectl create configmap "$name" \
		--namespace "$ns" \
		--from-file="${base}.json=$f" \
		--dry-run=client -o yaml |
		kubectl label --local -f - \
			grafana_dashboard=1 \
			app.kubernetes.io/part-of=colx \
			-o yaml |
		kubectl annotate --local -f - \
			grafana_folder=colx \
			-o yaml |
		kubectl apply -f -
done

echo "observability/apply.sh: applying alert rules"
kubectl apply --namespace "$ns" -f "$HERE/alerts/platform-rules.yaml"

echo "observability/apply.sh: done (${#dashboards[@]} dashboard(s), 1 rule group file)"
