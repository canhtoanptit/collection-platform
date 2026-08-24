package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	// Registers the "pgx" database/sql driver. goose is a database/sql
	// consumer, so the migration path needs the stdlib shim even though every
	// query in a service goes through pgx natively.
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

const (
	// driverName is the database/sql driver registered by pgx/v5/stdlib.
	driverName = "pgx"

	// migrateMaxOpenConns is deliberately 2, not 1. goose runs the whole
	// migration set on one *sql.Conn it pins for the advisory lock; with a
	// single-connection pool anything else that wanted a connection during the
	// run would deadlock against the lock holder, and goose refuses that
	// configuration outright for Go migrations.
	migrateMaxOpenConns = 2

	// lockProbeSeconds and lockProbeAttempts bound the wait for the advisory
	// lock: poll once a second for up to five minutes. The default is a 5s
	// poll, which makes a rolling Deployment's second replica wait up to five
	// seconds for a lock that is already free.
	lockProbeSeconds  = 1
	lockProbeAttempts = 300

	// migrationSuffix is the extension goose reads. Checked before handing the
	// FS over so "no migrations found" is reported against the path the caller
	// passed rather than as a confusing version mismatch later.
	migrationSuffix = ".sql"
)

// Migrate applies every pending migration in fsys under dir, in order, holding
// a Postgres session advisory lock for the whole run.
//
// The lock is what makes this safe to call from a service's own startup or from
// a Kubernetes Job with more than one replica: concurrent callers serialise on
// `pg_advisory_lock`, so exactly one applies each migration and the others
// return success having found nothing to do (goose's own
// lock.NewPostgresSessionLocker — a session-scoped lock on the single pinned
// connection, released with a detached context so a cancelled caller still
// unlocks).
//
// Out-of-order migrations are refused (goose's WithAllowOutofOrder(false),
// stated explicitly rather than left to the default): a migration that appears
// with a version below the current one means two branches merged without
// rebasing, and applying it silently would make two environments' schemas
// diverge while both reported the same version. CLAUDE.md §7 has the
// corollary — a merged migration file is frozen, so the fix is always a new
// file with a higher version.
//
// databaseURL is a DSN, not a Config: migrations run from `server migrate up`
// and from Jobs that have no pool, and they must not inherit a service's pool
// caps. fsys is normally the service's embedded FS:
//
//	//go:embed migrations/*.sql
//	var migrationsFS embed.FS
//
//	postgres.Migrate(ctx, cfg.DatabaseURL, migrationsFS, "migrations")
func Migrate(ctx context.Context, databaseURL string, fsys fs.FS, dir string) error {
	return withProvider(ctx, databaseURL, fsys, dir, func(ctx context.Context, p *goose.Provider) error {
		results, err := p.Up(ctx)
		if err != nil {
			return fmt.Errorf("applying migrations: %w", err)
		}
		version, err := p.GetDBVersion(ctx)
		if err != nil {
			return fmt.Errorf("reading the schema version after migrating: %w", err)
		}
		slog.Default().InfoContext(ctx, "migrations applied",
			slog.Int("applied", len(results)),
			slog.Int64("version", version))
		return nil
	})
}

// MigrateDownTo rolls the schema back to version, applying each migration's
// Down section in reverse order under the same advisory lock. Version 0 removes
// everything the migration set created.
//
// It exists for tests and local development. Never run it against a shared
// environment: CLAUDE.md §7 freezes merged migrations, and rolling back a
// migration other pods are already relying on is an outage, not a rollback. The
// production answer to a bad migration is a new migration.
func MigrateDownTo(ctx context.Context, databaseURL string, fsys fs.FS, dir string, version int64) error {
	if version < 0 {
		return fmt.Errorf("rolling back migrations: target version %d is negative", version)
	}
	return withProvider(ctx, databaseURL, fsys, dir, func(ctx context.Context, p *goose.Provider) error {
		if _, err := p.DownTo(ctx, version); err != nil {
			return fmt.Errorf("rolling migrations back to version %d: %w", version, err)
		}
		return nil
	})
}

// Version reports the schema version currently recorded in goose_db_version, or
// 0 when no migration has been applied. It is the assertion a deployment gate
// and an integration test both want: "the schema this code needs is the schema
// that is there".
func Version(ctx context.Context, databaseURL string, fsys fs.FS, dir string) (int64, error) {
	var version int64
	err := withProvider(ctx, databaseURL, fsys, dir, func(ctx context.Context, p *goose.Provider) error {
		v, err := p.GetDBVersion(ctx)
		if err != nil {
			return fmt.Errorf("reading the schema version: %w", err)
		}
		version = v
		return nil
	})
	return version, err
}

// withProvider opens a short-lived database/sql pool, builds the goose provider
// the three exported functions share, and closes everything afterwards.
func withProvider(
	ctx context.Context,
	databaseURL string,
	fsys fs.FS,
	dir string,
	fn func(context.Context, *goose.Provider) error,
) error {
	if databaseURL == "" {
		return errors.New("migrating: no database URL configured")
	}
	migrations, err := migrationsFS(fsys, dir)
	if err != nil {
		return err
	}

	db, err := sql.Open(driverName, databaseURL)
	if err != nil {
		return fmt.Errorf("opening a migration connection: %w", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(migrateMaxOpenConns)
	db.SetMaxIdleConns(1)
	// A migration connection is used once and discarded; leaving it in the pool
	// for the lifetime of a Job process serves nobody.
	db.SetConnMaxLifetime(10 * time.Minute)

	locker, err := lock.NewPostgresSessionLocker(
		lock.WithLockTimeout(lockProbeSeconds, lockProbeAttempts),
	)
	if err != nil {
		return fmt.Errorf("configuring the migration advisory lock: %w", err)
	}

	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations,
		goose.WithSessionLocker(locker),
		goose.WithAllowOutofOrder(false),
		// The global registry is package-level mutable state shared with any
		// other goose user in the process. Disabling it keeps two services'
		// migration sets — or a test's fixtures and a service's real set —
		// from leaking into each other.
		goose.WithDisableGlobalRegistry(true),
		goose.WithSlog(slog.Default()),
	)
	if err != nil {
		return fmt.Errorf("preparing the migration provider: %w", err)
	}
	return fn(ctx, provider)
}

// migrationsFS narrows fsys to dir and proves it holds at least one migration.
//
// The emptiness check is not pedantry: an embed pattern that does not match
// (a renamed directory, a service whose migrations moved) yields a perfectly
// valid empty FS, and goose would then happily report "0 migrations applied,
// version 0" — a green deployment onto an empty schema.
func migrationsFS(fsys fs.FS, dir string) (fs.FS, error) {
	if fsys == nil {
		return nil, errors.New("migrating: no migrations FS supplied")
	}

	sub := fsys
	if dir != "" && dir != "." {
		var err error
		if sub, err = fs.Sub(fsys, dir); err != nil {
			return nil, fmt.Errorf("migrating: reading migrations from %q: %w", dir, err)
		}
	}

	entries, err := fs.Glob(sub, "*"+migrationSuffix)
	if err != nil {
		return nil, fmt.Errorf("migrating: listing migrations in %q: %w", dir, err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf(
			"migrating: no %s migrations found in %q — check the //go:embed pattern",
			migrationSuffix, dir)
	}
	return sub, nil
}
