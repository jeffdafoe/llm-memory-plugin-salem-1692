package mem

import (
	"context"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// StructuresRepo is an in-memory implementation of sim.StructuresRepo.
// Owns structure + child rooms as one aggregate (per the per-aggregate
// repo design — Structure.Rooms is part of the loaded entity).
type StructuresRepo struct {
	structures map[sim.StructureID]*sim.Structure
}

func NewStructuresRepo() *StructuresRepo {
	return &StructuresRepo{structures: make(map[sim.StructureID]*sim.Structure)}
}

// Seed inserts structures directly. Tests use this to set up the
// world's named locations before LoadWorld.
func (r *StructuresRepo) Seed(structures map[sim.StructureID]*sim.Structure) {
	for id, s := range structures {
		r.structures[id] = s
	}
}

func (r *StructuresRepo) LoadAll(_ context.Context) (map[sim.StructureID]*sim.Structure, error) {
	out := make(map[sim.StructureID]*sim.Structure, len(r.structures))
	for id, s := range r.structures {
		out[id] = s
	}
	return out, nil
}

func (r *StructuresRepo) SaveSnapshot(_ context.Context, _ sim.Tx, structures map[sim.StructureID]*sim.Structure) error {
	r.structures = make(map[sim.StructureID]*sim.Structure, len(structures))
	for id, s := range structures {
		r.structures[id] = s
	}
	return nil
}
