package mem

import (
	"context"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// ItemKindsRepo is an in-memory implementation of sim.ItemKindsRepo.
// Reference state — no checkpoint save. Tests Seed the catalog;
// production loads from the item_kind + item_satisfies tables (pg impl
// ports later). Mirrors the RecipesRepo shape exactly.
type ItemKindsRepo struct {
	kinds map[sim.ItemKind]*sim.ItemKindDef
}

func NewItemKindsRepo() *ItemKindsRepo {
	return &ItemKindsRepo{kinds: make(map[sim.ItemKind]*sim.ItemKindDef)}
}

// Seed inserts item kinds directly. Tests use this to populate the catalog
// before LoadWorld. Like RecipesRepo.Seed, the entries are stored by
// pointer — callers must not mutate a *ItemKindDef after Seed unless they
// intend the change to be visible to the world (reference data is treated
// as read-only post-load).
func (r *ItemKindsRepo) Seed(kinds map[sim.ItemKind]*sim.ItemKindDef) {
	for id, k := range kinds {
		r.kinds[id] = k
	}
}

func (r *ItemKindsRepo) LoadAll(_ context.Context) (map[sim.ItemKind]*sim.ItemKindDef, error) {
	out := make(map[sim.ItemKind]*sim.ItemKindDef, len(r.kinds))
	for id, k := range r.kinds {
		out[id] = k
	}
	return out, nil
}

// SeedItemKinds is a convenience helper for tests that need a minimal
// catalog without constructing every field by hand. Builds a small set of
// tavern-style items mirroring v1's seed in ZBBS-091 (post-ZBBS-125
// calibration on ale + water). Callers can mutate the returned map or pass
// it through to ItemKindsRepo.Seed.
//
// Kept in the mem package (not engine/sim) so production code doesn't
// accidentally depend on a test fixture path. Tests in other packages can
// import this directly.
func SeedItemKinds() map[sim.ItemKind]*sim.ItemKindDef {
	return map[sim.ItemKind]*sim.ItemKindDef{
		"ale": {
			Name:         "ale",
			DisplayLabel: "Ale",
			Category:     sim.ItemCategoryDrink,
			Price:        2,
			SortOrder:    10,
			Satisfies: map[sim.NeedKey]int{
				"thirst": 4,
				"hunger": 2,
			},
		},
		"water": {
			Name:         "water",
			DisplayLabel: "Water",
			Category:     sim.ItemCategoryDrink,
			Price:        0,
			SortOrder:    20,
			Satisfies: map[sim.NeedKey]int{
				"thirst": 8,
			},
		},
		"bread": {
			Name:         "bread",
			DisplayLabel: "Bread",
			Category:     sim.ItemCategoryFood,
			Price:        2,
			SortOrder:    120,
			Satisfies: map[sim.NeedKey]int{
				"hunger": 8,
			},
		},
		"wheat": {
			Name:         "wheat",
			DisplayLabel: "Wheat",
			Category:     sim.ItemCategoryMaterial,
			Price:        1,
			SortOrder:    210,
			// No Satisfies — materials are not consumable on their own.
		},
	}
}
