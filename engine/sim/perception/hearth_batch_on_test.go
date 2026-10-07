package perception

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// TestBoostedBatchOn pins what counts as a batch the fire is working for: in
// flight, boosted, and the cook at the post. A finished-but-uncleared item or a
// batch paused while its cook is away gives the fire nothing to do.
func TestBoostedBatchOn(t *testing.T) {
	snap := &sim.Snapshot{Recipes: boostedPorridgeRecipes()}
	snap.Recipes["ale"] = &sim.ItemRecipe{OutputItem: "ale", OutputQty: 1}
	cook := func(item sim.ItemKind, remaining int64, inside sim.StructureID) *sim.ActorSnapshot {
		return &sim.ActorSnapshot{
			WorkStructureID:            "inn",
			InsideStructureID:          inside,
			ProductionItem:             item,
			ProductionRemainingSeconds: remaining,
		}
	}
	cases := []struct {
		name      string
		actor     *sim.ActorSnapshot
		structure sim.StructureID
		want      sim.ItemKind
	}{
		{"cooking at the post", cook("porridge", 2400, "inn"), "inn", "porridge"},
		{"no batch", cook("", 0, "inn"), "inn", ""},
		{"finished, not yet cleared", cook("porridge", 0, "inn"), "inn", ""},
		{"cook away, batch paused", cook("porridge", 2400, "store"), "inn", ""},
		{"batch the fire does not boost", cook("ale", 600, "inn"), "inn", ""},
		{"a different structure's hearth", cook("porridge", 2400, "inn"), "tavern", ""},
		{"no actor", nil, "inn", ""},
	}
	for _, tc := range cases {
		if got := boostedBatchOn(snap, tc.actor, tc.structure); got != tc.want {
			t.Errorf("%s: boostedBatchOn = %q, want %q", tc.name, got, tc.want)
		}
	}
}
