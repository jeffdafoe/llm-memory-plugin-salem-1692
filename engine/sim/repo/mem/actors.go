package mem

import (
	"context"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// ActorsRepo is an in-memory implementation of sim.ActorsRepo.
//
// LoadAll returns deep-cloned copies of whatever Seed/SaveSnapshot stored.
// SaveSnapshot deep-clones inbound entities before storing. The clones
// mimic the serialization boundary the pg impl will impose at cutover, so
// round-trip tests catch shape/ordering bugs in either direction.
type ActorsRepo struct {
	actors map[sim.ActorID]*sim.Actor
}

func NewActorsRepo() *ActorsRepo {
	return &ActorsRepo{actors: make(map[sim.ActorID]*sim.Actor)}
}

// Seed inserts actors directly without going through SaveSnapshot. Tests
// use this to set up the initial state before LoadWorld. Inputs are
// deep-cloned so test fixtures stay decoupled from world-side mutations.
func (r *ActorsRepo) Seed(actors map[sim.ActorID]*sim.Actor) {
	for id, a := range actors {
		r.actors[id] = sim.CloneActor(a)
	}
}

func (r *ActorsRepo) LoadAll(_ context.Context) (map[sim.ActorID]*sim.Actor, error) {
	out := make(map[sim.ActorID]*sim.Actor, len(r.actors))
	for id, a := range r.actors {
		out[id] = sim.CloneActor(a)
	}
	return out, nil
}

func (r *ActorsRepo) SaveSnapshot(_ context.Context, _ sim.Tx, actors map[sim.ActorID]*sim.Actor) error {
	r.actors = make(map[sim.ActorID]*sim.Actor, len(actors))
	for id, a := range actors {
		r.actors[id] = sim.CloneActor(a)
	}
	return nil
}
