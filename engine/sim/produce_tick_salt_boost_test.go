package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// produce_tick_salt_boost_test.go — LLM-444 simulation-level coverage of salt as
// an OPTIONAL cooking booster, in the shape the migration ships (stew: base yield
// with boost [{salt,1,+2}]). The generic booster mechanics and the cap-clamp
// cost semantics are pinned by produce_tick_booster_test.go (milk/sage) and
// produce_tick_iron_boost_test.go; these tests pin the LLM-444 liveness guarantee
// specific to the survival good:
//
//   - zero salt still lands a full base batch — a dish is NEVER gated on the
//     import, at the execution level, not just the perception cue (the permanent
//     "food is never gated" constraint in executable form);
//   - salt in hand at landing is consumed for the +2 bonus, exactly one sack per
//     landed batch.

// buildSaltCookWorld seeds a John-shaped tavernkeeper at his tavern with the
// LLM-444 salt-boosted stew recipe and the given inventory.
func buildSaltCookWorld(t *testing.T, stewCap int, inv map[sim.ItemKind]int) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		"stew": {Name: "stew", DisplayLabel: "stew", DisplayLabelSingular: "bowl of stew", DisplayLabelPlural: "stew", Category: sim.ItemCategoryFood, SortOrder: 200},
		"salt": {Name: "salt", DisplayLabel: "salt", DisplayLabelSingular: "sack of salt", DisplayLabelPlural: "sacks of salt", Category: sim.ItemCategoryMaterial, SortOrder: 410},
	})
	handles.Recipes.Seed(map[sim.ItemKind]*sim.ItemRecipe{
		"stew": {
			OutputItem:   "stew",
			OutputQty:    6,
			RateQty:      30,
			RatePerHours: 6,
			BoostInputs:  []sim.BoostInput{{Item: "salt", Qty: 1, BonusQty: 2}},
		},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"john": {
			ID:                "john",
			LLMAgent:          "john-tavern",
			Kind:              sim.KindNPCStateful,
			InsideStructureID: "tavern",
			WorkStructureID:   "tavern",
			Inventory:         inv,
			RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
				{Item: "stew", Source: sim.RestockSourceProduce, Max: stewCap},
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

// landStewCycle starts john's stew cycle and drives it to landing (the cycle
// back-dated past its full duration, then one tick).
func landStewCycle(t *testing.T, w *sim.World) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := w.Send(sim.StartProductionCycle("john", "stew", "", false)); err != nil {
		t.Fatalf("StartProductionCycle: %v", err)
	}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["john"].ProductionActivity.LastProgressAt = now.Add(-24 * time.Hour)
		return nil, nil
	}}); err != nil {
		t.Fatalf("rewind anchor: %v", err)
	}
	if _, err := w.Send(sim.ApplyProduceTick(now)); err != nil {
		t.Fatalf("tick: %v", err)
	}
}

// cookInventory reads john's landed inventory off the world goroutine.
func cookInventory(t *testing.T, w *sim.World) map[sim.ItemKind]int {
	t.Helper()
	got, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		out := map[sim.ItemKind]int{}
		for k, v := range world.Actors["john"].Inventory {
			out[k] = v
		}
		return out, nil
	}})
	if err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	return got.(map[sim.ItemKind]int)
}

// TestUnsaltedStewZeroSaltStillLands — the liveness leg at execution level: with
// no salt anywhere in the world, a stew batch starts, runs, and lands its full
// base yield. Salt is never a required input, so the empty import chain can never
// stop a dish from cooking.
func TestUnsaltedStewZeroSaltStillLands(t *testing.T) {
	w, cancel := buildSaltCookWorld(t, 30, map[sim.ItemKind]int{})
	defer cancel()
	landStewCycle(t, w)
	inv := cookInventory(t, w)
	if inv["stew"] != 6 {
		t.Errorf("stew = %d, want 6 (the full base batch, unboosted)", inv["stew"])
	}
	if inv["salt"] != 0 {
		t.Errorf("salt = %d, want 0", inv["salt"])
	}
}

// TestSaltBoostConsumesOneSackPerBatch — the boosted leg: a sack in hand at
// landing is consumed for the +2 bonus, exactly one sack per landed batch.
func TestSaltBoostConsumesOneSackPerBatch(t *testing.T) {
	w, cancel := buildSaltCookWorld(t, 30, map[sim.ItemKind]int{"stew": 6, "salt": 3})
	defer cancel()
	landStewCycle(t, w)
	inv := cookInventory(t, w)
	if inv["stew"] != 14 {
		t.Errorf("stew = %d, want 14 (6 + 6 base + 2 bonus)", inv["stew"])
	}
	if inv["salt"] != 2 {
		t.Errorf("salt = %d, want 2 (exactly one sack consumed)", inv["salt"])
	}
}
