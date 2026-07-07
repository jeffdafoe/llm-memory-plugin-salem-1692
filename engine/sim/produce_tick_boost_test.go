package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// produce_tick_boost_test.go — LLM-224 under one-shot cycles (LLM-319). A
// worker laboring AT the keeper's establishment scales the keeper's per-tick
// progress credit: credit = elapsed × (100 + helpers × LaborProduceBoostPct) /
// 100, re-sampled per tick so a helper hired mid-batch speeds the remainder.
//
// The fixture: hannah keeps the inn with a bread cycle in flight (batch of 2 @
// 2/hr → cycle 3600s of base work). With 40 minutes elapsed (2400s): base
// credit 2400 leaves the batch unfinished; one boost-50 helper credits 3600 —
// the cycle lands; two helpers credit 4800 — it lands with room to spare.

// buildBoostTestWorld seeds hannah (keeper, at the inn, bread producer) plus
// lewis and anne as potential helpers, standing where the test says.
func buildBoostTestWorld(t *testing.T, breadCap int, lewisInside, anneInside sim.StructureID) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		"bread": {Name: "bread", DisplayLabel: "Bread", DisplayLabelSingular: "loaf of bread", DisplayLabelPlural: "loaves of bread", Category: sim.ItemCategoryFood, SortOrder: 110},
	})
	handles.Recipes.Seed(map[sim.ItemKind]*sim.ItemRecipe{
		"bread": {
			OutputItem:   "bread",
			OutputQty:    2,
			RateQty:      2,
			RatePerHours: 1,
		},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {
			ID:                "hannah",
			LLMAgent:          "hannah-innkeeper",
			Kind:              sim.KindNPCStateful,
			InsideStructureID: "inn",
			WorkStructureID:   "inn",
			Inventory:         map[sim.ItemKind]int{},
			RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
				{Item: "bread", Source: sim.RestockSourceProduce, Max: breadCap},
			}},
		},
		"lewis": {ID: "lewis", LLMAgent: "salem-vendor", InsideStructureID: lewisInside},
		"anne":  {ID: "anne", LLMAgent: "salem-vendor", InsideStructureID: anneInside},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

func setLaborBoost(t *testing.T, w *sim.World, pct int) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.LaborProduceBoostPct = pct
		return nil, nil
	}}); err != nil {
		t.Fatalf("set boost: %v", err)
	}
}

func workingOffer(id sim.LaborID, worker, employer sim.ActorID) sim.LaborOffer {
	return sim.LaborOffer{
		ID: id, WorkerID: worker, EmployerID: employer,
		Reward: 2, DurationMin: 120, State: sim.LaborStateWorking,
	}
}

func breadCount(t *testing.T, w *sim.World) int {
	t.Helper()
	inv, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors["hannah"].Inventory["bread"], nil
	}})
	if err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	return inv.(int)
}

// startBoostCycle opens hannah's bread cycle and rewinds its anchor 40 minutes
// (2400s of elapsed wall time against a 3600s cycle — the base rate cannot
// finish it, the boosted rates can).
func startBoostCycle(t *testing.T, w *sim.World, now time.Time) {
	t.Helper()
	if _, err := w.Send(sim.StartProductionCycle("hannah", "bread")); err != nil {
		t.Fatalf("StartProductionCycle: %v", err)
	}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["hannah"].ProductionActivity.LastProgressAt = now.Add(-40 * time.Minute)
		return nil, nil
	}}); err != nil {
		t.Fatalf("rewind anchor: %v", err)
	}
}

func TestProduceTickLaborBoost(t *testing.T) {
	t.Run("one helper at the establishment lands the batch sooner", func(t *testing.T) {
		now := time.Now().UTC()
		w, cancel := buildBoostTestWorld(t, 10, "inn", "")
		defer cancel()
		setLaborBoost(t, w, 50)
		seedLaborOffer(t, w, workingOffer(1, "lewis", "hannah"))
		startBoostCycle(t, w, now)

		if _, err := w.Send(sim.ApplyProduceTick(now)); err != nil {
			t.Fatalf("tick: %v", err)
		}
		if got := breadCount(t, w); got != 2 {
			t.Errorf("bread = %d, want 2 (1.5x credit 3600 ≥ 3600s cycle — landed)", got)
		}
	})

	t.Run("base rate alone has not finished the cycle yet", func(t *testing.T) {
		now := time.Now().UTC()
		w, cancel := buildBoostTestWorld(t, 10, "well", "")
		defer cancel()
		setLaborBoost(t, w, 50)
		startBoostCycle(t, w, now)

		if _, err := w.Send(sim.ApplyProduceTick(now)); err != nil {
			t.Fatalf("tick: %v", err)
		}
		if got := breadCount(t, w); got != 0 {
			t.Errorf("bread = %d, want 0 (base credit 2400 < 3600s cycle)", got)
		}
	})

	t.Run("helper elsewhere does not boost", func(t *testing.T) {
		// A deal struck away from the establishment speeds nothing until the
		// worker is actually there — the location gate in laboringHelperCount.
		now := time.Now().UTC()
		w, cancel := buildBoostTestWorld(t, 10, "well", "")
		defer cancel()
		setLaborBoost(t, w, 50)
		seedLaborOffer(t, w, workingOffer(1, "lewis", "hannah"))
		startBoostCycle(t, w, now)

		if _, err := w.Send(sim.ApplyProduceTick(now)); err != nil {
			t.Fatalf("tick: %v", err)
		}
		if got := breadCount(t, w); got != 0 {
			t.Errorf("bread = %d, want 0 (base credit — helper is not at the inn)", got)
		}
	})

	t.Run("worker for another employer does not boost", func(t *testing.T) {
		now := time.Now().UTC()
		w, cancel := buildBoostTestWorld(t, 10, "inn", "")
		defer cancel()
		setLaborBoost(t, w, 50)
		seedLaborOffer(t, w, workingOffer(1, "lewis", "josiah"))
		startBoostCycle(t, w, now)

		if _, err := w.Send(sim.ApplyProduceTick(now)); err != nil {
			t.Fatalf("tick: %v", err)
		}
		if got := breadCount(t, w); got != 0 {
			t.Errorf("bread = %d, want 0 (base credit — lewis works for someone else)", got)
		}
	})

	t.Run("pct 0 disables the boost", func(t *testing.T) {
		now := time.Now().UTC()
		w, cancel := buildBoostTestWorld(t, 10, "inn", "")
		defer cancel()
		setLaborBoost(t, w, 0)
		seedLaborOffer(t, w, workingOffer(1, "lewis", "hannah"))
		startBoostCycle(t, w, now)

		if _, err := w.Send(sim.ApplyProduceTick(now)); err != nil {
			t.Fatalf("tick: %v", err)
		}
		if got := breadCount(t, w); got != 0 {
			t.Errorf("bread = %d, want 0 (boost disabled — base credit only)", got)
		}
	})

	t.Run("two helpers stack", func(t *testing.T) {
		now := time.Now().UTC()
		w, cancel := buildBoostTestWorld(t, 10, "inn", "inn")
		defer cancel()
		setLaborBoost(t, w, 50)
		seedLaborOffer(t, w, workingOffer(1, "lewis", "hannah"))
		seedLaborOffer(t, w, workingOffer(2, "anne", "hannah"))
		// A shorter window than the one-helper case still lands at 2x:
		// 31 min elapsed → credit 3720 ≥ 3600.
		if _, err := w.Send(sim.StartProductionCycle("hannah", "bread")); err != nil {
			t.Fatalf("StartProductionCycle: %v", err)
		}
		if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			world.Actors["hannah"].ProductionActivity.LastProgressAt = now.Add(-31 * time.Minute)
			return nil, nil
		}}); err != nil {
			t.Fatalf("rewind anchor: %v", err)
		}

		if _, err := w.Send(sim.ApplyProduceTick(now)); err != nil {
			t.Fatalf("tick: %v", err)
		}
		if got := breadCount(t, w); got != 2 {
			t.Errorf("bread = %d, want 2 (2x credit 3720 ≥ 3600s cycle)", got)
		}
	})

	t.Run("pending offer does not boost", func(t *testing.T) {
		// Only an ACCEPTED job helps — a pending solicitation is just talk.
		now := time.Now().UTC()
		w, cancel := buildBoostTestWorld(t, 10, "inn", "")
		defer cancel()
		setLaborBoost(t, w, 50)
		o := workingOffer(1, "lewis", "hannah")
		o.State = sim.LaborStatePending
		seedLaborOffer(t, w, o)
		startBoostCycle(t, w, now)

		if _, err := w.Send(sim.ApplyProduceTick(now)); err != nil {
			t.Fatalf("tick: %v", err)
		}
		if got := breadCount(t, w); got != 0 {
			t.Errorf("bread = %d, want 0 (base credit — offer still pending)", got)
		}
	})
}
