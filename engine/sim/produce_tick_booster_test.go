package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// produce_tick_booster_test.go — LLM-248. Optional booster inputs: a producer
// holding the booster consumes it per execution and mints bonus_qty extra
// output; holding none leaves base production untouched (no skip, no anchor
// penalty). The fixture mirrors the LLM-83 dairy edge: milk @ output_qty 4,
// rate 4/1h (one 4-milk execution per hour), boosted by 1 sage → +2 milk.

// buildBoosterTestWorld seeds elizabeth (dairy keeper at her farm, milk
// producer with a sage booster) with the given inventory and anchor.
func buildBoosterTestWorld(t *testing.T, anchor time.Time, milkCap int, inv map[sim.ItemKind]int) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Recipes.Seed(map[sim.ItemKind]*sim.ItemRecipe{
		"milk": {
			OutputItem:   "milk",
			OutputQty:    4,
			RateQty:      4,
			RatePerHours: 1,
			BoostInputs:  []sim.BoostInput{{Item: "sage", Qty: 1, BonusQty: 2}},
		},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"elizabeth": {
			ID:                "elizabeth",
			LLMAgent:          "elizabeth-dairy",
			InsideStructureID: "farm",
			WorkStructureID:   "farm",
			Inventory:         inv,
			ProduceState: map[sim.ItemKind]*sim.ProduceState{
				"milk": {Item: "milk", LastProducedAt: anchor},
			},
			RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
				{Item: "milk", Source: sim.RestockSourceProduce, Max: milkCap},
			}},
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

// boosterInv reads elizabeth's milk + sage counts off the world goroutine.
func boosterInv(t *testing.T, w *sim.World) (milk, sage int) {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		inv := world.Actors["elizabeth"].Inventory
		return [2]int{inv["milk"], inv["sage"]}, nil
	}})
	if err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	got := res.([2]int)
	return got[0], got[1]
}

// TestBoosterConsumedForBonusYield — the happy path. One hour elapsed → one
// execution: base +4 milk, sage held → 1 consumed, +2 bonus → 6 milk.
func TestBoosterConsumedForBonusYield(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildBoosterTestWorld(t, now.Add(-time.Hour), 30, map[sim.ItemKind]int{"sage": 3})
	defer cancel()

	res, err := w.Send(sim.ApplyProduceTick(now))
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	r := res.(sim.ProduceTickResult)
	if r.Executions != 1 {
		t.Fatalf("Executions = %d, want 1", r.Executions)
	}
	if len(r.Changes) != 1 || r.Changes[0].QuantityAdded != 6 {
		t.Errorf("Changes = %+v, want one +6 milk change", r.Changes)
	}
	milk, sage := boosterInv(t, w)
	if milk != 6 {
		t.Errorf("milk = %d, want 6 (4 base + 2 bonus)", milk)
	}
	if sage != 2 {
		t.Errorf("sage = %d, want 2 (1 consumed)", sage)
	}
}

// TestBoosterAbsentBaseProductionUntouched — no sage on hand: base production
// proceeds exactly as an unboosted recipe (no skip, no anchor penalty).
func TestBoosterAbsentBaseProductionUntouched(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildBoosterTestWorld(t, now.Add(-time.Hour), 30, map[sim.ItemKind]int{})
	defer cancel()

	res, err := w.Send(sim.ApplyProduceTick(now))
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	r := res.(sim.ProduceTickResult)
	if r.Executions != 1 {
		t.Fatalf("Executions = %d, want 1", r.Executions)
	}
	milk, sage := boosterInv(t, w)
	if milk != 4 {
		t.Errorf("milk = %d, want 4 (base only)", milk)
	}
	if sage != 0 {
		t.Errorf("sage = %d, want 0", sage)
	}
}

// TestBoosterPartialStockBoostsPartialExecutions — two executions owed (2h
// elapsed) but only 1 sage: both base batches mint (+8), one is boosted (+2),
// the single sage is consumed.
func TestBoosterPartialStockBoostsPartialExecutions(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildBoosterTestWorld(t, now.Add(-2*time.Hour), 30, map[sim.ItemKind]int{"sage": 1})
	defer cancel()

	res, err := w.Send(sim.ApplyProduceTick(now))
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	r := res.(sim.ProduceTickResult)
	if r.Executions != 1 {
		t.Fatalf("Executions = %d, want 1 (one tick entry fired)", r.Executions)
	}
	milk, sage := boosterInv(t, w)
	if milk != 10 {
		t.Errorf("milk = %d, want 10 (8 base + 2 bonus for the one boosted execution)", milk)
	}
	if sage != 0 {
		t.Errorf("sage = %d, want 0 (consumed)", sage)
	}
}

// TestBoosterBonusClampedToCap — cap 5, base execution mints 4 (room 1): the
// bonus is trimmed to the remaining headroom (+1, not +2). The sage is still
// consumed in full — the herb went into the batch; the cap trims carry, not
// cost.
func TestBoosterBonusClampedToCap(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildBoosterTestWorld(t, now.Add(-time.Hour), 5, map[sim.ItemKind]int{"sage": 2})
	defer cancel()

	res, err := w.Send(sim.ApplyProduceTick(now))
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	r := res.(sim.ProduceTickResult)
	if r.Executions != 1 {
		t.Fatalf("Executions = %d, want 1", r.Executions)
	}
	milk, sage := boosterInv(t, w)
	if milk != 5 {
		t.Errorf("milk = %d, want 5 (4 base + bonus clamped to cap)", milk)
	}
	if sage != 1 {
		t.Errorf("sage = %d, want 1 (consumed for the boosted execution)", sage)
	}
}

// TestBoosterZeroRoomSkipsConsumption — cap 4: the base execution exactly
// fills the cap (room 0), so the booster is NOT consumed (zero yield would be
// a pure waste of the herb).
func TestBoosterZeroRoomSkipsConsumption(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildBoosterTestWorld(t, now.Add(-time.Hour), 4, map[sim.ItemKind]int{"sage": 2})
	defer cancel()

	res, err := w.Send(sim.ApplyProduceTick(now))
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	r := res.(sim.ProduceTickResult)
	if r.Executions != 1 {
		t.Fatalf("Executions = %d, want 1", r.Executions)
	}
	milk, sage := boosterInv(t, w)
	if milk != 4 {
		t.Errorf("milk = %d, want 4 (base fills cap exactly)", milk)
	}
	if sage != 2 {
		t.Errorf("sage = %d, want 2 (nothing consumed at zero room)", sage)
	}
}
