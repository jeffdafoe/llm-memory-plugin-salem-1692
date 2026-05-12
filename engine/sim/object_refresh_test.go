package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// helpers — convert literal values to pointers for the optional fields.
func ip(v int) *int             { return &v }
func tp(t time.Time) *time.Time { return &t }

// buildRefreshTestWorld seeds a fixture with three objects:
//   - well: thirst refresh, finite supply (available=3 of max=5),
//     continuous regen 24h, dwell config (thirst -2 every 30 min)
//   - oak:  multi-attribute (tiredness, hunger), infinite supply
//   - dry_bush: hunger refresh, depleted (available=0)
//
// Plus one actor at the world origin with mid-range needs.
func buildRefreshTestWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	wellLastRefresh := time.Now().UTC().Add(-2 * time.Hour) // 2h ago
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"well": {
			ID: "well", AssetID: "well-stone", CurrentState: "default",
			X: 100, Y: 100,
			Refreshes: []*sim.ObjectRefresh{
				{
					Attribute:          "thirst",
					Amount:             -12,
					AvailableQuantity:  ip(3),
					MaxQuantity:        ip(5),
					RefreshMode:        sim.RefreshModeContinuous,
					RefreshPeriodHours: ip(24),
					LastRefreshAt:      tp(wellLastRefresh),
					DwellDelta:         ip(-2),
					DwellPeriodMinutes: ip(30),
				},
			},
		},
		"oak": {
			ID: "oak", AssetID: "tree-oak", CurrentState: "default",
			X: 500, Y: 500,
			Refreshes: []*sim.ObjectRefresh{
				{Attribute: "tiredness", Amount: -8},
				{Attribute: "hunger", Amount: -4}, // acorns; infinite (no AvailableQuantity)
			},
		},
		"dry_bush": {
			ID: "dry_bush", AssetID: "bush-berries", CurrentState: "default",
			X: 1000, Y: 1000,
			Refreshes: []*sim.ObjectRefresh{
				{
					Attribute:          "hunger",
					Amount:             -8,
					AvailableQuantity:  ip(0), // depleted
					MaxQuantity:        ip(4),
					RefreshMode:        sim.RefreshModePeriodic,
					RefreshPeriodHours: ip(8),
				},
			},
		},
		// Decorative object — no refreshes, never targeted.
		"bench": {ID: "bench", AssetID: "bench-wood", CurrentState: "default", X: 100, Y: 100},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {
			ID:       "hannah",
			LLMAgent: "hannah-innkeeper",
			Needs:    map[sim.NeedKey]int{"hunger": 10, "thirst": 18, "tiredness": 14},
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

// TestApplyObjectRefreshAtArrivalWell covers the happy path: actor arrives
// at well, thirst drops, supply decrements by one, dwell credit stamped.
func TestApplyObjectRefreshAtArrivalWell(t *testing.T) {
	w, cancel := buildRefreshTestWorld(t)
	defer cancel()

	res, err := w.Send(sim.ApplyObjectRefreshAtArrival("hannah", 110, 110))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	r := res.(sim.ArrivalRefreshResult)
	if r.ObjectID != "well" {
		t.Errorf("resolved object = %q, want well", r.ObjectID)
	}
	if len(r.Hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(r.Hits))
	}
	hit := r.Hits[0]
	if hit.Attribute != "thirst" || hit.Amount != -12 {
		t.Errorf("hit = %+v, want {thirst, -12}", hit)
	}
	if hit.NewValue != 6 {
		t.Errorf("post-clamp thirst = %d, want 6 (18 - 12)", hit.NewValue)
	}

	snap := w.Published()
	actor := snap.Actors["hannah"]
	if actor.Needs["thirst"] != 6 {
		t.Errorf("snap thirst = %d, want 6", actor.Needs["thirst"])
	}

	// Supply decremented from 3 to 2 — read via a snapshot command.
	avail, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return *world.VillageObjects["well"].Refreshes[0].AvailableQuantity, nil
		},
	})
	if avail.(int) != 2 {
		t.Errorf("well supply = %d, want 2", avail.(int))
	}

	// Dwell credit stamped.
	dwell, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			key := sim.DwellCreditKey{ObjectID: "well", Attribute: "thirst", Source: sim.DwellSourceObject}
			return world.Actors["hannah"].DwellCredits[key], nil
		},
	})
	dc := dwell.(*sim.DwellCredit)
	if dc == nil {
		t.Fatal("expected dwell credit row")
	}
	if dc.DwellDelta != -2 || dc.DwellPeriodMinutes != 30 {
		t.Errorf("dwell credit = %+v, want delta=-2 period=30", dc)
	}
	if dc.RemainingTicks != nil {
		t.Errorf("source=object RemainingTicks = %v, want nil", *dc.RemainingTicks)
	}
}

// TestApplyObjectRefreshAtArrivalMultiAttribute covers the oak case — one
// arrival produces two hits.
func TestApplyObjectRefreshAtArrivalMultiAttribute(t *testing.T) {
	w, cancel := buildRefreshTestWorld(t)
	defer cancel()

	res, _ := w.Send(sim.ApplyObjectRefreshAtArrival("hannah", 510, 510))
	r := res.(sim.ArrivalRefreshResult)
	if r.ObjectID != "oak" {
		t.Errorf("resolved object = %q, want oak", r.ObjectID)
	}
	if len(r.Hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(r.Hits))
	}

	snap := w.Published().Actors["hannah"]
	if snap.Needs["tiredness"] != 6 { // 14 - 8
		t.Errorf("tiredness = %d, want 6", snap.Needs["tiredness"])
	}
	if snap.Needs["hunger"] != 6 { // 10 - 4
		t.Errorf("hunger = %d, want 6", snap.Needs["hunger"])
	}
}

// TestApplyObjectRefreshAtArrivalDepletedSkipped covers the dry-well case.
func TestApplyObjectRefreshAtArrivalDepletedSkipped(t *testing.T) {
	w, cancel := buildRefreshTestWorld(t)
	defer cancel()

	res, _ := w.Send(sim.ApplyObjectRefreshAtArrival("hannah", 1010, 1010))
	r := res.(sim.ArrivalRefreshResult)
	if len(r.Hits) != 0 {
		t.Errorf("dry bush produced hits: %+v, want empty", r.Hits)
	}
	snap := w.Published().Actors["hannah"]
	if snap.Needs["hunger"] != 10 {
		t.Errorf("hunger changed: %d, want 10 (unchanged)", snap.Needs["hunger"])
	}
}

// TestApplyObjectRefreshAtArrivalOutOfRange covers the "no refresh object
// nearby" no-op.
func TestApplyObjectRefreshAtArrivalOutOfRange(t *testing.T) {
	w, cancel := buildRefreshTestWorld(t)
	defer cancel()

	res, _ := w.Send(sim.ApplyObjectRefreshAtArrival("hannah", 5000, 5000))
	r := res.(sim.ArrivalRefreshResult)
	if r.ObjectID != "" || len(r.Hits) != 0 {
		t.Errorf("out-of-range hit something: %+v", r)
	}
}

// TestApplyObjectRefreshAtArrivalUnknownActor covers the error branch.
func TestApplyObjectRefreshAtArrivalUnknownActor(t *testing.T) {
	w, cancel := buildRefreshTestWorld(t)
	defer cancel()

	_, err := w.Send(sim.ApplyObjectRefreshAtArrival("ghost", 100, 100))
	if err == nil {
		t.Fatal("expected error for unknown actor")
	}
}

// TestApplyObjectRefreshAtArrivalIgnoresBareObjects covers the "bench
// has no refresh rows" pass-through — even though it's at the same
// position as the well, the well wins because the bench has empty
// Refreshes. (The bench would be invisible to refresh either way.)
func TestApplyObjectRefreshAtArrivalIgnoresBareObjects(t *testing.T) {
	w, cancel := buildRefreshTestWorld(t)
	defer cancel()

	res, _ := w.Send(sim.ApplyObjectRefreshAtArrival("hannah", 100, 100))
	r := res.(sim.ArrivalRefreshResult)
	if r.ObjectID != "well" {
		t.Errorf("resolved object = %q, want well (bench has no refreshes)", r.ObjectID)
	}
}

// TestRegenObjectRefreshContinuous covers a continuous-mode regen step.
// Well: max=5, available=3, period=24h, last_refresh=2h ago. With unit
// time = 24/5 = 4.8h per unit, 2h elapsed → 0 units accrued. Run again
// after another 3h (5h total) → 1 unit (5h/4.8h = 1).
func TestRegenObjectRefreshContinuous(t *testing.T) {
	w, cancel := buildRefreshTestWorld(t)
	defer cancel()

	// Step 1: regen at +0 (already 2h elapsed in fixture). Should accrue 0.
	step1, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return sim.RegenObjectRefresh(world, time.Now().UTC()), nil
		},
	})
	if step1.(int) != 0 {
		t.Errorf("regen at 2h: touched = %d, want 0 (< unit time 4.8h)", step1.(int))
	}

	// Step 2: regen at +3h beyond fixture (5h total elapsed). Should accrue 1.
	step2, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			future := time.Now().UTC().Add(3 * time.Hour)
			return sim.RegenObjectRefresh(world, future), nil
		},
	})
	if step2.(int) != 1 {
		t.Errorf("regen at 5h: touched = %d, want 1", step2.(int))
	}
	avail, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return *world.VillageObjects["well"].Refreshes[0].AvailableQuantity, nil
		},
	})
	if avail.(int) != 4 {
		t.Errorf("well supply = %d, want 4 (3 + 1)", avail.(int))
	}
}

// TestRegenObjectRefreshPeriodic covers a periodic-mode regen — jumps to
// MaxQuantity once period elapses.
func TestRegenObjectRefreshPeriodic(t *testing.T) {
	repo, handles := mem.NewRepository()
	last := time.Now().UTC().Add(-9 * time.Hour) // 9h ago, period 8h
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"field": {
			ID: "field", AssetID: "field-wheat", CurrentState: "default",
			X: 0, Y: 0,
			Refreshes: []*sim.ObjectRefresh{
				{
					Attribute:          "hunger",
					Amount:             -10,
					AvailableQuantity:  ip(0),
					MaxQuantity:        ip(4),
					RefreshMode:        sim.RefreshModePeriodic,
					RefreshPeriodHours: ip(8),
					LastRefreshAt:      tp(last),
				},
			},
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	touched, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return sim.RegenObjectRefresh(world, time.Now().UTC()), nil
		},
	})
	if touched.(int) != 1 {
		t.Errorf("periodic regen touched = %d, want 1", touched.(int))
	}
	avail, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return *world.VillageObjects["field"].Refreshes[0].AvailableQuantity, nil
		},
	})
	if avail.(int) != 4 {
		t.Errorf("field supply = %d, want 4 (jumped to max)", avail.(int))
	}
}

// TestRegenObjectRefreshFirstPassStampsAnchor covers the freshly-loaded
// case: nil LastRefreshAt gets stamped on this pass with no regen.
func TestRegenObjectRefreshFirstPassStampsAnchor(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"new_well": {
			ID: "new_well", AssetID: "well-stone", CurrentState: "default",
			X: 0, Y: 0,
			Refreshes: []*sim.ObjectRefresh{
				{
					Attribute:          "thirst",
					Amount:             -12,
					AvailableQuantity:  ip(2),
					MaxQuantity:        ip(5),
					RefreshMode:        sim.RefreshModeContinuous,
					RefreshPeriodHours: ip(24),
					LastRefreshAt:      nil, // fresh, never regenerated
				},
			},
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	now := time.Now().UTC()
	touched, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return sim.RegenObjectRefresh(world, now), nil
		},
	})
	if touched.(int) != 0 {
		t.Errorf("first pass touched = %d, want 0 (anchor stamp only)", touched.(int))
	}
	anchor, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return world.VillageObjects["new_well"].Refreshes[0].LastRefreshAt, nil
		},
	})
	if anchor.(*time.Time) == nil {
		t.Fatal("expected anchor to be stamped")
	}
	if !anchor.(*time.Time).Equal(now) {
		t.Errorf("anchor = %v, want %v", *anchor.(*time.Time), now)
	}
}

// TestRegenObjectRefreshClampsAtMax covers the upper-bound clamp.
func TestRegenObjectRefreshClampsAtMax(t *testing.T) {
	repo, handles := mem.NewRepository()
	last := time.Now().UTC().Add(-72 * time.Hour) // way past period
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"bush": {
			ID: "bush", AssetID: "bush-berries", CurrentState: "default",
			Refreshes: []*sim.ObjectRefresh{
				{
					Attribute:          "hunger",
					Amount:             -4,
					AvailableQuantity:  ip(1),
					MaxQuantity:        ip(4),
					RefreshMode:        sim.RefreshModeContinuous,
					RefreshPeriodHours: ip(8),
					LastRefreshAt:      tp(last),
				},
			},
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return sim.RegenObjectRefresh(world, time.Now().UTC()), nil
		},
	})
	avail, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return *world.VillageObjects["bush"].Refreshes[0].AvailableQuantity, nil
		},
	})
	if avail.(int) != 4 {
		t.Errorf("over-period regen clamp = %d, want 4 (MaxQuantity)", avail.(int))
	}
}
