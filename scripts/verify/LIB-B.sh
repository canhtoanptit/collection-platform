#!/usr/bin/env bash
#
# scripts/verify/LIB-B.sh — verifies the second batch of platform libraries:
# platform/postgres (LIB-5) and platform/kafka (LIB-6).
#
# It asserts observable outcomes only: the two packages expose the API the briefs
# name, the module builds and tests green in BOTH workspace and standalone mode,
# every named delivery-semantics test actually runs against a real Postgres and a
# real broker, per-package coverage clears the >=85% floor from plan §7 Phase 3
# (integration counted), gofmt and golangci-lint are clean with *and* without the
# integration tag, and the forbidden LIB-C dependencies are absent. It ends with
# expected-FAIL assertions proving the guards bite rather than merely accepting
# good input.
#
# THE INTEGRATION-TAG CONVENTION
#
# platform/postgres and platform/kafka are the first packages in this module with
# tests that need Docker. Those tests carry `//go:build integration`, the same
# convention makefiles/service.mk's `test-integration` target uses:
#
#   go -C platform test ./... -race -count=1                 # unit, no Docker
#   go -C platform test ./... -tags integration -race         # + testcontainers
#
# so `make test-all` (a plain `go test ./...`) stays Docker-free, and this script
# runs both modes explicitly. Coverage is measured with the tag on, because the
# delivery semantics that matter — retry, DLQ, commit, drain — only exist against
# a broker.
#
# Environment: **Docker is required** and must have room to work: Redpanda
# refuses produce requests when its data volume has less than
# storage_min_free_bytes free (the tests lower that to 32 MiB, but the Docker VM
# still needs a few hundred MB), and postgres:16-alpine will not initialise a data
# directory on a full disk. Also needs bash, coreutils, go, git and
# ./bin/golangci-lint (installed by `make bootstrap`). No cloud, no credentials:
# the MSK IAM path is exercised with static environment credentials.
#
# Images (pinned, pulled on first run): postgres:16-alpine,
# docker.redpanda.com/redpandadata/redpanda:v25.2.4.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

MODULE_DIR="$REPO_ROOT/platform"
COVERAGE_FLOOR=85
# The packages this WP owns. Everything else in platform/ belongs to LIB-A and is
# only checked for collateral damage.
LIB_B_PACKAGES=(postgres kafka)
# Generous: a cold image pull plus two container suites.
INTEGRATION_TIMEOUT=25m

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

# check <description> <command...>       -- command must succeed
check() {
	local desc="$1"
	shift
	if "$@" >>"$TMP/last.log" 2>&1; then ok "$desc"; else bad "$desc (see $TMP/last.log)"; fi
}

# check_fails <description> <command...> -- command must FAIL (guard proof)
check_fails() {
	local desc="$1"
	shift
	if "$@" >>"$TMP/last.log" 2>&1; then
		bad "$desc (command unexpectedly succeeded)"
	else
		ok "$desc"
	fi
}

echo "=== 0. preconditions ==="
if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
	ok "Docker is available (testcontainers integration tests are part of this gate)"
else
	bad "Docker is not available — LIB-B's acceptance is the testcontainers suite, so this cannot pass without it"
fi
check "./bin/golangci-lint is installed (make bootstrap)" test -x ./bin/golangci-lint

echo
echo "=== 1. module layout ==="
check "the platform module exists" test -f platform/go.mod
check "module path is .../platform" \
	grep -qx 'module github.com/canhtoanptit/collection-platform/platform' platform/go.mod
# LIB-A's binding decision. A dependency that demanded a higher language version
# would be the wrong dependency, not a reason to move the module's baseline.
check "the module still declares go 1.25.0 (LIB-A's baseline, unchanged)" \
	grep -qx 'go 1.25.0' platform/go.mod

for pkg in "${LIB_B_PACKAGES[@]}"; do
	check "package exists: platform/$pkg" test -d "platform/$pkg"
	check "package has Go sources: platform/$pkg" \
		bash -c "ls platform/$pkg/*.go >/dev/null 2>&1"
	check "package ships unit tests: platform/$pkg" \
		bash -c "ls platform/$pkg/*_test.go >/dev/null 2>&1"
	check "tests are table-driven: platform/$pkg" \
		bash -c "grep -qE 'tests := \[\]struct|for _, tc := range' platform/$pkg/*_test.go"
done

# LIB-A must still be intact: this WP only adds packages and edits doc.go.
for pkg in events ids clock apierror httpkit health config otelkit authn; do
	check "LIB-A package untouched and present: platform/$pkg" test -d "platform/$pkg"
done
check "no LIB-A source file was modified (doc.go is the one allowed edit)" bash -c '
	set -euo pipefail
	touched="$(git status --porcelain -- \
		platform/events platform/ids platform/clock platform/apierror platform/httpkit \
		platform/health platform/config platform/otelkit platform/authn || true)"
	test -z "$touched" || { echo "$touched"; exit 1; }'

# LIB-C owns these. Creating one here would put two agents in one module (CLAUDE.md §7).
for pkg in outbox inbox idempotency ruledsl allocation modelclient testkit; do
	check "not built yet (LIB-C owns it): platform/$pkg" \
		bash -c "test ! -d platform/$pkg"
done

echo
echo "=== 2. the integration tests are gated behind the build tag ==="
# The gate is what keeps `make test-all` Docker-free. Assert it structurally as
# well as behaviourally: the files carry the tag, and a plain `go test` compiles
# without Docker being touched.
for pkg in "${LIB_B_PACKAGES[@]}"; do
	check "platform/$pkg has an integration test file" \
		test -f "platform/$pkg/integration_test.go"
	check "platform/$pkg/integration_test.go is tagged //go:build integration" \
		bash -c "head -1 platform/$pkg/integration_test.go | grep -qx '//go:build integration'"
done
check "no testcontainers usage escapes the tag (untagged files never import it)" bash -c '
	set -euo pipefail
	offenders=""
	for f in platform/postgres/*.go platform/kafka/*.go; do
		case "$f" in *integration_test.go) continue ;; esac
		if grep -q "testcontainers" "$f"; then offenders="$offenders $f"; fi
	done
	test -z "$offenders" || { echo "untagged files import testcontainers:$offenders"; exit 1; }'

echo
echo "=== 3. public API surface the briefs name ==="
# Asserted through `go doc`, so a renamed or removed symbol fails here rather
# than in a downstream service.
api_has() {
	local pkg="$1" symbol="$2"
	check "platform/$pkg exposes $symbol" \
		bash -c "go -C platform doc './$pkg' | grep -qF '$symbol'"
}
# Methods are listed under their type, not at package level.
type_has() {
	local pkg="$1" typ="$2" symbol="$3"
	check "platform/$pkg $typ has $symbol" \
		bash -c "go -C platform doc './$pkg' '$typ' | grep -qF '$symbol'"
}

# --- LIB-5: postgres
api_has postgres "func Connect(ctx context.Context, cfg Config) (*pgxpool.Pool, error)"
api_has postgres "func WithTx(ctx context.Context, db Beginner, fn func(pgx.Tx) error) error"
api_has postgres "func Migrate(ctx context.Context, databaseURL string, fsys fs.FS, dir string) error"
api_has postgres "func ReadyCheck(p Pinger) health.Check"
api_has postgres "type Config struct"
api_has postgres "type Beginner interface"
api_has postgres "type Pinger interface"
api_has postgres "ErrNestedTx"
# Down and version are what make "up, down, up again" assertable by a caller.
api_has postgres "func MigrateDownTo(ctx context.Context, databaseURL string, fsys fs.FS, dir string, version int64) error"
api_has postgres "func Version(ctx context.Context, databaseURL string, fsys fs.FS, dir string) (int64, error)"

# --- LIB-6: kafka
api_has kafka "type Publisher interface"
api_has kafka "type Config struct"
api_has kafka "type ConsumerConfig struct"
api_has kafka "type Handler func(ctx context.Context, env events.Envelope) error"
type_has kafka Publisher "Publish(ctx context.Context, topic, key string, value []byte, headers map[string]string) error"
type_has kafka FranzPublisher "func NewPublisher(cfg Config) (*FranzPublisher, error)"
type_has kafka FranzPublisher "func (p *FranzPublisher) Close()"
type_has kafka FranzPublisher "func (p *FranzPublisher) ReadyCheck() health.Check"
type_has kafka Consumer "func NewConsumer(cfg ConsumerConfig) (*Consumer, error)"
type_has kafka Consumer "func (c *Consumer) Run(ctx context.Context) error"
type_has kafka Consumer "func (c *Consumer) ReadyCheck() health.Check"
# The A§27 DLQ header names are part of the contract: the ingestion DLQ consumer
# and the replay endpoint read them.
for header in x-origin-topic x-origin-partition x-origin-offset x-error; do
	check "platform/kafka exports the A§27 DLQ header $header" \
		bash -c "go -C platform doc ./kafka | grep -qF '\"$header\"' || grep -qF '\"$header\"' platform/kafka/headers.go"
done

echo
echo "=== 4. the configuration structs bind the documented variables ==="
for var in DATABASE_URL DATABASE_MAX_CONNS DATABASE_MIN_CONNS DATABASE_STATEMENT_TIMEOUT; do
	check "postgres.Config binds \$$var" grep -q "env:\"$var" platform/postgres/postgres.go
done
check "postgres.Config requires DATABASE_URL" \
	grep -q 'env:"DATABASE_URL,required"' platform/postgres/postgres.go
for var in KAFKA_BROKERS KAFKA_TLS KAFKA_SASL_IAM_REGION KAFKA_CLIENT_ID; do
	check "kafka.Config binds \$$var" grep -q "env:\"$var" platform/kafka/kafka.go
done
for var in KAFKA_CONSUMER_GROUP KAFKA_TOPICS KAFKA_DLQ_TOPIC KAFKA_MAX_RETRIES KAFKA_RETRY_BACKOFF; do
	check "kafka.ConsumerConfig binds \$$var" grep -q "env:\"$var" platform/kafka/consumer.go
done
check "the default retry budget is 3 (A§27)" \
	grep -q 'defaultMaxRetries = 3' platform/kafka/consumer.go

echo
echo "=== 5. the durability settings the briefs require are actually set ==="
# These are one-line settings whose absence is invisible until an event is lost,
# so they are asserted in the source as well as by the unit tests that read them
# back off a live client (TestProducerDurability).
check "the producer requires acks from all in-sync replicas" \
	grep -q 'kgo.RequiredAcks(kgo.AllISRAcks())' platform/kafka/kafka.go
check "the idempotent producer is never disabled" \
	bash -c '! grep -rn "DisableIdempotentWrite" platform/kafka/*.go | grep -v _test.go | grep -v "^\s*//"'
check "the consumer commits explicitly, never automatically" \
	grep -q 'kgo.DisableAutoCommit()' platform/kafka/consumer.go
check "rebalances are blocked while a polled batch is in flight" \
	grep -q 'kgo.BlockRebalanceOnPoll()' platform/kafka/consumer.go
# The call form, with its paren: the package doc names the option in prose to
# explain why it is absent, and a prose mention must not fail this check.
check "auto topic creation is never requested (ADR-0004: auto-create is off)" \
	bash -c '! grep -rn "kgo.AllowAutoTopicCreation(" platform/kafka/*.go'
check "MSK IAM wires the franz-go AWS mechanism" \
	grep -q 'ManagedStreamingIAM' platform/kafka/kafka.go
check "migrations run under goose's Postgres session advisory lock" \
	bash -c '
		grep -q "goose.WithSessionLocker" platform/postgres/migrate.go
		grep -q "lock.NewPostgresSessionLocker" platform/postgres/migrate.go'
check "out-of-order migrations are refused explicitly" \
	grep -q 'goose.WithAllowOutofOrder(false)' platform/postgres/migrate.go
check "the pool is instrumented with an otel query tracer" \
	grep -q 'otelpgx.NewTracer' platform/postgres/postgres.go

echo
echo "=== 6. formatting, vet and lint (with and without the tag) ==="
check "gofmt -l platform is empty" bash -c '
	out="$(gofmt -l platform)"
	test -z "$out" || { echo "$out"; exit 1; }'
check "go vet ./... (platform)" go -C platform vet ./...
check "go vet -tags integration ./... (platform)" go -C platform vet -tags integration ./...
check "golangci-lint run ./... (platform)" \
	bash -c 'cd platform && "$0" run ./...' "$REPO_ROOT/bin/golangci-lint"
# The integration files are real code and are linted too — otherwise the largest
# test files in the module are the only unlinted ones.
check "golangci-lint run --build-tags=integration ./... (platform)" \
	bash -c 'cd platform && "$0" run --build-tags=integration ./...' "$REPO_ROOT/bin/golangci-lint"

echo
echo "=== 7. build and unit tests in workspace mode ==="
check "go -C platform build ./..." go -C platform build ./...
check "go -C platform build -tags integration ./..." go -C platform build -tags integration ./...
check "go -C platform test ./... -race -count=1 (unit only, no Docker)" \
	go -C platform test ./... -race -count=1

echo
echo "=== 8. build and test in standalone module mode (GOWORK=off) ==="
# A service depending on platform/ resolves it as a module, not a workspace
# member: an incomplete go.sum or a missing require only shows up here.
check "GOWORK=off go -C platform build ./..." env GOWORK=off go -C platform build ./...
check "GOWORK=off go -C platform vet -tags integration ./..." \
	env GOWORK=off go -C platform vet -tags integration ./...
check "GOWORK=off go -C platform test ./... -count=1" env GOWORK=off go -C platform test ./... -count=1
check "GOWORK=off go -C platform mod verify" env GOWORK=off go -C platform mod verify
check "go.mod and go.sum are tidy (GOWORK=off go mod tidy is a no-op)" bash -c '
	set -euo pipefail
	tmp="$1"
	cp platform/go.mod "$tmp/go.mod.before"
	cp platform/go.sum "$tmp/go.sum.before"
	GOWORK=off go -C platform mod tidy
	diff -q "$tmp/go.mod.before" platform/go.mod
	diff -q "$tmp/go.sum.before" platform/go.sum' _ "$TMP"

echo
echo "=== 9. dependency guard ==="
# Now allowed (LIB-B owns them), and each must be pinned to a released version.
dep_pinned() {
	local dep="$1" line
	line="$(grep -F "	$dep v" platform/go.mod | head -1 || true)"
	if [ -z "$line" ]; then
		bad "dependency missing from platform/go.mod: $dep"
		return
	fi
	printf '      %s\n' "$(printf '%s' "$line" | tr -s '[:blank:]' ' ' | sed 's/^ //')"
	ok "dependency pinned: $dep"
}
for dep in \
	github.com/jackc/pgx/v5 \
	github.com/pressly/goose/v3 \
	github.com/exaring/otelpgx \
	github.com/twmb/franz-go \
	github.com/aws/aws-sdk-go-v2/config \
	github.com/testcontainers/testcontainers-go \
	github.com/testcontainers/testcontainers-go/modules/postgres \
	github.com/testcontainers/testcontainers-go/modules/redpanda; do
	dep_pinned "$dep"
done

# Still forbidden. shopspring/decimal and kin-openapi arrive with LIB-C
# (ruledsl, testkit); chi is not this platform's router at all — httpkit is
# net/http + ServeMux.
for dep in \
	github.com/shopspring/decimal \
	github.com/getkin/kin-openapi \
	github.com/go-chi/chi \
	github.com/gorilla/mux \
	gorm.io/gorm \
	github.com/segmentio/kafka-go \
	github.com/confluentinc/confluent-kafka-go \
	github.com/golang-migrate/migrate \
	github.com/lib/pq; do
	check "forbidden dependency absent: $dep" \
		bash -c "! grep -q '$dep' platform/go.mod"
done

check "no direct dependency is a pseudo-version (the contracts replace aside)" bash -c '
	set -euo pipefail
	offenders="$(grep -E "v0\.0\.0-[0-9]{14}-" platform/go.mod |
		grep -v "// indirect" |
		grep -v "collection-platform/contracts" || true)"
	test -z "$offenders" || { echo "$offenders"; exit 1; }'

# D§58 anti-lock-in: exactly one package may know which Kafka client is in use.
check "only platform/kafka imports kgo (services never do)" bash -c '
	set -euo pipefail
	offenders="$(grep -rl "twmb/franz-go" platform --include="*.go" | grep -v "^platform/kafka/" || true)"
	test -z "$offenders" || { echo "$offenders"; exit 1; }'
check "only platform/postgres imports pgx" bash -c '
	set -euo pipefail
	offenders="$(grep -rl "jackc/pgx" platform --include="*.go" | grep -v "^platform/postgres/" || true)"
	test -z "$offenders" || { echo "$offenders"; exit 1; }'

echo
echo "=== 10. integration tests (Docker) + per-package coverage floor ==="
# One run does three jobs: it proves the container suites pass, it proves each
# named acceptance test actually ran (a `-run` pattern that matches nothing exits
# 0, so "the test passes" must also prove the test exists), and it produces the
# coverage profile the floor is measured from. With -race, because the consumer
# handles partitions concurrently and a data race there would corrupt ordering
# under exactly the load these tests create.
int_log="$TMP/integration.log"
integration_ok=0
if go -C platform test ./postgres/... ./kafka/... \
	-tags integration -race -count=1 -v \
	-timeout "$INTEGRATION_TIMEOUT" \
	-covermode=atomic -coverprofile="$TMP/integration.out" >"$int_log" 2>&1; then
	ok "the integration suite passes (postgres + kafka, testcontainers, -race)"
	integration_ok=1
else
	bad "the integration suite failed (see $int_log)"
	printf '      last failures:\n'
	grep -E '^\s*--- FAIL|^\s+[a-z_]+_test\.go:[0-9]+:' "$int_log" | head -20 | sed 's/^/      /'
fi
check "the -race run reported no data race" \
	bash -c "! grep -q 'DATA RACE' '$int_log'"

# named_test_ran <test name> <description>
named_test_ran() {
	local name="$1" desc="$2"
	if [ "$integration_ok" -ne 1 ]; then
		bad "$desc (the integration suite did not pass)"
		return
	fi
	if grep -qE "^--- PASS: ${name}( |$)" "$int_log"; then
		ok "$desc"
	else
		bad "$desc — no PASS line for ${name} (renamed, deleted or skipped)"
	fi
}

# --- LIB-5 acceptance (plan §7): "testcontainers migrate up/down/up idempotent;
#     rollback-on-error verified".
named_test_ran TestIntegrationMigrateUpDownUp \
	"migrate up -> version -> down -> up again is idempotent (LIB-5 acceptance)"
named_test_ran TestIntegrationMigrateConcurrent \
	"concurrent Migrate calls all succeed, serialised by the advisory lock (LIB-5 acceptance)"
named_test_ran TestIntegrationWithTx \
	"WithTx: commit persists, an error rolls back, a panic rolls back and re-panics (LIB-5 acceptance)"
named_test_ran TestIntegrationReadyCheck \
	"postgres.ReadyCheck is ready on a live pool and unready on a closed one"
named_test_ran TestIntegrationConnectAppliesPoolCaps \
	"the pool caps and runtime parameters reach the server, not just the struct"

# --- LIB-6 acceptance (plan §7): "ordered per key; poison -> DLQ with headers,
#     consumption continues; malformed envelope -> DLQ; otel headers propagate".
named_test_ran TestIntegrationOrderedDeliveryPerKey \
	"ordered delivery per key under concurrent publishing (A§26, LIB-6 acceptance)"
named_test_ran TestIntegrationPartitionsProceedConcurrently \
	"different partitions are handled concurrently, not one after another"
named_test_ran TestIntegrationPoisonMessageIsDeadLettered \
	"poison message: MaxRetries+1 attempts -> DLQ with origin headers -> consumption continues (A§27)"
named_test_ran TestIntegrationMalformedEnvelopeIsDeadLetteredWithoutRetries \
	"a malformed envelope goes straight to the DLQ, with no retries (LIB-6 acceptance)"
named_test_ran TestIntegrationUnknownEventTypeIsSkipped \
	"an unknown eventType is skipped and NOT dead-lettered (contracts/README §13)"
named_test_ran TestIntegrationPropagationReachesTheHandler \
	"trace, correlation id and causation-from-eventId reach the handler context (A§97)"
named_test_ran TestIntegrationGracefulShutdownCommitsProcessedOffsets \
	"graceful shutdown commits what it handled: a restart does not reprocess (LIB-6 acceptance)"
named_test_ran TestIntegrationUnsettledRecordIsNotCommitted \
	"a failed DLQ produce is never committed away: Run errors and the record is redelivered"
named_test_ran TestIntegrationReadyCheckPassesOnceTheGroupIsJoined \
	"kafka.ReadyCheck is ready only once the group is joined"
named_test_ran TestIntegrationPublishReportsBrokerRejection \
	"a publish the brokers never acknowledge is an error, not a silent success or a hang"

# --- unit-level acceptance that does not need Docker but is easy to lose.
for spec in \
	"TestProducerDurability|acks=all and the idempotent producer are read back off a live client" \
	"TestMetricNamesOnTheScrapeEndpoint|the Prometheus metric names an operator queries are pinned" \
	"TestMetricLabelsAreBounded|metric labels stay bounded (topic, group, reason)" \
	"TestWithTxOutcomes|the WithTx state x outcome table (commit/error/panic/begin/commit failure)" \
	"TestWithTxRefusesNesting|nested WithTx is refused rather than silently making a savepoint" \
	"TestConsumerConfigReportsEveryProblemAtOnce|ConsumerConfig names every wiring problem at once" \
	"TestMSKIAMResolvesCredentials|MSK IAM resolves credentials through the default AWS chain"; do
	name="${spec%%|*}"
	desc="${spec#*|}"
	named_test_ran "$name" "$desc"
done

if [ "$integration_ok" -eq 1 ]; then
	printf '      per-package coverage (integration counted):\n'
	while read -r pkg cov; do
		short="${pkg##*/}"
		printf '        %-10s %s%%\n' "$short" "$cov"
		if awk -v got="$cov" -v floor="$COVERAGE_FLOOR" 'BEGIN { exit !(got + 0 >= floor + 0) }'; then
			ok "package coverage ${cov}% >= ${COVERAGE_FLOOR}%: platform/$short"
		else
			bad "package coverage ${cov}% is below the ${COVERAGE_FLOOR}% floor: platform/$short"
		fi
	done < <(grep -oE 'ok[[:space:]]+\S+[[:space:]]+\S+[[:space:]]+coverage: [0-9.]+%' "$int_log" |
		awk '{ gsub("%", "", $NF); print $2, $NF }')

	# Both packages must be represented, or a silently-skipped package would look
	# like a pass.
	for pkg in "${LIB_B_PACKAGES[@]}"; do
		check "coverage was measured for platform/$pkg" \
			bash -c "grep -qE 'ok[[:space:]]+\S+/$pkg[[:space:]]+.*coverage:' '$int_log'"
	done
else
	bad "coverage was not measured (the integration suite did not pass)"
fi

echo
echo "=== 11. expected-FAIL: the guards bite ==="
# Each of these is a small Go program in a temp package under platform/, so it
# compiles against the real module and is removed afterwards.
guard_dir="$MODULE_DIR/zz_libbverify"
mkdir -p "$guard_dir"
trap 'rm -rf "$TMP" "$guard_dir"' EXIT

run_guard() {
	local name="$1" body="$2" want="$3" desc="$4"
	printf '%s' "$body" >"$guard_dir/${name}_test.go"
	if go -C platform test ./zz_libbverify -run "^Test${name}$" -count=1 >"$TMP/guard.log" 2>&1; then
		if [ "$want" = pass ]; then ok "$desc"; else bad "$desc (the guard did not reject it)"; fi
	else
		if [ "$want" = fail ]; then ok "$desc"; else bad "$desc (see $TMP/guard.log)"; fi
	fi
	rm -f "$guard_dir/${name}_test.go"
}

run_guard NoDLQTopic 'package zz_libbverify

import (
	"context"
	"strings"
	"testing"

	"github.com/canhtoanptit/collection-platform/contracts"
	"github.com/canhtoanptit/collection-platform/platform/events"
	"github.com/canhtoanptit/collection-platform/platform/kafka"
)

func TestNoDLQTopic(t *testing.T) {
	registry, err := events.NewRegistry(contracts.FS)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	// A consumer with nowhere to put a poison message would have to choose
	// between dropping it and blocking its partition forever (CLAUDE.md §5).
	_, err = kafka.NewConsumer(kafka.ConsumerConfig{
		Brokers:  []string{"localhost:9092"},
		Group:    "guard",
		Topics:   []string{"collections.case"},
		Registry: registry,
		Handler:  func(context.Context, events.Envelope) error { return nil },
	})
	if err == nil {
		t.Fatal("NewConsumer accepted a configuration with no DLQ topic")
	}
	if !strings.Contains(err.Error(), "KAFKA_DLQ_TOPIC") {
		t.Fatalf("the error does not name the variable to set: %v", err)
	}
}
' pass "a consumer with no DLQ topic is refused at construction"

run_guard NoRegistry 'package zz_libbverify

import (
	"context"
	"testing"

	"github.com/canhtoanptit/collection-platform/platform/events"
	"github.com/canhtoanptit/collection-platform/platform/kafka"
)

func TestNoRegistry(t *testing.T) {
	// ADR-0004 guarantees every message is validated at runtime. A consumer with
	// no registry would hand unvalidated JSON to business code.
	if _, err := kafka.NewConsumer(kafka.ConsumerConfig{
		Brokers:  []string{"localhost:9092"},
		Group:    "guard",
		Topics:   []string{"collections.case"},
		Handler:  func(context.Context, events.Envelope) error { return nil },
		DLQTopic: "collections.dlq.guard",
	}); err == nil {
		t.Fatal("NewConsumer accepted a configuration with no event registry")
	}
}
' pass "a consumer with no event registry is refused at construction"

run_guard NestedTx 'package zz_libbverify

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/canhtoanptit/collection-platform/platform/postgres"
)

// fakeTx satisfies pgx.Tx by embedding it: everything WithTx does not touch is
// left nil, so an unexpected call panics rather than passing quietly.
type fakeTx struct{ pgx.Tx }

func (f fakeTx) Begin(context.Context) (pgx.Tx, error) { return f, nil }
func (f fakeTx) Commit(context.Context) error          { return nil }
func (f fakeTx) Rollback(context.Context) error        { return nil }

func TestNestedTx(t *testing.T) {
	// pgx.Tx satisfies postgres.Beginner (its Begin opens a savepoint), so a
	// nested call compiles. It must not run: "rollback on error" would unwind
	// only the savepoint and let the outer transaction commit half a unit of work.
	err := postgres.WithTx(context.Background(), fakeTx{}, func(pgx.Tx) error {
		t.Error("the function ran inside a nested WithTx")
		return nil
	})
	if !errors.Is(err, postgres.ErrNestedTx) {
		t.Fatalf("WithTx on a pgx.Tx = %v, want ErrNestedTx", err)
	}
}
' pass "WithTx refuses a nested transaction instead of making a savepoint"

run_guard EmptyMigrationsFS 'package zz_libbverify

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/canhtoanptit/collection-platform/platform/postgres"
)

func TestEmptyMigrationsFS(t *testing.T) {
	// An embed pattern that matches nothing yields a valid, empty FS. Migrating
	// it must be an error: goose would otherwise report "version 0, nothing to
	// do" and a deployment onto an empty schema would look green.
	err := postgres.Migrate(context.Background(), "postgres://u@h/d",
		fstest.MapFS{"migrations/README.md": {Data: []byte("no sql here")}}, "migrations")
	if err == nil {
		t.Fatal("Migrate accepted a migrations FS with no .sql files")
	}
	if !strings.Contains(err.Error(), "no .sql migrations found") {
		t.Fatalf("the error does not say what is wrong: %v", err)
	}
}
' pass "an empty migrations FS is refused rather than silently migrating nothing"

run_guard SkipNotDeadLetter 'package zz_libbverify

import (
	"errors"
	"testing"

	"github.com/canhtoanptit/collection-platform/contracts"
	"github.com/canhtoanptit/collection-platform/platform/events"
)

func TestSkipNotDeadLetter(t *testing.T) {
	// The sentinel the consumer branches on. If this stopped being
	// ErrUnknownEvent, every new event type would become an incident for every
	// already-deployed consumer (contracts/README §13).
	registry, err := events.NewRegistry(contracts.FS)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	env, err := events.New("AccountTeleported", 1, "delinquency-service", "Account",
		"01M0KK4P3G0MQSQ3A1X2PMA6VX", map[string]any{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	raw, err := events.MarshalCanonical(env)
	if err != nil {
		t.Fatalf("MarshalCanonical: %v", err)
	}
	if _, err := registry.Decode(raw); !errors.Is(err, events.ErrUnknownEvent) {
		t.Fatalf("Decode on an unknown event type = %v, want ErrUnknownEvent", err)
	}
}
' pass "an unknown event type decodes to ErrUnknownEvent (the skip signal, not the DLQ signal)"

# A deliberately broken caller must not compile: services must not be able to
# reach the Kafka client through this package (D§58).
cat >"$guard_dir/nocompile_test.go" <<'GO'
package zz_libbverify

import (
	"testing"

	"github.com/canhtoanptit/collection-platform/platform/kafka"
)

func TestPublisherHidesTheClient(t *testing.T) {
	var p *kafka.FranzPublisher
	_ = p.Client()
}
GO
check_fails "the publisher exposes no Kafka client to reach around the abstraction" \
	go -C platform vet ./zz_libbverify
rm -f "$guard_dir/nocompile_test.go"

rm -rf "$guard_dir"
trap 'rm -rf "$TMP"' EXIT

echo
echo "=== 12. platform/doc.go records the new packages and the layering ==="
# still_to_come prints just the "Still to come:" block — up to the next doc
# heading — so the "Dependency direction" section, which names every package,
# cannot make this check pass or fail by accident.
still_to_come() {
	awk '/Still to come:/ { inblock = 1; next } /^\/\/ #/ { inblock = 0 } inblock' platform/doc.go
}
doc_lists_as_built() {
	local pkg="$1" builtLine="$2"
	if still_to_come | grep -q "$pkg"; then
		bad "doc.go still lists $pkg under \"Still to come\""
	elif grep -q "$builtLine" platform/doc.go; then
		ok "doc.go lists $pkg as built, not 'still to come'"
	else
		bad "doc.go does not describe $pkg in the built list (expected a line containing \"$builtLine\")"
	fi
}
doc_lists_as_built postgres "postgres     pgx pool"
doc_lists_as_built kafka "kafka        franz-go publisher"
todo_block="$(still_to_come)"
for pkg in outbox inbox idempotency ruledsl allocation modelclient testkit; do
	if printf '%s' "$todo_block" | grep -q "$pkg"; then
		ok "doc.go still lists $pkg as to come (LIB-C)"
	else
		bad "doc.go dropped $pkg from \"Still to come\" — LIB-C has not built it"
	fi
done
check "doc.go records the dependency direction: postgres -> health" \
	grep -qE '^//\s+postgres\s+-> health$' platform/doc.go
check "doc.go records the dependency direction: kafka -> events, otelkit, ..., health" \
	bash -c "grep -E '^//\s+kafka\s+->' platform/doc.go | grep -q events &&
	         grep -E '^//\s+kafka\s+->' platform/doc.go | grep -q otelkit &&
	         grep -E '^//\s+kafka\s+->' platform/doc.go | grep -q health"
check "doc.go documents the integration build tag" \
	bash -c "grep -q 'tags integration' platform/doc.go"
# The layering claim must be true, not merely written down.
check "postgres really does not import kafka, and kafka really does not import postgres" bash -c '
	set -euo pipefail
	! grep -rq "platform/kafka" platform/postgres
	! grep -rq "platform/postgres" platform/kafka'
check "neither package imports a domain service" bash -c '
	set -euo pipefail
	offenders="$(grep -rn "collection-platform/services" platform/postgres platform/kafka || true)"
	test -z "$offenders" || { echo "$offenders"; exit 1; }'

echo
echo "=== 13. ownership ==="
# `make ownership-check WP=LIB-B` is the lead's gate and inspects the whole
# working tree, so it also sees other work packages' in-flight changes — it
# cannot be asserted from inside one WP's script. What *can* be asserted here is
# that LIB-B's ownership entry is exactly the two globs it was delegated, and
# that this WP left the other WPs' verify scripts alone.
check "docs/ownership.yaml grants LIB-B platform/**" \
	bash -c 'sed -n "/^LIB-B:/,/^[A-Za-z]/p" docs/ownership.yaml | grep -q "\"platform/\*\*\""'
check "docs/ownership.yaml grants LIB-B its verify script" \
	bash -c 'sed -n "/^LIB-B:/,/^[A-Za-z]/p" docs/ownership.yaml | grep -q "\"scripts/verify/LIB-B.sh\""'
check "LIB-B owns nothing else" bash -c '
	set -euo pipefail
	globs="$(sed -n "/^LIB-B:/,/^[A-Za-z]/p" docs/ownership.yaml | grep -cE "^\s+- \"")"
	test "$globs" -eq 2'
check "no other WP'\''s verify script was modified or deleted" bash -c '
	set -euo pipefail
	touched="$(git status --porcelain -- scripts/verify | grep -v "^?? " || true)"
	test -z "$touched" || { echo "$touched"; exit 1; }'
check "this WP shipped scripts/verify/LIB-B.sh" test -x scripts/verify/LIB-B.sh
check "contracts/** was not modified (released contracts are immutable)" bash -c '
	set -euo pipefail
	touched="$(git status --porcelain -- contracts || true)"
	test -z "$touched" || { echo "$touched"; exit 1; }'

echo
printf 'LIB-B: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
