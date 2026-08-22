// Package platform is the shared Go module for the collections platform:
// cross-cutting libraries consumed by every service.
//
// Packages (built in Phase 3, LIB-1..9):
//
//	events       A§24 envelope + schema registry validation
//	outbox       transactional outbox + advisory-lock leader relay
//	inbox        consumer dedupe (processed_events)
//	idempotency  Idempotency-Key store + HTTP middleware (A§21)
//	kafka        franz-go publisher/consumer with DLQ semantics (A§27)
//	postgres     pgx pool, WithTx, goose migrate
//	otelkit      traces/metrics/logs + correlation propagation (A§97)
//	httpkit      server kit + middleware chain
//	apierror     A§20 error contract
//	authn        OIDC JWT validation + scopes
//	config       env-only config loading
//	health       /healthz + /readyz
//	ids          ULIDs
//	clock        injectable clock
//	ruledsl      decision rule DSL evaluator (A§55)
//	allocation   champion/challenger hash allocation (D§41)
//	modelclient  model contract client (A§56)
//	testkit      testcontainers + contract-test helpers
package platform
