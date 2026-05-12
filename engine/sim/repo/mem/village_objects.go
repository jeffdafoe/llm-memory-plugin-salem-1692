package mem

import (
	"context"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// VillageObjectsRepo is an in-memory implementation of
// sim.VillageObjectsRepo. Owns village_object + village_object_tag rows
// as one aggregate.
type VillageObjectsRepo struct {
	objects map[sim.VillageObjectID]*sim.VillageObject
}

func NewVillageObjectsRepo() *VillageObjectsRepo {
	return &VillageObjectsRepo{objects: make(map[sim.VillageObjectID]*sim.VillageObject)}
}

// Seed inserts objects directly. Tests use this to populate placements
// before LoadWorld.
func (r *VillageObjectsRepo) Seed(objects map[sim.VillageObjectID]*sim.VillageObject) {
	for id, o := range objects {
		r.objects[id] = o
	}
}

func (r *VillageObjectsRepo) LoadAll(_ context.Context) (map[sim.VillageObjectID]*sim.VillageObject, error) {
	out := make(map[sim.VillageObjectID]*sim.VillageObject, len(r.objects))
	for id, o := range r.objects {
		out[id] = o
	}
	return out, nil
}

func (r *VillageObjectsRepo) SaveSnapshot(_ context.Context, _ sim.Tx, objects map[sim.VillageObjectID]*sim.VillageObject) error {
	r.objects = make(map[sim.VillageObjectID]*sim.VillageObject, len(objects))
	for id, o := range objects {
		r.objects[id] = o
	}
	return nil
}
