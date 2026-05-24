package sim

import (
	"context"
	"time"
)

// Repository is the persistence facade for the world. Per-aggregate split:
// each sub-repo owns one entity and all its child tables. Cross-aggregate
// logic (e.g. pay between actors) lives as in-memory mutations in command
// handlers; persistence happens at checkpoint time via SaveSnapshot calls.
type Repository struct {
	Actors               ActorsRepo
	Structures           StructuresRepo
	Huddles              HuddlesRepo
	Scenes               ScenesRepo
	Orders               OrdersRepo
	Environment          EnvironmentRepo
	Assets               AssetsRepo
	Sprites              SpritesRepo
	AttributeDefinitions AttributeDefinitionsRepo
	Recipes              RecipesRepo
	ItemKinds            ItemKindsRepo
	Terrain              TerrainRepo
	VillageObjects       VillageObjectsRepo

	// Event sinks — write-through per-event, NOT part of the checkpoint tx.
	// agent-action-log is an audit trail appended outside the checkpoint.
	// (Pay-ledger was originally framed as an event log here too; the
	// PR S4 ledger-substrate redesign moved it onto World state. Pending
	// entries are now intentionally restart-lossy with no durable backing
	// at all — see engine/sim/pay_ledger.go and
	// work/tasks/payledger-restart-lossy/decision.)
	ActionLog ActionLogSink

	// TickTelemetry receives per-tick lifecycle records (Phase 2 PR 3a). The
	// reactor evaluator writes "deferred" records when admission control
	// turns an actor away; PR 3's worker pool adds started/completed/failed/
	// stale. Diagnostic only — never part of the checkpoint, never on the
	// hot path of a tick's correctness.
	TickTelemetry TickTelemetrySink

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

// OrdersRepo loads + checkpoints in-flight order state, and loads
// historical accepted-price observations for the price book seed
// at LoadWorld time.
type OrdersRepo interface {
	LoadAll(ctx context.Context) (map[OrderID]*Order, error)
	SaveSnapshot(ctx context.Context, tx Tx, orders map[OrderID]*Order) error

	// WriteTerminal durably persists a single Order at its terminal state —
	// the write-through half of the Slice 6 write-through-then-prune. It is
	// the one OrdersRepo method that also satisfies the narrow TerminalOrderSink
	// interface, so production wires `repo.Orders` directly as the World's sink
	// (World still depends only on TerminalOrderSink, not on this repo). See
	// finalizeOrderTerminal + SetTerminalOrderSink.
	WriteTerminal(ctx context.Context, o *Order) error

	// LoadRecentPrices returns up to perKeyCap most-recent accepted
	// PayLedger rows per (seller, item) tuple within the time window
	// (created_at >= since), packaged as PriceBookSeedRecord values for
	// World.SeedPriceBook. Returned in chronological order per key
	// (oldest first) so the ring-buffer push contract is satisfied
	// directly without re-sorting.
	//
	// Source table is pay_ledger — the source of truth for accepted
	// transactions across both ConsumeNow and take-home flows.
	// Implementations filter state='accepted' and bound by `since`.
	LoadRecentPrices(ctx context.Context, since time.Time, perKeyCap int) ([]PriceBookSeedRecord, error)
}

// AssetsRepo loads the asset catalog (assets + states + slots + lights +
// packs). Reference state — loaded at startup, hot-reloaded on SIGHUP.
// NOT part of the checkpoint cycle (admin edits write directly to the
// asset / asset_state tables through the editor port).
type AssetsRepo interface {
	LoadAll(ctx context.Context) (map[AssetID]*Asset, error)
}

// SpritesRepo loads the character-sprite catalog (npc_sprite + animation
// rows + packs) flattened into sim.Sprite aggregates keyed by sprite UUID.
// Reference state — loaded at startup, hot-reloaded on SIGHUP. Same
// lifecycle as AssetsRepo; NOT part of the checkpoint cycle (admin edits
// write directly to the npc_sprite tables through the editor port).
type SpritesRepo interface {
	LoadAll(ctx context.Context) (map[SpriteID]*Sprite, error)
}

// AttributeDefinitionsRepo loads the actor-assignable attribute-definition
// catalog (attribute_definition rows with scope IN ('actor','both')) into
// AttributeDefinition aggregates keyed by slug. Reference state — same
// lifecycle as AssetsRepo / SpritesRepo (load at startup, hot-reload on
// SIGHUP). NOT part of the checkpoint cycle: admin edits write directly to
// the attribute_definition table and the world rebuilds the map wholesale via
// this LoadAll.
type AttributeDefinitionsRepo interface {
	LoadAll(ctx context.Context) (map[string]*AttributeDefinition, error)
}

// RecipesRepo loads the item_recipe catalog. Reference state — same
// lifecycle as AssetsRepo (load at startup, hot-reload on SIGHUP).
type RecipesRepo interface {
	LoadAll(ctx context.Context) (map[ItemKind]*ItemRecipe, error)
}

// ItemKindsRepo loads the item_kind + item_satisfies catalog flattened into
// ItemKindDef aggregates. Reference state — same lifecycle as AssetsRepo /
// RecipesRepo (load at startup, hot-reload on SIGHUP). No checkpoint path:
// admin edits write directly to the underlying tables and the world
// rebuilds the map wholesale via this LoadAll.
type ItemKindsRepo interface {
	LoadAll(ctx context.Context) (map[ItemKind]*ItemKindDef, error)
}

// TerrainRepo loads the village_terrain blob. Reference state — one
// row in the table, hot-reload on SIGHUP if the operator edits the
// terrain at runtime.
type TerrainRepo interface {
	Load(ctx context.Context) (*Terrain, error)
}

// VillageObjectsRepo loads + checkpoints village_object + village_object_tag.
// Hot state: current_state mutates at phase transitions, occupancy refresh,
// admin set-state, etc. Tags can be added/removed at runtime.
type VillageObjectsRepo interface {
	LoadAll(ctx context.Context) (map[VillageObjectID]*VillageObject, error)
	SaveSnapshot(ctx context.Context, tx Tx, objects map[VillageObjectID]*VillageObject) error
}

// EnvironmentRepo loads + checkpoints world-level state: environment
// (transient — atmosphere prose, last-transition timestamps), phase
// (day/night), and settings (admin-tunable dawn/dusk/zoom/etc.).
//
// Settings are loaded at startup and hot-reloaded via SIGHUP per the
// data-partition design — they're reference state, NOT part of the
// checkpoint write. SaveSnapshot only writes env + phase.
type EnvironmentRepo interface {
	Load(ctx context.Context) (WorldEnvironment, Phase, WorldSettings, error)
	SaveSnapshot(ctx context.Context, tx Tx, env WorldEnvironment, phase Phase) error
}

// ActionLogSink appends action log rows per-event. v2 MVP wires a noop
// implementation (mem.noopActionLog) — the in-engine consumers
// (atmosphere digest, C2 consolidation) read from World.ActionLog
// directly; durable pg projection lands at cutover. See
// engine/sim/action_log.go for the entry shape.
type ActionLogSink interface {
	Append(ctx context.Context, entry ActionLogEntry) error
}

// TickTelemetryRecord is one entry in the per-tick lifecycle telemetry
// stream. PR 3a owns the minimal contract because the reactor evaluator is
// the first writer (the "deferred" record, written when admission control
// turns an actor away). PR 3 adds the worker-side Kinds (started /
// completed / failed / stale) and their Detail conventions.
//
// Kind is an OPEN string set — consumers must tolerate unknown values.
// Detail is structured and REDACTED: no raw prompts, raw LLM responses,
// tool arguments carrying private text, or memory payloads ever go here.
type TickTelemetryRecord struct {
	At        time.Time
	ActorID   ActorID
	AttemptID TickAttemptID
	Kind      string            // open set; PR 3a writes "deferred"
	Detail    map[string]string // structured + redacted
}

// TickTelemetrySink receives TickTelemetryRecords. Implementations MUST be
// non-blocking — WriteTickTelemetry runs on the world goroutine (for the
// evaluator's "deferred" records) and must never block it; on a full
// buffer the impl drops and counts rather than waiting. No context, no
// error return: telemetry is fire-and-forget and best-effort by contract.
type TickTelemetrySink interface {
	WriteTickTelemetry(TickTelemetryRecord)
}

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
