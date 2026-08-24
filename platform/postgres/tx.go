package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ErrNestedTx reports that [WithTx] was handed something that is already a
// transaction. See WithTx for why that is refused rather than nested.
var ErrNestedTx = errors.New("nested WithTx is not supported")

// Beginner starts a transaction. *pgxpool.Pool and *pgx.Conn both satisfy it,
// which is the whole point: WithTx works against a pool in production and
// against a single connection in a test that needs one session.
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// WithTx runs fn inside one transaction: begin, then commit if fn returns nil,
// roll back if it returns an error, and roll back and re-panic if it panics.
//
// It is the platform's only transaction boundary, and the reason the outbox
// pattern works: a state change and the outbox row that publishes it are
// arguments to the same fn, so the broker cannot learn about a change that was
// rolled back (ADR-0004). The same applies to inbox dedupe on the consuming
// side — the dedupe row and the side effect share this transaction or the
// exactly-once business effect claim is false.
//
// fn receives the transaction; the context comes from the closure:
//
//	err := postgres.WithTx(ctx, pool, func(tx pgx.Tx) error {
//	    if err := q.WithTx(tx).InsertCase(ctx, params); err != nil {
//	        return err
//	    }
//	    return outbox.Enqueue(ctx, tx, reg, env, topic, key)
//	})
//
// Rules worth knowing before using it:
//
//   - **Nesting is not supported.** pgx.Tx also satisfies Beginner (its Begin
//     opens a savepoint), so a nested call would compile — and would be a bug:
//     "rollback on error" would unwind only the savepoint and leave the outer
//     transaction free to commit half a unit of work. WithTx returns
//     [ErrNestedTx] instead. Pass the tx you already have to the function that
//     needs it.
//   - **Rollback is best effort by design.** If fn failed and the rollback also
//     failed, the error reported is fn's: it is the cause, and a failed rollback
//     on a broken connection is a consequence the server resolves by itself.
//   - **A panic is not swallowed.** The transaction is rolled back and the panic
//     continues, so httpkit's Recover middleware still produces the A§20 body
//     and the stack still reaches the logs. Panicking in a request path is not a
//     strategy (CLAUDE.md §3); this only makes sure it is not also a data
//     corruption.
//   - **fn must not retry internally.** Serialization failures and deadlocks
//     surface as errors from WithTx; retrying a whole unit of work is the
//     caller's decision because only the caller knows whether it is idempotent.
func WithTx(ctx context.Context, db Beginner, fn func(pgx.Tx) error) error {
	if db == nil {
		return errors.New("starting a transaction: no database handle")
	}
	if fn == nil {
		return errors.New("starting a transaction: no function to run")
	}
	if _, nested := db.(pgx.Tx); nested {
		return ErrNestedTx
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning a transaction: %w", err)
	}

	// committed guards the deferred rollback: after a successful commit there is
	// nothing to roll back, and pgx would return ErrTxClosed.
	committed := false
	defer func() {
		if committed {
			return
		}
		// The rollback must not inherit a cancelled context — a panic or a
		// deadline expiry is exactly when the rollback matters most, and a
		// cancelled context would make it a no-op that leaves the transaction
		// open until the server times the session out.
		_ = tx.Rollback(context.WithoutCancel(ctx))
	}()

	if err := fn(tx); err != nil {
		return fmt.Errorf("in transaction: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing the transaction: %w", err)
	}
	committed = true
	return nil
}
