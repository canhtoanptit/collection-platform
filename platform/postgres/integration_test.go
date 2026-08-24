//go:build integration

// Integration tests for platform/postgres. They need Docker and are gated
// behind the `integration` build tag, which is the same convention
// makefiles/service.mk uses (`go test -tags integration`), so a plain
// `go test ./...` — and therefore `make test-all` — stays Docker-free.
//
//	go -C platform test ./postgres/... -tags integration -count=1
package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

const (
	// postgresImage matches ADR-0003's engine major version. Pinned, and alpine
	// so a cold CI runner pulls ~90MB rather than ~450MB.
	postgresImage = "postgres:16-alpine"

	// containerStartTimeout covers an image pull on a cold cache.
	containerStartTimeout = 5 * time.Minute
	// fixtureVersion is the version of the last fixture migration.
	fixtureVersion = 2
)

var errRollbackMe = errors.New("this unit of work must not persist")

// startPostgres boots one container and returns its DSN. It is per-test rather
// than per-package: a shared server would let one test's schema leak into
// another's assertions, and the container starts in about a second once the
// image is local.
//
// This helper stays unexported. The shared testcontainers helpers services will
// use (platform/testkit) belong to LIB-C; duplicating six lines here is cheaper
// than two work packages editing one file.
func startPostgres(t *testing.T) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), containerStartTimeout)
	defer cancel()

	container, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("colx_test"),
		tcpostgres.WithUsername("colx"),
		tcpostgres.WithPassword("colx"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("starting %s: %v", postgresImage, err)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Logf("terminating the Postgres container: %v", err)
		}
	})

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("reading the container connection string: %v", err)
	}
	return dsn
}

func connectPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()

	pool, err := Connect(context.Background(), Config{
		URL: dsn, MaxConns: 4, MinConns: 1, ApplicationName: "platform-postgres-test",
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestIntegrationMigrateUpDownUp is the LIB-5 acceptance: up, assert the
// recorded version, all the way down, and up again with the same result. The
// second up proves the migration set is replayable — which is what makes a
// rebuilt dev environment (and a restored PITR snapshot) trustworthy.
func TestIntegrationMigrateUpDownUp(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	before, err := Version(ctx, dsn, fixtureMigrations, fixtureDir)
	if err != nil {
		t.Fatalf("reading the version of a fresh database: %v", err)
	}
	if before != 0 {
		t.Fatalf("a fresh database reports version %d, want 0", before)
	}

	assertUp := func(stage string) {
		t.Helper()
		if err := Migrate(ctx, dsn, fixtureMigrations, fixtureDir); err != nil {
			t.Fatalf("%s: Migrate: %v", stage, err)
		}
		version, err := Version(ctx, dsn, fixtureMigrations, fixtureDir)
		if err != nil {
			t.Fatalf("%s: Version: %v", stage, err)
		}
		if version != fixtureVersion {
			t.Fatalf("%s: version = %d, want %d", stage, version, fixtureVersion)
		}
		// The version table is bookkeeping; the schema is the fact. Assert the
		// column the second migration adds actually exists.
		assertColumnExists(t, dsn, "widget", "note")
	}

	assertUp("first up")

	// Idempotent: a second Migrate on an up-to-date schema is a no-op success.
	// This is the case every replica of a Deployment hits on every rollout.
	if err := Migrate(ctx, dsn, fixtureMigrations, fixtureDir); err != nil {
		t.Fatalf("re-running Migrate on an up-to-date schema: %v", err)
	}

	if err := MigrateDownTo(ctx, dsn, fixtureMigrations, fixtureDir, 0); err != nil {
		t.Fatalf("MigrateDownTo(0): %v", err)
	}
	after, err := Version(ctx, dsn, fixtureMigrations, fixtureDir)
	if err != nil {
		t.Fatalf("Version after rollback: %v", err)
	}
	if after != 0 {
		t.Fatalf("version after rolling back to 0 = %d, want 0", after)
	}
	if exists := tableExists(t, dsn, "widget"); exists {
		t.Fatal("the widget table survived a full rollback")
	}

	assertUp("second up")
}

// TestIntegrationMigrateConcurrent is the advisory-lock proof. Every replica of a
// Deployment runs migrations on startup, so the interesting case is not "does it
// work" but "does it work when eight of them start at the same second".
//
// All callers must return success, exactly one migration row per version must
// exist, and the schema must be the same as after a single run.
func TestIntegrationMigrateConcurrent(t *testing.T) {
	dsn := startPostgres(t)

	const replicas = 8

	// A barrier, so the goroutines contend rather than politely queue.
	var ready, done sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, replicas)

	ready.Add(replicas)
	done.Add(replicas)
	for i := range replicas {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			errs[i] = Migrate(context.Background(), dsn, fixtureMigrations, fixtureDir)
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("replica %d: Migrate: %v", i, err)
		}
	}
	if t.Failed() {
		t.FailNow()
	}

	// One row per migration, not eight: the lock serialised the runs rather than
	// letting them all apply the same file.
	var rows int
	queryRow(t, dsn, "SELECT count(*) FROM goose_db_version WHERE version_id > 0", &rows)
	if rows != fixtureVersion {
		t.Errorf("goose_db_version holds %d applied migrations, want %d", rows, fixtureVersion)
	}
	assertColumnExists(t, dsn, "widget", "note")
}

// TestIntegrationWithTx is the transaction-boundary acceptance against a real
// server: commit persists, a returned error rolls back, and a panic rolls back
// and still panics. Only the third case needs a real server to be convincing —
// the deferred rollback has to reach the wire, not just a counter.
func TestIntegrationWithTx(t *testing.T) {
	dsn := startPostgres(t)
	if err := Migrate(context.Background(), dsn, fixtureMigrations, fixtureDir); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pool := connectPool(t, dsn)

	insert := func(id string) func(pgx.Tx) error {
		return func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(),
				`INSERT INTO widget (id, name, amount_minor, currency) VALUES ($1, $2, $3, $4)`,
				id, "widget "+id, int64(50000), "EUR")
			return err
		}
	}

	tests := []struct {
		name        string
		id          string
		fn          func(pgx.Tx) error
		wantPanic   bool
		wantErr     error
		wantPersist bool
	}{
		{
			name:        "commit persists the row",
			id:          "W_COMMIT",
			fn:          insert("W_COMMIT"),
			wantPersist: true,
		},
		{
			name: "an error rolls the row back",
			id:   "W_ERROR",
			fn: func(tx pgx.Tx) error {
				if err := insert("W_ERROR")(tx); err != nil {
					return err
				}
				return errRollbackMe
			},
			wantErr: errRollbackMe,
		},
		{
			name: "a panic rolls the row back and re-panics",
			id:   "W_PANIC",
			fn: func(tx pgx.Tx) error {
				if err := insert("W_PANIC")(tx); err != nil {
					t.Errorf("inserting before the panic: %v", err)
				}
				panic("the handler exploded")
			},
			wantPanic: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			run := func() error { return WithTx(context.Background(), pool, tc.fn) }

			switch {
			case tc.wantPanic:
				func() {
					defer func() {
						if recover() == nil {
							t.Error("WithTx swallowed the panic")
						}
					}()
					_ = run()
				}()
			case tc.wantErr != nil:
				if err := run(); !errors.Is(err, tc.wantErr) {
					t.Fatalf("WithTx error = %v, want %v", err, tc.wantErr)
				}
			default:
				if err := run(); err != nil {
					t.Fatalf("WithTx: %v", err)
				}
			}

			var found int
			queryRow(t, dsn, fmt.Sprintf("SELECT count(*) FROM widget WHERE id = '%s'", tc.id), &found)
			want := 0
			if tc.wantPersist {
				want = 1
			}
			if found != want {
				t.Fatalf("rows with id %s = %d, want %d", tc.id, found, want)
			}
		})
	}

	// The pool must still be usable after a rollback and a panic: a leaked
	// transaction would show up here as a hang or a "conn busy" error.
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("the pool is unusable after a rollback and a panic: %v", err)
	}
	var open int
	queryRow(t, dsn,
		`SELECT count(*) FROM pg_stat_activity WHERE state = 'idle in transaction'`, &open)
	if open != 0 {
		t.Errorf("%d sessions are idle in transaction — a rollback did not reach the server", open)
	}
}

// TestIntegrationReadyCheck asserts both readiness states that matter in
// practice: a live pool is ready, and a closed one is not. Stopping the
// container to observe the transition costs a container restart for the same
// signal, so the closed-pool case stands in for it (the brief allows this).
func TestIntegrationReadyCheck(t *testing.T) {
	dsn := startPostgres(t)

	pool, err := Connect(context.Background(), Config{URL: dsn, MaxConns: 2, MinConns: 1})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	check := ReadyCheck(pool)
	if err := check.Probe(context.Background()); err != nil {
		t.Fatalf("a live pool reports not ready: %v", err)
	}

	pool.Close()
	if err := check.Probe(context.Background()); err == nil {
		t.Fatal("a closed pool reports ready — /readyz would keep the pod in the Service")
	}
}

// TestIntegrationConnectAppliesPoolCaps proves the caps and runtime parameters
// reach the server rather than merely populating a struct.
func TestIntegrationConnectAppliesPoolCaps(t *testing.T) {
	dsn := startPostgres(t)

	pool, err := Connect(context.Background(), Config{
		URL: dsn, MaxConns: 3, MinConns: 1,
		StatementTimeout: 1234 * time.Millisecond,
		ApplicationName:  "colx-cap-test",
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(pool.Close)

	if got := pool.Config().MaxConns; got != 3 {
		t.Errorf("pool MaxConns = %d, want 3", got)
	}

	ctx := context.Background()
	var statementTimeout, applicationName string
	if err := pool.QueryRow(ctx, "SHOW statement_timeout").Scan(&statementTimeout); err != nil {
		t.Fatalf("reading statement_timeout: %v", err)
	}
	if statementTimeout != "1234ms" {
		t.Errorf("statement_timeout = %q, want 1234ms", statementTimeout)
	}
	if err := pool.QueryRow(ctx, "SHOW application_name").Scan(&applicationName); err != nil {
		t.Fatalf("reading application_name: %v", err)
	}
	if applicationName != "colx-cap-test" {
		t.Errorf("application_name = %q, want colx-cap-test", applicationName)
	}
}

// --- assertions -------------------------------------------------------------

// queryRow runs a scalar query on a throwaway connection, so an assertion can
// never be answered from inside a transaction the test is examining.
func queryRow(t *testing.T, dsn, sql string, dest any) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting to assert %q: %v", sql, err)
	}
	defer func() { _ = conn.Close(ctx) }()

	if err := conn.QueryRow(ctx, sql).Scan(dest); err != nil {
		t.Fatalf("querying %q: %v", sql, err)
	}
}

func tableExists(t *testing.T, dsn, table string) bool {
	t.Helper()
	var exists bool
	queryRow(t, dsn, fmt.Sprintf(
		`SELECT to_regclass('public.%s') IS NOT NULL`, table), &exists)
	return exists
}

func assertColumnExists(t *testing.T, dsn, table, column string) {
	t.Helper()
	var exists bool
	queryRow(t, dsn, fmt.Sprintf(
		`SELECT EXISTS (SELECT 1 FROM information_schema.columns
		                WHERE table_name = '%s' AND column_name = '%s')`, table, column), &exists)
	if !exists {
		t.Fatalf("column %s.%s is missing — the schema does not match the recorded version", table, column)
	}
}
