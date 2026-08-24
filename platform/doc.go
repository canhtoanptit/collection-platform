// Package platform is the shared Go module for the collections platform:
// cross-cutting libraries consumed by every service.
//
// Built (LIB-A):
//
//	events       A§24 envelope + contracts-backed schema registry validation
//	ids          ULIDs, bare and operationally prefixed (FIL_ JOB_ REC_ COR_)
//	clock        injectable, UTC-enforcing clock
//	apierror     A§20 error contract + the correlation-id context key
//	httpkit      server kit (timeouts, graceful shutdown) + middleware chain
//	health       /healthz + /readyz
//	config       env-only configuration loading, fail-fast and fail-complete
//	otelkit      traces/metrics/logs + correlation propagation (A§97)
//	authn        OIDC JWT verification + deny-by-default scopes and groups
//	authn/authtest  fake OIDC issuer (RSA + JWKS + token minter) for tests
//
// Built (LIB-B):
//
//	postgres     pgx pool with otel query spans, WithTx, goose migrate under a
//	             session advisory lock, ReadyCheck
//	kafka        franz-go publisher (acks=all, idempotent, MSK IAM) and consumer
//	             group with per-partition ordering and DLQ semantics (A§27)
//
// Still to come:
//
//	outbox       transactional outbox + advisory-lock leader relay      (LIB-C)
//	inbox        consumer dedupe (processed_events)                     (LIB-C)
//	idempotency  Idempotency-Key store + HTTP middleware (A§21)         (LIB-C)
//	ruledsl      decision rule DSL evaluator (A§55)                     (LIB-C)
//	allocation   champion/challenger hash allocation (D§41)             (LIB-C)
//	modelclient  model contract client (A§56)                           (LIB-C)
//	testkit      testcontainers + contract-test helpers                 (LIB-C)
//
// # Dependency direction
//
// The built packages form a strict layering, so a service can adopt any of them
// without dragging in the rest:
//
//	ids, clock          no platform dependencies
//	events              -> ids, clock, contracts
//	apierror            -> ids
//	httpkit             -> apierror, ids
//	health, config      no platform dependencies
//	otelkit             -> httpkit (and so apierror)
//	authn               -> apierror, httpkit
//	postgres            -> health
//	kafka               -> events, otelkit, httpkit, health
//
// apierror owns the correlation-id context key because it is the lowest-level
// package that needs one — the A§20 body requires the field. httpkit re-exports
// the accessors under the names services use.
//
// postgres and kafka each depend on health rather than the other way round: the
// package that owns a dependency owns its readiness probe, so health never
// learns what a database or a broker is. kafka reaches into httpkit for the
// correlation-id accessors, because a consumer falls back to the envelope's
// correlationId when a producer set no header — the A§97 chain must not restart
// at the broker.
//
// Neither postgres nor kafka depends on the other, and neither knows anything
// about a domain. That is what lets LIB-C's outbox sit on top of both: it is the
// only package that needs a transaction and a publisher in the same sentence.
//
// # Integration tests
//
// postgres and kafka are the first packages here with tests that need Docker.
// Those tests live behind the `integration` build tag — the same convention
// makefiles/service.mk uses — so `go test ./...` and `make test-all` stay
// Docker-free and the container tests are an explicit opt-in:
//
//	go -C platform test ./... -race -count=1                  # unit only
//	go -C platform test ./... -tags integration -count=1      # + testcontainers
package platform
