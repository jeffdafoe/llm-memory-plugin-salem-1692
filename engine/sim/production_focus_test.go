package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// buildSmithWorld seeds a multi-output crafter ("smith") at its forge with the
// given produce restock entries, plus skillet+nail recipes and item kinds. The
// produce anchors are stamped at `anchor` so units are owed by tick time. No
// tickers are started (only w.Run), so commands apply deterministically.
func buildSmithWorld(t *testing.T, anchor time.Time, restock []sim.RestockEntry, inv map[sim.ItemKind]int) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		"skillet":   {Name: "skillet", DisplayLabel: "Skillet", Category: sim.ItemCategoryMaterial, SortOrder: 300},
		"nail":      {Name: "nail", DisplayLabel: "Nail", Category: sim.ItemCategoryMaterial, SortOrder: 310},
		"porridge":  {Name: "porridge", DisplayLabel: "Porridge", Category: sim.ItemCategoryFood, SortOrder: 130},
		"horseshoe": {Name: "horseshoe", DisplayLabel: "Horseshoe", Category: sim.ItemCategoryMaterial, SortOrder: 320}, // item kind, NO recipe seeded
	})
	handles.Recipes.Seed(map[sim.ItemKind]*sim.ItemRecipe{
		"skillet": {OutputItem: "skillet", OutputQty: 1, RateQty: 1, RatePerHours: 3, WholesalePrice: 5, RetailPrice: 10},
		"nail":    {OutputItem: "nail", OutputQty: 1, RateQty: 1, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 2},
	})
	ps := map[sim.ItemKind]*sim.ProduceState{}
	for _, e := range restock {
		if e.Source == sim.RestockSourceProduce {
			ps[e.Item] = &sim.ProduceState{Item: e.Item, LastProducedAt: anchor}
		}
	}
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"smith": {
			ID:                "smith",
			LLMAgent:          "smith-agent",
			Kind:              sim.KindNPCStateful,
			InsideStructureID: "forge",
			WorkStructureID:   "forge",
			Inventory:         inv,
			ProduceState:      ps,
			RestockPolicy:     &sim.RestockPolicy{Restock: restock},
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

func twoProduceEntries() []sim.RestockEntry {
	return []sim.RestockEntry{
		{Item: "skillet", Source: sim.RestockSourceProduce, Max: 5},
		{Item: "nail", Source: sim.RestockSourceProduce, Max: 20},
	}
}

// A multi-output crafter forges ONLY its chosen focus (LLM-116), not every
// produce entry in parallel — skillet is skipped while nail is the focus.
func TestProduceTickMultiOutputForgesOnlyFocus(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildSmithWorld(t, now.Add(-4*time.Hour), twoProduceEntries(), map[sim.ItemKind]int{})
	defer cancel()

	if _, err := w.Send(sim.SetProductionFocus("smith", "nail")); err != nil {
		t.Fatalf("SetProductionFocus: %v", err)
	}
	res, err := w.Send(sim.ApplyProduceTick(now))
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	r := res.(sim.ProduceTickResult)
	if len(r.Changes) == 0 {
		t.Fatalf("focused crafter produced nothing; want nails")
	}
	for _, c := range r.Changes {
		if c.Item != "nail" {
			t.Fatalf("forged %q; a focused multi-output crafter must make only its focus (nail)", c.Item)
		}
	}
}

// With no focus chosen, a multi-output crafter produces nothing — it must pick
// first (the forge cue + craft tool drive that choice).
func TestProduceTickMultiOutputUnfocusedForgesNothing(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildSmithWorld(t, now.Add(-4*time.Hour), twoProduceEntries(), map[sim.ItemKind]int{})
	defer cancel()

	res, err := w.Send(sim.ApplyProduceTick(now))
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if r := res.(sim.ProduceTickResult); r.Executions != 0 {
		t.Fatalf("unfocused multi-output crafter ran %d executions; want 0", r.Executions)
	}
}

// A single-output producer is unaffected by the focus gate — it keeps
// auto-producing its one good without ever calling craft.
func TestProduceTickSingleOutputAutoProduces(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildSmithWorld(t, now.Add(-4*time.Hour), []sim.RestockEntry{
		{Item: "skillet", Source: sim.RestockSourceProduce, Max: 5},
	}, map[sim.ItemKind]int{})
	defer cancel()

	res, err := w.Send(sim.ApplyProduceTick(now))
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if r := res.(sim.ProduceTickResult); r.Executions == 0 {
		t.Fatalf("single-output producer made nothing; want auto-produce with no focus")
	}
}

// SetProductionFocus rejects a good the actor does not produce, with an error
// the model can learn from (it resolves in the catalog but isn't on the policy).
func TestSetProductionFocusRejectsNonProducedItem(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildSmithWorld(t, now, twoProduceEntries(), map[sim.ItemKind]int{})
	defer cancel()

	if _, err := w.Send(sim.SetProductionFocus("smith", "porridge")); err == nil {
		t.Fatalf("SetProductionFocus accepted a good the smith doesn't make; want an error")
	}
}

// The production-choice producer wakes an idle, unfocused, at-forge multi-output
// crafter (with a below-cap good to make) by stamping exactly one warrant.
func TestEvaluateProductionChoiceWakesIdleCrafter(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildSmithWorld(t, now, twoProduceEntries(), map[sim.ItemKind]int{})
	defer cancel()

	res, err := w.Send(sim.EvaluateProductionChoice(now))
	if err != nil {
		t.Fatalf("EvaluateProductionChoice: %v", err)
	}
	if stamped := res.(int); stamped != 1 {
		t.Fatalf("stamped %d warrants; want 1 (the idle unfocused smith)", stamped)
	}
}

// A crafter productively making its focus is NOT re-woken — the producer only
// fires for an unfocused or maxed-out crafter.
func TestEvaluateProductionChoiceSkipsFocusedCrafter(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildSmithWorld(t, now, twoProduceEntries(), map[sim.ItemKind]int{})
	defer cancel()

	if _, err := w.Send(sim.SetProductionFocus("smith", "nail")); err != nil {
		t.Fatalf("SetProductionFocus: %v", err)
	}
	res, err := w.Send(sim.EvaluateProductionChoice(now))
	if err != nil {
		t.Fatalf("EvaluateProductionChoice: %v", err)
	}
	if stamped := res.(int); stamped != 0 {
		t.Fatalf("stamped %d warrants for a productively-focused crafter; want 0", stamped)
	}
}
