package pg

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// HuddlesRepo reads and writes Huddle rows against scene_huddle plus
// the huddle_member child table. Owns both as one aggregate; Members
// is the canonical membership representation in v2 (Actor.CurrentHuddleID
// is a denormalized cache reconstructed by Actors-pg-impl from the
// loaded huddle_member set).
//
// SaveSnapshot uses the generation-marker pattern with a child-table
// extension (Slice 10 — see
// `shared/notes/codebase/salem-engine-v2/pg-snapshot-pattern`). Both
// tables get per-row UPSERT inside the caller's checkpoint Tx, then
// per-table `DELETE WHERE snapshot_gen < $gen` prunes anything absent.
// Each table owns its own sequence; the parent's advisory lock covers
// both — huddle_member never SaveSnapshots independently.
//
// Concluded huddles persist as parent rows with `concluded_at IS NOT
// NULL` and zero child rows (sim.ConcludeHuddle wipes Huddle.Members
// in memory; the in-memory empty set produces no UPSERTs against
// huddle_member). The trailing child DELETE catches any historical
// rows defensively.
//
// HuddleID format constraint (scene_huddle_id_format CHECK) is
// coupled to engine/sim/huddle_commands.go::newHuddleID. If that
// function's output format changes, the CHECK must update in the
// same migration. See ZBBS-WORK-239 migration header for details.
type HuddlesRepo struct {
	pool Pool
}

// NewHuddlesRepo constructs a HuddlesRepo against the given pool.
// Normal wiring path is pg.NewRepository which wires this internally.
func NewHuddlesRepo(pool Pool) *HuddlesRepo {
	return &HuddlesRepo{pool: pool}
}

// loadAllSQLH selects every scene_huddle row. No JOIN — members are
// loaded by a separate query and stitched in by loadAllMembers.
// snapshot_gen omitted — pure sync bookkeeping, no in-memory
// representation. No ORDER BY — the in-memory model is map-keyed.
const loadAllSQLH = `
SELECT id, structure_id, started_at, concluded_at
  FROM scene_huddle`

// loadAllMembersSQLH selects every huddle_member row. Grouped by
// huddle_id in Go and attached to each parent's Members set.
const loadAllMembersSQLH = `
SELECT huddle_id, actor_id
  FROM huddle_member`

// upsertSQLH writes one scene_huddle row. snapshot_gen carries the
// new checkpoint gen so the trailing DELETE can prune stale rows.
// structure_id is nullable (outdoor huddles); concluded_at is nullable
// (active vs concluded); started_at is required (substrate-boundary
// validation rejects zero values).
const upsertSQLH = `
INSERT INTO scene_huddle (
    id, structure_id, started_at, concluded_at, snapshot_gen
) VALUES (
    $1, $2, $3, $4, $5
)
ON CONFLICT (id) DO UPDATE SET
    structure_id = EXCLUDED.structure_id,
    started_at   = EXCLUDED.started_at,
    concluded_at = EXCLUDED.concluded_at,
    snapshot_gen = EXCLUDED.snapshot_gen`

// deleteStaleSQLH prunes scene_huddle rows whose snapshot_gen is below
// the current checkpoint gen. Plain DELETE — scene_huddle has no
// self-FK, no CASCADE pathology to defend against (unlike Slice 9's
// village_object.attached_to).
const deleteStaleSQLH = `DELETE FROM scene_huddle WHERE snapshot_gen < $1`

// nextGenSQLH bumps the parent's sequence.
const nextGenSQLH = `SELECT nextval('huddle_snapshot_gen_seq')`

// advisoryLockSQLH is the single global lock for the huddle aggregate.
// Shared by both scene_huddle and huddle_member writes — both are
// part of the same SaveSnapshot Tx. Per the pg-snapshot-pattern note,
// the lock is held for the Tx duration and serializes concurrent
// SaveSnapshot calls for this aggregate.
//
// Multi-realm upgrade path: replace 0 with hashtext($realm_id) when
// realms land. Today single-realm so the global lock is correct.
const advisoryLockSQLH = `SELECT pg_advisory_xact_lock(hashtext('huddle_snapshot'), 0)`

// upsertSQLM writes one huddle_member row. The conflict arbiter is the
// uniq_huddle_member_actor index (UNIQUE(actor_id)) — the one-active-huddle-
// per-actor invariant — so an actor who moved huddles between checkpoints has
// their existing row REPOINTED to the new huddle in place. huddle_id is part of
// the PK, so the UPDATE rewrites it; that is valid in PG because actor_id is
// unique, so the resulting (huddle_id, actor_id) cannot collide.
//
// An earlier `ON CONFLICT (huddle_id, actor_id)` clause matched only the
// same-huddle case: a moved actor then INSERTed, collided with their stale row
// on uniq_huddle_member_actor (SQLSTATE 23505), and — firing in step 5 before
// the step-6 prune that clears that stale row — aborted the whole SaveWorld Tx
// every cycle (ZBBS-HOME-333). Repointing on actor_id is the correct upsert
// here; a genuine two-huddles-at-once world-side bug is detected + logged in
// the step-5 loop instead of failing the Tx.
const upsertSQLM = `
INSERT INTO huddle_member (
    huddle_id, actor_id, snapshot_gen
) VALUES (
    $1, $2, $3
)
ON CONFLICT (actor_id) DO UPDATE SET
    huddle_id    = EXCLUDED.huddle_id,
    snapshot_gen = EXCLUDED.snapshot_gen`

// deleteStaleSQLM prunes huddle_member rows absent from the snapshot.
// Plain DELETE — huddle_member has no children; FK CASCADE from
// scene_huddle handles the parent-deleted case.
const deleteStaleSQLM = `DELETE FROM huddle_member WHERE snapshot_gen < $1`

// nextGenSQLM bumps the child's sequence (independent gen tier from
// the parent).
const nextGenSQLM = `SELECT nextval('huddle_member_snapshot_gen_seq')`

// LoadAll loads every scene_huddle row plus its huddle_member children
// into memory.
//
// Runs against the pool directly (no Tx) — read-only restart path.
// Same posture as VillageObjectsRepo.LoadAll: relies on LoadAll
// running before the world goroutine starts and before any checkpoint
// writer can mutate these tables. Without that startup guarantee, the
// two queries could observe different committed states under
// READ COMMITTED.
//
// A huddle_member row whose huddle_id isn't in the loaded parent set
// surfaces as an error (FK CASCADE makes this impossible from valid
// writes; the guard surfaces schema drift loudly).
func (r *HuddlesRepo) LoadAll(ctx context.Context) (map[sim.HuddleID]*sim.Huddle, error) {
	rows, err := r.pool.Query(ctx, loadAllSQLH)
	if err != nil {
		return nil, fmt.Errorf("pg huddles LoadAll query: %w", err)
	}
	defer rows.Close()

	out := make(map[sim.HuddleID]*sim.Huddle)
	for rows.Next() {
		var (
			id          string
			structureID *string
			startedAt   time.Time
			concludedAt *time.Time
		)
		if err := rows.Scan(&id, &structureID, &startedAt, &concludedAt); err != nil {
			return nil, fmt.Errorf("pg huddles LoadAll scan: %w", err)
		}
		var sid sim.StructureID
		if structureID != nil {
			sid = sim.StructureID(*structureID)
		}
		out[sim.HuddleID(id)] = &sim.Huddle{
			ID:          sim.HuddleID(id),
			Members:     make(map[sim.ActorID]struct{}),
			StructureID: sid,
			StartedAt:   startedAt,
			ConcludedAt: concludedAt,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg huddles LoadAll iter: %w", err)
	}

	if err := r.loadAllMembers(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

// loadAllMembers reads every huddle_member row and attaches the
// actor_id to its parent's Members set. Orphan rows (huddle_id with
// no loaded parent) return an error — FK CASCADE makes this
// unreachable from valid writes, so seeing one means schema drift or
// a manual SQL write outside the engine's path.
func (r *HuddlesRepo) loadAllMembers(ctx context.Context, huddles map[sim.HuddleID]*sim.Huddle) error {
	rows, err := r.pool.Query(ctx, loadAllMembersSQLH)
	if err != nil {
		return fmt.Errorf("pg huddles LoadAll members query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var huddleID, actorID string
		if err := rows.Scan(&huddleID, &actorID); err != nil {
			return fmt.Errorf("pg huddles LoadAll members scan: %w", err)
		}
		parent, ok := huddles[sim.HuddleID(huddleID)]
		if !ok {
			return fmt.Errorf("pg huddles LoadAll: orphan member row huddle_id=%s actor_id=%s (parent missing — schema drift or out-of-band write)",
				huddleID, actorID)
		}
		parent.Members[sim.ActorID(actorID)] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("pg huddles LoadAll members iter: %w", err)
	}
	return nil
}

// SaveSnapshot writes the full Huddle + Members set durably using the
// generation-marker pattern (Slice 9/10 standard).
//
// Steps inside the caller's checkpoint Tx (order matters — parent
// settles before children sync):
//
//  0. Advisory lock — shared by both tables.
//  1. nextval(huddle_snapshot_gen_seq) → $genHuddle.
//  2. Per-row UPSERT scene_huddle, stamping snapshot_gen = $genHuddle.
//     Substrate-boundary validation: reject empty HuddleID, map-key
//     ↔ h.ID mismatch, concluded-with-non-empty-Members, zero
//     StartedAt. Nil entries silently skipped.
//  3. DELETE scene_huddle WHERE snapshot_gen < $genHuddle. Plain
//     DELETE — no self-FK. FK CASCADE drops orphan huddle_member rows
//     for deleted parents.
//  4. nextval(huddle_member_snapshot_gen_seq) → $genMember.
//  5. Per-huddle per-member UPSERT huddle_member, stamping new gen.
//     Concluded huddles have empty Members in memory so no UPSERTs
//     run for them; their member rows (if any) were already dropped
//     by step 3's FK CASCADE (concluded huddle still in snapshot
//     stays alive, but membership is empty so no upsert and trailing
//     delete cleans any stragglers).
//  6. DELETE huddle_member WHERE snapshot_gen < $genMember.
//
// Empty huddles map: both gens still bump, no UPSERTs run, both
// DELETEs sweep their tables.
//
// nil huddle entries in the map are silently skipped.
func (r *HuddlesRepo) SaveSnapshot(ctx context.Context, tx sim.Tx, huddles map[sim.HuddleID]*sim.Huddle) error {
	if tx == nil {
		return fmt.Errorf("pg huddles SaveSnapshot: nil tx")
	}

	// Step 0: advisory lock.
	if _, err := tx.Exec(ctx, advisoryLockSQLH); err != nil {
		return fmt.Errorf("pg huddles SaveSnapshot: advisory lock: %w", err)
	}

	// Step 1: parent gen.
	var huddleGen int64
	if err := tx.QueryRow(ctx, nextGenSQLH).Scan(&huddleGen); err != nil {
		return fmt.Errorf("pg huddles SaveSnapshot: nextval huddle: %w", err)
	}

	// Step 2: upsert each huddle.
	//
	// Substrate-boundary validation: every guard here defends an
	// invariant the in-memory model already enforces, but Commands are
	// public-callable. Catching shape bugs at the repo boundary
	// surfaces them on the failing checkpoint Tx instead of as a
	// partial-Tx CHECK / UNIQUE violation downstream, and avoids
	// stamping inconsistent state.
	for key, h := range huddles {
		if h == nil {
			continue
		}
		if h.ID == "" {
			return fmt.Errorf("pg huddles SaveSnapshot: empty HuddleID (map key=%s)", key)
		}
		if h.ID != key {
			return fmt.Errorf("pg huddles SaveSnapshot: map key=%s does not match h.ID=%s", key, h.ID)
		}
		if h.ConcludedAt != nil && len(h.Members) != 0 {
			return fmt.Errorf("pg huddles SaveSnapshot: id=%s concluded but Members non-empty (size=%d) — ConcludeHuddle must wipe Members",
				h.ID, len(h.Members))
		}
		if h.StartedAt.IsZero() {
			return fmt.Errorf("pg huddles SaveSnapshot: id=%s has zero StartedAt", h.ID)
		}
		// Empty StructureID (outdoor huddle) → SQL NULL. CHECK
		// constraint forbids empty-but-not-NULL.
		var structureArg any
		if h.StructureID != "" {
			structureArg = string(h.StructureID)
		}
		if _, err := tx.Exec(ctx, upsertSQLH,
			string(h.ID),  // $1 id (TEXT)
			structureArg,  // $2 structure_id (TEXT, nullable)
			h.StartedAt,   // $3 started_at
			h.ConcludedAt, // $4 concluded_at (nullable)
			huddleGen,     // $5 snapshot_gen
		); err != nil {
			return fmt.Errorf("pg huddles SaveSnapshot: upsert id=%s: %w", h.ID, err)
		}
	}

	// Step 3: prune absent parents. FK CASCADE drops their member rows.
	if _, err := tx.Exec(ctx, deleteStaleSQLH, huddleGen); err != nil {
		return fmt.Errorf("pg huddles SaveSnapshot: delete stale huddle: %w", err)
	}

	// Step 4: child gen — independent tier from parent.
	var memberGen int64
	if err := tx.QueryRow(ctx, nextGenSQLM).Scan(&memberGen); err != nil {
		return fmt.Errorf("pg huddles SaveSnapshot: nextval member: %w", err)
	}

	// Step 5: upsert each member of each non-concluded huddle.
	// Concluded huddles have empty Members in memory (ConcludeHuddle
	// wipes the map) so no upserts run for them. Iterating all huddles
	// is correct — empty-Members loops are no-ops.
	//
	// seenHuddleByActor surfaces a genuine world-side invariant violation (the
	// same actor listed as a live member of two huddles in one snapshot)
	// without failing the checkpoint: the first huddle iterated wins, any later
	// duplicate is logged and skipped (one write per actor, no flip-flopping
	// upsert). This is the loud signal the old fail-the-Tx clause reached for,
	// minus the durability outage.
	seenHuddleByActor := make(map[sim.ActorID]sim.HuddleID)
	for _, h := range huddles {
		if h == nil {
			continue
		}
		for actorID := range h.Members {
			if prev, dup := seenHuddleByActor[actorID]; dup {
				log.Printf("pg huddles SaveSnapshot: actor %s already in huddle %s, also listed in %s — keeping first; world-side membership bug",
					actorID, prev, h.ID)
				continue
			}
			seenHuddleByActor[actorID] = h.ID
			// LLM-452: transient visitors (vstr- ids) are members in the
			// live model but are NOT persisted to the uuid `actor` table
			// (partitioned persistence). A member row that can't be
			// reloaded shouldn't be written — persisting one leaves a
			// dangling huddle_member that fatals LoadWorld reconciliation
			// on the next boot (no FK CASCADE can exist to clean it, since
			// actor_id is text vstr- vs actor.id uuid). Skip only the DB
			// write — the dup check above still runs, so a visitor listed in
			// two huddles surfaces as the same world-side membership bug;
			// and the live membership stays intact in memory.
			if sim.IsVisitorActorID(actorID) {
				continue
			}
			if _, err := tx.Exec(ctx, upsertSQLM,
				string(h.ID),    // $1 huddle_id
				string(actorID), // $2 actor_id
				memberGen,       // $3 snapshot_gen
			); err != nil {
				return fmt.Errorf("pg huddles SaveSnapshot: upsert member huddle=%s actor=%s: %w",
					h.ID, actorID, err)
			}
		}
	}

	// Step 6: prune absent member rows.
	if _, err := tx.Exec(ctx, deleteStaleSQLM, memberGen); err != nil {
		return fmt.Errorf("pg huddles SaveSnapshot: delete stale member: %w", err)
	}
	return nil
}
