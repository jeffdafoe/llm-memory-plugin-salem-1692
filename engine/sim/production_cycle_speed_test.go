package sim_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// production_cycle_speed_test.go — LLM-511 speed-booster inputs at the sim level.
// A speed input is committed AT START: if the actor holds it when a cycle opens,
// StartProductionCycle consumes it (no refund) and shortens the cycle's
// RemainingSeconds by the rate factor (rate_pct 200 halves it). Holding none
// leaves the cycle at base rate — never a gate (the liveness rule). The shortened
// RemainingSeconds then flows through the ordinary produce tick, so it composes
// multiplicatively with the LLM-224 labor boost for free.
//
// Fixture: an Ezekiel-shaped smith forging shovels — base OutputQty 1 at 1 per
// 4h (CycleDurationSeconds 14400), speed_inputs [{iron,1,200}] (a bar in hand →
// 7200s of work). Mirrors the live shovel the LLM-511 migration authors.

// buildShovelSmithWorld seeds the smith at his forge with the LLM-511 shovel
// recipe and the given inventory, plus an optional helper standing at the forge
// for the labor-composition case.
func buildShovelSmithWorld(t *testing.T, inv map[sim.ItemKind]int, helperInside sim.StructureID) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		"shovel": {Name: "shovel", DisplayLabel: "shovel", DisplayLabelSingular: "shovel", DisplayLabelPlural: "shovels", Category: sim.ItemCategory("tool"), SortOrder: 350},
		"iron":   {Name: "iron", DisplayLabel: "bar iron", DisplayLabelSingular: "bar of iron", DisplayLabelPlural: "bars of iron", Category: sim.ItemCategoryMaterial, SortOrder: 400},
	})
	handles.Recipes.Seed(map[sim.ItemKind]*sim.ItemRecipe{
		"shovel": {
			OutputItem:     "shovel",
			OutputQty:      1,
			RateQty:        1,
			RatePerHours:   4,
			SpeedInputs:    []sim.SpeedInput{{Item: "iron", Qty: 1, RatePct: 200}},
			WholesalePrice: 6, RetailPrice: 12,
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
				{Item: "shovel", Source: sim.RestockSourceProduce, Max: 20},
			}},
		},
		"helper": {ID: "helper", LLMAgent: "salem-vendor", InsideStructureID: helperInside},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

// smithShovelInventory reads ezekiel's inventory off the world goroutine.
func smithShovelInventory(t *testing.T, w *sim.World) map[sim.ItemKind]int {
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

// smithRemainingSeconds reads the in-flight cycle's RemainingSeconds (0 if none).
func smithRemainingSeconds(t *testing.T, w *sim.World) int64 {
	t.Helper()
	got, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		if act := world.Actors["ezekiel"].ProductionActivity; act != nil {
			return act.RemainingSeconds, nil
		}
		return int64(0), nil
	}})
	if err != nil {
		t.Fatalf("read remaining: %v", err)
	}
	return got.(int64)
}

// rewindSmithAnchor back-dates the cycle anchor by d so the next tick credits d
// of elapsed work.
func rewindSmithAnchor(t *testing.T, w *sim.World, now time.Time, d time.Duration) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["ezekiel"].ProductionActivity.LastProgressAt = now.Add(-d)
		return nil, nil
	}}); err != nil {
		t.Fatalf("rewind anchor: %v", err)
	}
}

// TestSpeedInputHalvesCycleAndConsumes — a bar in hand at start halves the
// cycle's base-rate work (14400 → 7200) and is consumed on the spot, with the
// halved duration echoed on the start result.
func TestSpeedInputHalvesCycleAndConsumes(t *testing.T) {
	w, cancel := buildShovelSmithWorld(t, map[sim.ItemKind]int{"iron": 1}, "")
	defer cancel()

	res, err := w.Send(sim.StartProductionCycle("ezekiel", "shovel", "", false))
	if err != nil {
		t.Fatalf("StartProductionCycle: %v", err)
	}
	start := res.(sim.ProductionStartResult)
	if start.DurationSeconds != 7200 {
		t.Errorf("DurationSeconds = %d, want 7200 (14400 base halved by rate_pct 200)", start.DurationSeconds)
	}
	if !strings.Contains(start.InputsUsed, "iron") {
		t.Errorf("InputsUsed = %q, want it to name the consumed iron", start.InputsUsed)
	}
	if rem := smithRemainingSeconds(t, w); rem != 7200 {
		t.Errorf("RemainingSeconds = %d, want 7200", rem)
	}
	if inv := smithShovelInventory(t, w); inv["iron"] != 0 {
		t.Errorf("iron = %d, want 0 (the bar was consumed at start)", inv["iron"])
	}
}

// TestSpeedInputZeroHeldRunsBaseRate — the liveness leg: no iron means the cycle
// still starts, at full base-rate work, consuming nothing. A speed input never
// gates production.
func TestSpeedInputZeroHeldRunsBaseRate(t *testing.T) {
	w, cancel := buildShovelSmithWorld(t, map[sim.ItemKind]int{}, "")
	defer cancel()

	res, err := w.Send(sim.StartProductionCycle("ezekiel", "shovel", "", false))
	if err != nil {
		t.Fatalf("StartProductionCycle: %v", err)
	}
	if start := res.(sim.ProductionStartResult); start.DurationSeconds != 14400 {
		t.Errorf("DurationSeconds = %d, want 14400 (base rate, no iron)", start.DurationSeconds)
	}
	if rem := smithRemainingSeconds(t, w); rem != 14400 {
		t.Errorf("RemainingSeconds = %d, want 14400 (no speedup without iron)", rem)
	}
}

// TestSpeedInputEndToEndLandsInHalfTime — the shortened clock is real end to end:
// a sped cycle lands after 7200s of wall time, where the base cycle needs 14400s
// and has NOT landed at the same 7200s mark.
func TestSpeedInputEndToEndLandsInHalfTime(t *testing.T) {
	now := time.Now().UTC()

	t.Run("sped cycle lands at 7200s", func(t *testing.T) {
		w, cancel := buildShovelSmithWorld(t, map[sim.ItemKind]int{"iron": 1}, "")
		defer cancel()
		if _, err := w.Send(sim.StartProductionCycle("ezekiel", "shovel", "", false)); err != nil {
			t.Fatalf("StartProductionCycle: %v", err)
		}
		rewindSmithAnchor(t, w, now, 7200*time.Second)
		if _, err := w.Send(sim.ApplyProduceTick(now)); err != nil {
			t.Fatalf("tick: %v", err)
		}
		if inv := smithShovelInventory(t, w); inv["shovel"] != 1 {
			t.Errorf("shovel = %d, want 1 (sped cycle lands in 2h)", inv["shovel"])
		}
	})

	t.Run("base cycle has not landed at 7200s", func(t *testing.T) {
		w, cancel := buildShovelSmithWorld(t, map[sim.ItemKind]int{}, "")
		defer cancel()
		if _, err := w.Send(sim.StartProductionCycle("ezekiel", "shovel", "", false)); err != nil {
			t.Fatalf("StartProductionCycle: %v", err)
		}
		rewindSmithAnchor(t, w, now, 7200*time.Second)
		if _, err := w.Send(sim.ApplyProduceTick(now)); err != nil {
			t.Fatalf("tick: %v", err)
		}
		if inv := smithShovelInventory(t, w); inv["shovel"] != 0 {
			t.Errorf("shovel = %d, want 0 (base cycle needs 14400s)", inv["shovel"])
		}
	})
}

// TestSpeedInputComposesWithLabor — iron and a hired helper compound
// multiplicatively: the 14400s base halves to 7200s (iron), and a boost-50
// helper credits at 1.5x, so 4800s of wall time credits 7200 and lands the batch
// — a 3x speedup. Without the helper the same 4800s credits only 4800 and the
// batch is still in flight.
func TestSpeedInputComposesWithLabor(t *testing.T) {
	now := time.Now().UTC()

	t.Run("iron plus helper lands at 4800s", func(t *testing.T) {
		w, cancel := buildShovelSmithWorld(t, map[sim.ItemKind]int{"iron": 1}, "forge")
		defer cancel()
		setLaborBoost(t, w, 50)
		seedLaborOffer(t, w, workingOffer(1, "helper", "ezekiel"))
		if _, err := w.Send(sim.StartProductionCycle("ezekiel", "shovel", "", false)); err != nil {
			t.Fatalf("StartProductionCycle: %v", err)
		}
		rewindSmithAnchor(t, w, now, 4800*time.Second)
		if _, err := w.Send(sim.ApplyProduceTick(now)); err != nil {
			t.Fatalf("tick: %v", err)
		}
		if inv := smithShovelInventory(t, w); inv["shovel"] != 1 {
			t.Errorf("shovel = %d, want 1 (iron 2x × labor 1.5x → 3x, lands at 4800s)", inv["shovel"])
		}
	})

	t.Run("iron alone has not landed at 4800s", func(t *testing.T) {
		w, cancel := buildShovelSmithWorld(t, map[sim.ItemKind]int{"iron": 1}, "")
		defer cancel()
		if _, err := w.Send(sim.StartProductionCycle("ezekiel", "shovel", "", false)); err != nil {
			t.Fatalf("StartProductionCycle: %v", err)
		}
		rewindSmithAnchor(t, w, now, 4800*time.Second)
		if _, err := w.Send(sim.ApplyProduceTick(now)); err != nil {
			t.Fatalf("tick: %v", err)
		}
		if inv := smithShovelInventory(t, w); inv["shovel"] != 0 {
			t.Errorf("shovel = %d, want 0 (iron alone: 4800 < 7200 remaining)", inv["shovel"])
		}
	})
}
