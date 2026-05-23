package mem

import (
	"context"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// AttributeDefinitionsRepo is an in-memory implementation of
// sim.AttributeDefinitionsRepo. Reference state — no checkpoint save. Tests
// Seed the catalog; production loads the actor-scoped attribute_definition
// rows (pg impl).
type AttributeDefinitionsRepo struct {
	defs map[string]*sim.AttributeDefinition
}

func NewAttributeDefinitionsRepo() *AttributeDefinitionsRepo {
	return &AttributeDefinitionsRepo{defs: make(map[string]*sim.AttributeDefinition)}
}

// Seed inserts attribute definitions directly. Tests use this to populate the
// catalog before LoadWorld.
func (r *AttributeDefinitionsRepo) Seed(defs map[string]*sim.AttributeDefinition) {
	for slug, d := range defs {
		r.defs[slug] = d
	}
}

func (r *AttributeDefinitionsRepo) LoadAll(_ context.Context) (map[string]*sim.AttributeDefinition, error) {
	out := make(map[string]*sim.AttributeDefinition, len(r.defs))
	for slug, d := range r.defs {
		out[slug] = d
	}
	return out, nil
}
