package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

var errHandler = errors.New("the unit of work failed")

// TestWithTxOutcomes is the state × outcome table for the transaction boundary.
// Every row asserts what the caller sees *and* what happened to the transaction,
// because "returned an error" and "rolled back" are different claims and only
// the second one protects the outbox invariant.
func TestWithTxOutcomes(t *testing.T) {
	tests := []struct {
		name         string
		fn           func(pgx.Tx) error
		beginErr     error
		commitErr    error
		wantErr      error
		wantErrMsg   string
		wantCommits  int
		wantRollback int
		wantPanic    bool
	}{
		{
			name:        "success commits once and never rolls back",
			fn:          func(pgx.Tx) error { return nil },
			wantCommits: 1,
		},
		{
			name:         "a returned error rolls back and is wrapped",
			fn:           func(pgx.Tx) error { return errHandler },
			wantErr:      errHandler,
			wantErrMsg:   "in transaction",
			wantRollback: 1,
		},
		{
			name:         "a panic rolls back and keeps panicking",
			fn:           func(pgx.Tx) error { panic("boom") },
			wantPanic:    true,
			wantRollback: 1,
		},
		{
			name: "Begin failing is reported before fn runs",
			fn: func(pgx.Tx) error {
				t.Error("fn ran after Begin failed")
				return nil
			},
			beginErr:   errors.New("pool exhausted"),
			wantErrMsg: "beginning a transaction",
		},
		{
			name:         "Commit failing is reported and the deferred rollback still runs",
			fn:           func(pgx.Tx) error { return nil },
			commitErr:    errors.New("connection reset"),
			wantErrMsg:   "committing the transaction",
			wantCommits:  1,
			wantRollback: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := &fakeBeginner{beginErr: tc.beginErr, tx: &fakeTx{commitErr: tc.commitErr}}

			var err error
			run := func() { err = WithTx(context.Background(), db, tc.fn) }

			if tc.wantPanic {
				assertPanics(t, run)
			} else {
				run()
			}

			wantFailure := tc.wantErr != nil || tc.wantErrMsg != ""
			switch {
			case tc.wantPanic:
				// WithTx did not return; only the rollback counts below matter.
			case wantFailure:
				if err == nil {
					t.Fatal("WithTx returned nil for a failing unit of work")
				}
				if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
					t.Errorf("error %v does not wrap %v — the caller cannot classify it", err, tc.wantErr)
				}
				if tc.wantErrMsg != "" && !strings.Contains(err.Error(), tc.wantErrMsg) {
					t.Errorf("error %q does not say where it happened (%q)", err, tc.wantErrMsg)
				}
			case err != nil:
				t.Fatalf("WithTx: %v", err)
			}
			if db.tx.commits != tc.wantCommits {
				t.Errorf("commits = %d, want %d", db.tx.commits, tc.wantCommits)
			}
			if db.tx.rollbacks != tc.wantRollback {
				t.Errorf("rollbacks = %d, want %d", db.tx.rollbacks, tc.wantRollback)
			}
		})
	}
}

// TestWithTxRollbackSurvivesCancellation is the reason Rollback gets a detached
// context: the case where rollback matters most is a cancelled or expired
// request, and a rollback on a cancelled context is a no-op that leaves the
// transaction holding its locks until the server reaps the session.
func TestWithTxRollbackSurvivesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	db := &fakeBeginner{tx: &fakeTx{}}

	err := WithTx(ctx, db, func(pgx.Tx) error {
		cancel()
		return errHandler
	})
	if !errors.Is(err, errHandler) {
		t.Fatalf("WithTx error = %v, want the handler error", err)
	}
	if db.tx.rollbacks != 1 {
		t.Fatalf("rollbacks = %d, want 1", db.tx.rollbacks)
	}
	if db.tx.rollbackCtxErr != nil {
		t.Errorf("Rollback saw a cancelled context (%v); it must run detached", db.tx.rollbackCtxErr)
	}
}

// TestWithTxRefusesNesting is the guard, not the documentation. A nested call
// compiles because pgx.Tx satisfies Beginner, and "rollback on error" would then
// unwind only a savepoint while the outer transaction went on to commit half a
// unit of work.
func TestWithTxRefusesNesting(t *testing.T) {
	tx := &fakeTx{}

	err := WithTx(context.Background(), tx, func(pgx.Tx) error {
		t.Error("fn ran inside a nested WithTx")
		return nil
	})
	if !errors.Is(err, ErrNestedTx) {
		t.Fatalf("WithTx on a pgx.Tx = %v, want ErrNestedTx", err)
	}
	if tx.begins != 0 {
		t.Errorf("Begin was called %d times; the guard must refuse before beginning", tx.begins)
	}
}

func TestWithTxRejectsMissingArguments(t *testing.T) {
	tests := []struct {
		name string
		db   Beginner
		fn   func(pgx.Tx) error
	}{
		{"no database handle", nil, func(pgx.Tx) error { return nil }},
		{"no function", &fakeBeginner{tx: &fakeTx{}}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := WithTx(context.Background(), tc.db, tc.fn); err == nil {
				t.Fatal("WithTx accepted an incomplete call")
			}
		})
	}
}

func assertPanics(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Error("the panic was swallowed; httpkit.Recover would never see it")
		}
	}()
	fn()
}
