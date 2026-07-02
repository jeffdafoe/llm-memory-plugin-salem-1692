package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// produce_tick_boost_test.go — LLM-224. A worker laboring AT the keeper's
// establishment scales the keeper's produce tick: rateScalePct = 100 +
// helpers × LaborProduceBoostPct, applied by shrinking secondsPerUnit.
//
// The fixture: hannah keeps the inn and produces bread (2/hr batch of 2 →
// secondsPerUnit 1800, no inputs), anchor 90 min back. Base yield for 5400s
// is 3 units owed → 1 execution → +2 bread. With one boost-50 helper,
// secondsPerUnit becomes 1200 → 4 units owed → 2 executions → +4; two
// helpers → 900 → 6 owed → 3 executions → +6.

// buildBoostTestWorld seeds hannah (keeper, at the inn, bread producer) plus
// lewis and anne as potential helpers, standing where the test says.
func buildBoostTestWorld(t *testing.T, anchor time.Time, breadCap int, lewisInside, anneInside sim.StructureID) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
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
			InsideStructureID: "inn",
			WorkStructureID:   "inn",
			Inventory:         map[sim.ItemKind]int{},
			ProduceState: map[sim.ItemKind]*sim.ProduceState{
				"bread": {Item: "bread", LastProducedAt: anchor},
			},
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

func TestProduceTickLaborBoost(t *testing.T) {
	now := time.Now().UTC()
	anchor := now.Add(-90 * time.Minute)

	t.Run("one helper at the establishment speeds production", func(t *testing.T) {
		w, cancel := buildBoostTestWorld(t, anchor, 10, "inn", "")
		defer cancel()
		setLaborBoost(t, w, 50)
		seedLaborOffer(t, w, workingOffer(1, "lewis", "hannah"))

		if _, err := w.Send(sim.ApplyProduceTick(now)); err != nil {
			t.Fatalf("tick: %v", err)
		}
		if got := breadCount(t, w); got != 4 {
			t.Errorf("bread = %d, want 4 (1.5x rate: 4 units owed → 2 executions)", got)
		}
	})

	t.Run("two helpers stack", func(t *testing.T) {
		w, cancel := buildBoostTestWorld(t, anchor, 10, "inn", "inn")
		defer cancel()
		setLaborBoost(t, w, 50)
		seedLaborOffer(t, w, workingOffer(1, "lewis", "hannah"))
		seedLaborOffer(t, w, workingOffer(2, "anne", "hannah"))

		if _, err := w.Send(sim.ApplyProduceTick(now)); err != nil {
			t.Fatalf("tick: %v", err)
		}
		if got := breadCount(t, w); got != 6 {
			t.Errorf("bread = %d, want 6 (2x rate: 6 units owed → 3 executions)", got)
		}
	})

	t.Run("helper elsewhere does not boost", func(t *testing.T) {
		// A deal struck away from the establishment speeds nothing until the
		// worker is actually there — the location gate in laboringHelperCount.
		w, cancel := buildBoostTestWorld(t, anchor, 10, "well", "")
		defer cancel()
		setLaborBoost(t, w, 50)
		seedLaborOffer(t, w, workingOffer(1, "lewis", "hannah"))

		if _, err := w.Send(sim.ApplyProduceTick(now)); err != nil {
			t.Fatalf("tick: %v", err)
		}
		if got := breadCount(t, w); got != 2 {
			t.Errorf("bread = %d, want 2 (base rate — helper is not at the inn)", got)
		}
	})

	t.Run("worker for another employer does not boost", func(t *testing.T) {
		w, cancel := buildBoostTestWorld(t, anchor, 10, "inn", "")
		defer cancel()
		setLaborBoost(t, w, 50)
		seedLaborOffer(t, w, workingOffer(1, "lewis", "josiah"))

		if _, err := w.Send(sim.ApplyProduceTick(now)); err != nil {
			t.Fatalf("tick: %v", err)
		}
		if got := breadCount(t, w); got != 2 {
			t.Errorf("bread = %d, want 2 (base rate — lewis works for someone else)", got)
		}
	})

	t.Run("pct 0 disables the boost", func(t *testing.T) {
		w, cancel := buildBoostTestWorld(t, anchor, 10, "inn", "")
		defer cancel()
		setLaborBoost(t, w, 0)
		seedLaborOffer(t, w, workingOffer(1, "lewis", "hannah"))

		if _, err := w.Send(sim.ApplyProduceTick(now)); err != nil {
			t.Fatalf("tick: %v", err)
		}
		if got := breadCount(t, w); got != 2 {
			t.Errorf("bread = %d, want 2 (base rate — boost disabled)", got)
		}
	})

	t.Run("boosted production still respects the cap", func(t *testing.T) {
		// Cap 3 leaves headroom for one execution (batch of 2) — the boost
		// accelerates refill but never overstocks.
		w, cancel := buildBoostTestWorld(t, anchor, 3, "inn", "inn")
		defer cancel()
		setLaborBoost(t, w, 50)
		seedLaborOffer(t, w, workingOffer(1, "lewis", "hannah"))
		seedLaborOffer(t, w, workingOffer(2, "anne", "hannah"))

		if _, err := w.Send(sim.ApplyProduceTick(now)); err != nil {
			t.Fatalf("tick: %v", err)
		}
		if got := breadCount(t, w); got != 2 {
			t.Errorf("bread = %d, want 2 (headroom 3 → 1 execution despite 3 owed)", got)
		}
	})

	t.Run("pending offer does not boost", func(t *testing.T) {
		// Only an ACCEPTED job helps — a pending solicitation is just talk.
		w, cancel := buildBoostTestWorld(t, anchor, 10, "inn", "")
		defer cancel()
		setLaborBoost(t, w, 50)
		o := workingOffer(1, "lewis", "hannah")
		o.State = sim.LaborStatePending
		seedLaborOffer(t, w, o)

		if _, err := w.Send(sim.ApplyProduceTick(now)); err != nil {
			t.Fatalf("tick: %v", err)
		}
		if got := breadCount(t, w); got != 2 {
			t.Errorf("bread = %d, want 2 (base rate — offer still pending)", got)
		}
	})
}
