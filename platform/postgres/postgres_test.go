package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/canhtoanptit/collection-platform/platform/config"
)

// TestConfigDefaults pins the documented environment contract. A service reads
// these variable names from its Deployment, so a rename here is a silent
// production change: the pool would quietly fall back to its default.
func TestConfigDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/colx")

	cfg, err := config.Load[Config]()
	if err != nil {
		t.Fatalf("loading postgres.Config from the environment: %v", err)
	}

	tests := []struct {
		field string
		got   any
		want  any
	}{
		{"URL", cfg.URL, "postgres://u:p@localhost:5432/colx"},
		{"MaxConns", cfg.MaxConns, int32(defaultMaxConns)},
		{"MinConns", cfg.MinConns, int32(defaultMinConns)},
		{"MaxConnLifetime", cfg.MaxConnLifetime, defaultMaxConnLifetime},
		{"MaxConnIdleTime", cfg.MaxConnIdleTime, defaultMaxConnIdleTime},
		{"ConnectTimeout", cfg.ConnectTimeout, defaultConnectTimeout},
		{"StatementTimeout", cfg.StatementTimeout, defaultStatementTimeout},
		{"ApplicationName", cfg.ApplicationName, ""},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("Config.%s = %v, want %v", tc.field, tc.got, tc.want)
		}
	}
}

// TestConfigRequiresDatabaseURL proves the `required` tag bites: a pod deployed
// without DATABASE_URL must fail at startup, not on its first query.
func TestConfigRequiresDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	if err := unsetenv(t, "DATABASE_URL"); err != nil {
		t.Fatal(err)
	}

	if _, err := config.Load[Config](); err == nil {
		t.Fatal("config.Load[postgres.Config] succeeded with no DATABASE_URL")
	} else if !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("the error does not name DATABASE_URL: %v", err)
	}
}

// TestBuildPoolConfig covers the mapping from Config to pgxpool.Config, which is
// where a cap is silently dropped if the wiring is wrong.
func TestBuildPoolConfig(t *testing.T) {
	const dsn = "postgres://user:secret@db.internal:5432/colx_case"

	tests := []struct {
		name   string
		cfg    Config
		assert func(t *testing.T, got poolFacts)
	}{
		{
			name: "caps from Config are applied",
			cfg: Config{
				URL: dsn, MaxConns: 25, MinConns: 5,
				MaxConnLifetime: time.Hour, MaxConnIdleTime: 2 * time.Minute,
				ConnectTimeout: 3 * time.Second, StatementTimeout: 7 * time.Second,
				ApplicationName: "case-service",
			},
			assert: func(t *testing.T, got poolFacts) {
				if got.maxConns != 25 || got.minConns != 5 {
					t.Errorf("conns = (%d, %d), want (25, 5)", got.maxConns, got.minConns)
				}
				if got.maxConnLifetime != time.Hour || got.maxConnIdleTime != 2*time.Minute {
					t.Errorf("lifetimes = (%v, %v)", got.maxConnLifetime, got.maxConnIdleTime)
				}
				if got.connectTimeout != 3*time.Second {
					t.Errorf("connectTimeout = %v, want 3s", got.connectTimeout)
				}
				if got.runtimeParams["statement_timeout"] != "7000" {
					t.Errorf("statement_timeout = %q, want 7000 (ms)", got.runtimeParams["statement_timeout"])
				}
				if got.runtimeParams["application_name"] != "case-service" {
					t.Errorf("application_name = %q", got.runtimeParams["application_name"])
				}
			},
		},
		{
			name: "zero values fall back to the documented defaults",
			cfg:  Config{URL: dsn},
			assert: func(t *testing.T, got poolFacts) {
				if got.maxConns != defaultMaxConns {
					t.Errorf("MaxConns = %d, want %d", got.maxConns, defaultMaxConns)
				}
				if got.minConns != defaultMinConns {
					t.Errorf("MinConns = %d, want %d", got.minConns, defaultMinConns)
				}
				if got.maxConnLifetime != defaultMaxConnLifetime {
					t.Errorf("MaxConnLifetime = %v, want %v", got.maxConnLifetime, defaultMaxConnLifetime)
				}
				if got.runtimeParams["statement_timeout"] != "30000" {
					t.Errorf("statement_timeout = %q, want 30000", got.runtimeParams["statement_timeout"])
				}
			},
		},
		{
			name: "an unset application name is not sent",
			cfg:  Config{URL: dsn},
			assert: func(t *testing.T, got poolFacts) {
				if v, ok := got.runtimeParams["application_name"]; ok {
					t.Errorf("application_name was sent as %q; it should be absent", v)
				}
			},
		},
		{
			name: "a negative MinConns keeps nothing warm",
			cfg:  Config{URL: dsn, MinConns: -1},
			assert: func(t *testing.T, got poolFacts) {
				if got.minConns != 0 {
					t.Errorf("MinConns = %d, want 0", got.minConns)
				}
			},
		},
		{
			name: "statement_timeout is omitted when explicitly disabled",
			cfg:  Config{URL: dsn, StatementTimeout: -1},
			assert: func(t *testing.T, got poolFacts) {
				if v, ok := got.runtimeParams["statement_timeout"]; ok {
					t.Errorf("statement_timeout was sent as %q; -1 disables it", v)
				}
			},
		},
		{
			name: "every pool gets the otel query tracer",
			cfg:  Config{URL: dsn},
			assert: func(t *testing.T, got poolFacts) {
				if !got.traced {
					t.Error("ConnConfig.Tracer is nil — queries would produce no spans")
				}
			},
		},
		{
			name: "pool_* parameters in the DSN do not win over Config",
			cfg:  Config{URL: dsn + "?pool_max_conns=99", MaxConns: 4},
			assert: func(t *testing.T, got poolFacts) {
				if got.maxConns != 4 {
					t.Errorf("MaxConns = %d, want 4 — the DSN must not override the environment", got.maxConns)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := buildPoolConfig(tc.cfg)
			if err != nil {
				t.Fatalf("buildPoolConfig: %v", err)
			}
			tc.assert(t, factsFrom(cfg))
		})
	}
}

func TestBuildPoolConfigRejects(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantMsg string
	}{
		{"no URL", Config{}, "DATABASE_URL"},
		{"unparseable DSN", Config{URL: "postgres://%zz"}, "parsing the Postgres DSN"},
		{
			name:    "MinConns above MaxConns",
			cfg:     Config{URL: "postgres://u@h/d", MaxConns: 2, MinConns: 9},
			wantMsg: "exceeds MaxConns",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildPoolConfig(tc.cfg)
			if err == nil {
				t.Fatal("buildPoolConfig accepted an invalid configuration")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error %q does not mention %q", err, tc.wantMsg)
			}
		})
	}
}

// TestConnectRejectsBadConfig proves Connect fails before it dials when the
// configuration cannot produce a pool. The error must not echo the DSN, which
// carries a password.
func TestConnectRejectsBadConfig(t *testing.T) {
	_, err := Connect(context.Background(), Config{URL: "postgres://u:hunter2@%zz"})
	if err == nil {
		t.Fatal("Connect accepted an unparseable DSN")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("the error leaks the DSN password: %v", err)
	}
}

func TestReadyCheck(t *testing.T) {
	tests := []struct {
		name      string
		pinger    Pinger
		wantError bool
	}{
		{"a reachable pool is ready", stubPinger{}, false},
		{"an unreachable server is not ready", stubPinger{err: errors.New("dial tcp: refused")}, true},
		{"a missing pool is not ready", nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			check := ReadyCheck(tc.pinger)
			if check.Name != "postgres" {
				t.Errorf("Check.Name = %q, want postgres", check.Name)
			}
			err := check.Probe(context.Background())
			if (err != nil) != tc.wantError {
				t.Fatalf("Probe error = %v, wantError %v", err, tc.wantError)
			}
		})
	}
}

// poolFacts is the subset of pgxpool.Config the mapping test asserts on, so the
// table stays readable and does not reach into pgx internals field by field.
type poolFacts struct {
	maxConns, minConns               int32
	maxConnLifetime, maxConnIdleTime time.Duration
	connectTimeout                   time.Duration
	runtimeParams                    map[string]string
	traced                           bool
}

func factsFrom(cfg *pgxpool.Config) poolFacts {
	return poolFacts{
		maxConns:        cfg.MaxConns,
		minConns:        cfg.MinConns,
		maxConnLifetime: cfg.MaxConnLifetime,
		maxConnIdleTime: cfg.MaxConnIdleTime,
		connectTimeout:  cfg.ConnConfig.ConnectTimeout,
		runtimeParams:   cfg.ConnConfig.RuntimeParams,
		traced:          cfg.ConnConfig.Tracer != nil,
	}
}

type stubPinger struct{ err error }

func (s stubPinger) Ping(context.Context) error { return s.err }

// unsetenv makes a variable genuinely absent for the duration of a test.
// t.Setenv("X", "") sets it to the empty string, which caarlos0/env treats as
// present-and-empty rather than missing, so `required` would not fire.
func unsetenv(t *testing.T, key string) error {
	t.Helper()
	if err := os.Unsetenv(key); err != nil {
		return fmt.Errorf("unsetting %s: %w", key, err)
	}
	return nil
}
