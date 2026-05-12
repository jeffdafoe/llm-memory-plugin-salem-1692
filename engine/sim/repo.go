package sim

import "context"

// Repository is the persistence facade for the world. Per-aggregate split:
// each sub-repo owns one entity and all its child tables. Cross-aggregate
// logic (e.g. pay between actors) lives as in-memory mutations in command
// handlers; persistence happens at checkpoint time via SaveSnapshot calls.
type Repository struct {
	Actors      ActorsRepo
	Structures  StructuresRepo
	Huddles     HuddlesRepo
	Scenes      ScenesRepo
	Orders      OrdersRepo
	Environment EnvironmentRepo

	// Event sinks — write-through per-event, NOT part of the checkpoint tx.
	// Pay-ledger is an event log of attempts and outcomes; agent-action-log
	// is an audit trail. Both are appended outside the checkpoint.
	PayLedger PayLedgerSink
	ActionLog ActionLogSink

	// Begin opens a transaction used by the checkpoint flow. All aggregate
	// snapshots are written inside one Tx so the checkpoint is atomic; a
	// crash mid-write rolls back and leaves the previous checkpoint intact.
	Begin func(ctx context.Context) (Tx, error)
}

// ActorsRepo loads + checkpoints actors and all their child tables (needs,
// inventory, acquaintances, relationships, narrative, dwell credits,
// attributes). The repo handles join+insert internally; callers deal only
// in whole entities.
type ActorsRepo interface {
	LoadAll(ctx context.Context) (map[ActorID]*Actor, error)
	SaveSnapshot(ctx context.Context, tx Tx, actors map[ActorID]*Actor) error
}

// StructuresRepo loads + checkpoints structures (and child tables for
// loiter slots / rooms / asset placements when those are added).
type StructuresRepo interface {
	LoadAll(ctx context.Context) (map[StructureID]*Structure, error)
	SaveSnapshot(ctx context.Context, tx Tx, structures map[StructureID]*Structure) error
}

// HuddlesRepo loads + checkpoints huddle + scene_huddle child rows.
type HuddlesRepo interface {
	LoadAll(ctx context.Context) (map[HuddleID]*Huddle, error)
	SaveSnapshot(ctx context.Context, tx Tx, huddles map[HuddleID]*Huddle) error
}

// ScenesRepo loads + checkpoints scenes.
type ScenesRepo interface {
	LoadAll(ctx context.Context) (map[SceneID]*Scene, error)
	SaveSnapshot(ctx context.Context, tx Tx, scenes map[SceneID]*Scene) error
}

// OrdersRepo loads + checkpoints in-flight order state.
type OrdersRepo interface {
	LoadAll(ctx context.Context) (map[OrderID]*Order, error)
	SaveSnapshot(ctx context.Context, tx Tx, orders map[OrderID]*Order) error
}

// EnvironmentRepo loads + checkpoints world-level environment + phase.
type EnvironmentRepo interface {
	Load(ctx context.Context) (WorldEnvironment, Phase, error)
	SaveSnapshot(ctx context.Context, tx Tx, env WorldEnvironment, phase Phase) error
}

// PayLedgerSink appends pay-ledger events transactionally per-event, NOT
// at checkpoint time. Pay-ledger is an event log, not snapshot state.
type PayLedgerSink interface {
	Append(ctx context.Context, entry PayLedgerEntry) error
}

// ActionLogSink appends agent action log rows per-event.
type ActionLogSink interface {
	Append(ctx context.Context, entry ActionLogEntry) error
}

// PayLedgerEntry — concrete shape ported with pay/order subsystem.
type PayLedgerEntry struct{}

// ActionLogEntry — concrete shape ported with agent_tick port.
type ActionLogEntry struct{}

// Tx is a transaction handle exposing the pgx-style query surface our
// repos need. Production wires a real *pgx.Tx; mem fakes wire a no-op.
// Same repo code runs in both.
type Tx interface {
	Exec(ctx context.Context, sql string, args ...any) (CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) Row
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// CommandTag describes the result of an Exec — typically rows affected.
// Mirrors pgconn.CommandTag minimally.
type CommandTag interface {
	RowsAffected() int64
}

// Rows is an iterator over query results. Mirrors pgx.Rows minimally.
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}

// Row is a single-row query result. Mirrors pgx.Row.
type Row interface {
	Scan(dest ...any) error
}
