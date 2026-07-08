package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// production_choice_tick_test.go — LLM-319. The production-choice producer now
// wakes ANY idle producer (single- and multi-output) with nothing in the works
// and something craftable — and it is PACED: a granted decision (or a landed
// batch) holds off the re-nag for ProductionRenagInterval, so declining to
// produce is a decision that sticks instead of one re-litigated every scan.

// buildProducerWorld seeds one producer at its post with the given produce
// restock + inventory. Recipes: skillet (1 per 3h, no inputs), nail (1 per 1h,
// no inputs), stew (1 per 1h, needs 2 sage).
func buildProducerWorld(t *testing.T, restock []sim.RestockEntry, inv map[sim.ItemKind]int) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		"skillet": {Name: "skillet", DisplayLabel: "Skillet", DisplayLabelSingular: "skillet", DisplayLabelPlural: "skillets", Category: sim.ItemCategoryMaterial, SortOrder: 300},
		"nail":    {Name: "nail", DisplayLabel: "Nail", DisplayLabelSingular: "nail", DisplayLabelPlural: "nails", Category: sim.ItemCategoryMaterial, SortOrder: 310},
		"stew":    {Name: "stew", DisplayLabel: "Stew", Category: sim.ItemCategoryFood, SortOrder: 140},
		"sage":    {Name: "sage", DisplayLabel: "Sage", Category: sim.ItemCategoryMaterial, SortOrder: 240},
	})
	handles.Recipes.Seed(map[sim.ItemKind]*sim.ItemRecipe{
		"skillet": {OutputItem: "skillet", OutputQty: 1, RateQty: 1, RatePerHours: 3, WholesalePrice: 5, RetailPrice: 10},
		"nail":    {OutputItem: "nail", OutputQty: 1, RateQty: 1, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 2},
		"stew":    {OutputItem: "stew", OutputQty: 1, RateQty: 1, RatePerHours: 1, Inputs: []sim.RecipeInput{{Item: "sage", Qty: 2}}},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"smith": {
			ID:                "smith",
			LLMAgent:          "smith-agent",
			Kind:              sim.KindNPCStateful,
			InsideStructureID: "forge",
			WorkStructureID:   "forge",
			Inventory:         inv,
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

func evaluateChoice(t *testing.T, w *sim.World, now time.Time) int {
	t.Helper()
	res, err := w.Send(sim.EvaluateProductionChoice(now))
	if err != nil {
		t.Fatalf("EvaluateProductionChoice: %v", err)
	}
	return res.(int)
}

// The producer wakes an idle multi-output crafter with craftable goods.
func TestEvaluateProductionChoiceWakesIdleCrafter(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildProducerWorld(t, twoProduceEntries(), map[sim.ItemKind]int{})
	defer cancel()

	if stamped := evaluateChoice(t, w, now); stamped != 1 {
		t.Fatalf("stamped %d warrants; want 1 (the idle smith)", stamped)
	}
}

// LLM-319 headline: a SINGLE-output producer is woken too — its choice is the
// go/no-go on another batch.
func TestEvaluateProductionChoiceWakesSingleOutputProducer(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildProducerWorld(t, []sim.RestockEntry{
		{Item: "nail", Source: sim.RestockSourceProduce, Max: 20},
	}, map[sim.ItemKind]int{})
	defer cancel()

	if stamped := evaluateChoice(t, w, now); stamped != 1 {
		t.Fatalf("stamped %d warrants; want 1 (single-output producers decide per batch now)", stamped)
	}
}

// A producer with a batch in the works is left alone — nothing to decide until
// it lands.
func TestEvaluateProductionChoiceSkipsMidCycle(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildProducerWorld(t, twoProduceEntries(), map[sim.ItemKind]int{})
	defer cancel()

	if _, err := w.Send(sim.StartProductionCycle("smith", "nail")); err != nil {
		t.Fatalf("StartProductionCycle: %v", err)
	}
	if stamped := evaluateChoice(t, w, now); stamped != 0 {
		t.Fatalf("stamped %d warrants mid-cycle; want 0", stamped)
	}
}

// Nothing craftable (the only good is input-starved) → no wake to an
// impossible choice.
func TestEvaluateProductionChoiceSkipsWhenNothingCraftable(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildProducerWorld(t, []sim.RestockEntry{
		{Item: "stew", Source: sim.RestockSourceProduce, Max: 30},
	}, map[sim.ItemKind]int{}) // no sage
	defer cancel()

	if stamped := evaluateChoice(t, w, now); stamped != 0 {
		t.Fatalf("stamped %d warrants with nothing craftable; want 0", stamped)
	}
}

// The pacing: a granted decision holds off the re-nag until
// ProductionRenagInterval elapses. Without this, an every-minute scan
// re-litigates "make more?" until a weak model complies — the old
// auto-produce wearing a decision costume.
func TestEvaluateProductionChoiceBackoff(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildProducerWorld(t, twoProduceEntries(), map[sim.ItemKind]int{})
	defer cancel()

	if stamped := evaluateChoice(t, w, now); stamped != 1 {
		t.Fatalf("first scan stamped %d; want 1", stamped)
	}
	// Simulate the granted tick consuming the warrant: while one is pending,
	// restockEligible skips the actor anyway, so the pacing under test only
	// shows once the warrant is drained.
	drainWarrants(t, w, "smith")
	// One minute later — inside the interval, the decision stands.
	if stamped := evaluateChoice(t, w, now.Add(time.Minute)); stamped != 0 {
		t.Fatalf("scan inside the re-nag interval stamped %d; want 0", stamped)
	}
	// Past the interval — the situation is re-presented.
	if stamped := evaluateChoice(t, w, now.Add(sim.ProductionRenagInterval+time.Minute)); stamped != 1 {
		t.Fatalf("scan past the re-nag interval stamped %d; want 1", stamped)
	}
}

// drainWarrants clears the actor's pending warrants, standing in for the
// reactor tick that would have consumed them (no reactor runs in these tests).
func drainWarrants(t *testing.T, w *sim.World, actorID sim.ActorID) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors[actorID]
		a.Warrants = nil
		a.WarrantedSince = nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("drain warrants: %v", err)
	}
}

// A landed batch stamps the pacing too: its completion beat is the wake, so
// the scan must not pile a second nag on top of it.
func TestLandingHoldsOffTheScan(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildProducerWorld(t, twoProduceEntries(), map[sim.ItemKind]int{})
	defer cancel()

	if _, err := w.Send(sim.StartProductionCycle("smith", "nail")); err != nil {
		t.Fatalf("StartProductionCycle: %v", err)
	}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["smith"].ProductionActivity.LastProgressAt = now.Add(-2 * time.Hour)
		return nil, nil
	}}); err != nil {
		t.Fatalf("rewind: %v", err)
	}
	if _, err := w.Send(sim.ApplyProduceTick(now)); err != nil {
		t.Fatalf("tick: %v", err)
	}
	idle, _ := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors["smith"].ProductionActivity == nil, nil
	}})
	if !idle.(bool) {
		t.Fatalf("batch did not land; test setup broken")
	}
	if stamped := evaluateChoice(t, w, now.Add(time.Minute)); stamped != 0 {
		t.Fatalf("scan right after a landing stamped %d; want 0 (the completion beat is the wake)", stamped)
	}
}
