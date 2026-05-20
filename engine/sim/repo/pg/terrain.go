package pg

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// TerrainRepo reads the singleton village_terrain blob (id=1). Reference
// state — read-only, no checkpoint path (admin terrain edits write
// directly to village_terrain through the editor HTTP port).
type TerrainRepo struct {
	pool Pool
}

// NewTerrainRepo constructs a TerrainRepo against the given pool. Normal
// wiring path is pg.NewRepository.
func NewTerrainRepo(pool Pool) *TerrainRepo {
	return &TerrainRepo{pool: pool}
}

// loadTerrainSQL reads the singleton row. id=1 is enforced by the
// single_terrain CHECK constraint.
const loadTerrainSQL = `SELECT width, height, data FROM village_terrain WHERE id = 1`

// Load reads the village_terrain singleton. Returns (nil, nil) when no
// row exists — matches the mem repo contract and v1's HTTP 404 + client
// procedural-fallback behavior (LoadWorld tolerates a nil terrain).
//
// A row that IS present but whose dimensions or blob length don't match
// the fixed grid (MapW × MapH) is a hard schema error: sim.Terrain
// assumes a row-major MapW*MapH blob, and returning a wrong-sized one
// would silently corrupt pathfinding rather than fail loudly.
//
// Runs against the pool directly (no Tx) — read-only restart path, same
// posture as the other repos' LoadAll.
func (r *TerrainRepo) Load(ctx context.Context) (*sim.Terrain, error) {
	var (
		width, height int
		data          []byte
	)
	err := r.pool.QueryRow(ctx, loadTerrainSQL).Scan(&width, &height, &data)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("pg terrain Load: query: %w", err)
	}
	if width != sim.MapW || height != sim.MapH {
		return nil, fmt.Errorf("pg terrain Load: stored dimensions %dx%d do not match fixed grid %dx%d",
			width, height, sim.MapW, sim.MapH)
	}
	expected := sim.MapW * sim.MapH
	if len(data) != expected {
		return nil, fmt.Errorf("pg terrain Load: blob length %d != expected %d (%dx%d)",
			len(data), expected, sim.MapW, sim.MapH)
	}
	return &sim.Terrain{Data: data}, nil
}
