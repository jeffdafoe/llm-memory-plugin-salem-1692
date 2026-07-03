package sim_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// buildCookWorld seeds a multi-output crafter ("cook") at its forge — the
// tavernkeeper shape behind LLM-257 — with the given recipes + produce restock,
// starting inventory, and an initial production focus (may be ""). Item kinds are
// seeded for the palette these tests draw from. Produce anchors are stamped at
// `anchor`. No tickers run (only w.Run) so commands apply deterministically.
func buildCookWorld(t *testing.T, anchor time.Time, recipes map[sim.ItemKind]*sim.ItemRecipe, restock []sim.RestockEntry, inv map[sim.ItemKind]int, focus sim.ItemKind) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		"stew":  {Name: "stew", DisplayLabel: "Stew", Category: sim.ItemCategoryFood, SortOrder: 140},
		"water": {Name: "water", DisplayLabel: "Water", Category: sim.ItemCategoryDrink, SortOrder: 20},
		"sage":  {Name: "sage", DisplayLabel: "Sage", Category: sim.ItemCategoryMaterial, SortOrder: 240},
		"pie":   {Name: "pie", DisplayLabel: "Pie", Category: sim.ItemCategoryFood, SortOrder: 150},
		"flour": {Name: "flour", DisplayLabel: "Flour", Category: sim.ItemCategoryMaterial, SortOrder: 250},
	})
	handles.Recipes.Seed(recipes)
	ps := map[sim.ItemKind]*sim.ProduceState{}
	for _, e := range restock {
		if e.Source == sim.RestockSourceProduce {
			ps[e.Item] = &sim.ProduceState{Item: e.Item, LastProducedAt: anchor}
		}
	}
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"cook": {
			ID:                "cook",
			LLMAgent:          "cook-agent",
			Kind:              sim.KindNPCStateful,
			InsideStructureID: "tavern",
			WorkStructureID:   "tavern",
			Inventory:         inv,
			ProduceState:      ps,
			ProductionFocus:   focus,
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

// stewWaterRecipes/stewWaterRestock: stew needs 2 sage per batch; water is an
// origin good (no inputs). The minimal two-good shape that reproduces John Ellis's
// deadlock — a valued good he can't source starving out the no-input good he can.
func stewWaterRecipes() map[sim.ItemKind]*sim.ItemRecipe {
	return map[sim.ItemKind]*sim.ItemRecipe{
		"stew":  {OutputItem: "stew", OutputQty: 1, RateQty: 1, RatePerHours: 1, Inputs: []sim.RecipeInput{{Item: "sage", Qty: 2}}, WholesalePrice: 3, RetailPrice: 5},
		"water": {OutputItem: "water", OutputQty: 1, RateQty: 12, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 1},
	}
}

func stewWaterRestock() []sim.RestockEntry {
	return []sim.RestockEntry{
		{Item: "stew", Source: sim.RestockSourceProduce, Max: 30},
		{Item: "water", Source: sim.RestockSourceProduce, Max: 30},
	}
}

// twoStarvedRecipes/Restock: two input-bearing goods (stew needs sage, pie needs
// flour) and no origin good — used to exercise the "nothing craftable" branch.
func twoStarvedRecipes() map[sim.ItemKind]*sim.ItemRecipe {
	return map[sim.ItemKind]*sim.ItemRecipe{
		"stew": {OutputItem: "stew", OutputQty: 1, RateQty: 1, RatePerHours: 1, Inputs: []sim.RecipeInput{{Item: "sage", Qty: 2}}},
		"pie":  {OutputItem: "pie", OutputQty: 1, RateQty: 1, RatePerHours: 1, Inputs: []sim.RecipeInput{{Item: "flour", Qty: 2}}},
	}
}

func twoStarvedRestock() []sim.RestockEntry {
	return []sim.RestockEntry{
		{Item: "stew", Source: sim.RestockSourceProduce, Max: 30},
		{Item: "pie", Source: sim.RestockSourceProduce, Max: 30},
	}
}

// SetProductionFocus rejects focusing an input-starved good while another good is
// craftable now — the inputs-side sibling of the at-cap guard (LLM-257). The cook
// holds no sage, so stew is refused (naming the missing sage) and no-input water
// is accepted.
func TestSetProductionFocusRejectsInputStarvedWhenOtherCraftable(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildCookWorld(t, now, stewWaterRecipes(), stewWaterRestock(), map[sim.ItemKind]int{}, "")
	defer cancel()

	_, err := w.Send(sim.SetProductionFocus("cook", "stew"))
	if err == nil {
		t.Fatalf("accepted a sage-less stew while water is makeable; want an error")
	}
	if !strings.Contains(err.Error(), "sage") {
		t.Fatalf("rejection should name the missing input; got %q", err.Error())
	}
	if _, err := w.Send(sim.SetProductionFocus("cook", "water")); err != nil {
		t.Fatalf("rejected water (no inputs, makeable): %v", err)
	}
}

// When NOTHING is craftable — every good input-starved — focusing a starved good
// is allowed; there is nothing better to pick (mirrors the all-at-cap escape).
// Cook has neither sage (stew) nor flour (pie).
func TestSetProductionFocusAllowsInputStarvedWhenNothingCraftable(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildCookWorld(t, now, twoStarvedRecipes(), twoStarvedRestock(), map[sim.ItemKind]int{}, "")
	defer cancel()

	if _, err := w.Send(sim.SetProductionFocus("cook", "stew")); err != nil {
		t.Fatalf("rejected stew when nothing else is craftable either (nothing better to pick); want allow: %v", err)
	}
}

// The production-choice warrant fires for a keeper stuck on an unmakeable focus
// while a makeable good remains — the John-Ellis-frozen-on-stew wake (LLM-257).
// Focus is stew, the cook holds no sage, water is still makeable.
func TestEvaluateProductionChoiceWakesStarvedFocusCrafter(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildCookWorld(t, now, stewWaterRecipes(), stewWaterRestock(), map[sim.ItemKind]int{}, "stew")
	defer cancel()

	res, err := w.Send(sim.EvaluateProductionChoice(now))
	if err != nil {
		t.Fatalf("EvaluateProductionChoice: %v", err)
	}
	if stamped := res.(int); stamped != 1 {
		t.Fatalf("stamped %d warrants for a keeper stuck on an unmakeable stew (water still makeable); want 1", stamped)
	}
}

// A keeper whose focus is craftable (its inputs are on hand) is NOT re-woken.
func TestEvaluateProductionChoiceSkipsCraftableFocus(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildCookWorld(t, now, stewWaterRecipes(), stewWaterRestock(), map[sim.ItemKind]int{"sage": 10}, "stew")
	defer cancel()

	res, err := w.Send(sim.EvaluateProductionChoice(now))
	if err != nil {
		t.Fatalf("EvaluateProductionChoice: %v", err)
	}
	if stamped := res.(int); stamped != 0 {
		t.Fatalf("stamped %d warrants for a keeper whose stew focus is fully makeable; want 0", stamped)
	}
}

// A keeper with NOTHING craftable (every good input-starved) is left alone — no
// point waking it to a choice it can't fulfill.
func TestEvaluateProductionChoiceSkipsWhenNothingCraftable(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildCookWorld(t, now, twoStarvedRecipes(), twoStarvedRestock(), map[sim.ItemKind]int{}, "stew")
	defer cancel()

	res, err := w.Send(sim.EvaluateProductionChoice(now))
	if err != nil {
		t.Fatalf("EvaluateProductionChoice: %v", err)
	}
	if stamped := res.(int); stamped != 0 {
		t.Fatalf("stamped %d warrants when nothing is craftable; want 0", stamped)
	}
}

// The produce-tick-layer deadlock the choice-layer fix exists to prevent: with
// focus pinned to an input-starved stew, ApplyProduceTick makes NOTHING — not even
// the no-input water, which is locked out because it isn't the focus. produce_tick
// is deliberately unchanged (LLM-257); the fix keeps the focus from getting stuck
// here.
func TestProduceTickStarvedFocusLocksOutNoInputGood(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildCookWorld(t, now.Add(-4*time.Hour), stewWaterRecipes(), stewWaterRestock(), map[sim.ItemKind]int{}, "stew")
	defer cancel()

	res, err := w.Send(sim.ApplyProduceTick(now))
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if r := res.(sim.ProduceTickResult); r.Executions != 0 {
		t.Fatalf("ran %d executions while focus=stew is input-starved; the focus gate should lock out even no-input water", r.Executions)
	}
}

// waterCappedStewRecipes/Restock: water (no inputs) capped low at 5 so it can be
// driven to cap, alongside stew (needs sage) — the mixed at-cap / input-starved
// fixtures for the unified worth-choosing guard.
func waterCappedStewRecipes() map[sim.ItemKind]*sim.ItemRecipe {
	return map[sim.ItemKind]*sim.ItemRecipe{
		"water": {OutputItem: "water", OutputQty: 1, RateQty: 12, RatePerHours: 1},
		"stew":  {OutputItem: "stew", OutputQty: 1, RateQty: 1, RatePerHours: 1, Inputs: []sim.RecipeInput{{Item: "sage", Qty: 2}}},
	}
}

func waterCappedStewRestock() []sim.RestockEntry {
	return []sim.RestockEntry{
		{Item: "water", Source: sim.RestockSourceProduce, Max: 5},
		{Item: "stew", Source: sim.RestockSourceProduce, Max: 30},
	}
}

// Unified guard, mixed case: the chosen good is AT CAP and the only other good is
// input-starved — nothing is craftable, so the at-cap pick is allowed (there is
// nothing better; the same escape the all-at-cap case takes).
func TestSetProductionFocusAllowsAtCapChosenWhenNoOtherCraftable(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildCookWorld(t, now, waterCappedStewRecipes(), waterCappedStewRestock(), map[sim.ItemKind]int{"water": 5}, "")
	defer cancel()

	if _, err := w.Send(sim.SetProductionFocus("cook", "water")); err != nil {
		t.Fatalf("rejected at-cap water when the only alternative (stew) is input-starved; want allow: %v", err)
	}
}

// Unified guard, mixed case: the chosen good is AT CAP while another good IS
// craftable — reject and steer there, with the at-cap message (not the input-short
// one). Guards that the merged guard still distinguishes the two reasons.
func TestSetProductionFocusRejectsAtCapChosenWhenOtherCraftable(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildCookWorld(t, now, waterCappedStewRecipes(), waterCappedStewRestock(), map[sim.ItemKind]int{"water": 5, "sage": 10}, "")
	defer cancel()

	_, err := w.Send(sim.SetProductionFocus("cook", "water"))
	if err == nil {
		t.Fatalf("accepted at-cap water while stew is craftable; want an error")
	}
	if !strings.Contains(err.Error(), "hold") {
		t.Fatalf("at-cap rejection should use the 'all you can hold' message, not the input-short one; got %q", err.Error())
	}
}

// HasProduceInputs: every required input must be on hand for one execution; a
// no-input recipe is always satisfied (the origin-producer case makeableRecipe
// covers and this must agree with).
func TestHasProduceInputs(t *testing.T) {
	stew := &sim.ItemRecipe{Inputs: []sim.RecipeInput{{Item: "sage", Qty: 2}, {Item: "meat", Qty: 10}}}
	if !sim.HasProduceInputs(stew, map[sim.ItemKind]int{"sage": 2, "meat": 10}) {
		t.Fatalf("inputs exactly met should satisfy")
	}
	if sim.HasProduceInputs(stew, map[sim.ItemKind]int{"sage": 1, "meat": 10}) {
		t.Fatalf("one input short must NOT satisfy")
	}
	if !sim.HasProduceInputs(&sim.ItemRecipe{}, nil) {
		t.Fatalf("a no-input recipe is always satisfied, even with nil inventory")
	}
}
