package pg

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// VisitorsRepo reads and writes the visitor table — the durable mirror of the
// in-flight transient-visitor set (LLM-369). Visitors are firewalled from the
// 11-tier actor aggregate (ActorsRepo.SaveSnapshot skips every actor with
// VisitorState != nil), so this lean tier carries them separately so a traveler
// survives an engine restart instead of vanishing mid-scene.
//
// SaveSnapshot uses the single-table generation-marker pattern (same shape as
// labor_contract, LLM-259): advisory lock → nextval(gen) → per-row UPSERT
// stamping snapshot_gen → DELETE WHERE snapshot_gen < gen. A visitor that left
// the live set between checkpoints (departed + cleaned up) is absent from the
// map, so the trailing delete sweeps its row — the table stays a true mirror of
// the live in-flight set, crash-consistent with the rest of the checkpoint
// because it writes inside the same SaveWorld Tx.
//
// inside_structure_id is a soft TEXT ref to structure(id) with NO FK — the v2
// cross-aggregate posture (integrity revalidated Go-side at rehydrate), same as
// the actor aggregate's structure refs.
type VisitorsRepo struct {
	pool Pool
}

// NewVisitorsRepo constructs a VisitorsRepo against the given pool. Normal wiring
// path is pg.NewRepository which wires this internally.
func NewVisitorsRepo(pool Pool) *VisitorsRepo {
	return &VisitorsRepo{pool: pool}
}

// loadAllSQLV selects every visitor row. snapshot_gen omitted — pure sync
// bookkeeping with no in-memory representation. No ORDER BY — the in-memory
// model is map-keyed.
const loadAllSQLV = `
SELECT actor_id, display_name, archetype, origin, disposition,
       position_x, position_y, inside_structure_id, expires_at, phase
  FROM visitor`

// upsertSQLV writes one visitor row. snapshot_gen carries the new checkpoint gen
// so the trailing DELETE can prune stale rows.
const upsertSQLV = `
INSERT INTO visitor (
    actor_id, display_name, archetype, origin, disposition,
    position_x, position_y, inside_structure_id, expires_at, phase, snapshot_gen
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
)
ON CONFLICT (actor_id) DO UPDATE SET
    display_name        = EXCLUDED.display_name,
    archetype           = EXCLUDED.archetype,
    origin              = EXCLUDED.origin,
    disposition         = EXCLUDED.disposition,
    position_x          = EXCLUDED.position_x,
    position_y          = EXCLUDED.position_y,
    inside_structure_id = EXCLUDED.inside_structure_id,
    expires_at          = EXCLUDED.expires_at,
    phase               = EXCLUDED.phase,
    snapshot_gen        = EXCLUDED.snapshot_gen`

// deleteStaleSQLV prunes visitor rows whose snapshot_gen is below the current
// checkpoint gen — the visitors absent from this snapshot (departed + cleaned
// up between checkpoints). Plain DELETE — no self-FK.
const deleteStaleSQLV = `DELETE FROM visitor WHERE snapshot_gen < $1`

// nextGenSQLV bumps the aggregate's gen sequence.
const nextGenSQLV = `SELECT nextval('visitor_snapshot_gen_seq')`

// advisoryLockSQLV is the single global lock for the visitor aggregate, held for
// the Tx duration to serialize concurrent SaveSnapshot calls. Multi-realm upgrade
// path: replace 0 with hashtext($realm_id) when realms land.
const advisoryLockSQLV = `SELECT pg_advisory_xact_lock(hashtext('visitor_snapshot'), 0)`

// LoadAll loads every visitor row into a map of the reload-DTO the rehydrate pass
// (World.rehydrateVisitorsOnLoad) rebuilds a live Actor from. Runs against the
// pool directly (no Tx) — read-only restart path, same posture as the other
// LoadAll implementations.
func (r *VisitorsRepo) LoadAll(ctx context.Context) (map[sim.ActorID]*sim.LoadedVisitor, error) {
	rows, err := r.pool.Query(ctx, loadAllSQLV)
	if err != nil {
		return nil, fmt.Errorf("pg visitors LoadAll query: %w", err)
	}
	defer rows.Close()

	out := make(map[sim.ActorID]*sim.LoadedVisitor)
	for rows.Next() {
		var (
			actorID     string
			displayName string
			archetype   string
			origin      string
			disposition string
			posX        int
			posY        int
			insideID    *string
			expiresAt   time.Time
			phase       string
		)
		if err := rows.Scan(&actorID, &displayName, &archetype, &origin, &disposition,
			&posX, &posY, &insideID, &expiresAt, &phase); err != nil {
			return nil, fmt.Errorf("pg visitors LoadAll scan: %w", err)
		}
		var inside sim.StructureID
		if insideID != nil {
			inside = sim.StructureID(*insideID)
		}
		out[sim.ActorID(actorID)] = &sim.LoadedVisitor{
			ID:                sim.ActorID(actorID),
			DisplayName:       displayName,
			Pos:               sim.TilePos{X: posX, Y: posY},
			InsideStructureID: inside,
			VisitorState: &sim.VisitorState{
				Archetype:   archetype,
				Origin:      origin,
				Disposition: disposition,
				ExpiresAt:   expiresAt,
				Phase:       sim.VisitorPhase(phase),
			},
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg visitors LoadAll iter: %w", err)
	}
	return out, nil
}

// SaveSnapshot writes the in-flight visitor set durably via the generation-marker
// pattern, inside the caller's checkpoint Tx. It is the COMPLEMENT of
// ActorsRepo.SaveSnapshot: it is handed the same cp.Actors map and persists
// exactly the actors that aggregate skips (VisitorState != nil), so the two
// partition the actor set with no overlap.
//
//  0. Advisory lock.
//  1. nextval(visitor_snapshot_gen_seq) → $gen.
//  2. Per-row UPSERT of the visitor subset, stamping snapshot_gen = $gen.
//     Substrate-boundary validation: reject map-key ↔ a.ID mismatch, empty
//     DisplayName, empty phase (Go owns the phase allowlist; a spawn/cascade bug
//     that left it unset is worth surfacing on the failing checkpoint).
//  3. DELETE visitor WHERE snapshot_gen < $gen — sweep the visitors absent from
//     the snapshot (departed + cleaned up since the last checkpoint).
//
// Empty / visitor-less actors map: the gen still bumps, no UPSERTs run, the
// DELETE sweeps the whole table. nil entries are skipped (ActorsRepo.SaveSnapshot
// is the one that errors on a nil actor entry; here a nil is simply not a
// visitor).
func (r *VisitorsRepo) SaveSnapshot(ctx context.Context, tx sim.Tx, actors map[sim.ActorID]*sim.Actor) error {
	if tx == nil {
		return fmt.Errorf("pg visitors SaveSnapshot: nil tx")
	}

	if _, err := tx.Exec(ctx, advisoryLockSQLV); err != nil {
		return fmt.Errorf("pg visitors SaveSnapshot: advisory lock: %w", err)
	}

	var gen int64
	if err := tx.QueryRow(ctx, nextGenSQLV).Scan(&gen); err != nil {
		return fmt.Errorf("pg visitors SaveSnapshot: nextval: %w", err)
	}

	for key, a := range actors {
		if a == nil || a.VisitorState == nil {
			continue // not a visitor — the actor aggregate owns (or rejects) it
		}
		if a.ID != key {
			return fmt.Errorf("pg visitors SaveSnapshot: map key=%s does not match a.ID=%s", key, a.ID)
		}
		if strings.TrimSpace(a.DisplayName) == "" {
			return fmt.Errorf("pg visitors SaveSnapshot: id=%s has empty DisplayName", a.ID)
		}
		vs := a.VisitorState
		if !vs.Phase.Valid() {
			return fmt.Errorf("pg visitors SaveSnapshot: id=%s has invalid visitor phase %q (Go owns the allowlist)", a.ID, vs.Phase)
		}
		// inside_structure_id: bind "" as SQL NULL so the column round-trips
		// outdoors-or-inside cleanly (matches the visitor_inside_structure_id_nonempty
		// CHECK, which allows NULL but not '').
		var insideArg any
		if a.InsideStructureID != "" {
			insideArg = string(a.InsideStructureID)
		}
		if _, err := tx.Exec(ctx, upsertSQLV,
			string(a.ID),     // $1  actor_id
			a.DisplayName,    // $2  display_name
			vs.Archetype,     // $3  archetype
			vs.Origin,        // $4  origin
			vs.Disposition,   // $5  disposition
			a.Pos.X,          // $6  position_x
			a.Pos.Y,          // $7  position_y
			insideArg,        // $8  inside_structure_id (nullable)
			vs.ExpiresAt,     // $9  expires_at
			string(vs.Phase), // $10 phase
			gen,              // $11 snapshot_gen
		); err != nil {
			return fmt.Errorf("pg visitors SaveSnapshot: upsert id=%s: %w", a.ID, err)
		}
	}

	if _, err := tx.Exec(ctx, deleteStaleSQLV, gen); err != nil {
		return fmt.Errorf("pg visitors SaveSnapshot: delete stale: %w", err)
	}
	return nil
}
