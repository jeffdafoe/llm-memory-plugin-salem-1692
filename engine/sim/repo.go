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

	// MaxLedgerID returns the largest id present in pay_ledger (0 when the
	// table is empty). FinalizeLoad seeds the LedgerID allocator
	// (World.payLedgerSeq) from this so a mint after restart never reuses an
	// id that already exists durably — the in-memory PayLedger map holds only
	// restart-lossy pending entries and World.Orders only the in-flight subset,
	// so neither sees terminal-row ids. Without this floor a reused id would
	// make the checkpoint upsert clobber an unrelated historical row.
	// ZBBS-HOME-394.
	MaxLedgerID(ctx context.Context) (int64, error)

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
	// SaveMutableSettings upserts the runtime-tunable settings the admin config
	// write routes own (ZBBS-WORK-363) inside the checkpoint Tx. ONLY those keys
	// are written — the rest of the setting table is load-once / operator-tuned
	// out of band and must not be clobbered by the checkpoint.
	SaveMutableSettings(ctx context.Context, tx Tx, settings MutableWorldSettings) error
}

// DurableActionLogRow is the structured audit row the production
// ActionLogSink persists to the agent_action_log pg table. Unlike the
// lean in-memory ActionLogEntry (action_log.go) — which flattens every
// action to a single Text field for the in-engine atmosphere / C2
// consumers — this carries the full column set the API-side dream
// distiller (sim-conversation-distiller.js narrateEvent) renders from:
// a structured Payload plus the denormalized speaker name and source.
//
// Built at the cascade action-log subscribers (cascade/action_log.go),
// where the originating event still carries the structured fields
// (Paid.SellerID/Amount, OrderDelivered.Item/Qty, ActorArrived.Dest…)
// that the lean ring drops. The ring is appended separately and stays
// lean. Result is implicitly "ok": v2 logs committed actions only.
type DurableActionLogRow struct {
	ActorID     ActorID
	OccurredAt  time.Time
	ActionType  ActionType
	Payload     map[string]any // structured: recipient/amount/for, destination, item/qty, text, reason
	SpeakerName string         // actor DisplayName, re-denormalized for the distiller
	HuddleID    HuddleID       // "" for outdoor / pre-huddle / non-huddle actions
	Source      string         // "agent" | "player" | "engine"
}

// SimDayEvent is one agent_action_log row pulled for the daily sim-conversation
// push (ZBBS-WORK-376). It is the engine-side shape of the {at, kind, payload,
// speaker} event the API's POST /v1/sim/conversation-day distiller
// (sim-conversation-distiller.js narrateEvent) renders into a per-day narrative
// note feeding the four stateful NPCs' dream memory.
//
// One actor's day is that actor's own committed action rows PLUS the speech it
// overheard from huddle-mates while co-present — see
// (*pg.ActionLogRepo).LoadDayEvents for the presence-interval scoping. The
// cross-actor speech is what makes a keeper's day read as a conversation rather
// than a monologue.
//
// Wire serialization (JSON field tags, the {agent, day, events} POST envelope)
// is the push ticker's concern, so this domain type carries none.
type SimDayEvent struct {
	At      time.Time
	Kind    ActionType     // persisted v2-native action_type: spoke / paid / walked / delivered / consumed / took_break
	Payload map[string]any // structured row payload as stored (text / recipient+amount / destination / item+qty / reason)
	Speaker string         // agent_action_log.speaker_name — acting actor's display name; labels the distilled line
}

// HuddleTranscriptRow is one agent_action_log row pulled for the umbilical's
// durable conversation transcript (LLM-35). Where SimDayEvent is the daily-push
// shape — scoped to one actor's day with cross-actor presence intervals — this is
// the COMPLETE committed-action trail of a single huddle: every participant's
// rows, oldest-first, the durable companion to the live, retention-bounded
// /huddle ring. Source distinguishes the actor's origin (agent NPC vs human
// player vs engine), which the lean in-memory action-log ring doesn't carry.
type HuddleTranscriptRow struct {
	OccurredAt  time.Time
	Source      string     // agent_action_log.source: "agent" | "player" | "engine"
	SpeakerName string     // acting actor's display name
	ActionType  ActionType // spoke / paid / delivered / consumed / …
	Text        string     // payload.text — the spoken/narrated line; "" for textless actions
}

// SettlementRow is one accepted pay-with-item settlement read back from the durable
// agent_action_log `paid` audit beat (LLM-105) — the read model for the operator
// settlements lens. Buyer{ID,Name} is the actor who paid (pay is buyer-initiated,
// so actor_id is the buyer); SellerName the recipient; Amount the coins; Item the
// "1 stew" for-text; PayItems the barter goods the buyer paid WITH (nil for a
// pure-coin pay). LedgerID joins back to the pay-ledger entry and ConsumeNow marks
// an eat-here sale; both are zero/false on pre-LLM-105 rows that predate the payload
// enrichment (whose goods leg is unrecoverable). A give-away (free food) is
// Amount==0 AND no PayItems.
type SettlementRow struct {
	OccurredAt time.Time
	BuyerID    ActorID
	BuyerName  string
	SellerName string
	Amount     int
	Item       string
	PayItems   []ItemKindQty
	LedgerID   LedgerID
	ConsumeNow bool
	HuddleID   string
}

// SettlementFilter narrows a LoadSettlements read. Every field is optional — a zero
// value means "no filter on this dimension". ActorID scopes to one buyer; Since and
// Until bound occurred_at to [Since, Until); LedgerID pins one settlement (matches
// only post-LLM-105 rows, which carry ledger_id in the payload).
type SettlementFilter struct {
	ActorID  ActorID
	Since    time.Time
	Until    time.Time
	LedgerID LedgerID
}

// AgentActor pairs an actor's id with the llm-memory agent slug backing it.
// The daily sim-conversation push (ZBBS-WORK-376) enumerates these to know
// which actors to build a day-note for and under which agent namespace to POST
// it. Only actors with a non-empty llm_memory_agent qualify.
type AgentActor struct {
	ID    ActorID
	Agent string
}

// ActionLogSink durably persists committed action-log rows to the
// agent_action_log audit table — write-through per-event, OUTSIDE the
// checkpoint tx (see the Repository doc). The production impl (repo/pg)
// is ASYNC: Append enqueues on the world goroutine and a writer
// goroutine performs the INSERT off-goroutine, mirroring the checkpoint
// flow's "clone on-goroutine, write off-goroutine" posture
// (checkpoint.go) so the hot action-emit path never blocks on PG. The
// in-engine consumers (atmosphere digest, C2 consolidation) read
// World.ActionLog directly and do NOT depend on this sink. v2 MVP
// before this wiring used mem.noopActionLog. ZBBS-WORK-376.
type ActionLogSink interface {
	Append(ctx context.Context, row DurableActionLogRow) error
}

// NarrationExpansionSink durably persists LLM-generated narration pool
// phrases to the narration_pool_expansion table (ZBBS-WORK-399) —
// write-through at expansion time, OUTSIDE the checkpoint tx. Unlike
// ActionLogSink this is SYNCHRONOUS: Append is called from the expansion
// cascade goroutine (never the world goroutine), and the cascade must
// know the write landed before applying the phrases in memory
// (durable-first — a phrase the DB never saw would vanish on restart).
// Production impl: repo/pg NarrationExpansionRepo. Loading the persisted
// rows back happens at boot in main.go (LoadAll → MergeNarrationExpansions),
// not through this interface.
type NarrationExpansionSink interface {
	Append(ctx context.Context, poolKey string, phrases []string, generatedBy string) error
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

// PromptRecord is one actor tick's RENDERED DELIBERATION PROMPT — the full
// user-message text the harness built from perception and sent to the model.
// ZBBS-HOME-360.
//
// This is the deliberate counterpart to TickTelemetryRecord and breaks the
// SAME redaction rule on purpose: unlike telemetry (structured + redacted, "no
// raw prompts ever"), a PromptRecord carries the raw prompt text so an operator
// can see exactly what an NPC perceived when it made a decision. It is a
// debug-only surface and MUST reach ONLY the operator-gated umbilical — never
// telemetry, the action log, a player-facing path, or durable storage. It is
// in-memory and lost on restart by design.
type PromptRecord struct {
	At        time.Time
	ActorID   ActorID
	AttemptID TickAttemptID
	Prompt    string // the full rendered deliberation prompt (user message)
}

// PromptSink receives PromptRecords. Like TickTelemetrySink, implementations
// MUST be non-blocking — WritePrompt runs on the tick-worker goroutines and
// must never stall a worker; on overflow the impl drops the oldest rather than
// waiting. Fire-and-forget: no context, no error. A nil sink means "don't
// capture" (the umbilical-disabled default), so callers null-check before use.
type PromptSink interface {
	WritePrompt(PromptRecord)
}

// ChatRecord is one entry in the engine<->model chat exchange for a scene:
// either the rendered perception the engine SENT (Direction "perception", tx) or
// a model RESPONSE it got back (Direction "response", rx — carrying Content plus
// a compact ToolCalls summary). Keyed by SceneID (llm.NewSceneID — the same id
// stamped on chat_message_texts.scene_id in llm-memory), so the umbilical can
// return one scene's full conversation. ZBBS-HOME-382.
//
// SAME redaction rule as PromptRecord: it carries raw prompt/response text and
// MUST reach ONLY the operator-gated umbilical — never telemetry, the action
// log, a player-facing path, or durable storage. In-memory, lost on restart.
type ChatRecord struct {
	At        time.Time
	SceneID   string
	ActorID   ActorID
	AttemptID TickAttemptID
	Model     string
	Direction string // "perception" (engine->model, tx) or "response" (model->engine, rx)
	Content   string // rendered perception (tx) or model response content (rx)
	ToolCalls string // compact summary of the response's tool calls (rx only; empty for tx)
}

// ChatSink receives ChatRecords. Like PromptSink, implementations MUST be
// non-blocking — WriteChat runs on the tick-worker goroutines and must never
// stall a worker; on overflow the impl drops the oldest rather than waiting.
// Fire-and-forget: no context, no error. A nil sink means "don't capture" (the
// umbilical-disabled default), so callers null-check before use.
type ChatSink interface {
	WriteChat(ChatRecord)
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
