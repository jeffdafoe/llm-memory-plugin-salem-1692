package mem

import (
	"context"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// CourtCasesRepo is an in-memory sim.CourtCasesRepo (LLM-695). SaveSnapshot
// mirrors whatever the checkpoint hands it, matching the pg tier's upsert-only
// persistence (the in-memory set only grows).
type CourtCasesRepo struct {
	cases map[sim.CourtCaseID]*sim.CourtCase
}

func NewCourtCasesRepo() *CourtCasesRepo {
	return &CourtCasesRepo{cases: make(map[sim.CourtCaseID]*sim.CourtCase)}
}

func (r *CourtCasesRepo) Seed(cases map[sim.CourtCaseID]*sim.CourtCase) {
	for id, c := range cases {
		r.cases[id] = c.Clone()
	}
}

func (r *CourtCasesRepo) LoadAll(_ context.Context) (map[sim.CourtCaseID]*sim.CourtCase, error) {
	out := make(map[sim.CourtCaseID]*sim.CourtCase, len(r.cases))
	for id, c := range r.cases {
		out[id] = c.Clone()
	}
	return out, nil
}

func (r *CourtCasesRepo) SaveSnapshot(_ context.Context, _ sim.Tx, cases map[sim.CourtCaseID]*sim.CourtCase) error {
	for id, c := range cases {
		if c != nil {
			r.cases[id] = c.Clone()
		}
	}
	return nil
}
