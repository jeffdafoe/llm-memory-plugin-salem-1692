package mem

import (
	"context"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// ActorsRepo is an in-memory implementation of sim.ActorsRepo.
//
// LoadAll returns whatever the test Seeded. SaveSnapshot replaces the
// internal map with a copy of the supplied entities — matches the
// "entity-set replaces what's in DB inside the tx" semantics of the
// production pg impl.
type ActorsRepo struct {
	actors map[sim.ActorID]*sim.Actor
}

func NewActorsRepo() *ActorsRepo {
	return &ActorsRepo{actors: make(map[sim.ActorID]*sim.Actor)}
}

// Seed inserts actors directly without going through SaveSnapshot. Tests
// use this to set up the initial state before LoadWorld.
func (r *ActorsRepo) Seed(actors map[sim.ActorID]*sim.Actor) {
	for id, a := range actors {
		r.actors[id] = a
	}
}

func (r *ActorsRepo) LoadAll(_ context.Context) (map[sim.ActorID]*sim.Actor, error) {
	out := make(map[sim.ActorID]*sim.Actor, len(r.actors))
	for id, a := range r.actors {
		out[id] = a
	}
	return out, nil
}

func (r *ActorsRepo) SaveSnapshot(_ context.Context, _ sim.Tx, actors map[sim.ActorID]*sim.Actor) error {
	r.actors = make(map[sim.ActorID]*sim.Actor, len(actors))
	for id, a := range actors {
		r.actors[id] = a
	}
	return nil
}
