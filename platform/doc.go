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
// Still to come:
//
//	postgres     pgx pool, WithTx, goose migrate                       (LIB-B)
//	kafka        franz-go publisher/consumer with DLQ semantics (A§27)  (LIB-B)
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
//
// apierror owns the correlation-id context key because it is the lowest-level
// package that needs one — the A§20 body requires the field. httpkit re-exports
// the accessors under the names services use.
package platform
