package mem

import (
	"context"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// ScenesRepo is an in-memory implementation of sim.ScenesRepo.
//
// Same shape as the other mem repos: Seed for direct test fixture
// insertion, LoadAll/SaveSnapshot deep-clone via sim.CloneScene at the
// boundary so round-trip tests catch shape bugs that would otherwise
// only surface at pg cutover.
type ScenesRepo struct {
	scenes map[sim.SceneID]*sim.Scene
}

func NewScenesRepo() *ScenesRepo {
	return &ScenesRepo{scenes: make(map[sim.SceneID]*sim.Scene)}
}

func (r *ScenesRepo) Seed(scenes map[sim.SceneID]*sim.Scene) {
	for id, s := range scenes {
		r.scenes[id] = sim.CloneScene(s)
	}
}

func (r *ScenesRepo) LoadAll(_ context.Context) (map[sim.SceneID]*sim.Scene, error) {
	out := make(map[sim.SceneID]*sim.Scene, len(r.scenes))
	for id, s := range r.scenes {
		out[id] = sim.CloneScene(s)
	}
	return out, nil
}

func (r *ScenesRepo) SaveSnapshot(_ context.Context, _ sim.Tx, scenes map[sim.SceneID]*sim.Scene) error {
	r.scenes = make(map[sim.SceneID]*sim.Scene, len(scenes))
	for id, s := range scenes {
		r.scenes[id] = sim.CloneScene(s)
	}
	return nil
}
