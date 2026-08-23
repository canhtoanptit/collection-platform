// Package config loads a service's configuration from the environment, once, at
// startup, and fails fast.
//
// The convention, and the reason this package is three functions rather than a
// framework:
//
//   - **Environment only.** No files, no flags, no remote config service, no
//     defaults invented at the point of use. A pod's configuration is what its
//     Deployment and its ExternalSecrets say it is, and it is fully visible in
//     `kubectl describe`.
//   - **Loaded once in main, then passed down.** Nothing re-reads os.Getenv
//     later, so configuration cannot change under a running request and a test
//     can construct any configuration it likes by hand.
//   - **Fail fast, and fail complete.** A missing or malformed variable aborts
//     startup before the server binds — a service that starts without its
//     database URL only fails later, in front of a caller. Load reports *every*
//     problem in one error, because fixing five variables through five restarts
//     of a Kubernetes deployment is a bad afternoon.
//
// Secrets arrive the same way as everything else: as environment variables
// projected from an ExternalSecret referencing `colx/dev/*` (ADR-0011). Nothing
// in this package logs a value.
package config

import (
	"fmt"
	"log/slog"

	"github.com/caarlos0/env/v11"
)

// Base is the configuration every service shares. Embed it and add the
// service's own fields:
//
//	type Config struct {
//	    config.Base
//	    AgencyAPIURL string `env:"AGENCY_API_URL,required"`
//	}
//
// Only ServiceName is required here: a service without a name cannot label its
// telemetry. Everything else is optional at this level because not every
// deployable needs it — a CronJob has no HTTP address, the simulator has no
// issuer — and a service that *does* need one re-declares the field as
// `required` in its own struct.
type Base struct {
	// ServiceName is the deployable's name in kebab-case. It becomes the OTel
	// service.name, the log field `service`, the Kafka consumer group and the
	// event envelope's producer, so it must match the deployment.
	ServiceName string `env:"SERVICE_NAME,required"`
	// Env is the environment name (dev, staging, prod) reported as
	// deployment.environment on every span and metric.
	Env string `env:"ENVIRONMENT" envDefault:"dev"`

	// HTTPAddr is the listen address of the service's API.
	HTTPAddr string `env:"HTTP_ADDR" envDefault:":8080"`
	// MetricsAddr is the listen address of the Prometheus endpoint. It is a
	// separate port so the metrics endpoint is never exposed through an ingress
	// that fronts the API.
	MetricsAddr string `env:"METRICS_ADDR" envDefault:":9090"`

	// DatabaseURL is the service's own Postgres DSN. One database per service:
	// no service ever reads another's (CLAUDE.md §7).
	DatabaseURL string `env:"DATABASE_URL"`
	// KafkaBrokers is the bootstrap broker list, comma separated.
	KafkaBrokers []string `env:"KAFKA_BROKERS" envSeparator:","`

	// OTLPEndpoint is the OTLP gRPC collector address. Empty disables trace
	// export, which is what unit tests and local runs want. The name is the
	// OpenTelemetry standard variable, so the SDK and this struct never
	// disagree.
	OTLPEndpoint string `env:"OTEL_EXPORTER_OTLP_ENDPOINT"`

	// OIDCIssuer is the Cognito user-pool issuer URL used for JWKS discovery.
	OIDCIssuer string `env:"OIDC_ISSUER"`
	// OIDCAudience is the audience (or Cognito client_id) a token must carry.
	OIDCAudience string `env:"OIDC_AUDIENCE"`

	// LogLevel is the minimum slog level. Typed rather than a string so an
	// unparseable value is reported by Load alongside every other problem
	// instead of surfacing later as a silent fallback.
	LogLevel slog.Level `env:"LOG_LEVEL" envDefault:"info"`
}

// Load reads T from the environment.
//
// T must be a struct whose fields carry `env:` tags. The returned error names
// every variable that is missing or unparseable — not just the first — and
// wraps env.AggregateError, so a caller that wants the individual problems can
// reach them with errors.As.
func Load[T any]() (T, error) {
	var cfg T
	if err := env.Parse(&cfg); err != nil {
		var zero T
		return zero, fmt.Errorf("loading configuration from the environment: %w", err)
	}
	return cfg, nil
}

// LoadInto fills an existing struct, for the case where a caller has already
// built one (a test, or a command that layers flags over the environment).
func LoadInto(cfg any) error {
	if err := env.Parse(cfg); err != nil {
		return fmt.Errorf("loading configuration from the environment: %w", err)
	}
	return nil
}
