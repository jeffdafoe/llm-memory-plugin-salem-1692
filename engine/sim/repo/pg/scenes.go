package pg

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// ScenesRepo reads and writes Scene rows against `scene` plus the
// `scene_huddle_ref` child table that carries Scene.Huddles. Owns both
// as one aggregate.
//
// SaveSnapshot uses the generation-marker pattern (Slice 9/10/11/12
// precedent — see `shared/notes/codebase/salem-engine-v2/pg-snapshot-pattern`).
// Both tables get per-row UPSERT inside the caller's checkpoint Tx,
// then per-table `DELETE WHERE snapshot_gen < $gen` prunes anything
// absent. Each table owns its own sequence; the parent's advisory lock
// covers both.
//
// Persistence scope (option A — minimal + cascade-lifetime):
//   - PERSISTED: ID, OriginAt, OriginKind, Bound (Structure + Area
//     variants), OriginPosition, and the Huddles set via the child
//     table.
//   - DROPPED: ParticipantStateAtOrigin (heavy; loop-detection
//     re-anchors on first post-restart perception); QuoteIDs (already
//     rebuilt at LoadWorld via rebuildSceneQuoteIndex per PR S3).
//   - SKIPPED: Bound.Kind == SceneBoundUnbounded scenes are NOT
//     persisted — they "never officially end" in v2 and accumulate in
//     memory; persisting them would grow the pg table unboundedly.
//     Schema CHECK also forbids bound_kind='unbounded' (defense-in-depth).
//
// Concluded scenes are deleted from w.Scenes by the engine
// (engine/sim/huddle_commands.go:704); the gen-marker stale-DELETE
// sweep is the GC mechanism. No `concluded_at` column.
type ScenesRepo struct {
	pool Pool
}

// NewScenesRepo constructs a ScenesRepo against the given pool.
func NewScenesRepo(pool Pool) *ScenesRepo {
	return &ScenesRepo{pool: pool}
}

const loadAllSQLSc = `
SELECT id, origin_at, origin_kind, bound_kind,
       bound_structure_id, bound_anchor_x, bound_anchor_y, bound_radius,
       origin_position_x, origin_position_y
  FROM scene`

const loadAllRefsSQLSc = `
SELECT scene_id, huddle_id
  FROM scene_huddle_ref`

const upsertSQLSc = `
INSERT INTO scene (
    id, origin_at, origin_kind, bound_kind,
    bound_structure_id, bound_anchor_x, bound_anchor_y, bound_radius,
    origin_position_x, origin_position_y, snapshot_gen
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
)
ON CONFLICT (id) DO UPDATE SET
    origin_at          = EXCLUDED.origin_at,
    origin_kind        = EXCLUDED.origin_kind,
    bound_kind         = EXCLUDED.bound_kind,
    bound_structure_id = EXCLUDED.bound_structure_id,
    bound_anchor_x     = EXCLUDED.bound_anchor_x,
    bound_anchor_y     = EXCLUDED.bound_anchor_y,
    bound_radius       = EXCLUDED.bound_radius,
    origin_position_x  = EXCLUDED.origin_position_x,
    origin_position_y  = EXCLUDED.origin_position_y,
    snapshot_gen       = EXCLUDED.snapshot_gen`

const upsertSQLScRef = `
INSERT INTO scene_huddle_ref (scene_id, huddle_id, snapshot_gen)
VALUES ($1, $2, $3)
ON CONFLICT (scene_id, huddle_id) DO UPDATE SET
    snapshot_gen = EXCLUDED.snapshot_gen`

const deleteStaleSQLSc = `DELETE FROM scene WHERE snapshot_gen < $1`

const deleteStaleSQLScRef = `DELETE FROM scene_huddle_ref WHERE snapshot_gen < $1`

const nextGenSQLSc = `SELECT nextval('scene_snapshot_gen_seq')`

const nextGenSQLScRef = `SELECT nextval('scene_huddle_ref_snapshot_gen_seq')`

const advisoryLockSQLSc = `SELECT pg_advisory_xact_lock(hashtext('scene_snapshot'), 0)`

// LoadAll loads every scene row plus its scene_huddle_ref children into
// memory.
//
// Runs against the pool directly (no Tx — read-only restart path).
// Relies on LoadAll running before the world goroutine starts and
// before any checkpoint writer can mutate these tables.
//
// A scene_huddle_ref row whose scene_id isn't in the loaded parent set
// surfaces as an error (FK CASCADE makes this impossible from valid
// writes; the guard surfaces schema drift loudly).
func (r *ScenesRepo) LoadAll(ctx context.Context) (map[sim.SceneID]*sim.Scene, error) {
	rows, err := r.pool.Query(ctx, loadAllSQLSc)
	if err != nil {
		return nil, fmt.Errorf("pg scenes LoadAll query: %w", err)
	}
	defer rows.Close()

	out := make(map[sim.SceneID]*sim.Scene)
	for rows.Next() {
		var (
			id               string
			originAt         time.Time
			originKind       string
			boundKind        string
			boundStructureID *string
			boundAnchorX     *int
			boundAnchorY     *int
			boundRadius      *int
			originX          int
			originY          int
		)
		if err := rows.Scan(&id, &originAt, &originKind, &boundKind,
			&boundStructureID, &boundAnchorX, &boundAnchorY, &boundRadius,
			&originX, &originY); err != nil {
			return nil, fmt.Errorf("pg scenes LoadAll scan: %w", err)
		}
		bound, err := scanBound(boundKind, boundStructureID, boundAnchorX, boundAnchorY, boundRadius)
		if err != nil {
			return nil, fmt.Errorf("pg scenes LoadAll id=%s: %w", id, err)
		}
		sid := sim.SceneID(id)
		// Defensive against admin-direct writes / schema drift: PK
		// prevents duplicates in valid DB state, but matching the loud-
		// drift guards on orphan rows + unknown bound_kind. (code_review R1.)
		if _, exists := out[sid]; exists {
			return nil, fmt.Errorf("pg scenes LoadAll: duplicate scene id=%s in result set (schema drift or out-of-band write)", id)
		}
		out[sid] = &sim.Scene{
			ID:             sid,
			OriginAt:       originAt,
			OriginKind:     originKind,
			Bound:          bound,
			OriginPosition: sim.Position{X: originX, Y: originY},
			Huddles:        make(map[sim.HuddleID]struct{}),
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg scenes LoadAll iter: %w", err)
	}

	if err := r.loadAllRefs(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

// scanBound rebuilds a SceneBound from the variant columns. Mirrors
// validateBoundShape's rejections at the load boundary — the DB CHECK
// should prevent these states, but a corrupted row (admin-direct
// write, dropped CHECK during a botched migration, etc.) must surface
// as a load error rather than silently load with extra fields ignored.
// (code_review R1 2026-05-19.)
//
// Also: NewAreaBound clamps negative radii to 0, which would HIDE
// corruption on load. The explicit `*radius < 0` check below catches
// it loudly.
func scanBound(kind string, structureID *string, anchorX, anchorY, radius *int) (sim.SceneBound, error) {
	switch kind {
	case string(sim.SceneBoundStructure):
		if structureID == nil {
			return sim.SceneBound{}, fmt.Errorf("bound_kind=structure but bound_structure_id is NULL (schema drift)")
		}
		if strings.TrimSpace(*structureID) == "" {
			return sim.SceneBound{}, fmt.Errorf("bound_kind=structure but bound_structure_id is empty/whitespace (schema drift)")
		}
		if anchorX != nil || anchorY != nil || radius != nil {
			return sim.SceneBound{}, fmt.Errorf("bound_kind=structure but area columns (bound_anchor_x/y/radius) are populated (schema drift)")
		}
		return sim.NewStructureBound(sim.StructureID(*structureID)), nil
	case string(sim.SceneBoundArea):
		if anchorX == nil || anchorY == nil || radius == nil {
			return sim.SceneBound{}, fmt.Errorf("bound_kind=area but one of bound_anchor_x/y/radius is NULL (schema drift)")
		}
		if structureID != nil {
			return sim.SceneBound{}, fmt.Errorf("bound_kind=area but bound_structure_id is populated (schema drift)")
		}
		if *radius < 0 {
			return sim.SceneBound{}, fmt.Errorf("bound_kind=area but bound_radius=%d is negative (schema drift; NewAreaBound clamps to 0 which would hide this)", *radius)
		}
		return sim.NewAreaBound(sim.Position{X: *anchorX, Y: *anchorY}, *radius), nil
	default:
		return sim.SceneBound{}, fmt.Errorf("unknown bound_kind=%q (schema drift or new variant not handled here)", kind)
	}
}

// loadAllRefs reads every scene_huddle_ref row and attaches the
// huddle_id to its parent's Huddles set. Orphan rows (no parent in
// loaded set) return an error — FK CASCADE makes this unreachable
// from valid writes.
func (r *ScenesRepo) loadAllRefs(ctx context.Context, scenes map[sim.SceneID]*sim.Scene) error {
	rows, err := r.pool.Query(ctx, loadAllRefsSQLSc)
	if err != nil {
		return fmt.Errorf("pg scenes LoadAll refs query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var sceneID, huddleID string
		if err := rows.Scan(&sceneID, &huddleID); err != nil {
			return fmt.Errorf("pg scenes LoadAll refs scan: %w", err)
		}
		parent, ok := scenes[sim.SceneID(sceneID)]
		if !ok {
			return fmt.Errorf("pg scenes LoadAll: orphan scene_huddle_ref row scene_id=%s huddle_id=%s (parent missing — schema drift or out-of-band write)",
				sceneID, huddleID)
		}
		parent.Huddles[sim.HuddleID(huddleID)] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("pg scenes LoadAll refs iter: %w", err)
	}
	return nil
}

// SaveSnapshot writes the full Scene + Huddles set durably using the
// generation-marker pattern (Slice 9/10/11/12 standard).
//
// Steps inside the caller's checkpoint Tx (order matters — parent
// settles before children sync):
//
//  0. Advisory lock — shared by both tables.
//  1. nextval(scene_snapshot_gen_seq) → $genScene.
//  2. Pre-pass validation across the WHOLE snapshot. Identity checks
//     run for every non-nil scene INCLUDING Unbounded (corrupt
//     unbounded scenes shouldn't hide just because they get skipped
//     at upsert — design_review #8). Unbounded scenes are filtered
//     out of the upsert step but their identity is still validated.
//  3. Per-row UPSERT scene for each non-Unbounded scene.
//  4. DELETE scene WHERE snapshot_gen < $genScene. FK CASCADE from
//     scene_huddle_ref → scene drops orphan refs for deleted parents.
//  5. nextval(scene_huddle_ref_snapshot_gen_seq) → $genRef.
//  6. Per-scene per-huddle UPSERT scene_huddle_ref (only for persisted
//     scenes; Unbounded scenes' Huddles set is silently discarded).
//  7. DELETE scene_huddle_ref WHERE snapshot_gen < $genRef.
//
// Empty scenes map: both gens still bump, no UPSERTs run, both
// DELETEs sweep their tables.
func (r *ScenesRepo) SaveSnapshot(ctx context.Context, tx sim.Tx, scenes map[sim.SceneID]*sim.Scene) error {
	if tx == nil {
		return fmt.Errorf("pg scenes SaveSnapshot: nil tx")
	}

	// Step 0: advisory lock.
	if _, err := tx.Exec(ctx, advisoryLockSQLSc); err != nil {
		return fmt.Errorf("pg scenes SaveSnapshot: advisory lock: %w", err)
	}

	// Step 1: parent gen.
	var sceneGen int64
	if err := tx.QueryRow(ctx, nextGenSQLSc).Scan(&sceneGen); err != nil {
		return fmt.Errorf("pg scenes SaveSnapshot: nextval scene: %w", err)
	}

	// Step 2: pre-pass validation. Identity for ALL non-nil scenes
	// including Unbounded; Bound-variant shape for the kind that will
	// be persisted. Failures here surface as substrate errors before
	// any write.
	for key, s := range scenes {
		if s == nil {
			return fmt.Errorf("pg scenes SaveSnapshot: nil entry at map key=%s (use deletion via gen-marker absence, not nil)", key)
		}
		if strings.TrimSpace(string(s.ID)) == "" {
			return fmt.Errorf("pg scenes SaveSnapshot: empty SceneID (map key=%s)", key)
		}
		if s.ID != key {
			return fmt.Errorf("pg scenes SaveSnapshot: map key=%s does not match s.ID=%s", key, s.ID)
		}
		if strings.TrimSpace(s.OriginKind) == "" {
			return fmt.Errorf("pg scenes SaveSnapshot: id=%s has empty OriginKind", s.ID)
		}
		if s.OriginAt.IsZero() {
			return fmt.Errorf("pg scenes SaveSnapshot: id=%s has zero OriginAt", s.ID)
		}
		if err := validateBoundShape(s.ID, s.Bound); err != nil {
			return err
		}
		for hid := range s.Huddles {
			if strings.TrimSpace(string(hid)) == "" {
				return fmt.Errorf("pg scenes SaveSnapshot: id=%s has empty HuddleID in Huddles set", s.ID)
			}
		}
	}

	// Step 3: upsert each persisted scene (skip Unbounded).
	for _, s := range scenes {
		if s.Bound.Kind == sim.SceneBoundUnbounded {
			continue
		}
		structureArg, anchorXArg, anchorYArg, radiusArg := boundUpsertArgs(s.Bound)
		if _, err := tx.Exec(ctx, upsertSQLSc,
			string(s.ID),         // $1 id
			s.OriginAt,           // $2 origin_at
			s.OriginKind,         // $3 origin_kind
			string(s.Bound.Kind), // $4 bound_kind
			structureArg,         // $5 bound_structure_id
			anchorXArg,           // $6 bound_anchor_x
			anchorYArg,           // $7 bound_anchor_y
			radiusArg,            // $8 bound_radius
			s.OriginPosition.X,   // $9 origin_position_x
			s.OriginPosition.Y,   // $10 origin_position_y
			sceneGen,             // $11 snapshot_gen
		); err != nil {
			return fmt.Errorf("pg scenes SaveSnapshot: upsert scene id=%s: %w", s.ID, err)
		}
	}

	// Step 4: prune absent parents. FK CASCADE drops their refs.
	if _, err := tx.Exec(ctx, deleteStaleSQLSc, sceneGen); err != nil {
		return fmt.Errorf("pg scenes SaveSnapshot: delete stale scene: %w", err)
	}

	// Step 5: child gen.
	var refGen int64
	if err := tx.QueryRow(ctx, nextGenSQLScRef).Scan(&refGen); err != nil {
		return fmt.Errorf("pg scenes SaveSnapshot: nextval scene_huddle_ref: %w", err)
	}

	// Step 6: upsert each persisted scene's Huddles set. Skip Unbounded
	// (their Huddles set is discarded along with the scene).
	for _, s := range scenes {
		if s.Bound.Kind == sim.SceneBoundUnbounded {
			continue
		}
		for hid := range s.Huddles {
			if _, err := tx.Exec(ctx, upsertSQLScRef,
				string(s.ID), // $1 scene_id
				string(hid),  // $2 huddle_id
				refGen,       // $3 snapshot_gen
			); err != nil {
				return fmt.Errorf("pg scenes SaveSnapshot: upsert ref scene=%s huddle=%s: %w", s.ID, hid, err)
			}
		}
	}

	// Step 7: prune absent ref rows.
	if _, err := tx.Exec(ctx, deleteStaleSQLScRef, refGen); err != nil {
		return fmt.Errorf("pg scenes SaveSnapshot: delete stale scene_huddle_ref: %w", err)
	}
	return nil
}

// validateBoundShape checks SceneBound's tagged-union invariants at the
// substrate boundary. Mirrors the DB CHECK constraints so a corrupt
// in-memory Bound surfaces as a clean substrate error rather than a
// partial-Tx CHECK violation.
func validateBoundShape(id sim.SceneID, b sim.SceneBound) error {
	switch b.Kind {
	case sim.SceneBoundStructure:
		if b.StructureID == nil {
			return fmt.Errorf("pg scenes SaveSnapshot: id=%s Bound.Kind=structure but StructureID is nil", id)
		}
		if strings.TrimSpace(string(*b.StructureID)) == "" {
			return fmt.Errorf("pg scenes SaveSnapshot: id=%s Bound.Kind=structure has empty StructureID", id)
		}
		if b.Anchor != nil || b.Radius != nil {
			return fmt.Errorf("pg scenes SaveSnapshot: id=%s Bound.Kind=structure must not set Anchor/Radius", id)
		}
	case sim.SceneBoundArea:
		if b.Anchor == nil || b.Radius == nil {
			return fmt.Errorf("pg scenes SaveSnapshot: id=%s Bound.Kind=area requires Anchor and Radius", id)
		}
		if *b.Radius < 0 {
			return fmt.Errorf("pg scenes SaveSnapshot: id=%s Bound.Kind=area has negative Radius=%d", id, *b.Radius)
		}
		if b.StructureID != nil {
			return fmt.Errorf("pg scenes SaveSnapshot: id=%s Bound.Kind=area must not set StructureID", id)
		}
	case sim.SceneBoundUnbounded:
		// Valid kind — will be filtered from upsert. Lock down to
		// all-nil today (code_review R1): an Unbounded scene with
		// populated StructureID/Anchor/Radius is corrupt in-memory
		// state, and accepting it silently weakens the "pre-pass
		// validates the WHOLE snapshot" guarantee. If future
		// world-scope Unbounded variants need payload, loosen with a
		// matching schema migration + repo change.
		if b.StructureID != nil || b.Anchor != nil || b.Radius != nil {
			return fmt.Errorf("pg scenes SaveSnapshot: id=%s Bound.Kind=unbounded must not set StructureID/Anchor/Radius", id)
		}
	default:
		return fmt.Errorf("pg scenes SaveSnapshot: id=%s unknown Bound.Kind=%q", id, b.Kind)
	}
	return nil
}

// boundUpsertArgs converts a SceneBound to the four nullable bind
// values for the scene upsert. Called only after validateBoundShape
// has confirmed the variant is Structure or Area (Unbounded is
// filtered upstream).
func boundUpsertArgs(b sim.SceneBound) (structureArg, anchorXArg, anchorYArg, radiusArg any) {
	switch b.Kind {
	case sim.SceneBoundStructure:
		structureArg = string(*b.StructureID)
	case sim.SceneBoundArea:
		anchorXArg = b.Anchor.X
		anchorYArg = b.Anchor.Y
		radiusArg = *b.Radius
	}
	return
}
