package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

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
// columns the engine actually tracks. v1-only columns (`facing`,
// `inside`, `lateness_window_minutes`, `social_*`, the visitor cluster,
// PC-liveness stamps, etc.) are not in the UPSERT column list — on
// existing rows they retain their values across checkpoint
// (`ON CONFLICT DO UPDATE SET` only touches the listed columns); on
// newly-INSERTed actors they fall back to schema defaults. Visitor
// actors are filtered out of SaveSnapshot entirely (per visitor
// codebase note "No durable visitor row persistence"); their parent
// rows in v1 will be cleaned up by a separate cutover-prep migration.
//
// Empty-string ↔ NULL convention (Slice 1 establishes the pattern;
// codified at `shared/notes/codebase/salem-engine-v2/actors-pg` when
// the slice ships):
//
//   - ID-string fields where empty-string is the Go sentinel
//     (InsideStructureID, CurrentHuddleID, Home/Work StructureID):
//     scan target is `*string`; Go-side empty → SQL NULL on write.
//   - Plain string fields where empty-string is the sentinel
//     (Role, LLMAgent, LoginUsername, NextSelfTickReason): same.
//   - Pointer time fields (BreakUntil, SleepingUntil, LastTickedAt,
//     NextSelfTickAt): scan target is `*time.Time`; nil-or-value
//     round-trips through SQL NULL.
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

// loadAllSQLA selects the v2-owned column subset from `actor`. v1-only
// columns are deliberately omitted — they exist in the schema but no
// v2 code reads them and including them would burn bandwidth on the
// cold-start path. snapshot_gen omitted — pure sync bookkeeping.
//
// `::text` casts on UUID columns let pgx scan straight into `*string`
// scan targets, matching the rest of the slice's nullable-ID pattern.
//
// Visitor rows are NOT filtered out at the SQL layer — the design's
// posture is "v2 never reads/writes visitor columns; cutover-prep
// migration will delete visitor rows before cutover." Filtering at
// load time would mask schema drift; let LoadWorld see them and the
// orchestrator policy decide.
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
    next_self_tick_at,
    next_self_tick_reason,
    sleeping_until,
    move_attempt_counter,
    sim_state,
    sim_state_entered_at
  FROM actor`

// loadAllNeedsSQLA selects every actor_need row. Joined to actors in
// Go via actor_id.
const loadAllNeedsSQLA = `
SELECT actor_id::text, key, value
  FROM actor_need`

// loadAllInventorySQLA selects every actor_inventory row.
const loadAllInventorySQLA = `
SELECT actor_id::text, item_kind, quantity
  FROM actor_inventory`

// upsertSQLA writes one actor row. Column list = v2-owned subset only;
// v1-only columns are untouched on UPDATE (ON CONFLICT preserves them)
// and fall back to schema defaults on INSERT. snapshot_gen carries the
// new checkpoint gen so the trailing DELETE can prune absent rows.
const upsertSQLA = `
INSERT INTO actor (
    id, display_name, current_x, current_y,
    inside_structure_id, current_huddle_id, inside_room_id,
    home_structure_id, work_structure_id,
    coins, llm_memory_agent, role, login_username,
    schedule_start_minute, schedule_end_minute,
    last_agent_tick_at, break_until, next_self_tick_at,
    next_self_tick_reason, sleeping_until,
    move_attempt_counter, sim_state, sim_state_entered_at,
    snapshot_gen
) VALUES (
    $1, $2, $3, $4,
    $5, $6, $7,
    $8, $9,
    $10, $11, $12, $13,
    $14, $15,
    $16, $17, $18,
    $19, $20,
    $21, $22, $23,
    $24
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
    next_self_tick_at      = EXCLUDED.next_self_tick_at,
    next_self_tick_reason  = EXCLUDED.next_self_tick_reason,
    sleeping_until         = EXCLUDED.sleeping_until,
    move_attempt_counter   = EXCLUDED.move_attempt_counter,
    sim_state              = EXCLUDED.sim_state,
    sim_state_entered_at   = EXCLUDED.sim_state_entered_at,
    snapshot_gen           = EXCLUDED.snapshot_gen`

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
const upsertInventorySQLA = `
INSERT INTO actor_inventory (
    actor_id, item_kind, quantity, snapshot_gen
) VALUES (
    $1, $2, $3, $4
)
ON CONFLICT (actor_id, item_kind) DO UPDATE SET
    quantity     = EXCLUDED.quantity,
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
    actor_id::text, seed_text, evolving_summary,
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
// actor_id (1:1). seed_text is external input (dream pipeline); no
// cascade mutates it, but persistence round-trips it verbatim.
const upsertNarrativeSQLA = `
INSERT INTO actor_narrative_state (
    actor_id, seed_text, evolving_summary,
    last_consolidated_at, created_at, updated_at, snapshot_gen
) VALUES (
    $1, $2, $3,
    $4, $5, $6, $7
)
ON CONFLICT (actor_id) DO UPDATE SET
    seed_text            = EXCLUDED.seed_text,
    evolving_summary     = EXCLUDED.evolving_summary,
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
			nextSelfTickAt      *time.Time
			nextSelfTickReason  *string
			sleepingUntil       *time.Time
			moveAttemptCounter  int64
			simState            string
			simStateEnteredAt   time.Time
		)
		if err := rows.Scan(
			&id, &displayName, &currentX, &currentY,
			&insideStructureID, &currentHuddleID, &insideRoomID,
			&homeStructureID, &workStructureID,
			&coins, &llmMemoryAgent, &role, &loginUsername,
			&scheduleStartMinute, &scheduleEndMinute,
			&lastAgentTickAt, &breakUntil, &nextSelfTickAt,
			&nextSelfTickReason, &sleepingUntil,
			&moveAttemptCounter, &simState, &simStateEnteredAt,
		); err != nil {
			return nil, fmt.Errorf("pg actors LoadAll scan: %w", err)
		}

		var roomID sim.RoomID
		if insideRoomID != nil {
			roomID = sim.RoomID(*insideRoomID)
		}

		a := &sim.Actor{
			ID:                 sim.ActorID(id),
			DisplayName:        displayName,
			CurrentX:           currentX,
			CurrentY:           currentY,
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
			NextSelfTickAt:     nextSelfTickAt,
			NextSelfTickReason: deref(nextSelfTickReason),
			SleepingUntil:      sleepingUntil,
			MoveAttemptCounter: sim.MovementAttemptID(moveAttemptCounter),
			State:              sim.ActorState(simState),
			StateEnteredAt:     simStateEnteredAt,
			Needs:              make(map[sim.NeedKey]int),
			Inventory:          make(map[sim.ItemKind]int),
			Relationships:      make(map[sim.ActorID]*sim.Relationship),
			Acquaintances:      make(map[string]sim.Acquaintance),
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
		)
		if err := rows.Scan(&actorID, &itemKind, &quantity); err != nil {
			return fmt.Errorf("pg actors LoadAll inventory scan: %w", err)
		}
		parent, ok := actors[sim.ActorID(actorID)]
		if !ok {
			return fmt.Errorf("pg actors LoadAll: orphan inventory row actor_id=%s item_kind=%s (parent missing — schema drift or out-of-band write)",
				actorID, itemKind)
		}
		parent.Inventory[sim.ItemKind(itemKind)] = quantity
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
			lastConsolidatedAt *time.Time
			createdAt          time.Time
			updatedAt          time.Time
		)
		if err := rows.Scan(
			&actorID, &seedText, &evolvingSummary,
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

// SaveSnapshot writes the full actor aggregate (six tiers) durably
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
// all six share the one advisory lock:
//
//  0. Pre-pass validation: nil entries, empty/whitespace IDs, map-key
//     vs a.ID mismatch, empty DisplayName/State, zero StateEnteredAt,
//     half-set / out-of-range schedule, need values out of range,
//     empty need-key / inventory-kind, negative inventory, empty
//     relationship peer key, self-relationship, negative relationship
//     counts, empty / over-length acquaintance name.
//  1. Advisory lock — shared by all six tables.
//     2-4.   actor  : nextval → UPSERT → DELETE stale (FK CASCADE drops
//     children of absent parents).
//     5-7.   actor_need        : nextval → UPSERT → DELETE stale.
//     8-10.  actor_inventory   : nextval → UPSERT (skip zero-qty) → DELETE.
//     11-13. actor_relationship: nextval → UPSERT (skip nil) → DELETE.
//     14-16. actor_narrative_state: nextval → UPSERT (skip nil Narrative)
//     → DELETE.
//     17-19. npc_acquaintance  : nextval → UPSERT → DELETE stale.
//
// Empty actors map: all six gens still bump, no UPSERTs run, all six
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
	persisted := make([]*sim.Actor, 0, len(actors))
	for key, a := range actors {
		if a == nil {
			return fmt.Errorf("pg actors SaveSnapshot: nil entry at map key=%s (use deletion via gen-marker absence, not nil)", key)
		}
		if a.VisitorState != nil {
			continue // visitor actors filtered — see header comment
		}
		if strings.TrimSpace(string(a.ID)) == "" {
			return fmt.Errorf("pg actors SaveSnapshot: empty ActorID (map key=%s)", key)
		}
		if a.ID != key {
			return fmt.Errorf("pg actors SaveSnapshot: map key=%s does not match a.ID=%s", key, a.ID)
		}
		if strings.TrimSpace(a.DisplayName) == "" {
			return fmt.Errorf("pg actors SaveSnapshot: id=%s has empty DisplayName", a.ID)
		}
		if strings.TrimSpace(string(a.State)) == "" {
			return fmt.Errorf("pg actors SaveSnapshot: id=%s has empty State (FSM bug — every actor must be in a named state)", a.ID)
		}
		if a.StateEnteredAt.IsZero() {
			return fmt.Errorf("pg actors SaveSnapshot: id=%s has zero StateEnteredAt", a.ID)
		}
		// Schedule fields are all-or-none on the DB side
		// (actor_schedule_window_all_or_none CHECK).
		if (a.ScheduleStartMin == nil) != (a.ScheduleEndMin == nil) {
			return fmt.Errorf("pg actors SaveSnapshot: id=%s has half-set schedule (start=%v end=%v) — must be both set or both nil",
				a.ID, a.ScheduleStartMin, a.ScheduleEndMin)
		}
		// Schedule range: minute-of-day [0, 1439]. Explicit range check
		// guards intPtrToSQL's int16 narrowing.
		if a.ScheduleStartMin != nil && (*a.ScheduleStartMin < 0 || *a.ScheduleStartMin > 1439) {
			return fmt.Errorf("pg actors SaveSnapshot: id=%s ScheduleStartMin=%d out of range [0,1439]", a.ID, *a.ScheduleStartMin)
		}
		if a.ScheduleEndMin != nil && (*a.ScheduleEndMin < 0 || *a.ScheduleEndMin > 1439) {
			return fmt.Errorf("pg actors SaveSnapshot: id=%s ScheduleEndMin=%d out of range [0,1439]", a.ID, *a.ScheduleEndMin)
		}
		// Need values must fit the CHECK 0-24 range (Slice 121). Key
		// validation guards against whitespace-only keys that would
		// pass Go-side empty checks and trip a btrim CHECK mid-Tx.
		for k, v := range a.Needs {
			if strings.TrimSpace(string(k)) == "" {
				return fmt.Errorf("pg actors SaveSnapshot: id=%s has empty need key", a.ID)
			}
			if v < 0 || v > 24 {
				return fmt.Errorf("pg actors SaveSnapshot: id=%s need=%s value=%d out of range [0,24]", a.ID, k, v)
			}
		}
		// Inventory: reject negative quantities (almost certainly a
		// command-handler bug; silent-drop would mask the underlying
		// problem). qty=0 is allowed and treated as the deletion case
		// at the write step.
		for kind, qty := range a.Inventory {
			if strings.TrimSpace(string(kind)) == "" {
				return fmt.Errorf("pg actors SaveSnapshot: id=%s has empty inventory item kind", a.ID)
			}
			if qty < 0 {
				return fmt.Errorf("pg actors SaveSnapshot: id=%s inventory item=%s quantity=%d out of range (must be >= 0)",
					a.ID, kind, qty)
			}
		}
		// Relationships: peer key non-empty, no self-row (matches the
		// actor_relationship_no_self CHECK — catch in Go so it doesn't
		// trip mid-Tx as a worse error), non-negative counts. nil entries
		// are skipped at the write step (cloneRelationships precedent).
		for peerID, rel := range a.Relationships {
			if rel == nil {
				continue
			}
			if strings.TrimSpace(string(peerID)) == "" {
				return fmt.Errorf("pg actors SaveSnapshot: id=%s has empty relationship peer key", a.ID)
			}
			if peerID == a.ID {
				return fmt.Errorf("pg actors SaveSnapshot: id=%s has a self-relationship (peer == self) — violates actor_relationship_no_self", a.ID)
			}
			if rel.InteractionCount < 0 {
				return fmt.Errorf("pg actors SaveSnapshot: id=%s relationship peer=%s InteractionCount=%d out of range (must be >= 0)", a.ID, peerID, rel.InteractionCount)
			}
			if rel.DroppedFactCount < 0 {
				return fmt.Errorf("pg actors SaveSnapshot: id=%s relationship peer=%s DroppedFactCount=%d out of range (must be >= 0)", a.ID, peerID, rel.DroppedFactCount)
			}
		}
		// Acquaintances: other_name non-empty/non-whitespace (PK column,
		// btrim concern) and within VARCHAR(100) — reject over-length in
		// Go rather than eat a mid-Tx truncation/violation.
		for name := range a.Acquaintances {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("pg actors SaveSnapshot: id=%s has empty acquaintance name", a.ID)
			}
			// VARCHAR(100) counts characters, not bytes — use rune count so
			// a multibyte name (e.g. "Élisabeth") isn't rejected for being
			// over the byte length while under the char limit.
			if utf8.RuneCountInString(name) > 100 {
				return fmt.Errorf("pg actors SaveSnapshot: id=%s acquaintance name=%q exceeds 100 chars", a.ID, name)
			}
		}
		persisted = append(persisted, a)
	}

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
		if _, err := tx.Exec(ctx, upsertSQLA,
			string(a.ID),                            // $1 id
			a.DisplayName,                           // $2 display_name
			a.CurrentX,                              // $3 current_x
			a.CurrentY,                              // $4 current_y
			nilOnEmpty(string(a.InsideStructureID)), // $5 inside_structure_id
			nilOnEmpty(string(a.CurrentHuddleID)),   // $6 current_huddle_id
			nilOnZero(int64(a.InsideRoomID)),        // $7 inside_room_id
			nilOnEmpty(string(a.HomeStructureID)),   // $8 home_structure_id
			nilOnEmpty(string(a.WorkStructureID)),   // $9 work_structure_id
			a.Coins,                                 // $10 coins
			nilOnEmpty(a.LLMAgent),                  // $11 llm_memory_agent
			nilOnEmpty(a.Role),                      // $12 role
			nilOnEmpty(a.LoginUsername),             // $13 login_username
			intPtrToSQL(a.ScheduleStartMin),         // $14 schedule_start_minute
			intPtrToSQL(a.ScheduleEndMin),           // $15 schedule_end_minute
			a.LastTickedAt,                          // $16 last_agent_tick_at
			a.BreakUntil,                            // $17 break_until
			a.NextSelfTickAt,                        // $18 next_self_tick_at
			nilOnEmpty(a.NextSelfTickReason),        // $19 next_self_tick_reason
			a.SleepingUntil,                         // $20 sleeping_until
			int64(a.MoveAttemptCounter),             // $21 move_attempt_counter
			string(a.State),                         // $22 sim_state
			a.StateEnteredAt,                        // $23 sim_state_entered_at
			actorGen,                                // $24 snapshot_gen
		); err != nil {
			return fmt.Errorf("pg actors SaveSnapshot: upsert actor id=%s: %w", a.ID, err)
		}
	}

	// Step 4: prune absent parents. FK CASCADE drops their needs +
	// inventory rows automatically.
	if _, err := tx.Exec(ctx, deleteStaleSQLA, actorGen); err != nil {
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
	if _, err := tx.Exec(ctx, deleteStaleNeedSQLA, needGen); err != nil {
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
	// the only skip case here).
	for _, a := range persisted {
		for kind, qty := range a.Inventory {
			if qty == 0 {
				continue
			}
			if _, err := tx.Exec(ctx, upsertInventorySQLA,
				string(a.ID), // $1 actor_id
				string(kind), // $2 item_kind
				qty,          // $3 quantity
				invGen,       // $4 snapshot_gen
			); err != nil {
				return fmt.Errorf("pg actors SaveSnapshot: upsert inventory actor=%s item=%s: %w", a.ID, kind, err)
			}
		}
	}

	// Step 10: prune absent inventory rows (catches consumed-to-zero
	// entries that were skipped at step 9 plus item-removed entries).
	if _, err := tx.Exec(ctx, deleteStaleInvSQLA, invGen); err != nil {
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
			if rel == nil {
				continue
			}
			factsJSON, err := marshalSalientFacts(rel.SalientFacts)
			if err != nil {
				return fmt.Errorf("pg actors SaveSnapshot: marshal salient_facts actor=%s peer=%s: %w", a.ID, peerID, err)
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
	if _, err := tx.Exec(ctx, deleteStaleRelationshipSQLA, relGen); err != nil {
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
			a.Narrative.LastConsolidatedAt, // $4 last_consolidated_at
			a.Narrative.CreatedAt,          // $5 created_at
			a.Narrative.UpdatedAt,          // $6 updated_at
			narrGen,                        // $7 snapshot_gen
		); err != nil {
			return fmt.Errorf("pg actors SaveSnapshot: upsert narrative actor=%s: %w", a.ID, err)
		}
	}

	// Step 16: prune absent narrative rows.
	if _, err := tx.Exec(ctx, deleteStaleNarrativeSQLA, narrGen); err != nil {
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
	if _, err := tx.Exec(ctx, deleteStaleAcquaintanceSQLA, acqGen); err != nil {
		return fmt.Errorf("pg actors SaveSnapshot: delete stale acquaintance: %w", err)
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
