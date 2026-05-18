package pg

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// tx.go — adapters wrapping pgx types to satisfy the sim package's
// minimal interfaces. Same repo code runs against the real pgx in prod
// and pgxmock in tests because both produce values that satisfy these
// adapters.

// txAdapter wraps a pgx.Tx as sim.Tx. The pool's Begin returns one of
// these; the checkpoint flow passes it to SaveSnapshot calls.
type txAdapter struct {
	tx pgx.Tx
}

func (a *txAdapter) Exec(ctx context.Context, sql string, args ...any) (sim.CommandTag, error) {
	ct, err := a.tx.Exec(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return cmdTagAdapter{ct: ct}, nil
}

func (a *txAdapter) Query(ctx context.Context, sql string, args ...any) (sim.Rows, error) {
	rows, err := a.tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return rowsAdapter{rows: rows}, nil
}

func (a *txAdapter) QueryRow(ctx context.Context, sql string, args ...any) sim.Row {
	return rowAdapter{row: a.tx.QueryRow(ctx, sql, args...)}
}

func (a *txAdapter) Commit(ctx context.Context) error   { return a.tx.Commit(ctx) }
func (a *txAdapter) Rollback(ctx context.Context) error { return a.tx.Rollback(ctx) }

// cmdTagAdapter wraps pgconn.CommandTag as sim.CommandTag.
type cmdTagAdapter struct {
	ct pgconn.CommandTag
}

func (a cmdTagAdapter) RowsAffected() int64 { return a.ct.RowsAffected() }

// rowsAdapter wraps pgx.Rows as sim.Rows.
type rowsAdapter struct {
	rows pgx.Rows
}

func (a rowsAdapter) Next() bool             { return a.rows.Next() }
func (a rowsAdapter) Scan(dest ...any) error { return a.rows.Scan(dest...) }
func (a rowsAdapter) Err() error             { return a.rows.Err() }
func (a rowsAdapter) Close()                 { a.rows.Close() }

// rowAdapter wraps pgx.Row as sim.Row.
type rowAdapter struct {
	row pgx.Row
}

func (a rowAdapter) Scan(dest ...any) error { return a.row.Scan(dest...) }
