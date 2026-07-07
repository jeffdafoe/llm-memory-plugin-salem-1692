package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// produce_tick_booster_test.go — LLM-248 under one-shot cycles (LLM-319).
// Optional booster inputs are evaluated at LANDING — the same instant the old
// continuous tick consumed them (mint time): a producer holding the booster
// consumes it and the batch lands with bonus_qty extra output; holding none
// leaves the base batch untouched (no skip, no penalty). One cycle = one
// execution, so at most one booster charge per batch.
//
// The fixture mirrors the LLM-83 dairy edge: milk @ output_qty 4, rate 4/1h
// (one 4-milk cycle per hour), boosted by 1 sage → +2 milk.

// buildBoosterTestWorld seeds elizabeth (dairy keeper at her farm, milk
// producer with a sage booster) with the given inventory.
func buildBoosterTestWorld(t *testing.T, milkCap int, inv map[sim.ItemKind]int) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		"milk": {Name: "milk", DisplayLabel: "Milk", DisplayLabelSingular: "pail of milk", DisplayLabelPlural: "milk", Category: sim.ItemCategoryDrink, SortOrder: 30},
		"sage": {Name: "sage", DisplayLabel: "Sage", Category: sim.ItemCategoryMaterial, SortOrder: 240},
	})
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
			Kind:              sim.KindNPCStateful,
			InsideStructureID: "farm",
			WorkStructureID:   "farm",
			Inventory:         inv,
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

// landMilkCycle starts elizabeth's milk cycle and drives it to landing (the
// 3600s cycle back-dated past its full duration, then one tick).
func landMilkCycle(t *testing.T, w *sim.World) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := w.Send(sim.StartProductionCycle("elizabeth", "milk")); err != nil {
		t.Fatalf("StartProductionCycle: %v", err)
	}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["elizabeth"].ProductionActivity.LastProgressAt = now.Add(-2 * time.Hour)
		return nil, nil
	}}); err != nil {
		t.Fatalf("rewind anchor: %v", err)
	}
	if _, err := w.Send(sim.ApplyProduceTick(now)); err != nil {
		t.Fatalf("tick: %v", err)
	}
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

// TestBoosterConsumedForBonusYield — the happy path: the landed batch is base
// +4 milk; sage held → 1 consumed at landing, +2 bonus → 6 milk.
func TestBoosterConsumedForBonusYield(t *testing.T) {
	w, cancel := buildBoosterTestWorld(t, 30, map[sim.ItemKind]int{"sage": 3})
	defer cancel()

	landMilkCycle(t, w)
	milk, sage := boosterInv(t, w)
	if milk != 6 {
		t.Errorf("milk = %d, want 6 (4 base + 2 bonus)", milk)
	}
	if sage != 2 {
		t.Errorf("sage = %d, want 2 (1 consumed at landing)", sage)
	}
}

// TestBoosterAbsentBaseProductionUntouched — no sage on hand: the batch lands
// exactly as an unboosted recipe (no skip, no penalty).
func TestBoosterAbsentBaseProductionUntouched(t *testing.T) {
	w, cancel := buildBoosterTestWorld(t, 30, map[sim.ItemKind]int{})
	defer cancel()

	landMilkCycle(t, w)
	milk, sage := boosterInv(t, w)
	if milk != 4 {
		t.Errorf("milk = %d, want 4 (base only)", milk)
	}
	if sage != 0 {
		t.Errorf("sage = %d, want 0", sage)
	}
}

// TestBoosterBonusClampedToCap — cap 5, base batch lands 4 (room 1): the bonus
// is trimmed to the remaining headroom (+1, not +2). The sage is still
// consumed in full — the herb went into the batch; the cap trims carry, not
// cost.
func TestBoosterBonusClampedToCap(t *testing.T) {
	w, cancel := buildBoosterTestWorld(t, 5, map[sim.ItemKind]int{"sage": 2})
	defer cancel()

	landMilkCycle(t, w)
	milk, sage := boosterInv(t, w)
	if milk != 5 {
		t.Errorf("milk = %d, want 5 (4 base + bonus clamped to cap)", milk)
	}
	if sage != 1 {
		t.Errorf("sage = %d, want 1 (consumed for the boosted batch)", sage)
	}
}

// TestBoosterZeroRoomSkipsConsumption — cap 4: the base batch exactly fills
// the cap (room 0), so the booster is NOT consumed (zero yield would be a pure
// waste of the herb). The base batch itself lands in full regardless — its
// below-cap check happened at start, and the pot yields what it yields.
func TestBoosterZeroRoomSkipsConsumption(t *testing.T) {
	w, cancel := buildBoosterTestWorld(t, 4, map[sim.ItemKind]int{"sage": 2})
	defer cancel()

	landMilkCycle(t, w)
	milk, sage := boosterInv(t, w)
	if milk != 4 {
		t.Errorf("milk = %d, want 4 (base fills cap exactly)", milk)
	}
	if sage != 2 {
		t.Errorf("sage = %d, want 2 (nothing consumed at zero room)", sage)
	}
}
