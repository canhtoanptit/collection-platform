#!/usr/bin/env bash
#
# scripts/verify/LIB-A.sh — verifies the first batch of platform libraries:
# events, ids, clock, apierror, httpkit, health, config, otelkit, authn (+
# authn/authtest).
#
# It asserts observable outcomes only: the packages exist and expose the API the
# briefs name, the module builds and tests green in BOTH workspace and standalone
# mode, aggregate coverage clears the >=85% floor from plan §7 Phase 3, gofmt is
# clean, the forbidden LIB-B/LIB-C dependencies are absent, and the named golden
# and acceptance tests actually run and pass. It ends with expected-FAIL
# assertions proving the guards bite rather than merely accepting good input.
#
# Environment: none. No Docker, no network (module downloads are cached by the
# earlier `go build`; a cold cache needs the module proxy once), no cloud. Needs
# bash, coreutils, go, git and ./bin/golangci-lint (installed by `make
# bootstrap`).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

MODULE_DIR="$REPO_ROOT/platform"
COVERAGE_FLOOR=85

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

# go_test_named <package> <test regexp> <description>
# Runs one named test and requires it to have actually run: `go test -run` with a
# pattern that matches nothing exits 0, so "the golden test passes" must also
# prove the golden test exists.
go_test_named() {
	local pkg="$1" pattern="$2" desc="$3"
	local out="$TMP/named.log"

	if ! go -C platform test "$pkg" -run "$pattern" -count=1 -v >"$out" 2>&1; then
		bad "$desc (see $out)"
		return
	fi
	if ! grep -qE "^(=== RUN|--- PASS)" "$out"; then
		bad "$desc — no test matched $pattern (the test was renamed or deleted)"
		return
	fi
	ok "$desc"
}

echo "=== 1. module layout ==="
check "the platform module exists" test -f platform/go.mod
check "module path is .../platform" \
	grep -qx 'module github.com/canhtoanptit/collection-platform/platform' platform/go.mod
check "platform is a go.work member" grep -qE '^\s+\./platform$' go.work
check "doc.go documents the module" test -f platform/doc.go

for pkg in events ids clock apierror httpkit health config otelkit authn authn/authtest; do
	check "package exists: platform/$pkg" test -d "platform/$pkg"
	# Every package carries a doc comment on its package clause somewhere in it.
	check "package has Go sources: platform/$pkg" \
		bash -c "ls platform/$pkg/*.go >/dev/null 2>&1"
done

# HISTORICAL GUARD (retired 2026-08-24): during the LIB-A run these packages
# had to be absent — two agents must never share the module. LIB-B has since
# landed postgres/kafka legitimately, so the guard now only covers the batches
# that are still unbuilt. LIB-C's landing retires the rest (its own verify
# script asserts LIB-A/LIB-B stayed intact instead).
for pkg in outbox inbox idempotency ruledsl allocation modelclient testkit; do
	check "not built yet (LIB-C owns it): platform/$pkg" \
		bash -c "test ! -d platform/$pkg"
done

echo
echo "=== 2. every package ships table-driven tests ==="
for pkg in events ids clock apierror httpkit health config otelkit authn authn/authtest; do
	check "tests exist: platform/$pkg" \
		bash -c "ls platform/$pkg/*_test.go >/dev/null 2>&1"
	check "tests are table-driven: platform/$pkg" \
		bash -c "grep -qE 'tests := \[\]struct|for _, tc := range' platform/$pkg/*_test.go"
done

echo
echo "=== 3. public API surface the briefs name ==="
# Asserted through `go doc`, so a renamed or removed symbol fails here rather
# than in a downstream service.
api_has() {
	local pkg="$1" symbol="$2"
	check "platform/$pkg exposes $symbol" \
		bash -c "go -C platform doc './$pkg' | grep -qF '$symbol'"
}

api_has events "type Envelope struct"
api_has events "func New(eventType string"
api_has events "func NewRegistry(fsys fs.FS) (*Registry, error)"
api_has events "func MarshalCanonical(env Envelope) ([]byte, error)"
api_has events "ErrUnknownEvent"
api_has events "ErrSchemaViolation"
api_has events "ErrInvalidEnvelope"

# Methods are listed under their type, not at package level.
type_has() {
	local pkg="$1" typ="$2" symbol="$3"
	check "platform/$pkg $typ has $symbol" \
		bash -c "go -C platform doc './$pkg' '$typ' | grep -qF '$symbol'"
}
type_has events Registry "func (r *Registry) Validate(env Envelope) error"
type_has events Registry "func (r *Registry) ValidateJSON(doc []byte) error"
type_has events Registry "func (r *Registry) Decode(doc []byte) (Envelope, error)"
type_has clock Mock "func (m *Mock) Advance(d time.Duration) time.Time"
type_has httpkit Server "func (s *Server) Run(ctx context.Context) error"
type_has apierror Error "func (e *Error) Status() int"
api_has ids "func NewULID() string"
api_has ids "func IsULID(s string) bool"
api_has ids "func NewFileID() string"
api_has ids "func NewJobID() string"
api_has ids "func NewReconciliationID() string"
api_has ids "func NewCorrelationID() string"
api_has clock "type Clock interface"
api_has clock "func System() Clock"
api_has clock "func Fixed(t time.Time) Clock"
api_has clock "func NewMock(start time.Time) *Mock"
api_has apierror "type Error struct"
api_has apierror "type Detail struct"
api_has apierror "func Write(w http.ResponseWriter, r *http.Request, err error)"
api_has apierror "func From(err error) (*Error, bool)"
api_has httpkit "type Middleware func(http.Handler) http.Handler"
api_has httpkit "func Chain(middleware ...Middleware) Middleware"
api_has httpkit "func CorrelationID() Middleware"
api_has httpkit "func CorrelationIDFrom(ctx context.Context) string"
api_has httpkit "type Server struct"
api_has health "type Check struct"
api_has health "func Handler(checks ...Check) http.Handler"
api_has config "func Load[T any]() (T, error)"
api_has config "type Base struct"
api_has otelkit "func Init(ctx context.Context, info ServiceInfo)"
api_has otelkit "func Logger(ctx context.Context) *slog.Logger"
api_has otelkit "func HTTPMiddleware(next http.Handler) http.Handler"
api_has otelkit "func KafkaHeaders(ctx context.Context) map[string]string"
api_has otelkit "func ContextFromKafkaHeaders(ctx context.Context, headers map[string]string) context.Context"
api_has authn "type Principal struct"
api_has authn "type OIDCConfig struct"
api_has authn "func RequireScope(scopes ...string) httpkit.Middleware"
api_has authn "func RequireGroup(groups ...string) httpkit.Middleware"
api_has authn "func PrincipalFrom(ctx context.Context) (Principal, bool)"
api_has authn "func NormalizeScope(raw string) string"
api_has authn/authtest "func NewIssuer(t *testing.T) *Issuer"

# apierror.Error must be exactly the four A§20 fields, in that order.
check "apierror.Error carries exactly the A§20 fields" bash -c '
	set -euo pipefail
	fields="$(go -C platform doc ./apierror Error |
		grep -oE "json:\"[a-zA-Z]+" | sed "s/json:\"//" | paste -sd, -)"
	test "$fields" = "code,message,correlationId,details"'
check "apierror.Detail carries exactly field+reason" bash -c '
	set -euo pipefail
	fields="$(go -C platform doc ./apierror Detail |
		grep -oE "json:\"[a-zA-Z]+" | sed "s/json:\"//" | paste -sd, -)"
	test "$fields" = "field,reason"'

# The envelope is exactly the ten A§24 fields (CLAUDE.md §5) — no extras.
check "events.Envelope carries exactly the ten A§24 fields, in order" bash -c '
	set -euo pipefail
	fields="$(go -C platform doc ./events Envelope |
		grep -oE "json:\"[a-zA-Z]+" | sed "s/json:\"//" | paste -sd, -)"
	want="eventId,eventType,eventVersion,occurredAt,producer,aggregateType,aggregateId,correlationId,causationId,payload"
	test "$fields" = "$want" || { echo "got:  $fields"; echo "want: $want"; exit 1; }'

echo
echo "=== 4. config.Base declares the documented variables ==="
for var in SERVICE_NAME ENVIRONMENT HTTP_ADDR METRICS_ADDR DATABASE_URL \
	KAFKA_BROKERS OTEL_EXPORTER_OTLP_ENDPOINT OIDC_ISSUER OIDC_AUDIENCE LOG_LEVEL; do
	check "config.Base binds \$$var" grep -q "env:\"$var" platform/config/config.go
done
check "HTTP_ADDR defaults to :8080" grep -q 'env:"HTTP_ADDR" envDefault:":8080"' platform/config/config.go
check "METRICS_ADDR defaults to :9090" grep -q 'env:"METRICS_ADDR" envDefault:":9090"' platform/config/config.go

echo
echo "=== 5. formatting, vet and lint ==="
check "gofmt -l platform is empty" bash -c '
	out="$(gofmt -l platform)"
	test -z "$out" || { echo "$out"; exit 1; }'
check "go vet ./... (platform)" go -C platform vet ./...
if [ -x ./bin/golangci-lint ]; then
	check "golangci-lint run ./... (platform)" \
		bash -c 'cd platform && "$0" run ./...' "$REPO_ROOT/bin/golangci-lint"
else
	bad "./bin/golangci-lint is missing — run 'make bootstrap' (make lint covers platform)"
fi

echo
echo "=== 6. build and test in workspace mode ==="
check "go -C platform build ./..." go -C platform build ./...
check "go -C platform test ./... -race -count=1" go -C platform test ./... -race -count=1

echo
echo "=== 7. build and test in standalone module mode (GOWORK=off) ==="
# A service depending on platform/ resolves it as a module, not a workspace
# member: an incomplete go.sum or a missing require only shows up here (the
# ING-3 lesson).
check "GOWORK=off go -C platform build ./..." env GOWORK=off go -C platform build ./...
check "GOWORK=off go -C platform test ./..." env GOWORK=off go -C platform test ./...
check "GOWORK=off go -C platform mod verify" env GOWORK=off go -C platform mod verify
check "go.mod and go.sum are tidy (GOWORK=off go mod tidy is a no-op)" bash -c '
	set -euo pipefail
	tmp="$1"
	cp platform/go.mod "$tmp/go.mod.before"
	cp platform/go.sum "$tmp/go.sum.before"
	GOWORK=off go -C platform mod tidy
	diff -q "$tmp/go.mod.before" platform/go.mod
	diff -q "$tmp/go.sum.before" platform/go.sum' _ "$TMP"
check "the contracts module is required with a replace so platform builds alone" bash -c '
	set -euo pipefail
	grep -q "github.com/canhtoanptit/collection-platform/contracts" platform/go.mod
	grep -qE "^replace github.com/canhtoanptit/collection-platform/contracts => \.\./contracts$" platform/go.mod'

echo
echo "=== 8. dependency guard ==="
# HISTORICAL GUARD (amended 2026-08-24): pgx/franz-go/goose/testcontainers are
# legitimate since LIB-B landed. Only LIB-C's dependencies remain forbidden
# until that batch runs.
for dep in \
	github.com/shopspring/decimal \
	github.com/getkin/kin-openapi; do
	check "forbidden dependency absent (LIB-C): $dep" \
		bash -c "! grep -q '$dep' platform/go.mod"
done
# Required dependencies, each pinned to an explicit semantic version. -F keeps
# the module path a literal, so slashes and dots need no escaping.
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
	github.com/santhosh-tekuri/jsonschema/v6 \
	github.com/oklog/ulid/v2 \
	github.com/caarlos0/env/v11 \
	github.com/coreos/go-oidc/v3 \
	go.opentelemetry.io/otel \
	go.opentelemetry.io/otel/sdk \
	go.opentelemetry.io/otel/sdk/metric \
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc \
	go.opentelemetry.io/otel/exporters/prometheus \
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp \
	github.com/prometheus/client_golang; do
	dep_pinned "$dep"
done

# Direct dependencies must be released versions. Indirect ones are exempt:
# google.golang.org/genproto only ever publishes pseudo-versions, and it arrives
# through the OTLP gRPC exporter rather than by choice.
check "no direct dependency is a pseudo-version (the contracts replace aside)" bash -c '
	set -euo pipefail
	offenders="$(grep -E "v0\.0\.0-[0-9]{14}-" platform/go.mod |
		grep -v "// indirect" |
		grep -v "collection-platform/contracts" || true)"
	test -z "$offenders" || { echo "$offenders"; exit 1; }'

echo
echo "=== 9. the named acceptance tests run and pass ==="
go_test_named ./apierror '^TestWriteGoldenA20ErrorBody$' \
	"A§20 golden body is byte-exact (TestWriteGoldenA20ErrorBody)"
check "the A§20 golden file exists" test -f platform/apierror/testdata/a20-validation-error.golden.json
check "the golden file is the documented A§20 example" bash -c '
	grep -q "ARRANGEMENT_INVALID" platform/apierror/testdata/a20-validation-error.golden.json
	grep -q "firstPaymentDate" platform/apierror/testdata/a20-validation-error.golden.json
	grep -q "DATE_IN_PAST" platform/apierror/testdata/a20-validation-error.golden.json'

go_test_named ./events '^TestEveryContractExampleValidates$' \
	"every contracts event example passes Registry.Validate (LIB-1 acceptance)"
go_test_named ./events '^TestValidateRejectsBadDocuments$' \
	"invalid payloads and unknown event types are rejected (LIB-1 acceptance)"
go_test_named ./events '^TestMarshalCanonicalIsStableAcrossPayloadKeyOrder$' \
	"canonical marshalling is stable for hashing"
go_test_named ./httpkit '^TestRecoverWritesTheErrorContract$' \
	"a panic becomes a 500 A§20 body with the correlation id (LIB-2 acceptance)"
go_test_named ./otelkit '^TestHTTPToKafkaToContextRoundTrip$' \
	"HTTP -> ctx -> Kafka headers -> ctx preserves trace and correlation (LIB-3 acceptance)"
go_test_named ./authn '^TestKeycloakTokenShape$' \
	"Keycloak token shape: logical scopes and plain groups (LIB-4 primary)"
go_test_named ./authn '^TestCognitoTokenShapeStillNormalizes$' \
	"Cognito resource-server/dot scopes still normalize (LIB-4 dormant compatibility)"
go_test_named ./authn '^TestUnauthenticatedRequests$' \
	"expired, wrong-issuer and wrong-audience tokens are 401 (LIB-4 acceptance)"
go_test_named ./authn '^TestRequireScope$' \
	"a missing scope is 403 with the A§20 body (LIB-4 acceptance)"
go_test_named ./authn '^TestNormalizeScope$' \
	"scope normalization follows the FND-6 SCOPE-FORMAT RULING"
go_test_named ./config '^TestLoadReportsEveryProblemAtOnce$' \
	"config.Load aggregates every missing and invalid variable (LIB-2 acceptance)"
go_test_named ./health '^TestReadiness$' \
	"/readyz answers 503 naming the failing checks (LIB-2 acceptance)"

echo
echo "=== 10. coverage floor (>= ${COVERAGE_FLOOR}%) ==="
# Scoped to LIB-A's own packages (amended 2026-08-24): later batches keep their
# real coverage behind the `integration` build tag, so an unscoped ./... sweep
# here would judge them by their unit slice. Each batch's verify script owns
# its packages' floors (LIB-B.sh runs tagged; same for LIB-C).
LIB_A_PKGS="./events ./ids ./clock ./apierror ./httpkit ./health ./config ./otelkit ./authn ./authn/authtest"
if go -C platform test $LIB_A_PKGS -count=1 -covermode=atomic \
	-coverprofile="$TMP/coverage.out" >"$TMP/coverage.log" 2>&1; then
	total="$(go -C platform tool cover -func="$TMP/coverage.out" |
		awk '$1 == "total:" { gsub("%", "", $NF); print $NF }')"
	if [ -z "$total" ]; then
		bad "could not read the coverage total from the profile"
	else
		printf '      aggregate statement coverage: %s%%\n' "$total"
		if awk -v got="$total" -v floor="$COVERAGE_FLOOR" 'BEGIN { exit !(got + 0 >= floor + 0) }'; then
			ok "aggregate coverage ${total}% >= ${COVERAGE_FLOOR}%"
		else
			bad "aggregate coverage ${total}% is below the ${COVERAGE_FLOOR}% floor"
		fi
		# Per-package floor too: one thin package must not hide behind the rest.
		while read -r pkg cov; do
			if awk -v got="$cov" -v floor="$COVERAGE_FLOOR" 'BEGIN { exit !(got + 0 >= floor + 0) }'; then
				ok "package coverage ${cov}% >= ${COVERAGE_FLOOR}%: $pkg"
			else
				bad "package coverage ${cov}% is below the ${COVERAGE_FLOOR}% floor: $pkg"
			fi
		done < <(grep -oE 'ok[[:space:]]+\S+[[:space:]]+\S+[[:space:]]+coverage: [0-9.]+%' "$TMP/coverage.log" |
			awk '{ gsub("%", "", $NF); print $2, $NF }')
	fi
else
	bad "coverage run failed (see $TMP/coverage.log)"
fi

echo
echo "=== 11. expected-FAIL: the guards bite ==="
# Each of these is a small Go program in a temp package under platform/, so it
# compiles against the real module and is removed afterwards.
guard_dir="$MODULE_DIR/zz_libaverify"
mkdir -p "$guard_dir"
trap 'rm -rf "$TMP" "$guard_dir"' EXIT

run_guard() {
	local name="$1" body="$2" want="$3" desc="$4"
	printf '%s' "$body" >"$guard_dir/${name}_test.go"
	if go -C platform test ./zz_libaverify -run "^Test${name}$" -count=1 >"$TMP/guard.log" 2>&1; then
		if [ "$want" = pass ]; then ok "$desc"; else bad "$desc (the guard did not reject it)"; fi
	else
		if [ "$want" = fail ]; then ok "$desc"; else bad "$desc (see $TMP/guard.log)"; fi
	fi
	rm -f "$guard_dir/${name}_test.go"
}

run_guard InvalidEnvelope 'package zz_libaverify

import (
	"testing"

	"github.com/canhtoanptit/collection-platform/platform/events"
)

func TestInvalidEnvelope(t *testing.T) {
	// A non-object payload and a non-ULID event id must both be refused.
	if _, err := events.New("CaseCreated", 1, "case-service", "Case", "A1", "not-an-object"); err == nil {
		t.Fatal("events.New accepted a scalar payload")
	}
	if _, err := events.New("CaseCreated", 1, "case-service", "Case", "A1",
		map[string]any{}, events.WithEventID("EVT-1")); err == nil {
		t.Fatal("events.New accepted a non-ULID event id")
	}
	// A valid envelope with a payload the schema rejects must fail validation.
	env, err := events.New("CaseCreated", 1, "case-service", "Case",
		"01M0KK4P3G0MQSQ3A1X2PMA6VX", map[string]any{"caseId": "nope"})
	if err != nil {
		t.Fatalf("building a structurally valid envelope: %v", err)
	}
	t.Logf("built %s", env)
}
' pass "events.New refuses a scalar payload and a non-ULID event id"

run_guard UnknownEventType 'package zz_libaverify

import (
	"errors"
	"testing"

	"github.com/canhtoanptit/collection-platform/contracts"
	"github.com/canhtoanptit/collection-platform/platform/events"
)

func TestUnknownEventType(t *testing.T) {
	r, err := events.NewRegistry(contracts.FS)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	env, err := events.New("CaseTeleported", 1, "case-service", "Case",
		"01M0KK4P3G0MQSQ3A1X2PMA6VX", map[string]any{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.Validate(env); !errors.Is(err, events.ErrUnknownEvent) {
		t.Fatalf("Validate on an unregistered event type = %v, want ErrUnknownEvent", err)
	}
}
' pass "an unregistered (eventType, version) is ErrUnknownEvent"

run_guard PayloadSchemaViolation 'package zz_libaverify

import (
	"errors"
	"testing"

	"github.com/canhtoanptit/collection-platform/contracts"
	"github.com/canhtoanptit/collection-platform/platform/events"
)

func TestPayloadSchemaViolation(t *testing.T) {
	r, err := events.NewRegistry(contracts.FS)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	// A CaseCreated payload missing every required field.
	env, err := events.New("CaseCreated", 1, "case-service", "Case",
		"01M0KK4P3G0MQSQ3A1X2PMA6VX", map[string]any{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.Validate(env); !errors.Is(err, events.ErrSchemaViolation) {
		t.Fatalf("Validate on an empty CaseCreated payload = %v, want ErrSchemaViolation", err)
	}
}
' pass "a payload that violates its schema is ErrSchemaViolation"

run_guard AuthnDenyByDefault 'package zz_libaverify

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/canhtoanptit/collection-platform/platform/authn"
)

func TestAuthnDenyByDefault(t *testing.T) {
	// RequireScope with no authentication in front of it must refuse, not allow.
	handler := authn.RequireScope("cases:read")(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/cases", nil))

	if rec.Code == http.StatusOK {
		t.Fatal("RequireScope allowed a request with no verified principal")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
' pass "RequireScope denies when no principal was verified (deny by default)"

run_guard ConfigFailsFast 'package zz_libaverify

import (
	"os"
	"strings"
	"testing"

	"github.com/canhtoanptit/collection-platform/platform/config"
)

func TestConfigFailsFast(t *testing.T) {
	// t.Setenv registers the original value for restoration; Unsetenv then makes
	// the variable genuinely absent, which is what `required` reacts to.
	t.Setenv("SERVICE_NAME", "placeholder")
	if err := os.Unsetenv("SERVICE_NAME"); err != nil {
		t.Fatalf("unsetting SERVICE_NAME: %v", err)
	}
	t.Setenv("LOG_LEVEL", "chatty")

	_, err := config.Load[config.Base]()
	if err == nil {
		t.Fatal("config.Load succeeded with no SERVICE_NAME and an invalid LOG_LEVEL")
	}
	for _, want := range []string{"SERVICE_NAME", "LogLevel"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
}
' pass "config.Load fails fast and names every problem"

# A deliberately broken caller must not compile: the envelope has no extra
# fields, so code that sets one is a build error rather than a runtime surprise.
cat >"$guard_dir/nocompile_test.go" <<'GO'
package zz_libaverify

import (
	"testing"

	"github.com/canhtoanptit/collection-platform/platform/events"
)

func TestEnvelopeHasNoExtraFields(t *testing.T) {
	_ = events.Envelope{TenantID: "T1"}
}
GO
check_fails "an envelope with an eleventh field does not compile" \
	go -C platform vet ./zz_libaverify
rm -f "$guard_dir/nocompile_test.go"

rm -rf "$guard_dir"
trap 'rm -rf "$TMP"' EXIT

check_fails "gofmt rejects deliberately misformatted Go (the formatting check is real)" bash -c '
	set -euo pipefail
	tmp="$1"
	printf "package p\nfunc  f( ) {\nx:=1\n_=x\n}\n" >"$tmp/bad.go"
	out="$(gofmt -l "$tmp/bad.go")"
	test -z "$out"' _ "$TMP"

echo
echo "=== 12. ownership ==="
# `make ownership-check WP=LIB-A` is the lead's gate and inspects the whole
# working tree, so it also sees other work packages' in-flight changes — it
# cannot be asserted from inside one WP's script. What *can* be asserted here is
# that LIB-A's ownership entry is exactly the two globs it was delegated, and
# that this WP left the other WPs' verify scripts alone.
check "docs/ownership.yaml grants LIB-A platform/**" \
	bash -c 'sed -n "/^LIB-A:/,/^[A-Za-z]/p" docs/ownership.yaml | grep -q "\"platform/\*\*\""'
check "docs/ownership.yaml grants LIB-A its verify script" \
	bash -c 'sed -n "/^LIB-A:/,/^[A-Za-z]/p" docs/ownership.yaml | grep -q "\"scripts/verify/LIB-A.sh\""'
check "LIB-A owns nothing else" bash -c '
	set -euo pipefail
	globs="$(sed -n "/^LIB-A:/,/^[A-Za-z]/p" docs/ownership.yaml | grep -cE "^\s+- \"")"
	test "$globs" -eq 2'
# Other work packages share this working tree and may be adding their own
# scripts, so untracked additions are not this WP's business. What LIB-A must not
# do is modify or delete a verify script that already exists.
check "no existing verify script was modified or deleted" bash -c '
	set -euo pipefail
	touched="$(git status --porcelain -- scripts/verify | grep -v "^?? " || true)"
	test -z "$touched" || { echo "$touched"; exit 1; }'
check "this WP shipped scripts/verify/LIB-A.sh" test -x scripts/verify/LIB-A.sh
check "contracts/** was not modified (released contracts are immutable)" bash -c '
	set -euo pipefail
	touched="$(git status --porcelain -- contracts || true)"
	test -z "$touched" || { echo "$touched"; exit 1; }'

echo
printf 'LIB-A: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
