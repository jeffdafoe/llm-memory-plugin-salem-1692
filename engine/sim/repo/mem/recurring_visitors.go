package mem

import (
	"context"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// RecurringVisitorsRepo is an in-memory implementation of
// sim.RecurringVisitorsRepo (LLM-372). Seed populates initial state; LoadAll
// deep-clones; SaveSnapshot re-states the durable returner set from the in-memory
// map. UNLIKE the visitor tier, there is no generation-marker sweep — these rows
// outlive the visit — so SaveSnapshot simply mirrors whatever the checkpoint hands
// it (the in-memory set only ever grows), matching the pg tier's upsert-only
// persistence.
type RecurringVisitorsRepo struct {
	recurring map[sim.RecurringVisitorID]*sim.RecurringVisitor
}

func NewRecurringVisitorsRepo() *RecurringVisitorsRepo {
	return &RecurringVisitorsRepo{recurring: make(map[sim.RecurringVisitorID]*sim.RecurringVisitor)}
}

func (r *RecurringVisitorsRepo) Seed(recurring map[sim.RecurringVisitorID]*sim.RecurringVisitor) {
	for id, rv := range recurring {
		r.recurring[id] = cloneRecurringVisitor(rv)
	}
}

func (r *RecurringVisitorsRepo) LoadAll(_ context.Context) (map[sim.RecurringVisitorID]*sim.RecurringVisitor, error) {
	out := make(map[sim.RecurringVisitorID]*sim.RecurringVisitor, len(r.recurring))
	for id, rv := range r.recurring {
		out[id] = cloneRecurringVisitor(rv)
	}
	return out, nil
}

func (r *RecurringVisitorsRepo) SaveSnapshot(_ context.Context, _ sim.Tx, recurring map[sim.RecurringVisitorID]*sim.RecurringVisitor) error {
	next := make(map[sim.RecurringVisitorID]*sim.RecurringVisitor, len(recurring))
	for id, rv := range recurring {
		if rv == nil {
			continue
		}
		next[id] = cloneRecurringVisitor(rv)
	}
	r.recurring = next
	return nil
}

// cloneRecurringVisitor deep-copies a RecurringVisitor (including its
// Acquaintances map + entries) so Seed / LoadAll / SaveSnapshot don't alias the
// caller's structs across the fake-repo boundary.
func cloneRecurringVisitor(src *sim.RecurringVisitor) *sim.RecurringVisitor {
	if src == nil {
		return nil
	}
	cp := *src
	if src.Acquaintances != nil {
		cp.Acquaintances = make(map[sim.ActorID]*sim.RecurringAcquaintance, len(src.Acquaintances))
		for id, acq := range src.Acquaintances {
			if acq == nil {
				continue
			}
			a := *acq
			cp.Acquaintances[id] = &a
		}
	}
	return &cp
}
