package postgres

import (
	"context"
	"embed"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// fixtureMigrations is the two-migration set the integration tests apply. It is
// embedded here rather than in the integration file so the unit tests can assert
// the FS plumbing without Docker.
//
//go:embed testdata/migrations/*.sql
var fixtureMigrations embed.FS

const (
	fixtureDir = "testdata/migrations"

	// migrateProbeTimeout bounds the "server is not there" test. It must be
	// comfortably longer than the DSN's connect_timeout so the failure that is
	// asserted is the connection refusal, not this deadline.
	migrateProbeTimeout = 20 * time.Second
)

// TestMigrationsFS covers the FS narrowing, and in particular the empty-set
// refusal: a //go:embed pattern that matches nothing produces a valid, empty FS,
// and goose would then report "version 0, nothing to do" — a green deployment
// onto an empty schema.
func TestMigrationsFS(t *testing.T) {
	tests := []struct {
		name     string
		dir      string
		wantErr  string
		wantHits int
	}{
		{name: "the fixture set resolves", dir: fixtureDir, wantHits: 2},
		{name: "a directory that does not exist", dir: "nope", wantErr: "no .sql migrations found"},
		{name: "a path that escapes the FS", dir: "../escape", wantErr: "reading migrations from"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sub, err := migrationsFS(fixtureMigrations, tc.dir)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("migrationsFS(%q) succeeded; want an error", tc.dir)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("migrationsFS(%q): %v", tc.dir, err)
			}
			entries, err := fs.Glob(sub, "*"+migrationSuffix)
			if err != nil {
				t.Fatalf("listing the narrowed FS: %v", err)
			}
			if len(entries) != tc.wantHits {
				t.Fatalf("found %d migrations (%v), want %d", len(entries), entries, tc.wantHits)
			}
		})
	}
}

func TestMigrationsFSRejectsEmptyAndMissing(t *testing.T) {
	tests := []struct {
		name    string
		fsys    fs.FS
		dir     string
		wantErr string
	}{
		{
			name:    "no FS at all",
			fsys:    nil,
			dir:     "migrations",
			wantErr: "no migrations FS supplied",
		},
		{
			name:    "an FS with no .sql files",
			fsys:    fstest.MapFS{"migrations/README.md": {Data: []byte("nothing here")}},
			dir:     "migrations",
			wantErr: "no .sql migrations found",
		},
		{
			name:    "an entirely empty FS at the root",
			fsys:    fstest.MapFS{},
			dir:     "",
			wantErr: "no .sql migrations found",
		},
		{
			name:    "a root-level migration set with dir given as .",
			fsys:    fstest.MapFS{"00001_x.sql": {Data: []byte("-- +goose Up\nSELECT 1;\n")}},
			dir:     ".",
			wantErr: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := migrationsFS(tc.fsys, tc.dir)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("migrationsFS: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("migrationsFS accepted an FS with no migrations")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

// TestMigrateRejectsBadInput proves the three exported entry points fail before
// they dial. A missing DSN or an unmatched embed pattern must be an immediate,
// named error rather than a connection attempt to nowhere.
func TestMigrateRejectsBadInput(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		run     func() error
		wantErr string
	}{
		{
			name:    "Migrate with no database URL",
			run:     func() error { return Migrate(ctx, "", fixtureMigrations, fixtureDir) },
			wantErr: "no database URL configured",
		},
		{
			name:    "Migrate with no FS",
			run:     func() error { return Migrate(ctx, "postgres://u@h/d", nil, fixtureDir) },
			wantErr: "no migrations FS supplied",
		},
		{
			name:    "Migrate with an unmatched directory",
			run:     func() error { return Migrate(ctx, "postgres://u@h/d", fixtureMigrations, "sql") },
			wantErr: "no .sql migrations found",
		},
		{
			name: "MigrateDownTo with a negative target",
			run: func() error {
				return MigrateDownTo(ctx, "postgres://u@h/d", fixtureMigrations, fixtureDir, -1)
			},
			wantErr: "is negative",
		},
		{
			name: "MigrateDownTo with no database URL",
			run: func() error {
				return MigrateDownTo(ctx, "", fixtureMigrations, fixtureDir, 0)
			},
			wantErr: "no database URL configured",
		},
		{
			name: "Version with no database URL",
			run: func() error {
				_, err := Version(ctx, "", fixtureMigrations, fixtureDir)
				return err
			},
			wantErr: "no database URL configured",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil {
				t.Fatal("the call succeeded; want an error before any connection attempt")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

// TestMigrateReportsAnUnreachableServer proves the DSN reaches the driver: an
// address nothing listens on must surface as a connection error, not a panic and
// not a silent success.
func TestMigrateReportsAnUnreachableServer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), migrateProbeTimeout)
	defer cancel()

	// Port 1 on the loopback interface: reserved, and nothing binds it.
	err := Migrate(ctx, "postgres://nobody@127.0.0.1:1/colx?connect_timeout=1", fixtureMigrations, fixtureDir)
	if err == nil {
		t.Fatal("Migrate succeeded against an address nothing listens on")
	}
}
