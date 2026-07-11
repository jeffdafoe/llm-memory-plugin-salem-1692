package mem

import (
	"context"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// VisitorsRepo is an in-memory implementation of sim.VisitorsRepo (LLM-369),
// same shape as LaborContractsRepo. Seed populates initial state; LoadAll
// deep-clones; SaveSnapshot re-states the in-flight visitor set from the visitor
// subset of the actors map (VisitorState != nil), exactly what a real pg
// SaveSnapshot expresses (a complete re-statement of the persisted set).
type VisitorsRepo struct {
	visitors map[sim.ActorID]*sim.LoadedVisitor
}

func NewVisitorsRepo() *VisitorsRepo {
	return &VisitorsRepo{visitors: make(map[sim.ActorID]*sim.LoadedVisitor)}
}

func (r *VisitorsRepo) Seed(visitors map[sim.ActorID]*sim.LoadedVisitor) {
	for id, v := range visitors {
		r.visitors[id] = cloneLoadedVisitor(v)
	}
}

func (r *VisitorsRepo) LoadAll(_ context.Context) (map[sim.ActorID]*sim.LoadedVisitor, error) {
	out := make(map[sim.ActorID]*sim.LoadedVisitor, len(r.visitors))
	for id, v := range r.visitors {
		out[id] = cloneLoadedVisitor(v)
	}
	return out, nil
}

func (r *VisitorsRepo) SaveSnapshot(_ context.Context, _ sim.Tx, actors map[sim.ActorID]*sim.Actor) error {
	next := make(map[sim.ActorID]*sim.LoadedVisitor)
	for id, a := range actors {
		if a == nil || a.VisitorState == nil {
			continue // not a visitor — mirrors the pg tier's complementary filter
		}
		vs := *a.VisitorState
		next[id] = &sim.LoadedVisitor{
			ID:                a.ID,
			DisplayName:       a.DisplayName,
			Pos:               a.Pos,
			InsideStructureID: a.InsideStructureID,
			VisitorState:      &vs,
		}
	}
	r.visitors = next
	return nil
}

// cloneLoadedVisitor deep-copies a LoadedVisitor. Every field is a value type
// except VisitorState, whose pointer is cloned so Seed/LoadAll don't alias the
// caller's struct across the fake-repo boundary.
func cloneLoadedVisitor(v *sim.LoadedVisitor) *sim.LoadedVisitor {
	if v == nil {
		return nil
	}
	cp := *v
	if v.VisitorState != nil {
		vs := *v.VisitorState
		cp.VisitorState = &vs
	}
	return &cp
}
