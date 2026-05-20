package sim_test

import (
	"context"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// TestItemKindDef_Consumable covers the helper that derives consumability
// from the Satisfies slice: any entries → consumable, none → not. Mirrors v1's
// `satisfies_attribute IS NOT NULL` discriminator (pre-ZBBS-125) and the
// `EXISTS (... FROM item_satisfies)` discriminator (post-ZBBS-125).
func TestItemKindDef_Consumable(t *testing.T) {
	cases := []struct {
		name string
		def  sim.ItemKindDef
		want bool
	}{
		{
			name: "food with entries",
			def: sim.ItemKindDef{Satisfies: []sim.ItemSatisfaction{
				{Attribute: "hunger", Immediate: 8},
			}},
			want: true,
		},
		{
			name: "drink with multi-need entries",
			def: sim.ItemKindDef{Satisfies: []sim.ItemSatisfaction{
				{Attribute: "thirst", Immediate: 4},
				{Attribute: "hunger", Immediate: 2},
			}},
			want: true,
		},
		{
			name: "material with no entries",
			def:  sim.ItemKindDef{Satisfies: nil},
			want: false,
		},
		{
			name: "empty (non-nil) Satisfies slice is not consumable",
			def:  sim.ItemKindDef{Satisfies: []sim.ItemSatisfaction{}},
			want: false,
		},
		{
			name: "dwell-only stew is consumable (HasDwell triple set, Immediate=0)",
			def: sim.ItemKindDef{Satisfies: []sim.ItemSatisfaction{
				{Attribute: "hunger", DwellAmount: 1, DwellPeriodMinutes: 2, DwellTotalTicks: 8},
			}},
			want: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.def.Consumable(); got != c.want {
				t.Errorf("Consumable() = %v, want %v", got, c.want)
			}
		})
	}
}

// TestLoadWorldItemKinds exercises the full LoadWorld path for the new
// ItemKinds sub-repo: seed the mem-fake catalog, load the world, assert
// the map lands on World.ItemKinds with values preserved.
func TestLoadWorldItemKinds(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(mem.SeedItemKinds())

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}

	if got, want := len(w.ItemKinds), 5; got != want {
		t.Fatalf("w.ItemKinds count = %d, want %d", got, want)
	}

	ale, ok := w.ItemKinds["ale"]
	if !ok {
		t.Fatal("w.ItemKinds missing 'ale'")
	}
	if ale.DisplayLabel != "Ale" {
		t.Errorf("ale.DisplayLabel = %q, want Ale", ale.DisplayLabel)
	}
	if ale.Category != sim.ItemCategoryDrink {
		t.Errorf("ale.Category = %q, want drink", ale.Category)
	}
	if got, want := findSatisfies(t, ale, "thirst").Immediate, 4; got != want {
		t.Errorf("ale.Satisfies[thirst].Immediate = %d, want %d", got, want)
	}
	if got, want := findSatisfies(t, ale, "hunger").Immediate, 2; got != want {
		t.Errorf("ale.Satisfies[hunger].Immediate = %d, want %d", got, want)
	}
	if !ale.Consumable() {
		t.Error("ale.Consumable() = false, want true")
	}

	// Stew is the canonical dwell-bearing fixture (ZBBS-172): immediate-4 +
	// dwell triple (1 / 2min / 8 ticks). Total recovery over the full meal is
	// 4 + 8 = 12 hunger; walk-away abandons the dwell portion.
	stew, ok := w.ItemKinds["stew"]
	if !ok {
		t.Fatal("w.ItemKinds missing 'stew'")
	}
	stewHunger := findSatisfies(t, stew, "hunger")
	if got, want := stewHunger.Immediate, 4; got != want {
		t.Errorf("stew.Satisfies[hunger].Immediate = %d, want %d", got, want)
	}
	if got, want := stewHunger.DwellAmount, 1; got != want {
		t.Errorf("stew.Satisfies[hunger].DwellAmount = %d, want %d", got, want)
	}
	if got, want := stewHunger.DwellPeriodMinutes, 2; got != want {
		t.Errorf("stew.Satisfies[hunger].DwellPeriodMinutes = %d, want %d", got, want)
	}
	if got, want := stewHunger.DwellTotalTicks, 8; got != want {
		t.Errorf("stew.Satisfies[hunger].DwellTotalTicks = %d, want %d", got, want)
	}
	if !stewHunger.HasDwell() {
		t.Error("stew.Satisfies[hunger].HasDwell() = false, want true")
	}

	wheat, ok := w.ItemKinds["wheat"]
	if !ok {
		t.Fatal("w.ItemKinds missing 'wheat'")
	}
	if wheat.Category != sim.ItemCategoryMaterial {
		t.Errorf("wheat.Category = %q, want material", wheat.Category)
	}
	if wheat.Consumable() {
		t.Error("wheat.Consumable() = true, want false (material)")
	}
}

// findSatisfies finds the single ItemSatisfaction matching attr in def.Satisfies.
// Fails the test if zero or multiple entries match — the catalog contract is
// one row per (kind, attribute).
func findSatisfies(t *testing.T, def *sim.ItemKindDef, attr sim.NeedKey) sim.ItemSatisfaction {
	t.Helper()
	var hits []sim.ItemSatisfaction
	for _, s := range def.Satisfies {
		if s.Attribute == attr {
			hits = append(hits, s)
		}
	}
	if len(hits) == 0 {
		t.Fatalf("%s.Satisfies missing entry for %q", def.Name, attr)
	}
	if len(hits) > 1 {
		t.Fatalf("%s.Satisfies has %d entries for %q (want 1)", def.Name, len(hits), attr)
	}
	return hits[0]
}

// TestItemKindsRepo_SeedLoadAll covers the mem-fake directly: Seed populates
// LoadAll output, and the returned map is independent of the repo's
// internal map (callers can mutate it without corrupting subsequent loads).
// Mirrors RecipesRepo's reference-data semantics — entries are stored by
// pointer, so mutating a *ItemKindDef from outside would leak; that's a
// caller contract (reference data is read-only post-Seed), not a test
// invariant.
func TestItemKindsRepo_SeedLoadAll(t *testing.T) {
	ctx := context.Background()
	r := mem.NewItemKindsRepo()
	r.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		"bread": {
			Name: "bread", DisplayLabel: "Bread", Category: sim.ItemCategoryFood,
			Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger", Immediate: 8}},
		},
	})

	got1, err := r.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll #1: %v", err)
	}
	if len(got1) != 1 {
		t.Fatalf("LoadAll #1 size = %d, want 1", len(got1))
	}

	// Caller mutating the returned map must not corrupt the next LoadAll.
	delete(got1, "bread")
	got2, err := r.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll #2: %v", err)
	}
	if _, ok := got2["bread"]; !ok {
		t.Error("LoadAll #2 missing 'bread' after caller deleted from prior result — Seed map leaked")
	}
}
