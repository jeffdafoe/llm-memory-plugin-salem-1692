package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// produce_tick_iron_boost_test.go — LLM-442 simulation-level coverage of the
// two-leg nail recipe (inputless 1/hr rough base, iron booster +4), in the
// exact shape the migration ships: OutputQty 1, cap 20, boost [{iron,1,+4}].
// The generic booster mechanics are pinned by produce_tick_booster_test.go
// (milk/sage); these tests pin the LLM-442 economics against the smith's cap:
//
//   - zero iron still lands a batch — the rough-nails liveness leg at the
//     execution level, not just the perception cue;
//   - a full boost consumes exactly one bar per landed batch;
//   - at the cap edge (base fills the last slot, zero bonus room) the bar is
//     NOT consumed — the smith's purchased input is never spent on nothing;
//   - a partially clamped bonus still consumes the bar in full — the
//     documented LLM-248 semantics (the clamp trims yield, not cost: the bar
//     went into the forge either way). Pinned so a future "fix" can't flip it
//     silently in either direction without a conversation.

// buildIronSmithWorld seeds an Ezekiel-shaped smith at his forge with the
// LLM-442 nail recipe and the given inventory.
func buildIronSmithWorld(t *testing.T, nailCap int, inv map[sim.ItemKind]int) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		"nail": {Name: "nail", DisplayLabel: "Nail", DisplayLabelSingular: "nail", DisplayLabelPlural: "nails", Category: sim.ItemCategory("tool"), SortOrder: 340},
		"iron": {Name: "iron", DisplayLabel: "bar iron", DisplayLabelSingular: "bar of iron", DisplayLabelPlural: "bars of iron", Category: sim.ItemCategoryMaterial, SortOrder: 400},
	})
	handles.Recipes.Seed(map[sim.ItemKind]*sim.ItemRecipe{
		"nail": {
			OutputItem:   "nail",
			OutputQty:    1,
			RateQty:      1,
			RatePerHours: 1,
			BoostInputs:  []sim.BoostInput{{Item: "iron", Qty: 1, BonusQty: 4}},
		},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"ezekiel": {
			ID:                "ezekiel",
			LLMAgent:          "ezekiel-forge",
			Kind:              sim.KindNPCStateful,
			InsideStructureID: "forge",
			WorkStructureID:   "forge",
			Inventory:         inv,
			RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
				{Item: "nail", Source: sim.RestockSourceProduce, Max: nailCap},
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

// landNailCycle starts ezekiel's nail cycle and drives it to landing (the
// 1-hour cycle back-dated past its full duration, then one tick).
func landNailCycle(t *testing.T, w *sim.World) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := w.Send(sim.StartProductionCycle("ezekiel", "nail")); err != nil {
		t.Fatalf("StartProductionCycle: %v", err)
	}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["ezekiel"].ProductionActivity.LastProgressAt = now.Add(-2 * time.Hour)
		return nil, nil
	}}); err != nil {
		t.Fatalf("rewind anchor: %v", err)
	}
	if _, err := w.Send(sim.ApplyProduceTick(now)); err != nil {
		t.Fatalf("tick: %v", err)
	}
}

// smithInventory reads ezekiel's landed inventory off the world goroutine.
func smithInventory(t *testing.T, w *sim.World) map[sim.ItemKind]int {
	t.Helper()
	got, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		out := map[sim.ItemKind]int{}
		for k, v := range world.Actors["ezekiel"].Inventory {
			out[k] = v
		}
		return out, nil
	}})
	if err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	return got.(map[sim.ItemKind]int)
}

// TestRoughNailsZeroIronStillLands — the liveness leg at execution level: with
// no iron anywhere in the world, a nail batch starts, runs, and lands its base
// unit. The import chain being empty slows the forge; it can never stop it.
func TestRoughNailsZeroIronStillLands(t *testing.T) {
	w, cancel := buildIronSmithWorld(t, 20, map[sim.ItemKind]int{})
	defer cancel()
	landNailCycle(t, w)
	inv := smithInventory(t, w)
	if inv["nail"] != 1 {
		t.Errorf("nails = %d, want 1 (the rough base batch)", inv["nail"])
	}
	if inv["iron"] != 0 {
		t.Errorf("iron = %d, want 0", inv["iron"])
	}
}

// TestIronBoostConsumesOneBarPerBatch — the normal leg: a bar in hand at
// landing is consumed for the +4 bonus, exactly one bar per landed batch.
func TestIronBoostConsumesOneBarPerBatch(t *testing.T) {
	w, cancel := buildIronSmithWorld(t, 20, map[sim.ItemKind]int{"nail": 8, "iron": 3})
	defer cancel()
	landNailCycle(t, w)
	inv := smithInventory(t, w)
	if inv["nail"] != 13 {
		t.Errorf("nails = %d, want 13 (8 + 1 base + 4 bonus)", inv["nail"])
	}
	if inv["iron"] != 2 {
		t.Errorf("iron = %d, want 2 (exactly one bar consumed)", inv["iron"])
	}
}

// TestIronBoostAtCapEdgeSkipsBar — stock 19 of cap 20: the base unit fills the
// last slot, bonus room is zero, and landProductionCycle must SKIP the bar
// entirely (a fully clamped bonus skips consumption). The smith's purchased
// input is never spent on a batch that can't take the bonus.
func TestIronBoostAtCapEdgeSkipsBar(t *testing.T) {
	w, cancel := buildIronSmithWorld(t, 20, map[sim.ItemKind]int{"nail": 19, "iron": 1})
	defer cancel()
	landNailCycle(t, w)
	inv := smithInventory(t, w)
	if inv["nail"] != 20 {
		t.Errorf("nails = %d, want 20 (base unit lands, cap-full)", inv["nail"])
	}
	if inv["iron"] != 1 {
		t.Errorf("iron = %d, want 1 (bar NOT consumed when the bonus is fully clamped)", inv["iron"])
	}
}

// TestIronBoostPartialClampConsumesBar — stock 17 of cap 20: base lands (18),
// bonus room is 2 of the nominal 4, and the bar is consumed IN FULL for the
// trimmed bonus. This is the documented LLM-248 posture — the clamp trims
// yield, not cost; the bar went into the forge either way — pinned here so a
// future change to partial-clamp economics is a deliberate decision, not a
// drive-by.
func TestIronBoostPartialClampConsumesBar(t *testing.T) {
	w, cancel := buildIronSmithWorld(t, 20, map[sim.ItemKind]int{"nail": 17, "iron": 1})
	defer cancel()
	landNailCycle(t, w)
	inv := smithInventory(t, w)
	if inv["nail"] != 20 {
		t.Errorf("nails = %d, want 20 (17 + 1 base + 2 clamped bonus)", inv["nail"])
	}
	if inv["iron"] != 0 {
		t.Errorf("iron = %d, want 0 (partial clamp still consumes the bar — LLM-248 semantics)", inv["iron"])
	}
}
