package mem

import (
	"context"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// TerrainRepo is an in-memory implementation of sim.TerrainRepo.
// Reference state — no checkpoint save. Tests Seed; production loads
// the blob from village_terrain.id=1 (pg impl ports later).
//
// Default: a fully-walkable grass grid of MapW * MapH bytes. Tests
// that need water or roads call Seed with a custom blob.
type TerrainRepo struct {
	terrain *sim.Terrain
}

func NewTerrainRepo() *TerrainRepo {
	data := make([]byte, sim.MapW*sim.MapH)
	for i := range data {
		data[i] = sim.TerrainLightGrass
	}
	return &TerrainRepo{terrain: &sim.Terrain{Data: data}}
}

// Seed replaces the terrain blob. Tests use this to craft specific
// walkability scenarios.
func (r *TerrainRepo) Seed(terrain *sim.Terrain) {
	r.terrain = terrain
}

func (r *TerrainRepo) Load(_ context.Context) (*sim.Terrain, error) {
	if r.terrain == nil {
		return nil, nil
	}
	// Return a fresh copy so caller mutations don't leak into the seed.
	cp := make([]byte, len(r.terrain.Data))
	copy(cp, r.terrain.Data)
	return &sim.Terrain{Data: cp}, nil
}
