package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// fakeTx records the transaction boundary's decisions without a server.
//
// pgx.Tx is embedded as a nil interface on purpose: the twelve methods WithTx
// never touches stay unimplemented, and a future change that starts calling one
// of them panics loudly in a test instead of quietly passing against a stub that
// returned zero values. Embedding also makes *fakeTx satisfy pgx.Tx, which is
// what the nesting guard is asserted against.
type fakeTx struct {
	pgx.Tx

	begins    int
	commits   int
	rollbacks int

	commitErr error
	// rollbackCtxErr is the error of the context Rollback was called with. It
	// must stay nil even when the caller's context was cancelled.
	rollbackCtxErr error
}

func (f *fakeTx) Begin(context.Context) (pgx.Tx, error) {
	f.begins++
	return f, nil
}

func (f *fakeTx) Commit(context.Context) error {
	f.commits++
	return f.commitErr
}

func (f *fakeTx) Rollback(ctx context.Context) error {
	f.rollbacks++
	f.rollbackCtxErr = ctx.Err()
	return nil
}

// fakeBeginner stands in for a pool. It deliberately implements only Begin, so
// it is a Beginner and not a pgx.Tx — the nesting guard must not fire on it.
type fakeBeginner struct {
	tx       *fakeTx
	beginErr error
	begins   int
}

func (f *fakeBeginner) Begin(context.Context) (pgx.Tx, error) {
	f.begins++
	if f.beginErr != nil {
		return nil, f.beginErr
	}
	return f.tx, nil
}
