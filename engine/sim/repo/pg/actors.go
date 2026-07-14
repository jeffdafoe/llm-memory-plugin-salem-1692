package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// actorChildTables are every table that hangs off `actor`. Dropping an actor
// from a checkpoint (LLM-392) must block these tables' stale-row sweeps as
// well as `actor`'s own: the dropped actor's child rows are not re-upserted
// either, so they still carry the old gen, and an unblocked sweep would delete
// them — leaving the actor's parent row (preserved, because `actor`'s sweep is
// skipped) stripped of its needs, inventory and room grants. Half an actor is
// worse than a stale one.
var actorChildTables = []string{
	"actor_need",
	"actor_inventory",
	"actor_relationship",
	"actor_narrative_state",
	"npc_acquaintance",
	"actor_dwell_credit",
	"room_access",
	"actor_attribute",
	"actor_known_place",
}

// dropActor quarantines one actor out of this checkpoint and takes its whole
// subtree with it — see actorChildTables. The actor keeps whatever version it
// already had in Postgres; it does not vanish.
func dropActor(q *sim.Quarantine, id sim.ActorID, reason string) {
	q.Drop("actor", string(id), reason)
	for _, t := range actorChildTables {
		q.BlockSweep(t)
	}
}

// ActorsRepo reads and writes Actor rows against `actor` plus its child
// tables. Owns the whole aggregate; Slice 245 extends the load/save
// further with dwell credits, room access, attributes, produce state.
//
// SaveSnapshot uses the generation-marker pattern (Slice 9/10/11/12/13
// precedent — see
// `shared/notes/codebase/salem-engine-v2/pg-snapshot-pattern`). Six
// tiers as of Slice 2 (ZBBS-WORK-244): parent `actor`, `actor_need`,
// `actor_inventory`, plus the continuity layer `actor_relationship`,
// `actor_narrative_state`, `npc_acquaintance`. Each tier owns its own
// sequence; the parent's single advisory lock covers all six — child
// tables never SaveSnapshot independently.
//
// The continuity tier (Slice 2) persists what the consolidation
// cascades produce: per-pair `Relationship` (summary + salient-fact
// JSONB trail), per-actor `NarrativeState` (seed + evolving summary),
// and the binary `Acquaintance` "have we met by name" marker. Only
// shared-VA NPCs populate relationships/narrative; acquaintance applies
// to all NPCs. Visitor actors are filtered out at the parent level, so
// their continuity rows are never written.
//
// v1↔v2 column scope. v2 reads/writes only the subset of `actor`
// columns the engine actually tracks. The v1-only bookkeeping columns
// that used to ride along frozen (`inside`, `lateness_window_minutes`,
// the visitor cluster, PC-liveness stamps, etc.) were dropped by the
// ZBBS-WORK-389 migration after sitting years-stale with zero readers;
// that migration also deleted any surviving visitor actor rows (the
// cleanup an earlier revision of this comment deferred to "a separate
// cutover-prep migration"). Visitor actors are filtered out of
// SaveSnapshot entirely (per visitor codebase note "No durable visitor
// row persistence").
//
// `sprite_id` and `facing` graduated into the v2-tracked set with the
// agent-sprite work (ZBBS-WORK-257) — they back the client read surface's
// inlined agent sprite. sprite_id is nullable uuid (empty↔NULL); facing is
// NOT NULL with a CHECK'd enum, so writes coalesce empty→'south' (the schema
// default) via facingOrDefault.
//
// Empty-string ↔ NULL convention (Slice 1 establishes the pattern;
// codified at `shared/notes/codebase/salem-engine-v2/actors-pg` when
// the slice ships):
//
//   - ID-string fields where empty-string is the Go sentinel
//     (InsideStructureID, CurrentHuddleID, Home/Work StructureID):
//     scan target is `*string`; Go-side empty → SQL NULL on write.
//   - Plain string fields where empty-string is the sentinel
//     (Role, LLMAgent, LoginUsername): same.
//   - Pointer time fields (BreakUntil, SleepingUntil, LastTickedAt):
//     scan target is `*time.Time`; nil-or-value round-trips through
//     SQL NULL.
//   - Pointer int fields (ScheduleStartMin/EndMin): scan target is
//     `*int16` matching the SMALLINT column; Go-side `*int` converted
//     at the boundary.
//   - RoomID (int64 with 0-sentinel): scan target is `*int64`; NULL
//     → 0, value → RoomID(x). Symmetric on save.
type ActorsRepo struct {
	pool Pool
}

// NewActorsRepo constructs an ActorsRepo against the given pool.
// Normal wiring path is pg.NewRepository which wires this internally.
func NewActorsRepo(pool Pool) *ActorsRepo {
	return &ActorsRepo{pool: pool}
}

// loadAllSQLA selects the v2-owned column subset from `actor`
// (post-WORK-389 that is nearly the whole table; snapshot_gen is
// omitted — pure sync bookkeeping, and `admin` is read here but never
// written back, see upsertSQLA).
//
// `::text` casts on UUID columns let pgx scan straight into `*string`
// scan targets, matching the rest of the slice's nullable-ID pattern.
const loadAllSQLA = `
SELECT
    id::text,
    display_name,
    current_x,
    current_y,
    inside_structure_id::text,
    current_huddle_id::text,
    inside_room_id,
    home_structure_id::text,
    work_structure_id::text,
    coins,
    llm_memory_agent,
    role,
    login_username,
    schedule_start_minute,
    schedule_end_minute,
    last_agent_tick_at,
    break_until,
    sleeping_until,
    move_attempt_counter,
    sim_state,
    sprite_id::text,
    facing,
    admin,
    move_destination,
    production_item,
    production_batch_qty,
    production_remaining_seconds
  FROM actor`

// loadAllNeedsSQLA selects every actor_need row. Joined to actors in
// Go via actor_id.
const loadAllNeedsSQLA = `
SELECT actor_id::text, key, value
  FROM actor_need`

// loadAllInventorySQLA selects every actor_inventory row. uses_left is the
// durable-tool wear on the in-use unit (LLM-330) — NULL for ordinary stock
// and for a tool no execution has worn yet.
const loadAllInventorySQLA = `
SELECT actor_id::text, item_kind, quantity, uses_left
  FROM actor_inventory`

// upsertSQLA writes one actor row. Column list = v2-owned subset only
// (ON CONFLICT touches only listed columns; anything unlisted keeps its
// stored value on UPDATE and falls back to the schema default on
// INSERT). snapshot_gen carries the new checkpoint gen so the trailing
// DELETE can prune absent rows.
//
// `admin` is deliberately ABSENT here. It is externally-managed
// authorization state (set directly in the DB for village operators),
// not sim state — LoadWorld reads it, the checkpoint never writes it.
// ON CONFLICT touches only the listed columns, so an operator-set admin
// value survives every checkpoint. Do NOT add admin here. See migration
// ZBBS-WORK-271 + sim.Actor.IsAdmin.
const upsertSQLA = `
INSERT INTO actor (
    id, display_name, current_x, current_y,
    inside_structure_id, current_huddle_id, inside_room_id,
    home_structure_id, work_structure_id,
    coins, llm_memory_agent, role, login_username,
    schedule_start_minute, schedule_end_minute,
    last_agent_tick_at, break_until, sleeping_until,
    move_attempt_counter, sim_state,
    sprite_id, facing,
    snapshot_gen, move_destination,
    production_item, production_batch_qty, production_remaining_seconds
) VALUES (
    $1, $2, $3, $4,
    $5, $6, $7,
    $8, $9,
    $10, $11, $12, $13,
    $14, $15,
    $16, $17, $18,
    $19, $20,
    $21, $22,
    $23, $24,
    $25, $26, $27
)
ON CONFLICT (id) DO UPDATE SET
    display_name           = EXCLUDED.display_name,
    current_x              = EXCLUDED.current_x,
    current_y              = EXCLUDED.current_y,
    inside_structure_id    = EXCLUDED.inside_structure_id,
    current_huddle_id      = EXCLUDED.current_huddle_id,
    inside_room_id         = EXCLUDED.inside_room_id,
    home_structure_id      = EXCLUDED.home_structure_id,
    work_structure_id      = EXCLUDED.work_structure_id,
    coins                  = EXCLUDED.coins,
    llm_memory_agent       = EXCLUDED.llm_memory_agent,
    role                   = EXCLUDED.role,
    login_username         = EXCLUDED.login_username,
    schedule_start_minute  = EXCLUDED.schedule_start_minute,
    schedule_end_minute    = EXCLUDED.schedule_end_minute,
    last_agent_tick_at     = EXCLUDED.last_agent_tick_at,
    break_until            = EXCLUDED.break_until,
    sleeping_until         = EXCLUDED.sleeping_until,
    move_attempt_counter   = EXCLUDED.move_attempt_counter,
    sim_state              = EXCLUDED.sim_state,
    sprite_id              = EXCLUDED.sprite_id,
    facing                 = EXCLUDED.facing,
    snapshot_gen           = EXCLUDED.snapshot_gen,
    move_destination       = EXCLUDED.move_destination,
    production_item        = EXCLUDED.production_item,
    production_batch_qty   = EXCLUDED.production_batch_qty,
    production_remaining_seconds = EXCLUDED.production_remaining_seconds`

// upsertNeedSQLA writes one actor_need row. PK is (actor_id, key)
// per the table definition — UPSERT inserts new (actor, need)
// pairs and updates value for existing ones.
const upsertNeedSQLA = `
INSERT INTO actor_need (
    actor_id, key, value, snapshot_gen
) VALUES (
    $1, $2, $3, $4
)
ON CONFLICT (actor_id, key) DO UPDATE SET
    value        = EXCLUDED.value,
    snapshot_gen = EXCLUDED.snapshot_gen`

// upsertInventorySQLA writes one actor_inventory row. PK is
// (actor_id, item_kind). quantity > 0 enforced by table CHECK plus
// repo-side pre-pass (zero-qty entries dropped before this Exec).
// uses_left is the durable-tool wear on the in-use unit (LLM-330),
// NULL for ordinary stock / an unworn tool.
const upsertInventorySQLA = `
INSERT INTO actor_inventory (
    actor_id, item_kind, quantity, uses_left, snapshot_gen
) VALUES (
    $1, $2, $3, $4, $5
)
ON CONFLICT (actor_id, item_kind) DO UPDATE SET
    quantity     = EXCLUDED.quantity,
    uses_left    = EXCLUDED.uses_left,
    snapshot_gen = EXCLUDED.snapshot_gen`

const deleteStaleSQLA = `DELETE FROM actor             WHERE snapshot_gen < $1`
const deleteStaleNeedSQLA = `DELETE FROM actor_need        WHERE snapshot_gen < $1`
const deleteStaleInvSQLA = `DELETE FROM actor_inventory   WHERE snapshot_gen < $1`

const nextGenSQLA = `SELECT nextval('actor_snapshot_gen_seq')`
const nextGenNeedSQLA = `SELECT nextval('actor_need_snapshot_gen_seq')`
const nextGenInvSQLA = `SELECT nextval('actor_inventory_snapshot_gen_seq')`

// advisoryLockSQLA is the single global lock for the actor aggregate
// (all six tiers). Same pattern as Slice 11/12.
// Multi-realm upgrade path: replace 0 with hashtext($realm_id).
const advisoryLockSQLA = `SELECT pg_advisory_xact_lock(hashtext('actor_snapshot'), 0)`

// --- Slice 2 continuity tier (relationship / narrative / acquaintance) ---

// loadAllRelationshipsSQLA selects every actor_relationship row.
// `salient_facts` comes back as raw JSONB bytes, unmarshalled in Go via
// the salientFactRow DTO (lowercase keys match v1's {at, kind, text}).
const loadAllRelationshipsSQLA = `
SELECT
    actor_id::text, other_actor_id::text, summary_text, salient_facts,
    interaction_count, last_interaction_at, last_consolidated_at,
    created_at, updated_at, dropped_fact_count
  FROM actor_relationship`

// loadAllNarrativeSQLA selects every actor_narrative_state row (1:1
// with actor).
const loadAllNarrativeSQLA = `
SELECT
    actor_id::text, seed_text, evolving_summary, about_me,
    last_consolidated_at, created_at, updated_at
  FROM actor_narrative_state`

// loadAllAcquaintancesSQLA selects every npc_acquaintance row. The
// table keeps its pre-unification name; the FK was repointed to
// actor(id) by ZBBS-084. other_name is a TEXT name (not an FK) so
// NPC↔PC pairs work without a cross-table join.
const loadAllAcquaintancesSQLA = `
SELECT actor_id::text, other_name, first_interacted_at
  FROM npc_acquaintance`

// upsertRelationshipSQLA writes one actor_relationship row. PK is
// (actor_id, other_actor_id). salient_facts is bound as text + cast to
// jsonb (pgx encodes a Go string as text; the explicit ::jsonb cast is
// unambiguous and avoids relying on an implicit assignment cast).
// created_at / updated_at are written verbatim from the in-memory
// values (the cascades own them) rather than NOW() — keeps pg a
// faithful mirror of world state and preserves round-trip.
const upsertRelationshipSQLA = `
INSERT INTO actor_relationship (
    actor_id, other_actor_id, summary_text, salient_facts,
    interaction_count, last_interaction_at, last_consolidated_at,
    created_at, updated_at, dropped_fact_count, snapshot_gen
) VALUES (
    $1, $2, $3, $4::jsonb,
    $5, $6, $7,
    $8, $9, $10, $11
)
ON CONFLICT (actor_id, other_actor_id) DO UPDATE SET
    summary_text         = EXCLUDED.summary_text,
    salient_facts        = EXCLUDED.salient_facts,
    interaction_count    = EXCLUDED.interaction_count,
    last_interaction_at  = EXCLUDED.last_interaction_at,
    last_consolidated_at = EXCLUDED.last_consolidated_at,
    created_at           = EXCLUDED.created_at,
    updated_at           = EXCLUDED.updated_at,
    dropped_fact_count   = EXCLUDED.dropped_fact_count,
    snapshot_gen         = EXCLUDED.snapshot_gen`

// upsertNarrativeSQLA writes one actor_narrative_state row. PK is
// actor_id (1:1). seed_text is external input (dream pipeline) and
// evolving_summary is legacy (no longer written), but persistence
// round-trips both verbatim. about_me carries the rendered soul (LLM-199).
const upsertNarrativeSQLA = `
INSERT INTO actor_narrative_state (
    actor_id, seed_text, evolving_summary, about_me,
    last_consolidated_at, created_at, updated_at, snapshot_gen
) VALUES (
    $1, $2, $3, $4,
    $5, $6, $7, $8
)
ON CONFLICT (actor_id) DO UPDATE SET
    seed_text            = EXCLUDED.seed_text,
    evolving_summary     = EXCLUDED.evolving_summary,
    about_me             = EXCLUDED.about_me,
    last_consolidated_at = EXCLUDED.last_consolidated_at,
    created_at           = EXCLUDED.created_at,
    updated_at           = EXCLUDED.updated_at,
    snapshot_gen         = EXCLUDED.snapshot_gen`

// upsertAcquaintanceSQLA writes one npc_acquaintance row. PK is
// (actor_id, other_name).
const upsertAcquaintanceSQLA = `
INSERT INTO npc_acquaintance (
    actor_id, other_name, first_interacted_at, snapshot_gen
) VALUES (
    $1, $2, $3, $4
)
ON CONFLICT (actor_id, other_name) DO UPDATE SET
    first_interacted_at = EXCLUDED.first_interacted_at,
    snapshot_gen        = EXCLUDED.snapshot_gen`

const deleteStaleRelationshipSQLA = `DELETE FROM actor_relationship    WHERE snapshot_gen < $1`
const deleteStaleNarrativeSQLA = `DELETE FROM actor_narrative_state WHERE snapshot_gen < $1`
const deleteStaleAcquaintanceSQLA = `DELETE FROM npc_acquaintance      WHERE snapshot_gen < $1`

const nextGenRelationshipSQLA = `SELECT nextval('actor_relationship_snapshot_gen_seq')`
const nextGenNarrativeSQLA = `SELECT nextval('actor_narrative_state_snapshot_gen_seq')`
const nextGenAcquaintanceSQLA = `SELECT nextval('npc_acquaintance_snapshot_gen_seq')`

// --- Slice 3 final tier (dwell credit / produce state / room access / attribute) ---

// loadAllDwellCreditsSQLA selects every actor_dwell_credit row. The
// in-memory DwellCredit.Kind field has NO column in the v1 schema —
// item-source credits store object_id (the structure-as-object id) but
// not the originating item kind (see engine/dwell.go's INSERT). Faithful
// port: Kind is left empty on load, matching v1 (which has nowhere to
// read it from either). It regenerates only on the next item consume.
const loadAllDwellCreditsSQLA = `
SELECT
    actor_id::text, object_id::text, attribute, source,
    last_credited_at, remaining_ticks, dwell_delta, dwell_period_minutes
  FROM actor_dwell_credit`

// loadAllRoomAccessSQLA selects every room_access row. `kind` is NOT
// selected — it's the room category (common/private/staff), recomputed
// by canEnterRoom from the loaded Room at access-check time, and is not
// part of the in-memory RoomAccessKey. Source is DERIVED from
// granted_via_ledger_id: a non-NULL ledger id means a paid lodging grant
// (AccessSourceLedger); NULL means staff (AccessSourceStaff, which the
// engine normally derives from WorkStructureID and rarely persists). See
// the actors-pg codebase note for the reconciliation rationale.
const loadAllRoomAccessSQLA = `
SELECT
    actor_id::text, room_id, granted_via_ledger_id,
    granted_at, expires_at, active
  FROM room_access`

// loadAllAttributesSQLA selects every actor_attribute row. params is the
// raw JSONB blob, carried verbatim into Actor.Attributes[slug]. created_at
// is NOT selected — it's not modeled in-memory (the v1↔v2 column-scope
// posture: untouched on UPDATE, schema default on INSERT). The
// businessowner / restock projections are rebuilt from these raw rows in
// a LoadWorld carry-forward pass, not here.
const loadAllAttributesSQLA = `
SELECT actor_id::text, slug, params
  FROM actor_attribute`

// upsertDwellCreditSQLA writes one actor_dwell_credit row. PK is
// (actor_id, object_id, attribute, source). actor_id / object_id are
// bound as plain strings — pgx encodes text→uuid via the column type
// (Slice 1/2 precedent, no explicit cast). The baseline CHECKs
// (dwell_delta < 0, dwell_period_minutes > 0, source allowlist,
// remaining↔source pairing) are mirrored by the SaveSnapshot pre-pass so
// a violation is a clean substrate rejection rather than a mid-Tx CHECK.
const upsertDwellCreditSQLA = `
INSERT INTO actor_dwell_credit (
    actor_id, object_id, attribute, source,
    last_credited_at, remaining_ticks, dwell_delta, dwell_period_minutes,
    snapshot_gen
) VALUES (
    $1, $2, $3, $4,
    $5, $6, $7, $8,
    $9
)
ON CONFLICT (actor_id, object_id, attribute, source) DO UPDATE SET
    last_credited_at     = EXCLUDED.last_credited_at,
    remaining_ticks      = EXCLUDED.remaining_ticks,
    dwell_delta          = EXCLUDED.dwell_delta,
    dwell_period_minutes = EXCLUDED.dwell_period_minutes,
    snapshot_gen         = EXCLUDED.snapshot_gen`

// upsertRoomAccessSQLA writes one room_access row. PK is
// (room_id, actor_id) — so an actor holds at most ONE row per room
// regardless of source (the SaveSnapshot pre-pass rejects two in-memory
// entries for the same room under different sources). `kind` is NOT NULL
// with no default, so it is SYNTHESIZED from Source: ledger→'private'
// (paid bedroom), staff→'staff'. granted_at is written from CreatedAt
// verbatim (faithful mirror); granted_via_ledger_id is non-NULL exactly
// for ledger grants (the load-side Source derivation depends on this).
const upsertRoomAccessSQLA = `
INSERT INTO room_access (
    room_id, actor_id, granted_via_ledger_id,
    granted_at, expires_at, kind, active, snapshot_gen
) VALUES (
    $1, $2, $3,
    $4, $5, $6::room_kind, $7, $8
)
ON CONFLICT (room_id, actor_id) DO UPDATE SET
    granted_via_ledger_id = EXCLUDED.granted_via_ledger_id,
    granted_at            = EXCLUDED.granted_at,
    expires_at            = EXCLUDED.expires_at,
    kind                  = EXCLUDED.kind,
    active                = EXCLUDED.active,
    snapshot_gen          = EXCLUDED.snapshot_gen`

// upsertAttributeSQLA writes one actor_attribute row. PK is
// (actor_id, slug). created_at is deliberately omitted from BOTH the
// INSERT column list (falls back to schema DEFAULT now() on new rows)
// AND the ON CONFLICT SET (existing rows keep their original created_at)
// — same v1↔v2 column-scope posture as the parent actor row. params is
// bound as text + cast to jsonb (pgx encodes a Go string as text; the
// explicit ::jsonb cast is unambiguous).
const upsertAttributeSQLA = `
INSERT INTO actor_attribute (
    actor_id, slug, params, snapshot_gen
) VALUES (
    $1, $2, $3::jsonb, $4
)
ON CONFLICT (actor_id, slug) DO UPDATE SET
    params       = EXCLUDED.params,
    snapshot_gen = EXCLUDED.snapshot_gen`

const deleteStaleDwellCreditSQLA = `DELETE FROM actor_dwell_credit  WHERE snapshot_gen < $1`
const deleteStaleRoomAccessSQLA = `DELETE FROM room_access         WHERE snapshot_gen < $1`
const deleteStaleAttributeSQLA = `DELETE FROM actor_attribute     WHERE snapshot_gen < $1`

const nextGenDwellCreditSQLA = `SELECT nextval('actor_dwell_credit_snapshot_gen_seq')`
const nextGenRoomAccessSQLA = `SELECT nextval('room_access_snapshot_gen_seq')`
const nextGenAttributeSQLA = `SELECT nextval('actor_attribute_snapshot_gen_seq')`

// --- LLM-77 known-places tier (durable world-memory) ---

// loadAllKnownPlacesSQLA selects every actor_known_place row. affordances
// comes back as raw JSONB bytes (a JSON array of capability-token strings),
// unmarshalled in Go. place_ref is a uuid; ::text so it scans straight into
// the PlaceRef string (same posture as actor_id and the structure refs).
const loadAllKnownPlacesSQLA = `
SELECT
    actor_id::text, place_ref::text, place_kind, affordances,
    first_learned_at, last_experienced_at
  FROM actor_known_place`

// upsertKnownPlaceSQLA writes one actor_known_place row. PK is
// (actor_id, place_ref). affordances is bound as text + cast to jsonb (same
// posture as salient_facts). first_learned_at / last_experienced_at are
// written verbatim from the in-memory values — pg stays a faithful mirror.
const upsertKnownPlaceSQLA = `
INSERT INTO actor_known_place (
    actor_id, place_ref, place_kind, affordances,
    first_learned_at, last_experienced_at, snapshot_gen
) VALUES (
    $1, $2, $3, $4::jsonb,
    $5, $6, $7
)
ON CONFLICT (actor_id, place_ref) DO UPDATE SET
    place_kind          = EXCLUDED.place_kind,
    affordances         = EXCLUDED.affordances,
    first_learned_at    = EXCLUDED.first_learned_at,
    last_experienced_at = EXCLUDED.last_experienced_at,
    snapshot_gen        = EXCLUDED.snapshot_gen`

const deleteStaleKnownPlaceSQLA = `DELETE FROM actor_known_place   WHERE snapshot_gen < $1`

const nextGenKnownPlaceSQLA = `SELECT nextval('actor_known_place_snapshot_gen_seq')`

// LoadAll loads every actor row plus its needs and inventory children.
//
// Runs against the pool directly (no Tx — read-only restart path).
// Relies on LoadAll running before the world goroutine starts and
// before any checkpoint writer can mutate these tables. Without that
// startup guarantee, the three queries could observe different
// committed states under READ COMMITTED.
//
// Orphan child rows (FK to a missing parent actor_id) surface as an
// error — FK CASCADE makes this unreachable from valid writes; the
// guard surfaces schema drift loudly.
//
// ephemeral fields (Warrants, RecentActions, MoveIntent, ring buffers,
// etc.) are left at Go zero values; they regenerate on first reactor
// activity. See engine/sim/actor.go for the per-field ephemeral
// annotations.
func (r *ActorsRepo) LoadAll(ctx context.Context) (map[sim.ActorID]*sim.Actor, error) {
	rows, err := r.pool.Query(ctx, loadAllSQLA)
	if err != nil {
		return nil, fmt.Errorf("pg actors LoadAll query: %w", err)
	}
	defer rows.Close()

	out := make(map[sim.ActorID]*sim.Actor)
	for rows.Next() {
		var (
			id                  string
			displayName         string
			currentX, currentY  int
			insideStructureID   *string
			currentHuddleID     *string
			insideRoomID        *int64
			homeStructureID     *string
			workStructureID     *string
			coins               int
			llmMemoryAgent      *string
			role                *string
			loginUsername       *string
			scheduleStartMinute *int16
			scheduleEndMinute   *int16
			lastAgentTickAt     *time.Time
			breakUntil          *time.Time
			sleepingUntil       *time.Time
			moveAttemptCounter  int64
			simState            string
			spriteID            *string
			facing              string
			isAdmin             bool
			moveDestination     []byte
			productionItem      string
			productionBatchQty  int
			productionRemaining int64
		)
		if err := rows.Scan(
			&id, &displayName, &currentX, &currentY,
			&insideStructureID, &currentHuddleID, &insideRoomID,
			&homeStructureID, &workStructureID,
			&coins, &llmMemoryAgent, &role, &loginUsername,
			&scheduleStartMinute, &scheduleEndMinute,
			&lastAgentTickAt, &breakUntil, &sleepingUntil,
			&moveAttemptCounter, &simState,
			&spriteID, &facing,
			&isAdmin, &moveDestination,
			&productionItem, &productionBatchQty, &productionRemaining,
		); err != nil {
			return nil, fmt.Errorf("pg actors LoadAll scan: %w", err)
		}

		// Rehydrate an in-flight production cycle (LLM-319). LastProgressAt is
		// deliberately left zero: the first post-restart produce tick stamps the
		// anchor without crediting, so engine downtime never counts as work.
		var productionActivity *sim.ProductionActivity
		if productionItem != "" && productionRemaining > 0 {
			productionActivity = &sim.ProductionActivity{
				Item:             sim.ItemKind(productionItem),
				BatchQty:         productionBatchQty,
				RemainingSeconds: productionRemaining,
			}
		}

		resumeDest, err := decodeMoveDestination(moveDestination)
		if err != nil {
			// A malformed blob must not block the world load — the walk is
			// best-effort recovery state, not substrate. Log and drop it.
			log.Printf("pg actors LoadAll: actor %s: dropping malformed move_destination: %v", id, err)
			resumeDest = nil
		}

		var roomID sim.RoomID
		if insideRoomID != nil {
			roomID = sim.RoomID(*insideRoomID)
		}

		a := &sim.Actor{
			ID:                 sim.ActorID(id),
			DisplayName:        displayName,
			Kind:               sim.ClassifyActorKind(deref(loginUsername), deref(llmMemoryAgent)),
			Pos:                sim.TilePos{X: currentX, Y: currentY},
			InsideStructureID:  sim.StructureID(deref(insideStructureID)),
			CurrentHuddleID:    sim.HuddleID(deref(currentHuddleID)),
			InsideRoomID:       roomID,
			HomeStructureID:    sim.StructureID(deref(homeStructureID)),
			WorkStructureID:    sim.StructureID(deref(workStructureID)),
			Coins:              coins,
			LLMAgent:           deref(llmMemoryAgent),
			Role:               deref(role),
			LoginUsername:      deref(loginUsername),
			ScheduleStartMin:   derefInt16(scheduleStartMinute),
			ScheduleEndMin:     derefInt16(scheduleEndMinute),
			LastTickedAt:       lastAgentTickAt,
			BreakUntil:         breakUntil,
			SleepingUntil:      sleepingUntil,
			MoveAttemptCounter: sim.MovementAttemptID(moveAttemptCounter),
			State:              sim.ActorState(simState),
			SpriteID:           sim.SpriteID(deref(spriteID)),
			Facing:             facing,
			IsAdmin:            isAdmin,
			ProductionActivity: productionActivity,
			ResumeDestination:  resumeDest,
			Needs:              make(map[sim.NeedKey]int),
			Inventory:          make(map[sim.ItemKind]int),
			Relationships:      make(map[sim.ActorID]*sim.Relationship),
			Acquaintances:      make(map[string]sim.Acquaintance),
			DwellCredits:       make(map[sim.DwellCreditKey]*sim.DwellCredit),
			RoomAccess:         make(map[sim.RoomAccessKey]*sim.RoomAccess),
			Attributes:         make(map[string][]byte),
			KnownPlaces:        make(map[sim.PlaceRef]*sim.KnownPlace),
		}
		out[a.ID] = a
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg actors LoadAll iter: %w", err)
	}

	if err := r.loadAllNeeds(ctx, out); err != nil {
		return nil, err
	}
	if err := r.loadAllInventory(ctx, out); err != nil {
		return nil, err
	}
	if err := r.loadAllRelationships(ctx, out); err != nil {
		return nil, err
	}
	if err := r.loadAllNarrative(ctx, out); err != nil {
		return nil, err
	}
	if err := r.loadAllAcquaintances(ctx, out); err != nil {
		return nil, err
	}
	if err := r.loadAllDwellCredits(ctx, out); err != nil {
		return nil, err
	}
	if err := r.loadAllRoomAccess(ctx, out); err != nil {
		return nil, err
	}
	if err := r.loadAllAttributes(ctx, out); err != nil {
		return nil, err
	}
	if err := r.loadAllKnownPlaces(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

// loadAllNeeds reads every actor_need row and attaches it to the parent
// actor's Needs map. Orphan rows return an error.
func (r *ActorsRepo) loadAllNeeds(ctx context.Context, actors map[sim.ActorID]*sim.Actor) error {
	rows, err := r.pool.Query(ctx, loadAllNeedsSQLA)
	if err != nil {
		return fmt.Errorf("pg actors LoadAll needs query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			actorID string
			key     string
			value   int
		)
		if err := rows.Scan(&actorID, &key, &value); err != nil {
			return fmt.Errorf("pg actors LoadAll needs scan: %w", err)
		}
		parent, ok := actors[sim.ActorID(actorID)]
		if !ok {
			return fmt.Errorf("pg actors LoadAll: orphan need row actor_id=%s key=%s (parent missing — schema drift or out-of-band write)",
				actorID, key)
		}
		parent.Needs[sim.NeedKey(key)] = value
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("pg actors LoadAll needs iter: %w", err)
	}
	return nil
}

// loadAllInventory reads every actor_inventory row and attaches it to
// the parent actor's Inventory map. Orphan rows return an error.
func (r *ActorsRepo) loadAllInventory(ctx context.Context, actors map[sim.ActorID]*sim.Actor) error {
	rows, err := r.pool.Query(ctx, loadAllInventorySQLA)
	if err != nil {
		return fmt.Errorf("pg actors LoadAll inventory query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			actorID  string
			itemKind string
			quantity int
			usesLeft *int
		)
		if err := rows.Scan(&actorID, &itemKind, &quantity, &usesLeft); err != nil {
			return fmt.Errorf("pg actors LoadAll inventory scan: %w", err)
		}
		parent, ok := actors[sim.ActorID(actorID)]
		if !ok {
			return fmt.Errorf("pg actors LoadAll: orphan inventory row actor_id=%s item_kind=%s (parent missing — schema drift or out-of-band write)",
				actorID, itemKind)
		}
		parent.Inventory[sim.ItemKind(itemKind)] = quantity
		// Durable-tool wear (LLM-330): a positive uses_left restores the
		// in-use unit's remaining uses. NULL / non-positive means fresh —
		// applyToolWear treats a missing entry as full durability.
		if usesLeft != nil && *usesLeft > 0 {
			if parent.ToolWear == nil {
				parent.ToolWear = make(map[sim.ItemKind]int)
			}
			parent.ToolWear[sim.ItemKind(itemKind)] = *usesLeft
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("pg actors LoadAll inventory iter: %w", err)
	}
	return nil
}

// loadAllRelationships reads every actor_relationship row and attaches
// it to the owning actor's Relationships map (keyed by the peer's
// ActorID). salient_facts arrives as raw JSONB bytes and is unmarshalled
// via the salientFactRow DTO. Orphan rows (owning actor missing) return
// an error.
//
// Peer refs are NOT validated against the loaded actor set here — a
// relationship pointing at an actor absent from world state is tolerated
// (the live consolidation / perception code already skips unresolvable
// peers). This is deliberately more lenient than Slice 1's structure-ref
// validation; see the actors-pg codebase note.
func (r *ActorsRepo) loadAllRelationships(ctx context.Context, actors map[sim.ActorID]*sim.Actor) error {
	rows, err := r.pool.Query(ctx, loadAllRelationshipsSQLA)
	if err != nil {
		return fmt.Errorf("pg actors LoadAll relationships query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			actorID            string
			otherActorID       string
			summaryText        string
			salientFactsJSON   []byte
			interactionCount   int
			lastInteractionAt  *time.Time
			lastConsolidatedAt *time.Time
			createdAt          time.Time
			updatedAt          time.Time
			droppedFactCount   int
		)
		if err := rows.Scan(
			&actorID, &otherActorID, &summaryText, &salientFactsJSON,
			&interactionCount, &lastInteractionAt, &lastConsolidatedAt,
			&createdAt, &updatedAt, &droppedFactCount,
		); err != nil {
			return fmt.Errorf("pg actors LoadAll relationships scan: %w", err)
		}
		parent, ok := actors[sim.ActorID(actorID)]
		if !ok {
			return fmt.Errorf("pg actors LoadAll: orphan relationship row actor_id=%s other=%s (parent missing — schema drift or out-of-band write)",
				actorID, otherActorID)
		}
		// Load-side shape validation, symmetric with SaveSnapshot's
		// pre-pass. v2's posture is "Go owns the invariants" (the schema
		// deliberately omits CHECKs that would refuse engine output — see
		// sim_state). So Load is the enforcement layer for data arriving
		// from out-of-band writes / legacy rows, not the DB. Peer
		// resolution against world state stays lenient (handled by the
		// live consolidation/render code); only shape is enforced here.
		if strings.TrimSpace(otherActorID) == "" {
			return fmt.Errorf("pg actors LoadAll: relationship actor_id=%s has empty other_actor_id", actorID)
		}
		if sim.ActorID(otherActorID) == parent.ID {
			return fmt.Errorf("pg actors LoadAll: relationship actor_id=%s has self-relationship (other == self)", actorID)
		}
		if interactionCount < 0 {
			return fmt.Errorf("pg actors LoadAll: relationship actor_id=%s other=%s interaction_count=%d out of range (must be >= 0)", actorID, otherActorID, interactionCount)
		}
		if droppedFactCount < 0 {
			return fmt.Errorf("pg actors LoadAll: relationship actor_id=%s other=%s dropped_fact_count=%d out of range (must be >= 0)", actorID, otherActorID, droppedFactCount)
		}
		facts, err := unmarshalSalientFacts(salientFactsJSON)
		if err != nil {
			return fmt.Errorf("pg actors LoadAll: relationship actor_id=%s other=%s salient_facts unmarshal: %w",
				actorID, otherActorID, err)
		}
		parent.Relationships[sim.ActorID(otherActorID)] = &sim.Relationship{
			SummaryText:        summaryText,
			SalientFacts:       facts,
			InteractionCount:   interactionCount,
			LastInteractionAt:  lastInteractionAt,
			LastConsolidatedAt: lastConsolidatedAt,
			CreatedAt:          createdAt,
			UpdatedAt:          updatedAt,
			DroppedFactCount:   droppedFactCount,
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("pg actors LoadAll relationships iter: %w", err)
	}
	return nil
}

// loadAllNarrative reads every actor_narrative_state row (1:1 with
// actor) and attaches it to the owning actor's Narrative pointer.
// Orphan rows return an error.
func (r *ActorsRepo) loadAllNarrative(ctx context.Context, actors map[sim.ActorID]*sim.Actor) error {
	rows, err := r.pool.Query(ctx, loadAllNarrativeSQLA)
	if err != nil {
		return fmt.Errorf("pg actors LoadAll narrative query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			actorID            string
			seedText           string
			evolvingSummary    string
			aboutMe            string
			lastConsolidatedAt *time.Time
			createdAt          time.Time
			updatedAt          time.Time
		)
		if err := rows.Scan(
			&actorID, &seedText, &evolvingSummary, &aboutMe,
			&lastConsolidatedAt, &createdAt, &updatedAt,
		); err != nil {
			return fmt.Errorf("pg actors LoadAll narrative scan: %w", err)
		}
		parent, ok := actors[sim.ActorID(actorID)]
		if !ok {
			return fmt.Errorf("pg actors LoadAll: orphan narrative row actor_id=%s (parent missing — schema drift or out-of-band write)",
				actorID)
		}
		parent.Narrative = &sim.NarrativeState{
			SeedText:           seedText,
			EvolvingSummary:    evolvingSummary,
			AboutMe:            aboutMe,
			LastConsolidatedAt: lastConsolidatedAt,
			CreatedAt:          createdAt,
			UpdatedAt:          updatedAt,
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("pg actors LoadAll narrative iter: %w", err)
	}
	return nil
}

// loadAllAcquaintances reads every npc_acquaintance row and attaches it
// to the owning actor's Acquaintances map (keyed by the other party's
// display name / character name). Orphan rows return an error.
func (r *ActorsRepo) loadAllAcquaintances(ctx context.Context, actors map[sim.ActorID]*sim.Actor) error {
	rows, err := r.pool.Query(ctx, loadAllAcquaintancesSQLA)
	if err != nil {
		return fmt.Errorf("pg actors LoadAll acquaintances query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			actorID           string
			otherName         string
			firstInteractedAt time.Time
		)
		if err := rows.Scan(&actorID, &otherName, &firstInteractedAt); err != nil {
			return fmt.Errorf("pg actors LoadAll acquaintances scan: %w", err)
		}
		parent, ok := actors[sim.ActorID(actorID)]
		if !ok {
			return fmt.Errorf("pg actors LoadAll: orphan acquaintance row actor_id=%s other_name=%s (parent missing — schema drift or out-of-band write)",
				actorID, otherName)
		}
		// Load-side shape validation, symmetric with SaveSnapshot.
		if strings.TrimSpace(otherName) == "" {
			return fmt.Errorf("pg actors LoadAll: acquaintance actor_id=%s has empty other_name", actorID)
		}
		if utf8.RuneCountInString(otherName) > 100 {
			return fmt.Errorf("pg actors LoadAll: acquaintance actor_id=%s other_name=%q exceeds 100 chars", actorID, otherName)
		}
		parent.Acquaintances[otherName] = sim.Acquaintance{FirstInteractedAt: firstInteractedAt}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("pg actors LoadAll acquaintances iter: %w", err)
	}
	return nil
}

// loadAllDwellCredits reads every actor_dwell_credit row and attaches it
// to the owning actor's DwellCredits map (keyed by object+attribute+
// source). Orphan rows return an error. Load-side shape validation is
// symmetric with SaveSnapshot's pre-pass — v2's posture is "Go owns the
// invariants," so Load enforces the same shape the baseline CHECKs would,
// surfacing out-of-band / legacy bad rows as clean errors.
func (r *ActorsRepo) loadAllDwellCredits(ctx context.Context, actors map[sim.ActorID]*sim.Actor) error {
	rows, err := r.pool.Query(ctx, loadAllDwellCreditsSQLA)
	if err != nil {
		return fmt.Errorf("pg actors LoadAll dwell credits query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			actorID            string
			objectID           string
			attribute          string
			source             string
			lastCreditedAt     time.Time
			remainingTicks     *int
			dwellDelta         int
			dwellPeriodMinutes int
		)
		if err := rows.Scan(
			&actorID, &objectID, &attribute, &source,
			&lastCreditedAt, &remainingTicks, &dwellDelta, &dwellPeriodMinutes,
		); err != nil {
			return fmt.Errorf("pg actors LoadAll dwell credits scan: %w", err)
		}
		parent, ok := actors[sim.ActorID(actorID)]
		if !ok {
			return fmt.Errorf("pg actors LoadAll: orphan dwell credit row actor_id=%s object=%s attr=%s (parent missing — schema drift or out-of-band write)",
				actorID, objectID, attribute)
		}
		if err := validateDwellCreditShape(actorID, objectID, attribute, source, remainingTicks, dwellDelta, dwellPeriodMinutes); err != nil {
			return fmt.Errorf("pg actors LoadAll: %w", err)
		}
		key := sim.DwellCreditKey{
			ObjectID:  sim.VillageObjectID(objectID),
			Attribute: sim.NeedKey(attribute),
			Source:    sim.DwellCreditSource(source),
		}
		parent.DwellCredits[key] = &sim.DwellCredit{
			ObjectID:           sim.VillageObjectID(objectID),
			Attribute:          sim.NeedKey(attribute),
			Source:             sim.DwellCreditSource(source),
			LastCreditedAt:     lastCreditedAt,
			RemainingTicks:     remainingTicks,
			DwellDelta:         dwellDelta,
			DwellPeriodMinutes: dwellPeriodMinutes,
			// Kind is not persisted (no column); see loadAllDwellCreditsSQLA.
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("pg actors LoadAll dwell credits iter: %w", err)
	}
	return nil
}

// productionItemArg / productionBatchQtyArg / productionRemainingArg project
// an actor's in-flight production cycle (LLM-319) onto the actor row's three
// production columns. A nil activity writes the idle sentinel (”, 0, 0) —
// the same shape the load side reads back as "no cycle".
func productionItemArg(pa *sim.ProductionActivity) string {
	if pa == nil {
		return ""
	}
	return string(pa.Item)
}

func productionBatchQtyArg(pa *sim.ProductionActivity) int {
	if pa == nil {
		return 0
	}
	return pa.BatchQty
}

func productionRemainingArg(pa *sim.ProductionActivity) int64 {
	if pa == nil {
		return 0
	}
	return pa.RemainingSeconds
}

// loadAllRoomAccess reads every room_access row and attaches it to the
// owning actor's RoomAccess map (keyed by room+source). Orphan rows
// return an error. Source is derived from granted_via_ledger_id:
// non-NULL → ledger (with LedgerID); NULL → staff. `kind` is intentionally
// not read (canEnterRoom recomputes room category from the Room).
func (r *ActorsRepo) loadAllRoomAccess(ctx context.Context, actors map[sim.ActorID]*sim.Actor) error {
	rows, err := r.pool.Query(ctx, loadAllRoomAccessSQLA)
	if err != nil {
		return fmt.Errorf("pg actors LoadAll room access query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			actorID            string
			roomID             int64
			grantedViaLedgerID *int64
			grantedAt          time.Time
			expiresAt          *time.Time
			active             bool
		)
		if err := rows.Scan(&actorID, &roomID, &grantedViaLedgerID, &grantedAt, &expiresAt, &active); err != nil {
			return fmt.Errorf("pg actors LoadAll room access scan: %w", err)
		}
		parent, ok := actors[sim.ActorID(actorID)]
		if !ok {
			return fmt.Errorf("pg actors LoadAll: orphan room access row actor_id=%s room_id=%d (parent missing — schema drift or out-of-band write)",
				actorID, roomID)
		}
		source := sim.AccessSourceStaff
		var ledgerID int64
		if grantedViaLedgerID != nil {
			source = sim.AccessSourceLedger
			ledgerID = *grantedViaLedgerID
		}
		// Shape validation symmetric with SaveSnapshot — a non-positive
		// granted_via_ledger_id derives source=ledger but stores an invalid
		// LedgerID, which Save would reject; catch it on load too.
		if err := validateRoomAccessShape(roomID, source, ledgerID); err != nil {
			return fmt.Errorf("pg actors LoadAll: actor_id=%s %w", actorID, err)
		}
		key := sim.RoomAccessKey{RoomID: sim.RoomID(roomID), Source: source}
		if _, exists := parent.RoomAccess[key]; exists {
			return fmt.Errorf("pg actors LoadAll: duplicate room access actor_id=%s room_id=%d source=%s (PK is (room_id, actor_id) — schema drift or out-of-band write)",
				actorID, roomID, source)
		}
		parent.RoomAccess[key] = &sim.RoomAccess{
			RoomID:    sim.RoomID(roomID),
			Source:    source,
			LedgerID:  ledgerID,
			ExpiresAt: expiresAt,
			Active:    active,
			CreatedAt: grantedAt,
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("pg actors LoadAll room access iter: %w", err)
	}
	return nil
}

// loadAllAttributes reads every actor_attribute row and attaches the raw
// params JSONB to the owning actor's Attributes map (keyed by slug).
// Orphan rows return an error. The repo stays a dumb mirror — the
// businessowner / restock projections are rebuilt from these raw rows in
// a LoadWorld carry-forward pass.
func (r *ActorsRepo) loadAllAttributes(ctx context.Context, actors map[sim.ActorID]*sim.Actor) error {
	rows, err := r.pool.Query(ctx, loadAllAttributesSQLA)
	if err != nil {
		return fmt.Errorf("pg actors LoadAll attributes query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			actorID string
			slug    string
			params  []byte
		)
		if err := rows.Scan(&actorID, &slug, &params); err != nil {
			return fmt.Errorf("pg actors LoadAll attributes scan: %w", err)
		}
		parent, ok := actors[sim.ActorID(actorID)]
		if !ok {
			return fmt.Errorf("pg actors LoadAll: orphan attribute row actor_id=%s slug=%s (parent missing — schema drift or out-of-band write)",
				actorID, slug)
		}
		if strings.TrimSpace(slug) == "" {
			return fmt.Errorf("pg actors LoadAll: attribute actor_id=%s has empty slug", actorID)
		}
		// Shape validation symmetric with SaveSnapshot (slug ≤ VARCHAR(64),
		// params valid JSON). DB-origin jsonb rows are always valid, but
		// this catches legacy / out-of-band drift before it survives into
		// the projection rebuild or a later checkpoint.
		if utf8.RuneCountInString(slug) > 64 {
			return fmt.Errorf("pg actors LoadAll: attribute actor_id=%s slug=%q exceeds 64 chars", actorID, slug)
		}
		if len(params) > 0 && !json.Valid(params) {
			return fmt.Errorf("pg actors LoadAll: attribute actor_id=%s slug=%q has invalid JSON params", actorID, slug)
		}
		// Copy the scan buffer — pgx may reuse the underlying array across
		// rows.Next() calls, so storing the slice directly would alias.
		buf := make([]byte, len(params))
		copy(buf, params)
		parent.Attributes[slug] = buf
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("pg actors LoadAll attributes iter: %w", err)
	}
	return nil
}

// loadAllKnownPlaces reads every actor_known_place row and attaches it to the
// owning actor's KnownPlaces map (keyed by PlaceRef). affordances arrives as
// raw JSONB bytes (a JSON array of capability tokens). Orphan rows (owning
// actor missing) return an error. Load-side shape validation is symmetric with
// SaveSnapshot's pre-pass — v2's posture is "Go owns the invariants" (the
// schema deliberately omits a place_kind CHECK, see sim_state), so Load is the
// enforcement layer for the kind allowlist + non-empty ref against out-of-band
// / legacy rows. LLM-77.
func (r *ActorsRepo) loadAllKnownPlaces(ctx context.Context, actors map[sim.ActorID]*sim.Actor) error {
	rows, err := r.pool.Query(ctx, loadAllKnownPlacesSQLA)
	if err != nil {
		return fmt.Errorf("pg actors LoadAll known_places query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			actorID           string
			placeRef          string
			placeKind         string
			affordancesJSON   []byte
			firstLearnedAt    time.Time
			lastExperiencedAt time.Time
		)
		if err := rows.Scan(
			&actorID, &placeRef, &placeKind, &affordancesJSON,
			&firstLearnedAt, &lastExperiencedAt,
		); err != nil {
			return fmt.Errorf("pg actors LoadAll known_places scan: %w", err)
		}
		parent, ok := actors[sim.ActorID(actorID)]
		if !ok {
			return fmt.Errorf("pg actors LoadAll: orphan known_place row actor_id=%s place_ref=%s (parent missing — schema drift or out-of-band write)",
				actorID, placeRef)
		}
		if strings.TrimSpace(placeRef) == "" {
			return fmt.Errorf("pg actors LoadAll: known_place actor_id=%s has empty place_ref", actorID)
		}
		if !sim.PlaceKind(placeKind).Valid() {
			return fmt.Errorf("pg actors LoadAll: known_place actor_id=%s place_ref=%s has unknown place_kind %q", actorID, placeRef, placeKind)
		}
		affordances, err := unmarshalAffordances(affordancesJSON)
		if err != nil {
			return fmt.Errorf("pg actors LoadAll: known_place actor_id=%s place_ref=%s affordances unmarshal: %w",
				actorID, placeRef, err)
		}
		// Affordance-token shape, symmetric with SaveSnapshot's pre-pass: reject
		// empty/whitespace tokens from out-of-band / legacy rows. Token FORMAT
		// (the "<cap>:<detail>" convention) and sortedness are recorder
		// niceties, deliberately NOT enforced here — the vocabulary grows with
		// later consumers (LLM-78/79) and a hard format CHECK would wedge every
		// checkpoint the moment a new token shape lands (the sim_state posture).
		for _, aff := range affordances {
			if strings.TrimSpace(aff) == "" {
				return fmt.Errorf("pg actors LoadAll: known_place actor_id=%s place_ref=%s has an empty affordance token", actorID, placeRef)
			}
		}
		parent.KnownPlaces[sim.PlaceRef(placeRef)] = &sim.KnownPlace{
			Ref:               sim.PlaceRef(placeRef),
			Kind:              sim.PlaceKind(placeKind),
			Affordances:       affordances,
			FirstLearnedAt:    firstLearnedAt,
			LastExperiencedAt: lastExperiencedAt,
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("pg actors LoadAll known_places iter: %w", err)
	}
	return nil
}

// SaveSnapshot writes the full actor aggregate (eleven tiers) durably
// using the generation-marker pattern (Slice 9/10/11/12/13 standard).
//
// Visitor actors (VisitorState != nil) are filtered out entirely —
// they're transient by design and persisting them would leave stale
// visitor rows after restart that the visitor cascade has no reload
// path for. The filter is at the parent level, so visitor relationships
// / narrative / acquaintance are excluded structurally with no extra
// guard. See engine/sim/visitor.go and the visitor codebase note.
//
// Steps inside the caller's checkpoint Tx (order matters — validation
// runs first so shape errors abort BEFORE the advisory lock; parent
// settles before children sync). Each tier owns its own gen sequence;
// all ten share the one advisory lock:
//
//  0. Pre-pass validation: nil entries, empty/whitespace IDs, map-key
//     vs a.ID mismatch, empty DisplayName/State,
//     half-set / out-of-range schedule, need values out of range,
//     empty need-key / inventory-kind, negative inventory, empty
//     relationship peer key, self-relationship, negative relationship
//     counts, empty / over-length acquaintance name, dwell-credit shape
//     (source allowlist, remaining↔source pairing, dwell_delta < 0,
//     period > 0), malformed production activity (empty item /
//     non-positive batch or remaining — LLM-319), room-access
//     room>0 + source/ledger-id pairing + per-room single-source, empty
//     attribute slug / over-length / invalid JSON params.
//  1. Advisory lock — shared by all nine tables.
//     2-4.   actor  : nextval → UPSERT → DELETE stale (FK CASCADE drops
//     children of absent parents). The in-flight production cycle
//     (LLM-319) rides the actor row itself (production_item /
//     production_batch_qty / production_remaining_seconds).
//     5-7.   actor_need        : nextval → UPSERT → DELETE stale.
//     8-10.  actor_inventory   : nextval → UPSERT (skip zero-qty) → DELETE.
//     11-13. actor_relationship: nextval → UPSERT (skip nil) → DELETE.
//     14-16. actor_narrative_state: nextval → UPSERT (skip nil Narrative)
//     → DELETE.
//     17-19. npc_acquaintance  : nextval → UPSERT → DELETE stale.
//     20-22. actor_dwell_credit : nextval → UPSERT → DELETE stale.
//     23-25. room_access        : nextval → UPSERT (skip nil) → DELETE.
//     26-28. actor_attribute    : nextval → UPSERT → DELETE stale.
//
// Empty actors map: all nine gens still bump, no UPSERTs run, all nine
// DELETEs sweep their tables.
//
// nil actor entries surface as an error (structures.go precedent;
// silent skip would mask command-handler bugs).
func (r *ActorsRepo) SaveSnapshot(ctx context.Context, tx sim.Tx, actors map[sim.ActorID]*sim.Actor) error {
	if tx == nil {
		return fmt.Errorf("pg actors SaveSnapshot: nil tx")
	}

	// Step 0: validate the whole snapshot in a pre-pass — BEFORE the
	// advisory lock, so validation failures don't block behind another
	// in-flight checkpoint or take the lock unnecessarily. Catches shape
	// bugs (empty IDs, map-key/a.ID mismatch, empty DisplayName, out-of-
	// range schedule/need/inventory values) cleanly. Visitor filter ALSO
	// happens here: visitor actors don't need to satisfy the substrate
	// rules for persisted actors.
	//
	// Validation uses strings.TrimSpace on string fields to match the
	// DB-side btrim CHECKs / NOT-NULL implicit empty checks — without
	// that, a whitespace-only ID / DisplayName passes Go validation and
	// trips the CHECK mid-Tx, producing a worse error than a clean
	// substrate rejection. (Slice 12 R1 precedent.)
	//
	// Range checks are explicit (not relying on int16/SMALLINT narrowing)
	// because Go-side int16(v) wraps silently — a 40000-minute value
	// would pgx-encode as a wrapped negative without ever tripping the
	// SMALLINT range error. (Slice 1 R1 precedent.)
	q := quarantineOf(tx)
	persisted := validateActorsSnapshot(actors, q)

	// Step 1: advisory lock — serializes concurrent actor SaveSnapshot.
	// AFTER validation so shape-error returns don't take the lock.
	if _, err := tx.Exec(ctx, advisoryLockSQLA); err != nil {
		return fmt.Errorf("pg actors SaveSnapshot: advisory lock: %w", err)
	}

	// Step 2: parent gen.
	var actorGen int64
	if err := tx.QueryRow(ctx, nextGenSQLA).Scan(&actorGen); err != nil {
		return fmt.Errorf("pg actors SaveSnapshot: nextval actor: %w", err)
	}

	// Step 3: upsert each persisted actor.
	for _, a := range persisted {
		// An invalid Facing is row CONTENT, so it must not veto the checkpoint
		// (LLM-392). validateFacing already maps "" -> the default heading, so a
		// bad value is clamped the same way rather than costing the actor.
		facing, err := validateFacing(a.Facing)
		if err != nil {
			q.Clamp("actor", string(a.ID), fmt.Sprintf("invalid facing %q, clamped to the default heading: %v", a.Facing, err))
			facing, _ = validateFacing("")
		}
		if _, err := tx.Exec(ctx, upsertSQLA,
			string(a.ID),                                 // $1 id
			a.DisplayName,                                // $2 display_name
			a.Pos.X,                                      // $3 current_x
			a.Pos.Y,                                      // $4 current_y
			nilOnEmpty(string(a.InsideStructureID)),      // $5 inside_structure_id
			nilOnEmpty(string(a.CurrentHuddleID)),        // $6 current_huddle_id
			nilOnZero(int64(a.InsideRoomID)),             // $7 inside_room_id
			nilOnEmpty(string(a.HomeStructureID)),        // $8 home_structure_id
			nilOnEmpty(string(a.WorkStructureID)),        // $9 work_structure_id
			a.Coins,                                      // $10 coins
			nilOnEmpty(a.LLMAgent),                       // $11 llm_memory_agent
			nilOnEmpty(a.Role),                           // $12 role
			nilOnEmpty(a.LoginUsername),                  // $13 login_username
			intPtrToSQL(a.ScheduleStartMin),              // $14 schedule_start_minute
			intPtrToSQL(a.ScheduleEndMin),                // $15 schedule_end_minute
			a.LastTickedAt,                               // $16 last_agent_tick_at
			a.BreakUntil,                                 // $17 break_until
			a.SleepingUntil,                              // $18 sleeping_until
			int64(a.MoveAttemptCounter),                  // $19 move_attempt_counter
			string(a.State),                              // $20 sim_state
			nilOnEmpty(string(a.SpriteID)),               // $21 sprite_id (nullable uuid)
			facing,                                       // $22 facing (validated above)
			actorGen,                                     // $23 snapshot_gen
			encodeMoveDestination(a.MoveIntent),          // $24 move_destination
			productionItemArg(a.ProductionActivity),      // $25 production_item
			productionBatchQtyArg(a.ProductionActivity),  // $26 production_batch_qty
			productionRemainingArg(a.ProductionActivity), // $27 production_remaining_seconds
		); err != nil {
			return fmt.Errorf("pg actors SaveSnapshot: upsert actor id=%s: %w", a.ID, err)
		}
	}

	// Step 4: prune absent parents. FK CASCADE drops their needs +
	// inventory rows automatically.
	if err := execSweep(ctx, tx, "actor", deleteStaleSQLA, actorGen); err != nil {
		return fmt.Errorf("pg actors SaveSnapshot: delete stale actor: %w", err)
	}

	// Step 5: need gen — independent tier.
	var needGen int64
	if err := tx.QueryRow(ctx, nextGenNeedSQLA).Scan(&needGen); err != nil {
		return fmt.Errorf("pg actors SaveSnapshot: nextval need: %w", err)
	}

	// Step 6: upsert each non-zero need. (Value 0 is a real, meaningful
	// state — "this need is fully satisfied" — and stored explicitly
	// because the absence of a row vs. a 0-value row is semantically
	// different: missing row means the need has never been observed,
	// while value=0 means "satisfied right now." Keep the row.)
	for _, a := range persisted {
		for k, v := range a.Needs {
			if q.Dropped("actor_need", childID(a.ID, string(k))) {
				continue
			}
			if _, err := tx.Exec(ctx, upsertNeedSQLA,
				string(a.ID), // $1 actor_id
				string(k),    // $2 key
				v,            // $3 value
				needGen,      // $4 snapshot_gen
			); err != nil {
				return fmt.Errorf("pg actors SaveSnapshot: upsert need actor=%s key=%s: %w", a.ID, k, err)
			}
		}
	}

	// Step 7: prune absent need rows.
	if err := execSweep(ctx, tx, "actor_need", deleteStaleNeedSQLA, needGen); err != nil {
		return fmt.Errorf("pg actors SaveSnapshot: delete stale need: %w", err)
	}

	// Step 8: inventory gen — independent tier.
	var invGen int64
	if err := tx.QueryRow(ctx, nextGenInvSQLA).Scan(&invGen); err != nil {
		return fmt.Errorf("pg actors SaveSnapshot: nextval inventory: %w", err)
	}

	// Step 9: upsert each non-zero inventory entry. quantity > 0 is the
	// CHECK invariant — zero-qty entries are deleted (the validation
	// pre-pass already rejected negative quantities, so qty == 0 is
	// the only skip case here). uses_left carries the durable-tool wear
	// on the kind's in-use unit (LLM-330) — NULL when unworn; a wear
	// entry without a live inventory row rides nothing and is dropped.
	for _, a := range persisted {
		for kind, qty := range a.Inventory {
			if q.Dropped("actor_inventory", childID(a.ID, string(kind))) {
				continue
			}
			if qty == 0 {
				continue
			}
			// Nullable-write idiom (village_objects precedent): nil interface
			// → SQL NULL for ordinary stock / an unworn tool.
			var usesLeft any
			if wear, ok := a.ToolWear[kind]; ok && wear > 0 {
				usesLeft = wear
			}
			if _, err := tx.Exec(ctx, upsertInventorySQLA,
				string(a.ID), // $1 actor_id
				string(kind), // $2 item_kind
				qty,          // $3 quantity
				usesLeft,     // $4 uses_left (nil → NULL)
				invGen,       // $5 snapshot_gen
			); err != nil {
				return fmt.Errorf("pg actors SaveSnapshot: upsert inventory actor=%s item=%s: %w", a.ID, kind, err)
			}
		}
	}

	// Step 10: prune absent inventory rows (catches consumed-to-zero
	// entries that were skipped at step 9 plus item-removed entries).
	if err := execSweep(ctx, tx, "actor_inventory", deleteStaleInvSQLA, invGen); err != nil {
		return fmt.Errorf("pg actors SaveSnapshot: delete stale inventory: %w", err)
	}

	// Step 11: relationship gen — independent tier (Slice 2).
	var relGen int64
	if err := tx.QueryRow(ctx, nextGenRelationshipSQLA).Scan(&relGen); err != nil {
		return fmt.Errorf("pg actors SaveSnapshot: nextval relationship: %w", err)
	}

	// Step 12: upsert each relationship. nil entries are skipped
	// (cloneRelationships precedent — a nil value is a command-handler
	// bug, not a row to persist). salient_facts is marshalled to JSON
	// with lowercase keys to match v1's stored shape.
	for _, a := range persisted {
		for peerID, rel := range a.Relationships {
			if q.Dropped("actor_relationship", childID(a.ID, string(peerID))) {
				continue
			}
			if rel == nil {
				continue
			}
			factsJSON, err := marshalSalientFacts(rel.SalientFacts)
			if err != nil {
				// Unmarshalable facts are row content, not a broken Tx — drop the
				// one relationship, keep the actor and the checkpoint (LLM-392).
				q.Drop("actor_relationship", childID(a.ID, string(peerID)),
					fmt.Sprintf("salient_facts will not marshal: %v", err))
				continue
			}
			if _, err := tx.Exec(ctx, upsertRelationshipSQLA,
				string(a.ID),           // $1 actor_id
				string(peerID),         // $2 other_actor_id
				rel.SummaryText,        // $3 summary_text
				factsJSON,              // $4 salient_facts (::jsonb)
				rel.InteractionCount,   // $5 interaction_count
				rel.LastInteractionAt,  // $6 last_interaction_at
				rel.LastConsolidatedAt, // $7 last_consolidated_at
				rel.CreatedAt,          // $8 created_at
				rel.UpdatedAt,          // $9 updated_at
				rel.DroppedFactCount,   // $10 dropped_fact_count
				relGen,                 // $11 snapshot_gen
			); err != nil {
				return fmt.Errorf("pg actors SaveSnapshot: upsert relationship actor=%s peer=%s: %w", a.ID, peerID, err)
			}
		}
	}

	// Step 13: prune absent relationship rows.
	if err := execSweep(ctx, tx, "actor_relationship", deleteStaleRelationshipSQLA, relGen); err != nil {
		return fmt.Errorf("pg actors SaveSnapshot: delete stale relationship: %w", err)
	}

	// Step 14: narrative gen — independent tier (Slice 2).
	var narrGen int64
	if err := tx.QueryRow(ctx, nextGenNarrativeSQLA).Scan(&narrGen); err != nil {
		return fmt.Errorf("pg actors SaveSnapshot: nextval narrative: %w", err)
	}

	// Step 15: upsert narrative for actors that have one. nil Narrative
	// → no row written; a pre-existing row gets swept by the tier DELETE
	// (an actor whose Narrative was cleared between checkpoints).
	for _, a := range persisted {
		if a.Narrative == nil {
			continue
		}
		if _, err := tx.Exec(ctx, upsertNarrativeSQLA,
			string(a.ID),                   // $1 actor_id
			a.Narrative.SeedText,           // $2 seed_text
			a.Narrative.EvolvingSummary,    // $3 evolving_summary
			a.Narrative.AboutMe,            // $4 about_me
			a.Narrative.LastConsolidatedAt, // $5 last_consolidated_at
			a.Narrative.CreatedAt,          // $6 created_at
			a.Narrative.UpdatedAt,          // $7 updated_at
			narrGen,                        // $8 snapshot_gen
		); err != nil {
			return fmt.Errorf("pg actors SaveSnapshot: upsert narrative actor=%s: %w", a.ID, err)
		}
	}

	// Step 16: prune absent narrative rows.
	if err := execSweep(ctx, tx, "actor_narrative_state", deleteStaleNarrativeSQLA, narrGen); err != nil {
		return fmt.Errorf("pg actors SaveSnapshot: delete stale narrative: %w", err)
	}

	// Step 17: acquaintance gen — independent tier (Slice 2).
	var acqGen int64
	if err := tx.QueryRow(ctx, nextGenAcquaintanceSQLA).Scan(&acqGen); err != nil {
		return fmt.Errorf("pg actors SaveSnapshot: nextval acquaintance: %w", err)
	}

	// Step 18: upsert each acquaintance.
	for _, a := range persisted {
		for name, acq := range a.Acquaintances {
			if q.Dropped("npc_acquaintance", childID(a.ID, name)) {
				continue
			}
			if _, err := tx.Exec(ctx, upsertAcquaintanceSQLA,
				string(a.ID),          // $1 actor_id
				name,                  // $2 other_name
				acq.FirstInteractedAt, // $3 first_interacted_at
				acqGen,                // $4 snapshot_gen
			); err != nil {
				return fmt.Errorf("pg actors SaveSnapshot: upsert acquaintance actor=%s name=%s: %w", a.ID, name, err)
			}
		}
	}

	// Step 19: prune absent acquaintance rows.
	if err := execSweep(ctx, tx, "npc_acquaintance", deleteStaleAcquaintanceSQLA, acqGen); err != nil {
		return fmt.Errorf("pg actors SaveSnapshot: delete stale acquaintance: %w", err)
	}

	// Step 20: dwell-credit gen — independent tier (Slice 3).
	var dwellGen int64
	if err := tx.QueryRow(ctx, nextGenDwellCreditSQLA).Scan(&dwellGen); err != nil {
		return fmt.Errorf("pg actors SaveSnapshot: nextval dwell credit: %w", err)
	}

	// Step 21: upsert each dwell credit. nil entries skipped (pre-pass
	// validated the rest). The map key supplies the PK columns.
	for _, a := range persisted {
		for dk, dc := range a.DwellCredits {
			if q.Dropped("actor_dwell_credit", childID(a.ID, fmt.Sprintf("%s/%s/%s", dk.ObjectID, dk.Attribute, dk.Source))) {
				continue
			}
			if dc == nil {
				continue
			}
			if _, err := tx.Exec(ctx, upsertDwellCreditSQLA,
				string(a.ID),          // $1 actor_id
				string(dk.ObjectID),   // $2 object_id
				string(dk.Attribute),  // $3 attribute
				string(dk.Source),     // $4 source
				dc.LastCreditedAt,     // $5 last_credited_at
				dc.RemainingTicks,     // $6 remaining_ticks (nil for object)
				dc.DwellDelta,         // $7 dwell_delta (< 0)
				dc.DwellPeriodMinutes, // $8 dwell_period_minutes
				dwellGen,              // $9 snapshot_gen
			); err != nil {
				return fmt.Errorf("pg actors SaveSnapshot: upsert dwell credit actor=%s obj=%s attr=%s: %w", a.ID, dk.ObjectID, dk.Attribute, err)
			}
		}
	}

	// Step 22: prune absent dwell-credit rows.
	if err := execSweep(ctx, tx, "actor_dwell_credit", deleteStaleDwellCreditSQLA, dwellGen); err != nil {
		return fmt.Errorf("pg actors SaveSnapshot: delete stale dwell credit: %w", err)
	}

	// (The actor_produce_state tier is retired with continuous auto-produce —
	// LLM-319. The in-flight production cycle rides the actor row itself.)

	// Step 23: room-access gen — independent tier (Slice 3).
	var roomGen int64
	if err := tx.QueryRow(ctx, nextGenRoomAccessSQLA).Scan(&roomGen); err != nil {
		return fmt.Errorf("pg actors SaveSnapshot: nextval room access: %w", err)
	}

	// Step 27: upsert each room-access grant. nil skipped. kind is
	// synthesized from source (ledger→private, staff→staff);
	// granted_via_ledger_id is non-NULL only for ledger grants so the
	// load-side Source derivation round-trips. The partial unique index
	// ux_room_access_one_private_active (one active private occupant per
	// room) could otherwise trip mid-loop if a stale active private row for
	// a room is still present (gen-marker sweeps AFTER upserts) when a new
	// occupant's active row UPSERTs; the pre-pass activePrivateRooms guard
	// rejects a double-occupancy snapshot up front, so this loop never
	// presents two active private claimants for one room to the index.
	for _, a := range persisted {
		for rk, ra := range a.RoomAccess {
			if ra == nil {
				continue
			}
			id := roomAccessID(rk.RoomID, a.ID, rk.Source)
			if q.Dropped("room_access", id) {
				continue
			}
			// room_access carries TWO non-deferred cross-aggregate FKs, and
			// neither can be left to Postgres to police:
			//
			//   granted_via_ledger_id -> pay_ledger(id)   (Orders runs first)
			//   room_id               -> structure_room(id) (Structures runs first)
			//
			// If the referenced row was quarantined by its own writer, this grant
			// must go too. The FK alone is NOT a sufficient guard: it only fires
			// when the parent row is BRAND NEW. Once the parent exists durably
			// from an earlier checkpoint, the FK is perfectly satisfied by the
			// STALE row — so the grant would be written fresh against a superseded
			// payment, or against a room whose definition we just declined to
			// update. Postgres cannot see either as wrong; only we can.
			if ra.LedgerID != 0 && q.Dropped("pay_ledger", ledgerRowID(ra.LedgerID)) {
				q.Drop("room_access", id,
					fmt.Sprintf("ledger row %d was quarantined by Orders — a room grant cannot outlive the payment that bought it", ra.LedgerID))
				continue
			}
			if q.Dropped("structure_room", roomRowID(rk.RoomID)) {
				q.Drop("room_access", id,
					fmt.Sprintf("room %d was quarantined by Structures and has no durable row — a grant cannot reference a room that does not exist", rk.RoomID))
				continue
			}
			if _, err := tx.Exec(ctx, upsertRoomAccessSQLA,
				int64(rk.RoomID),                     // $1 room_id
				string(a.ID),                         // $2 actor_id
				nilOnZero(ra.LedgerID),               // $3 granted_via_ledger_id
				ra.CreatedAt,                         // $4 granted_at
				ra.ExpiresAt,                         // $5 expires_at
				string(roomKindForSource(rk.Source)), // $6 kind
				ra.Active,                            // $7 active
				roomGen,                              // $8 snapshot_gen
			); err != nil {
				return fmt.Errorf("pg actors SaveSnapshot: upsert room access actor=%s room=%d: %w", a.ID, rk.RoomID, err)
			}
		}
	}

	// Step 28: prune absent room-access rows.
	if err := execSweep(ctx, tx, "room_access", deleteStaleRoomAccessSQLA, roomGen); err != nil {
		return fmt.Errorf("pg actors SaveSnapshot: delete stale room access: %w", err)
	}

	// Step 29: attribute gen — independent tier (Slice 3).
	var attrGen int64
	if err := tx.QueryRow(ctx, nextGenAttributeSQLA).Scan(&attrGen); err != nil {
		return fmt.Errorf("pg actors SaveSnapshot: nextval attribute: %w", err)
	}

	// Step 30: upsert each attribute. The raw params bytes are written
	// back verbatim (empty → '{}' for the NOT NULL jsonb column). created_at
	// is preserved by the UPSERT (not in the SET list); new rows get the
	// schema default.
	for _, a := range persisted {
		for slug, params := range a.Attributes {
			if q.Dropped("actor_attribute", childID(a.ID, slug)) {
				continue
			}
			if _, err := tx.Exec(ctx, upsertAttributeSQLA,
				string(a.ID),              // $1 actor_id
				slug,                      // $2 slug
				jsonOrEmptyObject(params), // $3 params (::jsonb)
				attrGen,                   // $4 snapshot_gen
			); err != nil {
				return fmt.Errorf("pg actors SaveSnapshot: upsert attribute actor=%s slug=%s: %w", a.ID, slug, err)
			}
		}
	}

	// Step 31: prune absent attribute rows.
	if err := execSweep(ctx, tx, "actor_attribute", deleteStaleAttributeSQLA, attrGen); err != nil {
		return fmt.Errorf("pg actors SaveSnapshot: delete stale attribute: %w", err)
	}

	// Step 32: known-place gen — independent tier (LLM-77).
	var knownPlaceGen int64
	if err := tx.QueryRow(ctx, nextGenKnownPlaceSQLA).Scan(&knownPlaceGen); err != nil {
		return fmt.Errorf("pg actors SaveSnapshot: nextval known_place: %w", err)
	}

	// Step 33: upsert each known place. nil entries skipped (cloneKnownPlaces
	// precedent). affordances marshalled to a JSON array of capability tokens.
	for _, a := range persisted {
		for ref, kp := range a.KnownPlaces {
			if q.Dropped("actor_known_place", childID(a.ID, string(ref))) {
				continue
			}
			if kp == nil {
				continue
			}
			affJSON, err := marshalAffordances(kp.Affordances)
			if err != nil {
				q.Drop("actor_known_place", childID(a.ID, string(ref)),
					fmt.Sprintf("affordances will not marshal: %v", err))
				continue
			}
			if _, err := tx.Exec(ctx, upsertKnownPlaceSQLA,
				string(a.ID),         // $1 actor_id
				string(ref),          // $2 place_ref
				string(kp.Kind),      // $3 place_kind
				affJSON,              // $4 affordances (::jsonb)
				kp.FirstLearnedAt,    // $5 first_learned_at
				kp.LastExperiencedAt, // $6 last_experienced_at
				knownPlaceGen,        // $7 snapshot_gen
			); err != nil {
				return fmt.Errorf("pg actors SaveSnapshot: upsert known_place actor=%s ref=%s: %w", a.ID, ref, err)
			}
		}
	}

	// Step 34: prune absent known-place rows.
	if err := execSweep(ctx, tx, "actor_known_place", deleteStaleKnownPlaceSQLA, knownPlaceGen); err != nil {
		return fmt.Errorf("pg actors SaveSnapshot: delete stale known_place: %w", err)
	}
	return nil
}

// deref unwraps a *string to its value or empty-string-on-nil. The
// empty-string-sentinel convention says "" round-trips through SQL
// NULL via nilOnEmpty / *string scan.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// derefInt16 converts a *int16 scan target (matching SMALLINT NULL) to
// the Go-side *int sentinel that sim.Actor uses. nil-on-NULL preserved.
func derefInt16(v *int16) *int {
	if v == nil {
		return nil
	}
	x := int(*v)
	return &x
}

// nilOnEmpty returns nil (→ SQL NULL) if the string is empty,
// otherwise returns the string verbatim. Pairs with *string scan
// targets in LoadAll to round-trip the empty-string sentinel through
// SQL NULL.
func nilOnEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// validateFacing maps an actor's Facing to the value written to the facing
// column. actor.facing is NOT NULL with a CHECK constraint restricting it to
// {north,south,east,west}. Empty coalesces to the schema default 'south' —
// the v2 engine doesn't manage facing (the client derives it from movement),
// and 'south' is the table's own DEFAULT, the no-information fallback the
// column already expects. A non-empty value MUST be a valid enum member;
// anything else is rejected here so a bad value surfaces at the offending
// actor rather than failing the whole checkpoint Tx late on the DB CHECK
// (which would also lose every other actor's write in that Tx).
func validateFacing(facing string) (string, error) {
	switch facing {
	case "":
		return "south", nil
	case "north", "south", "east", "west":
		return facing, nil
	default:
		return "", fmt.Errorf("invalid actor facing %q", facing)
	}
}

// nilOnZero is the int64 sibling of nilOnEmpty — used for the
// InsideRoomID 0-sentinel which maps to SQL NULL. RoomID's "0 when not
// in a room" comment is documented at engine/sim/room.go.
func nilOnZero(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// intPtrToSQL converts the Go-side *int (used for ScheduleStartMin /
// ScheduleEndMin) into a value pgx will encode correctly to a NULLABLE
// SMALLINT column. Returns nil for nil-pointer, otherwise an int16.
func intPtrToSQL(v *int) any {
	if v == nil {
		return nil
	}
	x := int16(*v)
	return x
}

// derefTime unwraps a *time.Time scan target (matching a nullable
// TIMESTAMPTZ column) to a value time.Time, returning the Go zero time on
// NULL. Pairs with nilOnZeroTime on the write side.
func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// nilOnZeroTime is the time sibling of nilOnZero — the Go zero time.Time
// (the "never produced" / unset sentinel for ProduceState.LastProducedAt)
// maps to SQL NULL on write.
func nilOnZeroTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// moveDestinationJSON is the actor.move_destination wire shape
// (ZBBS-HOME-449): the in-flight walk's destination, checkpointed so a
// restart can resume the walk. A dedicated struct rather than
// json-tagging sim.MoveDestination keeps the persisted format an
// explicit, owned contract of this layer (sim types stay wire-free).
type moveDestinationJSON struct {
	Kind        string `json:"kind"`
	StructureID string `json:"structure_id,omitempty"`
	ObjectID    string `json:"object_id,omitempty"`
	X           *int   `json:"x,omitempty"`
	Y           *int   `json:"y,omitempty"`
	Knock       bool   `json:"knock,omitempty"`
}

// encodeMoveDestination derives the move_destination column value from
// the actor's live MoveIntent at checkpoint-write time: NULL when the
// actor isn't walking, else the destination as jsonb. Deriving from the
// live intent (instead of a parallel persisted field) means a walk that
// ends normally clears its column on the next checkpoint for free.
func encodeMoveDestination(mi *sim.MoveIntent) any {
	if mi == nil {
		return nil
	}
	d := mi.Destination
	out := moveDestinationJSON{Kind: string(d.Kind), Knock: d.Knock}
	if d.StructureID != nil {
		out.StructureID = string(*d.StructureID)
	}
	if d.ObjectID != nil {
		out.ObjectID = string(*d.ObjectID)
	}
	if d.Position != nil {
		x, y := d.Position.X, d.Position.Y
		out.X, out.Y = &x, &y
	}
	blob, err := json.Marshal(out)
	if err != nil {
		// Marshal of a flat struct of strings/ints cannot fail; treat a
		// failure as "no walk" rather than aborting the checkpoint.
		return nil
	}
	return blob
}

// decodeMoveDestination parses a move_destination blob back into a
// sim.MoveDestination for Actor.ResumeDestination. nil blob (SQL NULL —
// the actor wasn't walking) returns nil. Field-level validation is
// deliberately shallow: kind must be known and its required target
// present; whether the target still RESOLVES (structure deleted during
// downtime) is the boot re-dispatch's job, where MoveActor rejects it
// with a logged, per-actor failure.
func decodeMoveDestination(blob []byte) (*sim.MoveDestination, error) {
	if len(blob) == 0 {
		return nil, nil
	}
	var in moveDestinationJSON
	if err := json.Unmarshal(blob, &in); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	out := sim.MoveDestination{Kind: sim.MoveDestinationKind(in.Kind), Knock: in.Knock}
	switch out.Kind {
	case sim.MoveDestinationStructureEnter, sim.MoveDestinationStructureVisit:
		if in.StructureID == "" {
			return nil, fmt.Errorf("kind %q missing structure_id", in.Kind)
		}
		id := sim.StructureID(in.StructureID)
		out.StructureID = &id
	case sim.MoveDestinationObjectVisit:
		if in.ObjectID == "" {
			return nil, fmt.Errorf("kind %q missing object_id", in.Kind)
		}
		id := sim.VillageObjectID(in.ObjectID)
		out.ObjectID = &id
	case sim.MoveDestinationPosition:
		if in.X == nil || in.Y == nil {
			return nil, fmt.Errorf("kind %q missing x/y", in.Kind)
		}
		out.Position = &sim.Position{X: *in.X, Y: *in.Y}
	default:
		return nil, fmt.Errorf("unknown kind %q", in.Kind)
	}
	return &out, nil
}

// roomKindForSource synthesizes the NOT-NULL room_access.kind column
// value from the in-memory RoomAccessSource. The in-memory model keys
// access by (room, source) and does not carry the room category; ledger
// grants are paid private bedrooms, staff access is to staff rooms. Any
// unknown source defaults to staff (the most-restrictive non-private
// category); the SaveSnapshot pre-pass rejects unknown sources before
// this is reached, so the default is defensive only.
func roomKindForSource(s sim.RoomAccessSource) sim.RoomKind {
	if s == sim.AccessSourceLedger {
		return sim.RoomKindPrivate
	}
	return sim.RoomKindStaff
}

// jsonOrEmptyObject returns the params bytes as a string for the $N::jsonb
// cast, substituting the empty JSON object for an empty/nil blob (the
// actor_attribute.params column is NOT NULL DEFAULT '{}').
func jsonOrEmptyObject(b []byte) string {
	if len(b) == 0 {
		return "{}"
	}
	return string(b)
}

// validateDwellCreditShape enforces the actor_dwell_credit baseline
// CHECKs in Go (source allowlist, remaining_ticks↔source pairing,
// dwell_delta < 0, dwell_period_minutes > 0) plus non-empty key columns.
// Shared by LoadAll (out-of-band / legacy row defense) and SaveSnapshot
// (catch shape bugs before the lock). actorID is for the error message
// only. Returns a wrapped, context-free error (caller prefixes its phase).
func validateDwellCreditShape(actorID, objectID, attribute, source string, remainingTicks *int, dwellDelta, dwellPeriodMinutes int) error {
	if strings.TrimSpace(objectID) == "" {
		return fmt.Errorf("dwell credit actor_id=%s has empty object_id", actorID)
	}
	if strings.TrimSpace(attribute) == "" {
		return fmt.Errorf("dwell credit actor_id=%s object=%s has empty attribute", actorID, objectID)
	}
	switch sim.DwellCreditSource(source) {
	case sim.DwellSourceObject:
		if remainingTicks != nil {
			return fmt.Errorf("dwell credit actor_id=%s object=%s attr=%s is source=object but has non-nil remaining_ticks (violates remaining↔source pairing)", actorID, objectID, attribute)
		}
	case sim.DwellSourceItem:
		if remainingTicks == nil {
			return fmt.Errorf("dwell credit actor_id=%s object=%s attr=%s is source=item but has nil remaining_ticks (violates remaining↔source pairing)", actorID, objectID, attribute)
		}
		if *remainingTicks <= 0 {
			return fmt.Errorf("dwell credit actor_id=%s object=%s attr=%s has remaining_ticks=%d (must be > 0)", actorID, objectID, attribute, *remainingTicks)
		}
	default:
		return fmt.Errorf("dwell credit actor_id=%s object=%s attr=%s has unknown source=%q (must be object|item)", actorID, objectID, attribute, source)
	}
	if dwellDelta >= 0 {
		return fmt.Errorf("dwell credit actor_id=%s object=%s attr=%s has dwell_delta=%d (must be < 0)", actorID, objectID, attribute, dwellDelta)
	}
	if dwellPeriodMinutes <= 0 {
		return fmt.Errorf("dwell credit actor_id=%s object=%s attr=%s has dwell_period_minutes=%d (must be > 0)", actorID, objectID, attribute, dwellPeriodMinutes)
	}
	return nil
}

// validateRoomAccessShape enforces the room-access source↔ledger-id
// invariant in Go (room_id > 0; ledger ⇒ LedgerID > 0; staff ⇒ LedgerID
// == 0; known source). Shared by LoadAll (where source is derived from
// granted_via_ledger_id) and SaveSnapshot, so a non-positive ledger id —
// which the load derivation would still classify as ledger — is rejected
// on both sides. Returns a context-free error (caller prefixes its phase).
func validateRoomAccessShape(roomID int64, source sim.RoomAccessSource, ledgerID int64) error {
	if roomID <= 0 {
		return fmt.Errorf("room access has non-positive room_id=%d", roomID)
	}
	switch source {
	case sim.AccessSourceLedger:
		if ledgerID <= 0 {
			return fmt.Errorf("ledger room access room_id=%d has non-positive ledger id=%d", roomID, ledgerID)
		}
	case sim.AccessSourceStaff:
		if ledgerID != 0 {
			return fmt.Errorf("staff room access room_id=%d has ledger id=%d (must be 0)", roomID, ledgerID)
		}
	default:
		return fmt.Errorf("room access room_id=%d has unknown source=%q", roomID, source)
	}
	return nil
}

// salientFactRow is the JSONB serialization shape for a single
// SalientFact. Lowercase json tags match v1's stored {at, kind, text}
// element shape (ZBBS-WORK-213) so hand-seeded rows and any v1 reader
// round-trip cleanly. Kept repo-local rather than tagging the sim
// SalientFact type — persistence detail stays out of the domain model.
type salientFactRow struct {
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`
	Text string    `json:"text"`
}

// marshalSalientFacts serializes a SalientFacts slice to a JSON string
// for the salient_facts JSONB column. A nil/empty slice marshals to
// `[]`, matching the column's DEFAULT and v1's stored shape. Returned
// as a string so pgx binds it as text for the `$N::jsonb` cast.
func marshalSalientFacts(facts []sim.SalientFact) (string, error) {
	rows := make([]salientFactRow, len(facts))
	for i, f := range facts {
		rows[i] = salientFactRow{At: f.At, Kind: string(f.Kind), Text: f.Text}
	}
	b, err := json.Marshal(rows)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// unmarshalSalientFacts parses the salient_facts JSONB bytes back into a
// SalientFacts slice. Empty input (NULL or `[]`) yields a nil slice so
// a relationship with no facts round-trips to the Go zero value rather
// than an allocated empty slice.
func unmarshalSalientFacts(b []byte) ([]sim.SalientFact, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var rows []salientFactRow
	if err := json.Unmarshal(b, &rows); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	facts := make([]sim.SalientFact, len(rows))
	for i, r := range rows {
		facts[i] = sim.SalientFact{At: r.At, Kind: sim.InteractionKind(r.Kind), Text: r.Text}
	}
	return facts, nil
}

// marshalAffordances serializes a known place's affordance tokens to a JSON
// array string for the affordances JSONB column. A nil/empty slice marshals to
// `[]`, matching the column DEFAULT. Returned as a string so pgx binds it as
// text for the `$N::jsonb` cast. LLM-77.
func marshalAffordances(affordances []string) (string, error) {
	if len(affordances) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(affordances)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// unmarshalAffordances parses the affordances JSONB bytes back into a token
// slice. The column is NOT NULL (DEFAULT '[]'), so input is normally a JSON
// array; an empty byte slice or `[]` yields a nil slice so a place with no
// affordances round-trips to the Go zero value rather than an allocated empty
// slice. LLM-77.
func unmarshalAffordances(b []byte) ([]string, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var tokens []string
	if err := json.Unmarshal(b, &tokens); err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, nil
	}
	return tokens, nil
}

// validateActorsSnapshot is the actor aggregate's substrate-boundary validation
// (LLM-392). It returns the actors that will be written, and records everything
// it had to correct or leave behind on q.
//
// Pure by design — no Tx, no SQL, no I/O. The checkpoint's quarantine POLICY
// (what gets clamped, what gets dropped, which actor keeps a contested room)
// is the part that has to be right, and a pure function is the only way to test
// it directly instead of through a wall of mocked SQL expectations. It is also
// the honest shape: none of this needs a database to decide.
func validateActorsSnapshot(actors map[sim.ActorID]*sim.Actor, q *sim.Quarantine) []*sim.Actor {
	// LLM-392: this pre-pass no longer VETOES the checkpoint. Every check
	// below used to `return`, which aborted SaveWorld's whole transaction —
	// so one actor with an out-of-range need silently killed durability for
	// the entire village until someone noticed. Now a bad row is quarantined
	// (q.Drop) or corrected (q.Clamp) and the checkpoint carries on. See
	// sim/checkpoint_quarantine.go for the policy; the short version:
	//
	//   - clamp a value that is merely out of its column's range;
	//   - drop a row that cannot be meaningfully written at all;
	//   - a dropped ACTOR takes its child rows with it (the write steps below
	//     consult q.Dropped), because a fresh child against the stale parent
	//     row Postgres still holds would be a cross-generation hybrid.
	//
	// Clamping mutates the actor in place. That is safe and deliberate: `a`
	// points into the CheckpointSnapshot's deep clone, which belongs to this
	// checkpoint alone (built on the world goroutine, disconnected from live
	// state, discarded when SaveWorld returns). The LIVE world keeps its bad
	// value — the clamp corrects only what we persist — so the alarm keeps
	// firing every cycle until the actual world bug is fixed, which is the
	// behaviour we want.

	// Iterate in sorted ID order. Map order is random, and two of the checks
	// below are CROSS-actor (the single-active-private-occupant room guard):
	// with random order, WHICH of two double-booked actors keeps the room
	// would differ run to run, so the durable state would flap between
	// checkpoints. Sorted order makes the survivor stable. It also makes
	// `persisted` — and therefore every write loop below — deterministic.
	keys := make([]sim.ActorID, 0, len(actors))
	for key := range actors {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	persisted := make([]*sim.Actor, 0, len(actors))
	// activePrivateRooms tracks, across ALL persisted actors, which rooms
	// already have an active ledger (→ private kind) grant. Enforces the
	// ux_room_access_one_private_active partial unique index in Go so a
	// double-occupancy snapshot is caught here — as a dropped grant — rather
	// than as a mid-Tx unique-violation when the second active private row
	// UPSERTs ahead of the stale DELETE (the gen-marker order is
	// upsert-then-sweep). This is the guard that keeps a double-booked room
	// from wedging the checkpoint the way a double-booked LEDGER row did for
	// 17.5 hours on 2026-07-12 (LLM-391/LLM-392).
	activePrivateRooms := make(map[sim.RoomID]privateClaim)
	for _, key := range keys {
		a := actors[key]
		if a == nil {
			// A nil entry has no identity to persist and nothing to drop — the
			// gen-marker sweep is how an actor is deleted, so a nil here is a
			// command-handler bug. Record it against the map key and skip.
			dropActor(q, key, "nil actor entry in snapshot (deletion goes through gen-marker absence, not nil)")
			continue
		}
		if a.VisitorState != nil {
			continue // visitor actors filtered — see header comment
		}
		if strings.TrimSpace(string(a.ID)) == "" {
			dropActor(q, key, "empty ActorID")
			continue
		}
		if a.ID != key {
			// Keyed on the MAP KEY, like every other aggregate: a.ID is precisely the
			// field we do not trust here, and it may even name a DIFFERENT valid actor.
			dropActor(q, key, fmt.Sprintf("map key=%s does not match a.ID=%s", key, a.ID))
			continue
		}
		if strings.TrimSpace(a.DisplayName) == "" {
			dropActor(q, a.ID, "empty DisplayName")
			continue
		}
		if strings.TrimSpace(string(a.State)) == "" {
			dropActor(q, a.ID, "empty State (FSM bug — every actor must be in a named state)")
			continue
		}
		// Schedule fields are all-or-none on the DB side
		// (actor_schedule_window_all_or_none CHECK). A half-set schedule can't
		// be repaired (we don't know the missing end), so clear both — the
		// actor persists with no schedule window rather than not at all.
		if (a.ScheduleStartMin == nil) != (a.ScheduleEndMin == nil) {
			q.Clamp("actor", string(a.ID), fmt.Sprintf("half-set schedule (start=%v end=%v) cleared — must be both set or both nil",
				a.ScheduleStartMin, a.ScheduleEndMin))
			a.ScheduleStartMin, a.ScheduleEndMin = nil, nil
		}
		// Schedule range: minute-of-day [0, 1439]. Explicit range check
		// guards intPtrToSQL's int16 narrowing.
		if a.ScheduleStartMin != nil && (*a.ScheduleStartMin < 0 || *a.ScheduleStartMin > 1439) {
			clamped := clampInt(*a.ScheduleStartMin, 0, 1439)
			q.Clamp("actor", string(a.ID), fmt.Sprintf("ScheduleStartMin=%d out of range [0,1439], clamped to %d", *a.ScheduleStartMin, clamped))
			a.ScheduleStartMin = &clamped
		}
		if a.ScheduleEndMin != nil && (*a.ScheduleEndMin < 0 || *a.ScheduleEndMin > 1439) {
			clamped := clampInt(*a.ScheduleEndMin, 0, 1439)
			q.Clamp("actor", string(a.ID), fmt.Sprintf("ScheduleEndMin=%d out of range [0,1439], clamped to %d", *a.ScheduleEndMin, clamped))
			a.ScheduleEndMin = &clamped
		}
		// Need values must fit the CHECK 0-24 range (Slice 121). An out-of-
		// range need is an arithmetic bug in the needs tick, not a reason to
		// lose the actor — clamp it. An empty key can't be a PK, so that row
		// alone is dropped.
		for k, v := range a.Needs {
			if strings.TrimSpace(string(k)) == "" {
				q.Drop("actor_need", childID(a.ID, string(k)), "empty need key")
				continue
			}
			if v < 0 || v > 24 {
				clamped := clampInt(v, 0, 24)
				q.Clamp("actor_need", childID(a.ID, string(k)), fmt.Sprintf("value=%d out of range [0,24], clamped to %d", v, clamped))
				a.Needs[k] = clamped
			}
		}
		// Inventory: a negative quantity is a command-handler bug. Clamp to 0
		// — the write step treats qty==0 as the deletion case, so the row goes
		// away rather than persisting a nonsense negative stock.
		for kind, qty := range a.Inventory {
			if strings.TrimSpace(string(kind)) == "" {
				q.Drop("actor_inventory", childID(a.ID, string(kind)), "empty inventory item kind")
				continue
			}
			if qty < 0 {
				q.Clamp("actor_inventory", childID(a.ID, string(kind)), fmt.Sprintf("quantity=%d negative, clamped to 0 — the row is not written, and is removed by the stale-row sweep once one runs unblocked", qty))
				a.Inventory[kind] = 0
			}
		}
		// Tool wear (LLM-330): entries must be positive — applyToolWear deletes
		// at zero, so a non-positive entry is a mechanics bug.
		//
		// There is NO actor_tool_wear table: wear rides on the inventory row's
		// uses_left column. So a bad entry is a CLAMP on actor_inventory, not a
		// drop — the tool still persists, merely unworn — and it must not be
		// recorded against a table name that does not exist, which would name a
		// phantom table in the alarm and block a sweep that isn't there.
		for kind, wear := range a.ToolWear {
			if strings.TrimSpace(string(kind)) == "" || wear <= 0 {
				q.Clamp("actor_inventory", childID(a.ID, string(kind)), fmt.Sprintf("tool wear uses_left=%d must be > 0 — wear cleared, the item persists unworn", wear))
				delete(a.ToolWear, kind)
			}
		}
		// Relationships: peer key non-empty, no self-row (matches the
		// actor_relationship_no_self CHECK), non-negative counts. Negative
		// counts are clamped; a structurally impossible row (no peer, or a
		// self-relationship) is dropped. nil entries are skipped at write.
		for peerID, rel := range a.Relationships {
			if rel == nil {
				continue
			}
			if strings.TrimSpace(string(peerID)) == "" {
				q.Drop("actor_relationship", childID(a.ID, string(peerID)), "empty relationship peer key")
				continue
			}
			if peerID == a.ID {
				q.Drop("actor_relationship", childID(a.ID, string(peerID)), "self-relationship (peer == self) — violates actor_relationship_no_self")
				continue
			}
			if rel.InteractionCount < 0 {
				q.Clamp("actor_relationship", childID(a.ID, string(peerID)), fmt.Sprintf("InteractionCount=%d negative, clamped to 0", rel.InteractionCount))
				rel.InteractionCount = 0
			}
			if rel.DroppedFactCount < 0 {
				q.Clamp("actor_relationship", childID(a.ID, string(peerID)), fmt.Sprintf("DroppedFactCount=%d negative, clamped to 0", rel.DroppedFactCount))
				rel.DroppedFactCount = 0
			}
		}
		// Acquaintances: other_name is a PK column, so an empty or over-long
		// name can't be written — drop the row. (Truncating to fit VARCHAR(100)
		// could collide with another acquaintance's name, so no clamp here.)
		for name := range a.Acquaintances {
			if strings.TrimSpace(name) == "" {
				q.Drop("npc_acquaintance", childID(a.ID, name), "empty acquaintance name")
				continue
			}
			// VARCHAR(100) counts characters, not bytes — use rune count so
			// a multibyte name (e.g. "Élisabeth") isn't rejected for being
			// over the byte length while under the char limit.
			if utf8.RuneCountInString(name) > 100 {
				q.Drop("npc_acquaintance", childID(a.ID, name), fmt.Sprintf("acquaintance name %q exceeds 100 chars", name))
			}
		}
		// Dwell credits: shape mirrors the baseline CHECKs (source allowlist,
		// remaining↔source pairing, dwell_delta < 0, period > 0). The map key
		// is the PK source of truth; the struct's redundant ObjectID/
		// Attribute/Source must agree with it. A malformed credit is dropped —
		// there is no safe repair for a disagreeing composite key.
		for dk, dc := range a.DwellCredits {
			if dc == nil {
				continue
			}
			id := childID(a.ID, fmt.Sprintf("%s/%s/%s", dk.ObjectID, dk.Attribute, dk.Source))
			if dc.ObjectID != dk.ObjectID || dc.Attribute != dk.Attribute || dc.Source != dk.Source {
				q.Drop("actor_dwell_credit", id, fmt.Sprintf("struct (obj=%s attr=%s src=%s) disagrees with map key (obj=%s attr=%s src=%s)",
					dc.ObjectID, dc.Attribute, dc.Source, dk.ObjectID, dk.Attribute, dk.Source))
				continue
			}
			if err := validateDwellCreditShape(string(a.ID), string(dk.ObjectID), string(dk.Attribute), string(dk.Source), dc.RemainingTicks, dc.DwellDelta, dc.DwellPeriodMinutes); err != nil {
				q.Drop("actor_dwell_credit", id, err.Error())
				continue
			}
			if dc.LastCreditedAt.IsZero() {
				q.Drop("actor_dwell_credit", id, "zero LastCreditedAt (last_credited_at is NOT NULL)")
			}
		}
		// Production activity (LLM-319): a live cycle must carry a non-empty
		// item and positive batch/remaining. A malformed window is cleared —
		// the actor persists with no production cycle, which is exactly what
		// the load side would have inferred from it anyway.
		if pa := a.ProductionActivity; pa != nil {
			if strings.TrimSpace(string(pa.Item)) == "" {
				q.Clamp("actor", string(a.ID), "production activity with empty item cleared")
				a.ProductionActivity = nil
			} else if pa.BatchQty <= 0 || pa.RemainingSeconds <= 0 {
				q.Clamp("actor", string(a.ID), fmt.Sprintf("production activity item=%s has non-positive batch_qty=%d / remaining_seconds=%d, cleared",
					pa.Item, pa.BatchQty, pa.RemainingSeconds))
				a.ProductionActivity = nil
			}
		}
		// Room access: struct must agree with key; source↔ledger-id pairing
		// (shared with the load derivation via validateRoomAccessShape);
		// CreatedAt non-zero (granted_at is NOT NULL). The table PK is
		// (room_id, actor_id), so two in-memory entries for the same room
		// under different sources would collide on UPSERT — drop the second.
		// The cross-actor activePrivateRooms guard enforces the single-active-
		// private-occupant index the same way. nil skipped at write.
		//
		// Sorted key order here for the same reason as the actor loop: which
		// of two colliding grants survives must not depend on map order.
		seenRooms := make(map[sim.RoomID]sim.RoomAccessSource)
		for _, rk := range sortedRoomKeys(a.RoomAccess) {
			ra := a.RoomAccess[rk]
			if ra == nil {
				continue
			}
			id := roomAccessID(rk.RoomID, a.ID, rk.Source)
			if ra.RoomID != rk.RoomID || ra.Source != rk.Source {
				q.Drop("room_access", id, fmt.Sprintf("struct (room=%d src=%s) disagrees with map key (room=%d src=%s)",
					ra.RoomID, ra.Source, rk.RoomID, rk.Source))
				continue
			}
			if err := validateRoomAccessShape(int64(rk.RoomID), rk.Source, ra.LedgerID); err != nil {
				q.Drop("room_access", id, err.Error())
				continue
			}
			if prior, dup := seenRooms[rk.RoomID]; dup {
				q.Drop("room_access", id, fmt.Sprintf("second room-access entry for room=%d (sources %s and %s) — PK (room_id, actor_id) holds one row per room",
					rk.RoomID, prior, rk.Source))
				continue
			}
			if ra.CreatedAt.IsZero() {
				q.Drop("room_access", id, fmt.Sprintf("room=%d has zero CreatedAt (granted_at is NOT NULL)", rk.RoomID))
				continue
			}
			// Single active private occupant per room
			// (ux_room_access_one_private_active — a partial UNIQUE on room_id
			// alone, which upsertRoomAccessSQLA's ON CONFLICT (room_id, actor_id)
			// arbiter does NOT resolve).
			//
			// When two actors claim one room, BOTH are dropped — we do not pick a
			// winner. Picking one looks tempting (lowest actor id) but is unsafe:
			// dropping the loser blocks the room_access sweep, so the loser's
			// STALE durable row survives, still active and still private. The
			// "winner"'s fresh UPSERT then collides with that surviving row on the
			// partial index and aborts the whole checkpoint mid-Tx — the very
			// outage this ticket exists to prevent, in half of all double-booked
			// rooms (whenever the durable incumbent happens to sort higher).
			//
			// Dropping both is the safe reading of an ambiguous world: we cannot
			// tell who owns the room, so we change nothing about it. Each grant
			// keeps its previous durable version and the alarm names both.
			if rk.Source == sim.AccessSourceLedger && ra.Active {
				if prior, taken := activePrivateRooms[rk.RoomID]; taken {
					q.Drop("room_access", id, fmt.Sprintf("two actors hold an active ledger (private) grant for room=%d (%s and %s) — violates ux_room_access_one_private_active; BOTH grants are dropped, the room's durable occupancy is left untouched",
						rk.RoomID, prior.actor, a.ID))
					// Retroactively drop the earlier claimant too.
					q.Drop("room_access", prior.key, fmt.Sprintf("two actors hold an active ledger (private) grant for room=%d (%s and %s) — violates ux_room_access_one_private_active; BOTH grants are dropped, the room's durable occupancy is left untouched",
						rk.RoomID, prior.actor, a.ID))
					continue
				}
				activePrivateRooms[rk.RoomID] = privateClaim{actor: a.ID, key: id}
			}
			seenRooms[rk.RoomID] = rk.Source
		}
		// Attributes: slug non-empty/non-whitespace and within VARCHAR(64);
		// params must be valid JSON (the column is jsonb; an invalid blob
		// would trip the ::jsonb cast mid-Tx). All three are unwritable rows,
		// so all three drop.
		for slug, params := range a.Attributes {
			id := childID(a.ID, slug)
			if strings.TrimSpace(slug) == "" {
				q.Drop("actor_attribute", id, "empty attribute slug")
				continue
			}
			if utf8.RuneCountInString(slug) > 64 {
				q.Drop("actor_attribute", id, fmt.Sprintf("attribute slug %q exceeds 64 chars", slug))
				continue
			}
			if len(params) > 0 && !json.Valid(params) {
				q.Drop("actor_attribute", id, fmt.Sprintf("attribute slug %q has invalid JSON params", slug))
			}
		}
		// KnownPlaces: place_ref non-empty (PK column), place_kind in the
		// allowlist (the schema has no CHECK — Go owns it, sim_state posture),
		// affordance tokens non-empty. nil entries skipped at the write step.
		// LLM-77.
		for ref, kp := range a.KnownPlaces {
			if kp == nil {
				continue
			}
			id := childID(a.ID, string(ref))
			if strings.TrimSpace(string(ref)) == "" {
				q.Drop("actor_known_place", id, "empty known-place ref")
				continue
			}
			// The map key is the authoritative place_ref (what gets persisted +
			// reloaded as the PK); a non-empty kp.Ref that disagrees is a
			// malformed entry that would read differently before vs after a
			// checkpoint.
			if kp.Ref != "" && kp.Ref != ref {
				q.Drop("actor_known_place", id, fmt.Sprintf("known-place key=%s has mismatched Ref=%s", ref, kp.Ref))
				continue
			}
			if !kp.Kind.Valid() {
				q.Drop("actor_known_place", id, fmt.Sprintf("known-place ref=%s has unknown place_kind %q", ref, kp.Kind))
				continue
			}
			for _, aff := range kp.Affordances {
				if strings.TrimSpace(aff) == "" {
					q.Drop("actor_known_place", id, fmt.Sprintf("known-place ref=%s has an empty affordance token", ref))
					break
				}
			}
		}
		persisted = append(persisted, a)
	}
	return persisted
}

// privateClaim is the first actor seen holding an active ledger (private) grant
// on a room, plus the quarantine key that grant was recorded under — so that a
// SECOND claimant can retroactively drop the first as well. See the
// activePrivateRooms guard: an ambiguous room is left alone entirely rather than
// having a winner picked, because dropping only the loser leaves the loser's
// stale active row in place for the winner's UPSERT to collide with.
type privateClaim struct {
	actor sim.ActorID
	key   string
}
