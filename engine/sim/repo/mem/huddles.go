package mem

import (
	"context"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// HuddlesRepo is an in-memory implementation of sim.HuddlesRepo, same
// shape as ActorsRepo.
type HuddlesRepo struct {
	huddles map[sim.HuddleID]*sim.Huddle
}

func NewHuddlesRepo() *HuddlesRepo {
	return &HuddlesRepo{huddles: make(map[sim.HuddleID]*sim.Huddle)}
}

func (r *HuddlesRepo) Seed(huddles map[sim.HuddleID]*sim.Huddle) {
	for id, h := range huddles {
		r.huddles[id] = h
	}
}

func (r *HuddlesRepo) LoadAll(_ context.Context) (map[sim.HuddleID]*sim.Huddle, error) {
	out := make(map[sim.HuddleID]*sim.Huddle, len(r.huddles))
	for id, h := range r.huddles {
		out[id] = h
	}
	return out, nil
}

func (r *HuddlesRepo) SaveSnapshot(_ context.Context, _ sim.Tx, huddles map[sim.HuddleID]*sim.Huddle) error {
	r.huddles = make(map[sim.HuddleID]*sim.Huddle, len(huddles))
	for id, h := range huddles {
		r.huddles[id] = h
	}
	return nil
}
