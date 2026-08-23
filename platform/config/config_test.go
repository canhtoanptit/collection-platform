package config_test

import (
	"errors"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/caarlos0/env/v11"

	"github.com/canhtoanptit/collection-platform/platform/config"
)

// clearEnv unsets every variable Base reads, so a test starts from a known
// environment whatever the developer has exported. t.Setenv restores it.
func clearEnv(t *testing.T) {
	t.Helper()

	for _, k := range []string{
		"SERVICE_NAME", "ENVIRONMENT", "HTTP_ADDR", "METRICS_ADDR",
		"DATABASE_URL", "KAFKA_BROKERS", "OTEL_EXPORTER_OTLP_ENDPOINT",
		"OIDC_ISSUER", "OIDC_AUDIENCE", "LOG_LEVEL",
	} {
		t.Setenv(k, "")
		if err := unset(k); err != nil {
			t.Fatalf("unsetting %s: %v", k, err)
		}
	}
}

func TestLoadBaseDefaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("SERVICE_NAME", "case-service")

	got, err := config.Load[config.Base]()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := config.Base{
		ServiceName: "case-service",
		Env:         "dev",
		HTTPAddr:    ":8080",
		MetricsAddr: ":9090",
		LogLevel:    slog.LevelInfo,
	}
	if got.ServiceName != want.ServiceName {
		t.Errorf("ServiceName = %q, want %q", got.ServiceName, want.ServiceName)
	}
	if got.Env != want.Env {
		t.Errorf("Env = %q, want %q", got.Env, want.Env)
	}
	if got.HTTPAddr != want.HTTPAddr {
		t.Errorf("HTTPAddr = %q, want %q", got.HTTPAddr, want.HTTPAddr)
	}
	if got.MetricsAddr != want.MetricsAddr {
		t.Errorf("MetricsAddr = %q, want %q", got.MetricsAddr, want.MetricsAddr)
	}
	if got.LogLevel != want.LogLevel {
		t.Errorf("LogLevel = %v, want %v", got.LogLevel, want.LogLevel)
	}
	// Optional values stay empty rather than being invented.
	if got.DatabaseURL != "" || got.OTLPEndpoint != "" || got.OIDCIssuer != "" || got.OIDCAudience != "" {
		t.Errorf("optional fields were given defaults: %+v", got)
	}
	if len(got.KafkaBrokers) != 0 {
		t.Errorf("KafkaBrokers = %v, want empty", got.KafkaBrokers)
	}
}

func TestLoadBaseFromTheEnvironment(t *testing.T) {
	clearEnv(t)

	env := map[string]string{
		"SERVICE_NAME":                "arrangement-service",
		"ENVIRONMENT":                 "staging",
		"HTTP_ADDR":                   "127.0.0.1:9000",
		"METRICS_ADDR":                "127.0.0.1:9999",
		"DATABASE_URL":                "postgres://colx@db:5432/arrangement?sslmode=require",
		"KAFKA_BROKERS":               "b-1:9098,b-2:9098,b-3:9098",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "otel-collector:4317",
		"OIDC_ISSUER":                 "https://keycloak.colx-dev.internal/realms/colx",
		"OIDC_AUDIENCE":               "colx-api",
		"LOG_LEVEL":                   "debug",
	}
	for k, v := range env {
		t.Setenv(k, v)
	}

	got, err := config.Load[config.Base]()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got.ServiceName != "arrangement-service" || got.Env != "staging" {
		t.Errorf("identity = %q/%q", got.ServiceName, got.Env)
	}
	if got.HTTPAddr != "127.0.0.1:9000" || got.MetricsAddr != "127.0.0.1:9999" {
		t.Errorf("addresses = %q/%q", got.HTTPAddr, got.MetricsAddr)
	}
	if got.DatabaseURL != env["DATABASE_URL"] {
		t.Errorf("DatabaseURL = %q", got.DatabaseURL)
	}
	if want := []string{"b-1:9098", "b-2:9098", "b-3:9098"}; !slices.Equal(got.KafkaBrokers, want) {
		t.Errorf("KafkaBrokers = %v, want %v", got.KafkaBrokers, want)
	}
	if got.OTLPEndpoint != "otel-collector:4317" {
		t.Errorf("OTLPEndpoint = %q", got.OTLPEndpoint)
	}
	if got.OIDCIssuer != env["OIDC_ISSUER"] || got.OIDCAudience != "colx-api" {
		t.Errorf("OIDC = %q/%q", got.OIDCIssuer, got.OIDCAudience)
	}
	if got.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, want debug", got.LogLevel)
	}
}

func TestLogLevelParsing(t *testing.T) {
	tests := []struct {
		value   string
		want    slog.Level
		wantErr bool
	}{
		{"debug", slog.LevelDebug, false},
		{"info", slog.LevelInfo, false},
		{"INFO", slog.LevelInfo, false},
		{"warn", slog.LevelWarn, false},
		{"error", slog.LevelError, false},
		{"DEBUG+2", slog.LevelDebug + 2, false},
		{"verbose", 0, true},
		{"trace", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.value, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("SERVICE_NAME", "case-service")
			t.Setenv("LOG_LEVEL", tc.value)

			got, err := config.Load[config.Base]()
			switch {
			case tc.wantErr && err == nil:
				t.Fatalf("Load accepted LOG_LEVEL=%q", tc.value)
			case tc.wantErr:
				if !strings.Contains(err.Error(), "LogLevel") {
					t.Errorf("error = %q, want it to name the offending field", err)
				}
			case err != nil:
				t.Fatalf("Load: %v", err)
			case got.LogLevel != tc.want:
				t.Errorf("LogLevel = %v, want %v", got.LogLevel, tc.want)
			}
		})
	}
}

// TestLoadReportsEveryProblemAtOnce is the fail-fast-and-fail-complete
// convention: five bad variables must take one restart to diagnose, not five.
func TestLoadReportsEveryProblemAtOnce(t *testing.T) {
	clearEnv(t)
	t.Setenv("LOG_LEVEL", "chatty")

	type serviceConfig struct {
		config.Base
		AgencyAPIURL string `env:"AGENCY_API_URL,required"`
		MaxRetries   int    `env:"MAX_RETRIES,required"`
		DryRun       bool   `env:"DRY_RUN,required"`
	}
	t.Setenv("MAX_RETRIES", "many")
	t.Setenv("DRY_RUN", "perhaps")

	_, err := config.Load[serviceConfig]()
	if err == nil {
		t.Fatal("Load succeeded with four broken variables")
	}

	var aggregate env.AggregateError
	if !errors.As(err, &aggregate) {
		t.Fatalf("error does not wrap env.AggregateError: %v", err)
	}
	if len(aggregate.Errors) < 4 {
		t.Errorf("aggregate reports %d problems, want at least 4: %v", len(aggregate.Errors), err)
	}
	// Required variables are named by their env key; unparseable values are
	// named by their struct field. Both must appear, or the operator has to
	// guess which of the two kinds of problem they are looking at.
	for _, want := range []string{"SERVICE_NAME", "AGENCY_API_URL", "MaxRetries", "DryRun", "LogLevel"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

func TestLoadReturnsTheZeroValueOnFailure(t *testing.T) {
	clearEnv(t)

	got, err := config.Load[config.Base]()
	if err == nil {
		t.Fatal("Load succeeded without SERVICE_NAME")
	}
	if !reflect.DeepEqual(got, config.Base{}) {
		t.Errorf("Load returned a partially populated config alongside the error: %+v", got)
	}
}

// TestLoadRejectsANonStruct guards the generic parameter: a caller who writes
// Load[string]() gets an error, not a mysterious zero value.
func TestLoadRejectsANonStruct(t *testing.T) {
	clearEnv(t)

	if _, err := config.Load[string](); err == nil {
		t.Error("Load[string]() succeeded")
	}
	if _, err := config.Load[map[string]string](); err == nil {
		t.Error("Load[map[string]string]() succeeded")
	}
}

func TestLoadIntoAnExistingStruct(t *testing.T) {
	clearEnv(t)
	t.Setenv("SERVICE_NAME", "case-service")
	t.Setenv("HTTP_ADDR", ":8081")

	// A value set before loading is overwritten by the environment when the
	// variable is present, and left alone when it is not.
	cfg := config.Base{HTTPAddr: ":1234", DatabaseURL: "postgres://preset"}
	if err := config.LoadInto(&cfg); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}
	if cfg.HTTPAddr != ":8081" {
		t.Errorf("HTTPAddr = %q, want the environment's :8081", cfg.HTTPAddr)
	}
	if cfg.DatabaseURL != "postgres://preset" {
		t.Errorf("DatabaseURL = %q, want the preset value preserved", cfg.DatabaseURL)
	}

	if err := config.LoadInto(config.Base{}); err == nil {
		t.Error("LoadInto accepted a non-pointer")
	}
}

// TestBaseTagsCoverEveryField catches the easy mistake of adding a field to Base
// and forgetting its env tag, which would leave it silently unconfigurable.
func TestBaseTagsCoverEveryField(t *testing.T) {
	clearEnv(t)
	t.Setenv("SERVICE_NAME", "case-service")

	fields := reflectFields(config.Base{})
	for _, f := range fields {
		if f.tag == "" {
			t.Errorf("Base.%s has no env tag", f.name)
		}
	}
	if len(fields) != 10 {
		t.Errorf("Base has %d fields, the documented set is 10 — update the docs and the verify script", len(fields))
	}
}
