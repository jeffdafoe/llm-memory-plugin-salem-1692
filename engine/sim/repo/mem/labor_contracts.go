package mem

import (
	"context"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// LaborContractsRepo is an in-memory implementation of sim.LaborContractsRepo,
// same shape as HuddlesRepo / OrdersRepo. Seed populates initial state; LoadAll
// deep-clones via CloneLaborOffer; SaveSnapshot replaces the map wholesale so
// the in-memory "checkpoint" matches what a real pg SaveSnapshot expresses (a
// complete re-statement of the persisted accepted-contract set). LLM-259.
type LaborContractsRepo struct {
	contracts map[sim.LaborID]*sim.LaborOffer
}

func NewLaborContractsRepo() *LaborContractsRepo {
	return &LaborContractsRepo{contracts: make(map[sim.LaborID]*sim.LaborOffer)}
}

func (r *LaborContractsRepo) Seed(contracts map[sim.LaborID]*sim.LaborOffer) {
	for id, o := range contracts {
		r.contracts[id] = sim.CloneLaborOffer(o)
	}
}

func (r *LaborContractsRepo) LoadAll(_ context.Context) (map[sim.LaborID]*sim.LaborOffer, error) {
	out := make(map[sim.LaborID]*sim.LaborOffer, len(r.contracts))
	for id, o := range r.contracts {
		out[id] = sim.CloneLaborOffer(o)
	}
	return out, nil
}

func (r *LaborContractsRepo) SaveSnapshot(_ context.Context, _ sim.Tx, contracts map[sim.LaborID]*sim.LaborOffer) error {
	r.contracts = make(map[sim.LaborID]*sim.LaborOffer, len(contracts))
	for id, o := range contracts {
		r.contracts[id] = sim.CloneLaborOffer(o)
	}
	return nil
}
