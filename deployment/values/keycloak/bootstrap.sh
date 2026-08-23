#!/usr/bin/env bash
#
# deployment/values/keycloak/bootstrap.sh — the two halves of the Keycloak deploy
# that Helm cannot do.
#
#   bootstrap.sh pre  <namespace>   create/update the realm ConfigMap
#   bootstrap.sh post <namespace>   run the kcadm Job (client secrets + drift check)
#
# Called by deployment/helmfile.yaml as presync/postsync hooks on the keycloak
# release, and by `make up-all`. Also safe to run by hand.
#
# Needs: bash, kubectl, and a kubeconfig pointing at colx-dev.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

phase="${1:-}"
ns="${2:-platform}"

if ! command -v kubectl >/dev/null 2>&1; then
	echo "keycloak/bootstrap.sh: kubectl not found — this step needs cluster credentials" >&2
	exit 1
fi

case "$phase" in
pre)
	# The realm JSON is mounted at /opt/keycloak/data/import, where
	# `kc.sh start --import-realm` looks. Recreated on every deploy so an edited
	# file reaches the pod; whether Keycloak *applies* it is a separate question
	# (see `post`).
	echo "keycloak/bootstrap.sh: applying ConfigMap keycloak-realm in $ns"
	kubectl create configmap keycloak-realm \
		--namespace "$ns" \
		--from-file="realm-colx.json=$HERE/realm-colx.json" \
		--dry-run=client -o yaml |
		kubectl apply -f -
	;;

post)
	echo "keycloak/bootstrap.sh: running the kcadm bootstrap Job in $ns"

	# Jobs are immutable, so a redeploy has to delete and recreate. --wait keeps
	# the old pod from racing the new one.
	kubectl delete job keycloak-bootstrap --namespace "$ns" --ignore-not-found --wait=true

	kubectl apply -f "$HERE/client-secrets-job.yaml"

	# 10 minutes: Keycloak's first boot imports the realm and migrates its
	# schema, and the Job waits for the realm endpoint before doing anything.
	if ! kubectl wait --namespace "$ns" --for=condition=complete \
		--timeout=600s job/keycloak-bootstrap; then
		echo "keycloak/bootstrap.sh: Job did not complete — logs follow" >&2
		kubectl logs --namespace "$ns" job/keycloak-bootstrap --tail=200 >&2 || true
		exit 1
	fi

	kubectl logs --namespace "$ns" job/keycloak-bootstrap --tail=50
	;;

*)
	echo "usage: bootstrap.sh {pre|post} [namespace]" >&2
	exit 2
	;;
esac
