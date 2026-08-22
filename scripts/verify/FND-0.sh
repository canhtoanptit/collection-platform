#!/usr/bin/env bash
#
# scripts/verify/FND-0.sh — verifies the repo toolchain FND-0 established: the Go
# workspace and pinned tools, the root Makefile + makefiles/service.mk contract,
# the lint configuration, and the Helm library chart with its render-test
# consumer.
#
# It asserts observable outcomes only — files exist, versions match the pin,
# `make` targets exit 0, `helm lint` is clean, and the library chart actually
# renders objects through a consumer — and it ends with expected-FAIL assertions
# proving the guards bite (a library chart is not installable, and the delegation
# targets refuse to run without a WP id).
#
# Environment: none (no Docker, no network, no cloud). Needs bash, coreutils, go,
# git and helm. `make bootstrap` is run automatically if ./bin/golangci-lint is
# missing; that step downloads golangci-lint, so the very first run needs network.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

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

echo "=== 1. toolchain files ==="
for f in go.work Makefile makefiles/service.mk .golangci.yml mise.toml tools/go.mod tools/go.sum; do
	check "exists: $f" test -f "$f"
done
check "go.work pins the toolchain version (go 1.2x)" grep -qE '^go 1\.[0-9]+' go.work
check "go.work excludes tools/ from the workspace (tool deps must not drive workspace MVS)" \
	bash -c '! grep -qE "^\s+\./tools$" go.work'
check "every workspace member exists on disk" bash -c '
	set -euo pipefail
	sed -n "/^use (/,/^)/p" go.work | grep -oE "\./[a-z0-9/_-]+" | while read -r m; do
		test -f "$m/go.mod" || { echo "missing $m/go.mod"; exit 1; }
	done'

tools_directives="$(sed -n '/^tool (/,/^)/p' tools/go.mod | grep -c 'github.com/' || true)"
check "tools/go.mod declares >= 6 pinned tool directives (found $tools_directives)" \
	test "$tools_directives" -ge 6
for t in vacuum oasdiff oapi-codegen sqlc goose go-test-coverage; do
	check "tool pinned: $t" grep -q "$t" tools/go.mod
done

echo
echo "=== 2. golangci-lint matches the Makefile pin ==="
pin="$(grep -E '^GOLANGCI_LINT_VERSION' Makefile | sed 's/.*= *//' | tr -d ' ')"
check "Makefile pins a golangci-lint version (found '${pin:-none}')" test -n "$pin"
if [ ! -x ./bin/golangci-lint ]; then
	echo "      ./bin/golangci-lint missing — running make bootstrap"
	make bootstrap >"$TMP/bootstrap.log" 2>&1 ||
		bad "make bootstrap failed (see $TMP/bootstrap.log)"
fi
check "./bin/golangci-lint is installed and runnable" test -x ./bin/golangci-lint
installed="$(./bin/golangci-lint version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1)"
check "installed golangci-lint ${installed:-none} == Makefile pin ${pin:-none}" \
	test "v${installed:-x}" = "$pin"

echo
echo "=== 3. the repo-wide make targets are green ==="
# Sequential and deliberately not parallel: `make lint` is the slowest step and a
# failure there is almost always the reason build/test fail too.
check "make lint (golangci-lint over every workspace module)" make lint
check "make build-all (go build over every workspace module)" make build-all
check "make test-all (go test over every workspace module)" make test-all
check "go -C tools build ./... (the tools module compiles, incl. contractcheck)" \
	env GOWORK=off go -C tools build ./...

echo
echo "=== 4. helm charts ==="
if ! command -v helm >/dev/null 2>&1; then
	bad "helm is not installed — install helm 3.x or later and re-run (FND-0 charts cannot be verified without it)"
else
	check "helm dependency build vendors the library into render-test" \
		helm dependency build deployment/charts/render-test
	check "helm lint --strict deployment/charts/collections-service" \
		helm lint --strict deployment/charts/collections-service
	check "helm lint --strict deployment/charts/render-test" \
		helm lint --strict deployment/charts/render-test
	check "collections-service is declared type: library" \
		grep -qE '^type:\s*library' deployment/charts/collections-service/Chart.yaml
	check "render-test depends on the library chart by file:// path" \
		grep -qF 'file://../collections-service' deployment/charts/render-test/Chart.yaml

	echo
	echo "=== 5. the library chart renders through its consumer ==="
	if helm template deployment/charts/render-test >"$TMP/rendered.yaml" 2>"$TMP/render.err"; then
		ok "helm template deployment/charts/render-test"
	else
		bad "helm template deployment/charts/render-test ($(tail -1 "$TMP/render.err"))"
	fi
	check "the render is not empty" test -s "$TMP/rendered.yaml"
	for kind in ServiceAccount Service Deployment HorizontalPodAutoscaler CronJob Job; do
		check "rendered object: $kind" grep -qE "^kind: $kind$" "$TMP/rendered.yaml"
	done
	check "the migrate Job carries the pre-install/pre-upgrade helm hook" \
		grep -qE 'helm.sh/hook.*pre-install,pre-upgrade' "$TMP/rendered.yaml"
	check "every rendered container image is explicit (no empty image: field)" \
		bash -c '! grep -nE "^\s+image:\s*(\"\")?\s*$" '"$TMP/rendered.yaml"

	echo
	echo "=== 6. expected-FAIL: the guards bite ==="
	check_fails "a library chart is not installable on its own (proves collections-service is a library)" \
		helm template deployment/charts/collections-service
fi

echo
check_fails "make verify refuses to run without WP=<id>" make verify
check_fails "make ownership-check refuses to run without WP=<id>" make ownership-check
check_fails "make tf-plan refuses to run without STACK=<nn-name>" make tf-plan

echo
printf 'FND-0: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
