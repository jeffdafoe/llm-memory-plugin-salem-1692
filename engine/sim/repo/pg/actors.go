package pg

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// ActorsRepo reads and writes Actor rows against `actor` plus the
// `actor_need` and `actor_inventory` child tables. Owns all three as
// one aggregate; later slices (244, 245) extend the load/save with
// acquaintances, relationships, narrative, dwell credits, attributes.
//
// SaveSnapshot uses the generation-marker pattern (Slice 9/10/11/12/13
// precedent — see
// `shared/notes/codebase/salem-engine-v2/pg-snapshot-pattern`). Three
// tiers in this slice: parent `actor`, `actor_need`, `actor_inventory`.
// Each tier owns its own sequence; the parent's advisory lock covers
// all three — child tables never SaveSnapshot independently.
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
// (parent + needs + inventory). Same pattern as Slice 11/12.
// Multi-realm upgrade path: replace 0 with hashtext($realm_id).
const advisoryLockSQLA = `SELECT pg_advisory_xact_lock(hashtext('actor_snapshot'), 0)`

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

// SaveSnapshot writes the full Actor + Needs + Inventory set durably
// using the generation-marker pattern (Slice 9/10/11/12/13 standard).
//
// Visitor actors (VisitorState != nil) are filtered out entirely —
// they're transient by design and persisting them would leave stale
// visitor rows after restart that the visitor cascade has no reload
// path for. See engine/sim/visitor.go and the visitor codebase note.
//
// Steps inside the caller's checkpoint Tx (order matters — validation
// runs first so shape errors abort BEFORE the advisory lock; parent
// settles before children sync):
//
//  0. Pre-pass validation: nil entries, empty/whitespace IDs, map-key
//     vs a.ID mismatch, empty DisplayName/State, zero StateEnteredAt,
//     half-set schedule, schedule/need values out of range, empty/
//     whitespace need-key or inventory-kind, negative inventory.
//  1. Advisory lock — shared by all three tables.
//  2. nextval(actor_snapshot_gen_seq) → $genActor.
//  3. Per-row UPSERT actor.
//  4. DELETE actor WHERE snapshot_gen < $genActor. FK CASCADE from
//     actor_need/actor_inventory drops their child rows for actors
//     that fell out of the snapshot.
//  5. nextval(actor_need_snapshot_gen_seq) → $genNeed.
//  6. Per-actor per-need UPSERT actor_need.
//  7. DELETE actor_need WHERE snapshot_gen < $genNeed.
//  8. nextval(actor_inventory_snapshot_gen_seq) → $genInv.
//  9. Per-actor per-item UPSERT actor_inventory, skipping zero-qty
//     entries (the CHECK forbids them; the per-row drop is the
//     mechanism for "actor consumed all of X — remove the row").
//
// 10. DELETE actor_inventory WHERE snapshot_gen < $genInv.
//
// Empty actors map: all three gens still bump, no UPSERTs run, all
// three DELETEs sweep their tables.
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
