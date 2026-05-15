package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// buildProduceTestWorld seeds two recipes and a tavernkeeper actor at
// work, ready to produce. lastProduced controls how much time has
// elapsed at the start of the test.
func buildProduceTestWorld(t *testing.T, lastProduced time.Time, restock []sim.RestockEntry, inv map[sim.ItemKind]int) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Recipes.Seed(map[sim.ItemKind]*sim.ItemRecipe{
		// 1 stew per hour, requires 1 vegetable + 1 water per stew.
		"stew": {
			OutputItem:     "stew",
			OutputQty:      1,
			RateQty:        1,
			RatePerHours:   1,
			Inputs:         []sim.RecipeInput{{Item: "vegetable", Qty: 1}, {Item: "water", Qty: 1}},
			WholesalePrice: 2,
			RetailPrice:    4,
		},
		// Free bread: 2 per hour, no inputs.
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
			Inventory:         inv,
			ProduceState: map[sim.ItemKind]*sim.ProduceState{
				"stew":  {Item: "stew", LastProducedAt: lastProduced},
				"bread": {Item: "bread", LastProducedAt: lastProduced},
			},
			RestockPolicy: &sim.RestockPolicy{Restock: restock},
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

// TestApplyProduceTickFreeRecipe covers the no-inputs path. Bread @
// 2/hr, 90 min elapsed → 3 units produced (rounded down? actually with
// output_qty=2 and rate_qty=2/rate_per_hours=1, seconds_per_unit=1800,
// 90min=5400s → 3 units owed, executions = 3/2 = 1 execution → +2
// bread). Anchor advances by exactly 2 units * 1800s = 1h.
func TestApplyProduceTickFreeRecipe(t *testing.T) {
	now := time.Now().UTC()
	anchor := now.Add(-90 * time.Minute)
	w, cancel := buildProduceTestWorld(t, anchor, []sim.RestockEntry{
		{Item: "bread", Source: sim.RestockSourceProduce, Max: 10},
	}, map[sim.ItemKind]int{})
	defer cancel()

	res, err := w.Send(sim.ApplyProduceTick(now))
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	r := res.(sim.ProduceTickResult)
	if r.Executions != 1 {
		t.Errorf("Executions = %d, want 1", r.Executions)
	}

	snap := w.Published().Actors["hannah"]
	// ActorSnapshot.InventoryHash is sum of quantities; check the
	// underlying actor.
	inv, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return world.Actors["hannah"].Inventory["bread"], nil
		},
	})
	if inv.(int) != 2 {
		t.Errorf("bread inventory = %d, want 2", inv.(int))
	}
	_ = snap
}

// TestApplyProduceTickWithInputs covers the input-required path. Stew
// @ 1/hr, 90 min elapsed, but only 1 vegetable + 1 water available, so
// 1 execution → consume both inputs, +1 stew.
func TestApplyProduceTickWithInputs(t *testing.T) {
	now := time.Now().UTC()
	anchor := now.Add(-90 * time.Minute)
	w, cancel := buildProduceTestWorld(t, anchor, []sim.RestockEntry{
		{Item: "stew", Source: sim.RestockSourceProduce, Max: 5},
	}, map[sim.ItemKind]int{"vegetable": 1, "water": 1})
	defer cancel()

	res, _ := w.Send(sim.ApplyProduceTick(now))
	r := res.(sim.ProduceTickResult)
	if r.Executions != 1 {
		t.Errorf("Executions = %d, want 1", r.Executions)
	}

	inv, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return map[string]int{
				"stew":      world.Actors["hannah"].Inventory["stew"],
				"vegetable": world.Actors["hannah"].Inventory["vegetable"],
				"water":     world.Actors["hannah"].Inventory["water"],
			}, nil
		},
	})
	got := inv.(map[string]int)
	if got["stew"] != 1 {
		t.Errorf("stew = %d, want 1", got["stew"])
	}
	if got["vegetable"] != 0 || got["water"] != 0 {
		t.Errorf("inputs not consumed cleanly: %v", got)
	}
}

// TestApplyProduceTickSkipsOnInputShortage covers design decision #7 —
// missing input means skip entirely, anchor doesn't advance.
func TestApplyProduceTickSkipsOnInputShortage(t *testing.T) {
	now := time.Now().UTC()
	anchor := now.Add(-90 * time.Minute)
	w, cancel := buildProduceTestWorld(t, anchor, []sim.RestockEntry{
		{Item: "stew", Source: sim.RestockSourceProduce, Max: 5},
	}, map[sim.ItemKind]int{"vegetable": 1}) // no water
	defer cancel()

	res, _ := w.Send(sim.ApplyProduceTick(now))
	if res.(sim.ProduceTickResult).Executions != 0 {
		t.Errorf("Executions = %d, want 0 (input short)", res.(sim.ProduceTickResult).Executions)
	}
	// Anchor should NOT have advanced.
	state, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return world.Actors["hannah"].ProduceState["stew"].LastProducedAt, nil
		},
	})
	if !state.(time.Time).Equal(anchor) {
		t.Errorf("anchor advanced despite skip: %v vs %v", state.(time.Time), anchor)
	}
	// Vegetable preserved (no partial consumption).
	veg, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return world.Actors["hannah"].Inventory["vegetable"], nil
		},
	})
	if veg.(int) != 1 {
		t.Errorf("vegetable consumed despite skip: %d", veg.(int))
	}
}

// TestApplyProduceTickGateOffShift covers the gate — actor NOT inside
// their work structure → no production.
func TestApplyProduceTickGateOffShift(t *testing.T) {
	now := time.Now().UTC()
	anchor := now.Add(-90 * time.Minute)
	w, cancel := buildProduceTestWorld(t, anchor, []sim.RestockEntry{
		{Item: "bread", Source: sim.RestockSourceProduce, Max: 10},
	}, map[sim.ItemKind]int{})
	defer cancel()

	// Move hannah away from work.
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			sim.SetActorInsideStructure(world, world.Actors["hannah"], "tavern")
			return nil, nil
		},
	})

	res, _ := w.Send(sim.ApplyProduceTick(now))
	if res.(sim.ProduceTickResult).Executions != 0 {
		t.Errorf("Executions = %d off-shift, want 0", res.(sim.ProduceTickResult).Executions)
	}
}

// TestApplyProduceTickGateSleeping covers the sleep gate.
func TestApplyProduceTickGateSleeping(t *testing.T) {
	now := time.Now().UTC()
	anchor := now.Add(-90 * time.Minute)
	w, cancel := buildProduceTestWorld(t, anchor, []sim.RestockEntry{
		{Item: "bread", Source: sim.RestockSourceProduce, Max: 10},
	}, map[sim.ItemKind]int{})
	defer cancel()

	// Mark sleeping until 1h in the future.
	future := now.Add(1 * time.Hour)
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Actors["hannah"].SleepingUntil = &future
			return nil, nil
		},
	})

	res, _ := w.Send(sim.ApplyProduceTick(now))
	if res.(sim.ProduceTickResult).Executions != 0 {
		t.Errorf("Executions = %d while sleeping, want 0", res.(sim.ProduceTickResult).Executions)
	}
}

// TestApplyProduceTickCapAdvancesAnchor covers the at-cap case: anchor
// jumps to now (no back-credit) when there's no headroom.
func TestApplyProduceTickCapAdvancesAnchor(t *testing.T) {
	now := time.Now().UTC()
	anchor := now.Add(-90 * time.Minute)
	w, cancel := buildProduceTestWorld(t, anchor, []sim.RestockEntry{
		{Item: "bread", Source: sim.RestockSourceProduce, Max: 5},
	}, map[sim.ItemKind]int{"bread": 5}) // already at cap
	defer cancel()

	res, _ := w.Send(sim.ApplyProduceTick(now))
	if res.(sim.ProduceTickResult).Executions != 0 {
		t.Errorf("Executions = %d at cap, want 0", res.(sim.ProduceTickResult).Executions)
	}
	// Anchor advanced to now (no back-credit).
	state, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return world.Actors["hannah"].ProduceState["bread"].LastProducedAt, nil
		},
	})
	if !state.(time.Time).Equal(now) {
		t.Errorf("anchor at cap = %v, want %v (now)", state.(time.Time), now)
	}
}

// TestApplyProduceTickFirstObservation covers the no-prior-state case:
// first encounter stamps the anchor without producing.
func TestApplyProduceTickFirstObservation(t *testing.T) {
	now := time.Now().UTC()
	repo, handles := mem.NewRepository()
	handles.Recipes.Seed(map[sim.ItemKind]*sim.ItemRecipe{
		"bread": {OutputItem: "bread", OutputQty: 2, RateQty: 2, RatePerHours: 1},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {
			ID:                "hannah",
			LLMAgent:          "hannah-innkeeper",
			InsideStructureID: "inn",
			WorkStructureID:   "inn",
			Inventory:         map[sim.ItemKind]int{},
			// No ProduceState seeded — should be initialized on first tick.
			RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
				{Item: "bread", Source: sim.RestockSourceProduce, Max: 10},
			}},
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	res, _ := w.Send(sim.ApplyProduceTick(now))
	if res.(sim.ProduceTickResult).Executions != 0 {
		t.Errorf("first observation Executions = %d, want 0 (anchor stamp only)", res.(sim.ProduceTickResult).Executions)
	}
	// State now exists with anchor=now.
	state, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return world.Actors["hannah"].ProduceState["bread"].LastProducedAt, nil
		},
	})
	if !state.(time.Time).Equal(now) {
		t.Errorf("first-pass anchor = %v, want %v", state.(time.Time), now)
	}
}

// TestApplyProduceTickAdvancesByExactConsumedTime covers anchor math:
// bread, 90 min elapsed, output_qty=2, seconds_per_unit=1800. 1
// execution consumes 2 units * 1800 = 3600s, so anchor advances by
// exactly 1h and the remaining 30 min residual stays.
func TestApplyProduceTickAdvancesByExactConsumedTime(t *testing.T) {
	now := time.Now().UTC()
	anchor := now.Add(-90 * time.Minute)
	w, cancel := buildProduceTestWorld(t, anchor, []sim.RestockEntry{
		{Item: "bread", Source: sim.RestockSourceProduce, Max: 10},
	}, map[sim.ItemKind]int{})
	defer cancel()

	_, _ = w.Send(sim.ApplyProduceTick(now))

	state, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return world.Actors["hannah"].ProduceState["bread"].LastProducedAt, nil
		},
	})
	wantAnchor := anchor.Add(1 * time.Hour) // exactly one execution worth
	if !state.(time.Time).Equal(wantAnchor) {
		t.Errorf("anchor advanced to %v, want %v (residual 30 min preserved)",
			state.(time.Time), wantAnchor)
	}
}

// TestRestockEntryCap covers the Max/Target preference.
func TestRestockEntryCap(t *testing.T) {
	cases := []struct {
		max, target, want int
	}{
		{10, 0, 10},
		{0, 8, 8},
		{10, 8, 10}, // Max wins
		{0, 0, 0},
	}
	for _, c := range cases {
		e := sim.RestockEntry{Max: c.max, Target: c.target}
		if got := e.Cap(); got != c.want {
			t.Errorf("Cap(max=%d target=%d) = %d, want %d", c.max, c.target, got, c.want)
		}
	}
}

// TestRestockPolicyProduceEntriesFilters covers the filter helper.
func TestRestockPolicyProduceEntriesFilters(t *testing.T) {
	p := &sim.RestockPolicy{Restock: []sim.RestockEntry{
		{Item: "bread", Source: sim.RestockSourceProduce},
		{Item: "ale", Source: sim.RestockSourceBuy},
		{Item: "stew", Source: sim.RestockSourceProduce},
	}}
	got := p.ProduceEntries()
	if len(got) != 2 {
		t.Fatalf("ProduceEntries count = %d, want 2", len(got))
	}
	if got[0].Item != "bread" || got[1].Item != "stew" {
		t.Errorf("ProduceEntries = %+v, want [bread, stew]", got)
	}
}
