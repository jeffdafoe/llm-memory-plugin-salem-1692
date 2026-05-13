package mem

import (
	"context"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// RecipesRepo is an in-memory implementation of sim.RecipesRepo.
// Reference state — no checkpoint save. Tests Seed the catalog;
// production loads from the item_recipe table (pg impl ports later).
type RecipesRepo struct {
	recipes map[sim.ItemKind]*sim.ItemRecipe
}

func NewRecipesRepo() *RecipesRepo {
	return &RecipesRepo{recipes: make(map[sim.ItemKind]*sim.ItemRecipe)}
}

// Seed inserts recipes directly. Tests use this to populate the catalog
// before LoadWorld.
func (r *RecipesRepo) Seed(recipes map[sim.ItemKind]*sim.ItemRecipe) {
	for id, rec := range recipes {
		r.recipes[id] = rec
	}
}

func (r *RecipesRepo) LoadAll(_ context.Context) (map[sim.ItemKind]*sim.ItemRecipe, error) {
	out := make(map[sim.ItemKind]*sim.ItemRecipe, len(r.recipes))
	for id, rec := range r.recipes {
		out[id] = rec
	}
	return out, nil
}
