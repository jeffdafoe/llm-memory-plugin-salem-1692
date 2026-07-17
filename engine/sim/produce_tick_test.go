package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// produce_tick_test.go — LLM-319 one-shot production cycle mechanics. The tick
// is a progress/landing resolver: nothing is made without a produce call
// (StartProductionCycle), progress accrues only at-post + awake + not-degraded
// (pause, never cancel), and a due cycle lands exactly one batch then stops.

// buildCycleTestWorld seeds two recipes and an innkeeper actor at work.
// Recipes: stew (1 per 1h, inputs vegetable+water — cycle 3600s) and bread
// (batch of 2 per 1h, no inputs — secondsPerUnit 1800, cycle 3600s).
func buildCycleTestWorld(t *testing.T, restock []sim.RestockEntry, inv map[sim.ItemKind]int) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		"stew":  {Name: "stew", DisplayLabel: "Stew", DisplayLabelSingular: "bowl of stew", DisplayLabelPlural: "stew", Category: sim.ItemCategoryFood, SortOrder: 100},
		"bread": {Name: "bread", DisplayLabel: "Bread", DisplayLabelSingular: "loaf of bread", DisplayLabelPlural: "loaves of bread", Category: sim.ItemCategoryFood, SortOrder: 110},
	})
	handles.Recipes.Seed(map[sim.ItemKind]*sim.ItemRecipe{
		"stew": {
			OutputItem:     "stew",
			OutputQty:      1,
			RateQty:        1,
			RatePerHours:   1,
			Inputs:         []sim.RecipeInput{{Item: "vegetable", Qty: 1}, {Item: "water", Qty: 1}},
			WholesalePrice: 2,
			RetailPrice:    4,
		},
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

// startCycle starts a production cycle and rewinds its progress anchor to
// `anchor`, so a subsequent ApplyProduceTick(now) sees a deterministic elapsed
// window (StartProductionCycle stamps the anchor with the wall clock).
func startCycle(t *testing.T, w *sim.World, actorID sim.ActorID, item string, anchor time.Time) {
	t.Helper()
	if _, err := w.Send(sim.StartProductionCycle(actorID, item)); err != nil {
		t.Fatalf("StartProductionCycle(%s): %v", item, err)
	}
	rewindProductionAnchor(t, w, actorID, anchor)
}

// rewindProductionAnchor overwrites the in-flight cycle's LastProgressAt.
func rewindProductionAnchor(t *testing.T, w *sim.World, actorID sim.ActorID, anchor time.Time) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors[actorID].ProductionActivity.LastProgressAt = anchor
		return nil, nil
	}}); err != nil {
		t.Fatalf("rewind anchor: %v", err)
	}
}

// productionActivityOf reads the actor's live activity (nil when idle).
func productionActivityOf(t *testing.T, w *sim.World, actorID sim.ActorID) *sim.ProductionActivity {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		if pa := world.Actors[actorID].ProductionActivity; pa != nil {
			cp := *pa
			return &cp, nil
		}
		return (*sim.ProductionActivity)(nil), nil
	}})
	if err != nil {
		t.Fatalf("read activity: %v", err)
	}
	return res.(*sim.ProductionActivity)
}

// (inventoryOf is shared from gather_commands_test.go.)

// TestProduceTickNothingWithoutCycle is the LLM-319 headline: NO good is
// produced without a produce call — the continuous auto-fill is gone for
// single-output producers too. Hannah stands at her post with a producible
// entry and plenty of elapsed time; the tick mints nothing.
func TestProduceTickNothingWithoutCycle(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildCycleTestWorld(t, []sim.RestockEntry{
		{Item: "bread", Source: sim.RestockSourceProduce, Max: 10},
	}, map[sim.ItemKind]int{})
	defer cancel()

	res, err := w.Send(sim.ApplyProduceTick(now.Add(4 * time.Hour)))
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if r := res.(sim.ProduceTickResult); r.Executions != 0 {
		t.Errorf("Executions = %d with no cycle started, want 0 (auto-produce is retired)", r.Executions)
	}
	if got := inventoryOf(t, w, "hannah", "bread"); got != 0 {
		t.Errorf("bread = %d, want 0", got)
	}
}

// TestProduceTickProgressesThenLands drives one full cycle: a bread batch
// (3600s of work) progresses without landing at 30 min, then lands at 70 min —
// +2 bread, window cleared, the landing recorded on the RecentProduce ring and
// the re-decide pacing stamp (ProductionNagAt) set.
func TestProduceTickProgressesThenLands(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildCycleTestWorld(t, []sim.RestockEntry{
		{Item: "bread", Source: sim.RestockSourceProduce, Max: 10},
	}, map[sim.ItemKind]int{})
	defer cancel()

	startCycle(t, w, "hannah", "bread", now.Add(-30*time.Minute))

	// 30 minutes of the 60-minute cycle done — no landing yet.
	res, err := w.Send(sim.ApplyProduceTick(now))
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if r := res.(sim.ProduceTickResult); r.Executions != 0 {
		t.Fatalf("Executions = %d at half-cycle, want 0", r.Executions)
	}
	act := productionActivityOf(t, w, "hannah")
	if act == nil {
		t.Fatalf("activity cleared at half-cycle; want in flight")
	}
	if act.RemainingSeconds != 3600-1800 {
		t.Errorf("RemainingSeconds = %d, want 1800 (30 of 60 min credited)", act.RemainingSeconds)
	}

	// 40 more minutes — the cycle is due; the batch lands.
	later := now.Add(40 * time.Minute)
	res, err = w.Send(sim.ApplyProduceTick(later))
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	r := res.(sim.ProduceTickResult)
	if r.Executions != 1 {
		t.Fatalf("Executions = %d at cycle end, want 1", r.Executions)
	}
	if got := inventoryOf(t, w, "hannah", "bread"); got != 2 {
		t.Errorf("bread = %d, want 2 (one batch of OutputQty 2)", got)
	}
	if productionActivityOf(t, w, "hannah") != nil {
		t.Errorf("activity still set after landing; want nil (one cycle per call)")
	}
	stamped, _ := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["hannah"]
		return [2]bool{len(a.RecentProduce) == 1, a.ProductionNagAt.Equal(later)}, nil
	}})
	got := stamped.([2]bool)
	if !got[0] {
		t.Errorf("RecentProduce ring not recorded on landing")
	}
	if !got[1] {
		t.Errorf("ProductionNagAt not stamped on landing (the completion beat is the wake)")
	}
}

// TestProduceTickLandsExactlyOneBatch — one call, ONE batch: after the landing,
// further elapsed time mints nothing until produce is called again.
func TestProduceTickLandsExactlyOneBatch(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildCycleTestWorld(t, []sim.RestockEntry{
		{Item: "bread", Source: sim.RestockSourceProduce, Max: 10},
	}, map[sim.ItemKind]int{})
	defer cancel()

	startCycle(t, w, "hannah", "bread", now.Add(-2*time.Hour))
	if _, err := w.Send(sim.ApplyProduceTick(now)); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if got := inventoryOf(t, w, "hannah", "bread"); got != 2 {
		t.Fatalf("bread = %d after landing, want 2", got)
	}

	// Hours pass; no new cycle was started — nothing more is made.
	res, err := w.Send(sim.ApplyProduceTick(now.Add(6 * time.Hour)))
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if r := res.(sim.ProduceTickResult); r.Executions != 0 {
		t.Errorf("Executions = %d after the batch landed, want 0 (no continuation)", r.Executions)
	}
	if got := inventoryOf(t, w, "hannah", "bread"); got != 2 {
		t.Errorf("bread = %d, want still 2", got)
	}
}

// TestProduceTickPausesAwayFromPost — leaving the post PAUSES the batch: the
// away tick discards the elapsed time (no credit, no cancel), and the batch
// resumes when the actor is back.
func TestProduceTickPausesAwayFromPost(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildCycleTestWorld(t, []sim.RestockEntry{
		{Item: "bread", Source: sim.RestockSourceProduce, Max: 10},
	}, map[sim.ItemKind]int{})
	defer cancel()

	startCycle(t, w, "hannah", "bread", now.Add(-45*time.Minute))
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.SetActorInsideStructure(world, world.Actors["hannah"], "tavern")
		return nil, nil
	}}); err != nil {
		t.Fatalf("move: %v", err)
	}

	if _, err := w.Send(sim.ApplyProduceTick(now)); err != nil {
		t.Fatalf("tick: %v", err)
	}
	act := productionActivityOf(t, w, "hannah")
	if act == nil {
		t.Fatalf("activity cancelled by leaving the post; want paused")
	}
	if act.RemainingSeconds != 3600 {
		t.Errorf("RemainingSeconds = %d after away tick, want 3600 (away time discarded)", act.RemainingSeconds)
	}
	if !act.LastProgressAt.Equal(now) {
		t.Errorf("anchor = %v after away tick, want %v (advanced so the away time never banks)", act.LastProgressAt, now)
	}

	// Back at the post: a full cycle of at-post time lands the batch.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.SetActorInsideStructure(world, world.Actors["hannah"], "inn")
		return nil, nil
	}}); err != nil {
		t.Fatalf("move back: %v", err)
	}
	rewindProductionAnchor(t, w, "hannah", now.Add(-61*time.Minute))
	if _, err := w.Send(sim.ApplyProduceTick(now)); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if got := inventoryOf(t, w, "hannah", "bread"); got != 2 {
		t.Errorf("bread = %d after resumed cycle, want 2", got)
	}
}

// TestProduceTickPausesWhileSleeping — the awake gate pauses progress (no
// overnight free goods for an innkeeper sleeping at her workplace).
func TestProduceTickPausesWhileSleeping(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildCycleTestWorld(t, []sim.RestockEntry{
		{Item: "bread", Source: sim.RestockSourceProduce, Max: 10},
	}, map[sim.ItemKind]int{})
	defer cancel()

	startCycle(t, w, "hannah", "bread", now.Add(-2*time.Hour))
	future := now.Add(time.Hour)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["hannah"].SleepingUntil = &future
		return nil, nil
	}}); err != nil {
		t.Fatalf("sleep: %v", err)
	}

	if _, err := w.Send(sim.ApplyProduceTick(now)); err != nil {
		t.Fatalf("tick: %v", err)
	}
	act := productionActivityOf(t, w, "hannah")
	if act == nil || act.RemainingSeconds != 3600 {
		t.Errorf("sleeping tick credited work (activity=%+v); want untouched 3600s", act)
	}
}

// TestProduceTickPausesWhileDegraded — the LLM-304 legacy full block, kept
// reachable at StallDegradedProducePct == 0 (LLM-446): the batch pauses until
// the owner mends the business.
func TestProduceTickPausesWhileDegraded(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildCycleTestWorld(t, []sim.RestockEntry{
		{Item: "bread", Source: sim.RestockSourceProduce, Max: 10},
	}, map[sim.ItemKind]int{})
	defer cancel()

	startCycle(t, w, "hannah", "bread", now.Add(-2*time.Hour))
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.StallWearDegradeThreshold = 600
		world.Settings.StallDegradedProducePct = 0 // explicit: the legacy full-block mode
		if world.VillageObjects == nil {
			world.VillageObjects = map[sim.VillageObjectID]*sim.VillageObject{}
		}
		world.VillageObjects["inn"] = &sim.VillageObject{
			ID: "inn", OwnerActorID: "hannah", Tags: []string{sim.TagBusiness}, Wear: 650,
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if _, err := w.Send(sim.ApplyProduceTick(now)); err != nil {
		t.Fatalf("tick: %v", err)
	}
	act := productionActivityOf(t, w, "hannah")
	if act == nil || act.RemainingSeconds != 3600 {
		t.Errorf("degraded-business tick credited work (activity=%+v); want untouched 3600s", act)
	}
	if got := inventoryOf(t, w, "hannah", "bread"); got != 0 {
		t.Errorf("bread = %d while degraded, want 0", got)
	}
}

// TestProduceTickDegradedSlowsToPct — LLM-446: at a positive
// StallDegradedProducePct a degraded business still advances its batch, at the
// sapped rate (the way out of the sole-nail-producer self-repair deadlock: the
// smith limps, he doesn't stop). An hour at the post under pct 50 credits half
// an hour of work.
func TestProduceTickDegradedSlowsToPct(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildCycleTestWorld(t, []sim.RestockEntry{
		{Item: "bread", Source: sim.RestockSourceProduce, Max: 10},
	}, map[sim.ItemKind]int{})
	defer cancel()

	startCycle(t, w, "hannah", "bread", now.Add(-time.Hour))
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.StallWearDegradeThreshold = 600
		world.Settings.StallDegradedProducePct = 50
		if world.VillageObjects == nil {
			world.VillageObjects = map[sim.VillageObjectID]*sim.VillageObject{}
		}
		world.VillageObjects["inn"] = &sim.VillageObject{
			ID: "inn", OwnerActorID: "hannah", Tags: []string{sim.TagBusiness}, Wear: 650,
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if _, err := w.Send(sim.ApplyProduceTick(now)); err != nil {
		t.Fatalf("tick: %v", err)
	}
	act := productionActivityOf(t, w, "hannah")
	if act == nil || act.RemainingSeconds != 1800 {
		t.Errorf("degraded tick at pct 50 credited wrong work (activity=%+v); want 3600s elapsed -> 1800s credited -> 1800s remaining", act)
	}
}

// TestProduceTickZeroAnchorStampsWithoutCredit — the post-restart posture: the
// pg loader leaves LastProgressAt zero, so the first tick stamps the anchor
// without crediting and engine downtime never counts as work.
func TestProduceTickZeroAnchorStampsWithoutCredit(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildCycleTestWorld(t, []sim.RestockEntry{
		{Item: "bread", Source: sim.RestockSourceProduce, Max: 10},
	}, map[sim.ItemKind]int{})
	defer cancel()

	// Simulate the loader's rehydrated window: fields set, anchor zero.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["hannah"].ProductionActivity = &sim.ProductionActivity{
			Item: "bread", BatchQty: 2, RemainingSeconds: 1800,
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed activity: %v", err)
	}

	if _, err := w.Send(sim.ApplyProduceTick(now)); err != nil {
		t.Fatalf("tick: %v", err)
	}
	act := productionActivityOf(t, w, "hannah")
	if act == nil {
		t.Fatalf("activity gone after first post-restart tick")
	}
	if act.RemainingSeconds != 1800 {
		t.Errorf("RemainingSeconds = %d, want untouched 1800 (zero anchor stamps only)", act.RemainingSeconds)
	}
	if !act.LastProgressAt.Equal(now) {
		t.Errorf("anchor = %v, want %v", act.LastProgressAt, now)
	}

	// The second tick credits normally from the stamped anchor.
	later := now.Add(31 * time.Minute)
	if _, err := w.Send(sim.ApplyProduceTick(later)); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if got := inventoryOf(t, w, "hannah", "bread"); got != 2 {
		t.Errorf("bread = %d after resumed remainder, want 2", got)
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
