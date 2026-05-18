package pg

import (
	"context"
	"fmt"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// VillageObjectsRepo reads and writes VillageObject rows against
// village_object. Owns the table as one aggregate: the v1 sidecar
// village_object_tag is collapsed into the parent's tags TEXT[] column
// by migration ZBBS-WORK-237.
//
// SaveSnapshot semantics use the generation-marker pattern (Slice 9 —
// see `shared/notes/codebase/salem-engine-v2/pg-snapshot-pattern`).
// Per-row UPSERT inside one Tx, stamping the new gen on every snapshot
// row, then `DELETE WHERE snapshot_gen < $gen` prunes anything absent
// from the snapshot. The supplied map IS the complete VillageObject set.
//
// LoadAll returns Refreshes: nil — per-instance refresh state lives in
// a separate object_refresh table whose port is a follow-up slice (v1
// has 3 columns, v2 needs 6+; schema redesign, not just a port). The
// in-memory subsystem reconstructs refresh state from catalog defaults
// on first tick until that slice ships.
type VillageObjectsRepo struct {
	pool Pool
}

// NewVillageObjectsRepo constructs a VillageObjectsRepo against the
// given pool. Mainly for callers that want to swap just this sub-repo;
// the normal path is pg.NewRepository which wires this internally.
func NewVillageObjectsRepo(pool Pool) *VillageObjectsRepo {
	return &VillageObjectsRepo{pool: pool}
}

// loadAllSQL selects every village_object row. No JOIN — tags collapsed
// onto the parent (TEXT[]) by migration ZBBS-WORK-237. owner is the v1
// legacy string still in place for v1 reader compat; v2 reads
// owner_actor_id (typed) which the migration backfilled via the actor
// table join.
//
// snapshot_gen is not selected — it's pure sync bookkeeping for the
// SaveSnapshot delete-stale step and has no in-memory representation.
//
// No ORDER BY — the in-memory model is map-keyed, so row order doesn't
// matter. Skipping the sort keeps LoadAll cheap at restart.
const loadAllSQLVO = `
SELECT
    id, asset_id, current_state, x, y, placed_by, display_name,
    entry_policy, owner_actor_id, attached_to,
    loiter_offset_x, loiter_offset_y, available_quantity, tags
FROM village_object`

// upsertSQLVO writes one VillageObject row. snapshot_gen is included
// in both INSERT and UPDATE branches — every snapshot row carries the
// gen for this checkpoint so the trailing DELETE can prune stale rows
// (i.e., rows present in pg but absent from the snapshot).
//
// ON CONFLICT (id) infers village_object's PRIMARY KEY (id) — UUID,
// established by ZBBS-006-assets. If a future migration changes the
// key shape, this conflict target needs to follow.
//
// On conflict, every v2-owned column gets updated (this IS the snapshot
// — we're asserting the row's current state in full). The v1 legacy
// owner column is NOT touched; v1 readers still own that. created_at
// is NOT touched on update (audit-only field; preserved from original
// INSERT).
const upsertSQLVO = `
INSERT INTO village_object (
    id, asset_id, current_state, x, y, placed_by, display_name,
    entry_policy, owner_actor_id, attached_to,
    loiter_offset_x, loiter_offset_y, available_quantity, tags,
    snapshot_gen
) VALUES (
    $1::uuid, $2::uuid, $3, $4, $5, $6, $7,
    $8, $9, $10::uuid,
    $11, $12, $13, $14,
    $15
)
ON CONFLICT (id) DO UPDATE SET
    asset_id           = EXCLUDED.asset_id,
    current_state      = EXCLUDED.current_state,
    x                  = EXCLUDED.x,
    y                  = EXCLUDED.y,
    placed_by          = EXCLUDED.placed_by,
    display_name       = EXCLUDED.display_name,
    entry_policy       = EXCLUDED.entry_policy,
    owner_actor_id     = EXCLUDED.owner_actor_id,
    attached_to        = EXCLUDED.attached_to,
    loiter_offset_x    = EXCLUDED.loiter_offset_x,
    loiter_offset_y    = EXCLUDED.loiter_offset_y,
    available_quantity = EXCLUDED.available_quantity,
    tags               = EXCLUDED.tags,
    snapshot_gen       = EXCLUDED.snapshot_gen`

// deleteStaleSQLVO prunes village_object rows whose snapshot_gen is
// less than the just-bumped checkpoint gen — i.e., rows that exist in
// pg but were absent from the in-memory snapshot map. This is the
// generation-marker pattern's delete-absent step.
//
// FK village_object.attached_to → village_object.id ON DELETE CASCADE
// means dropping a parent would also drop its attached overlays. The
// safer DELETE refuses to drop a stale parent that still has a fresh
// child attached to it — a naive `DELETE WHERE snapshot_gen < $1`
// would silently destroy the fresh child via CASCADE. World-side
// invariants normally keep parent + child in the same gen tier; this
// guard is defense-in-depth, and the follow-up orphan check (see
// orphanCheckSQLVO) surfaces any cross-tier violation loudly.
const deleteStaleSQLVO = `
DELETE FROM village_object stale
 WHERE stale.snapshot_gen < $1
   AND NOT EXISTS (
       SELECT 1 FROM village_object fresh
        WHERE fresh.attached_to = stale.id
          AND fresh.snapshot_gen = $1
   )`

// orphanCheckSQLVO counts fresh children that reference a stale parent
// (one whose snapshot_gen is older than the current checkpoint gen).
// World-side invariants should keep parent + child in the same gen
// tier, so this count is always 0 in practice. A non-zero count means
// the world goroutine wrote a fresh child referencing a parent that
// isn't in the snapshot — a bug surface the caller needs to know
// about. SaveSnapshot returns an error and rolls back the Tx so the
// invariant violation is loud rather than silent.
const orphanCheckSQLVO = `
SELECT COUNT(*) FROM village_object fresh
  JOIN village_object stale ON fresh.attached_to = stale.id
 WHERE fresh.snapshot_gen = $1 AND stale.snapshot_gen < $1`

// nextGenSQLVO bumps the per-aggregate sequence and returns the new
// gen. Per-aggregate sequence (not a process-local counter) is atomic,
// persistent across restart, and avoids cross-aggregate coordination
// at checkpoint time.
const nextGenSQLVO = `SELECT nextval('village_object_snapshot_gen_seq')`

// advisoryLockSQLVO serializes SaveSnapshot calls for village_object
// at the Tx boundary. Held for the Tx duration (released automatically
// on commit/rollback). The gen-marker pattern is only correct when
// snapshots for the same aggregate don't interleave — a concurrent
// older snapshot's delete-stale step would silently wipe a newer
// snapshot's just-written rows (gen=10 vs gen=11; the older Tx's
// `DELETE WHERE snapshot_gen < 10` runs after the newer Tx commits
// at gen=11, deleting nothing — BUT the newer Tx's `DELETE WHERE
// snapshot_gen < 11` runs after the older Tx commits at gen=10,
// deleting the older Tx's just-written set).
//
// In v2's single-world-goroutine architecture this is normally
// guaranteed by caller serialization (the world goroutine is the sole
// checkpoint writer). The advisory lock makes the invariant enforced
// rather than assumed — defense against future callers that bypass
// the world goroutine (admin tools, cutover scripts, parallel
// migrations).
//
// `hashtext` maps the aggregate label to a 32-bit int; the
// 2-int-arg form of pg_advisory_xact_lock takes a (classid, objid)
// pair, so the second arg discriminates between aggregates sharing a
// hashtext collision. We use hashtext('village_object_snapshot') as
// classid and 0 as objid — collisions across different aggregates'
// labels are statistically negligible at the 32-bit hash space and a
// false-positive collision just means brief serialization between
// unrelated aggregates' snapshots, not correctness loss.
const advisoryLockSQLVO = `SELECT pg_advisory_xact_lock(hashtext('village_object_snapshot'), 0)`

// LoadAll loads every village_object row into memory.
//
// Runs against the pool directly (no Tx) — read-only restart path,
// same posture as OrdersRepo.LoadAll.
//
// Returns objects with Refreshes: nil. See type doc-comment for
// rationale; cross-restart refresh state is a follow-up slice.
func (r *VillageObjectsRepo) LoadAll(ctx context.Context) (map[sim.VillageObjectID]*sim.VillageObject, error) {
	rows, err := r.pool.Query(ctx, loadAllSQLVO)
	if err != nil {
		return nil, fmt.Errorf("pg village_objects LoadAll query: %w", err)
	}
	defer rows.Close()

	out := make(map[sim.VillageObjectID]*sim.VillageObject)
	for rows.Next() {
		var (
			id           string
			assetID      string
			currentState string
			x, y         float64
			placedBy     string
			displayName  string
			entryPolicy  string
			ownerActorID *string // NULL when no owner
			attachedTo   *string // NULL when not an overlay
			loiterX      *int
			loiterY      *int
			availableQty int
			tags         []string
		)
		if err := rows.Scan(
			&id, &assetID, &currentState, &x, &y, &placedBy, &displayName,
			&entryPolicy, &ownerActorID, &attachedTo,
			&loiterX, &loiterY, &availableQty, &tags,
		); err != nil {
			return nil, fmt.Errorf("pg village_objects LoadAll scan: %w", err)
		}
		var owner sim.ActorID
		if ownerActorID != nil {
			owner = sim.ActorID(*ownerActorID)
		}
		var attached sim.VillageObjectID
		if attachedTo != nil {
			attached = sim.VillageObjectID(*attachedTo)
		}
		// Normalize nil → empty slice. The column is NOT NULL DEFAULT
		// '{}' so this should never trigger in practice, but a future
		// schema drift (manual SQL writing NULL) shouldn't propagate as
		// nil into the in-memory shape — SaveSnapshot normalizes nil to
		// empty too, so consistent semantic on both sides.
		if tags == nil {
			tags = []string{}
		}
		out[sim.VillageObjectID(id)] = &sim.VillageObject{
			ID:                sim.VillageObjectID(id),
			AssetID:           sim.AssetID(assetID),
			CurrentState:      currentState,
			X:                 x,
			Y:                 y,
			PlacedBy:          placedBy,
			DisplayName:       displayName,
			EntryPolicy:       sim.EntryPolicy(entryPolicy),
			OwnerActorID:      owner,
			AttachedTo:        attached,
			LoiterOffsetX:     loiterX,
			LoiterOffsetY:     loiterY,
			Tags:              tags,
			AvailableQuantity: availableQty,
			// Refreshes intentionally nil — see type doc-comment.
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg village_objects LoadAll iter: %w", err)
	}
	return out, nil
}

// SaveSnapshot writes the full VillageObject set durably using the
// generation-marker pattern. The supplied map IS the complete snapshot;
// any village_object row whose id is absent gets DELETEd.
//
// Three steps inside the caller's checkpoint Tx:
//
//  1. SELECT nextval(seq) → $gen.
//  2. Per-row UPSERT, stamping snapshot_gen = $gen on each.
//  3. DELETE WHERE snapshot_gen < $gen — prune anything absent from
//     the snapshot.
//
// All steps share the Tx so the checkpoint is atomic — a crash
// mid-snapshot rolls back, leaving pg consistent with the prior
// checkpoint.
//
// Empty map: $gen is still bumped, no UPSERTs happen, DELETE removes
// every row in the table (snapshot semantic: "this is everything;
// empty means nothing should be here").
//
// nil object entries in the map are silently skipped (matches Slice 5
// OrdersRepo.SaveSnapshot pattern).
func (r *VillageObjectsRepo) SaveSnapshot(ctx context.Context, tx sim.Tx, objects map[sim.VillageObjectID]*sim.VillageObject) error {
	if tx == nil {
		return fmt.Errorf("pg village_objects SaveSnapshot: nil tx")
	}

	// Step 0: acquire the per-aggregate advisory lock. Held for the Tx
	// duration; serializes concurrent SaveSnapshot calls for this
	// aggregate. See advisoryLockSQLVO doc for rationale.
	if _, err := tx.Exec(ctx, advisoryLockSQLVO); err != nil {
		return fmt.Errorf("pg village_objects SaveSnapshot: advisory lock: %w", err)
	}

	// Step 1: bump the sequence for this checkpoint.
	var gen int64
	if err := tx.QueryRow(ctx, nextGenSQLVO).Scan(&gen); err != nil {
		return fmt.Errorf("pg village_objects SaveSnapshot: nextval: %w", err)
	}

	// Step 2: upsert each object, stamping the new gen.
	for _, obj := range objects {
		if obj == nil {
			continue
		}
		// owner_actor_id is nullable; convert empty ActorID to nil so pg
		// stores NULL (not the empty string — semantically "no owner",
		// matching the nullable column intent).
		var ownerArg any
		if obj.OwnerActorID != "" {
			ownerArg = string(obj.OwnerActorID)
		}
		var attachedArg any
		if obj.AttachedTo != "" {
			attachedArg = string(obj.AttachedTo)
		}
		// tags: nil → empty slice (pg column is NOT NULL DEFAULT '{}').
		tags := obj.Tags
		if tags == nil {
			tags = []string{}
		}
		if _, err := tx.Exec(ctx, upsertSQLVO,
			string(obj.ID),          // $1 id (UUID)
			string(obj.AssetID),     // $2 asset_id (UUID)
			obj.CurrentState,        // $3 current_state
			obj.X,                   // $4 x
			obj.Y,                   // $5 y
			obj.PlacedBy,            // $6 placed_by
			obj.DisplayName,         // $7 display_name
			string(obj.EntryPolicy), // $8 entry_policy
			ownerArg,                // $9 owner_actor_id (nullable)
			attachedArg,             // $10 attached_to (nullable UUID)
			obj.LoiterOffsetX,       // $11 loiter_offset_x (nullable)
			obj.LoiterOffsetY,       // $12 loiter_offset_y (nullable)
			obj.AvailableQuantity,   // $13 available_quantity
			tags,                    // $14 tags (text[])
			gen,                     // $15 snapshot_gen
		); err != nil {
			return fmt.Errorf("pg village_objects SaveSnapshot: upsert id=%s: %w", obj.ID, err)
		}
	}

	// Step 3: prune absent rows. The safer DELETE keeps stale parents
	// alive when they still have fresh children attached, so the FK
	// CASCADE never destroys fresh data.
	if _, err := tx.Exec(ctx, deleteStaleSQLVO, gen); err != nil {
		return fmt.Errorf("pg village_objects SaveSnapshot: delete stale: %w", err)
	}

	// Step 4: orphan check. Any fresh child referencing a stale parent
	// means the world-side parent/child-same-gen invariant got broken
	// (the safer DELETE preserved the stale parent so the FK didn't
	// CASCADE-destroy the child, but the in-memory snapshot is internally
	// inconsistent). Error + rollback so the violation surfaces.
	var orphanCount int64
	if err := tx.QueryRow(ctx, orphanCheckSQLVO, gen).Scan(&orphanCount); err != nil {
		return fmt.Errorf("pg village_objects SaveSnapshot: orphan check: %w", err)
	}
	if orphanCount > 0 {
		return fmt.Errorf("pg village_objects SaveSnapshot: world invariant violation — %d fresh children reference stale parents (cross-tier snapshot; world goroutine bug)", orphanCount)
	}
	return nil
}
