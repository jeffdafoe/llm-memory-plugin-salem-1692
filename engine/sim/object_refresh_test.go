package sim_test

import (
	"context"
	"fmt"
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
//   - sell_bush: yield-only gather (amount=0, gather_item=berries), finite
//     supply — the forage-to-sell row (LLM-24)
//
// Plus one actor at the world origin with mid-range needs.
func buildRefreshTestWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	wellLastRefresh := time.Now().UTC().Add(-2 * time.Hour) // 2h ago
	// resolveLoiteringObject (the arrival resolver) only considers NAMED
	// objects with a resolvable asset, and measures Chebyshev tiles to the
	// loiter pin. Seed the assets, name each refresh object, and give it a
	// zero loiter offset so its pin lands on its anchor tile — a test then
	// places the actor there with placeAtObjectPin.
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"well-stone":   {ID: "well-stone"},
		"tree-oak":     {ID: "tree-oak"},
		"bush-berries": {ID: "bush-berries"},
	})
	zero := 0
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"well": {
			ID: "well", DisplayName: "Well", AssetID: "well-stone", CurrentState: "default",
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
			Pos: sim.WorldPos{X: 100, Y: 100},
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
			ID: "oak", DisplayName: "Oak", AssetID: "tree-oak", CurrentState: "default",
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
			Pos: sim.WorldPos{X: 500, Y: 500},
			Refreshes: []*sim.ObjectRefresh{
				{Attribute: "tiredness", Amount: -8},
				{Attribute: "hunger", Amount: -4}, // acorns; infinite (no AvailableQuantity)
			},
		},
		"dry_bush": {
			ID: "dry_bush", DisplayName: "Berry Bush", AssetID: "bush-berries", CurrentState: "default",
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
			Pos: sim.WorldPos{X: 1000, Y: 1000},
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
		"sell_bush": {
			ID: "sell_bush", DisplayName: "Berry Patch", AssetID: "bush-berries", CurrentState: "default",
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
			Pos: sim.WorldPos{X: 1500, Y: 1500},
			Refreshes: []*sim.ObjectRefresh{
				{
					Attribute:          "hunger",
					Amount:             0, // yield-only: forage-to-sell, no consume-in-place need
					AvailableQuantity:  ip(3),
					MaxQuantity:        ip(3), // full at seed → continuous regen skips it (keeps the regen-count tests stable)
					RefreshMode:        sim.RefreshModeContinuous,
					RefreshPeriodHours: ip(6),
					LastRefreshAt:      tp(time.Now().UTC()),
					GatherItem:         "berries",
				},
			},
		},
		// Decorative object — no refreshes, never targeted.
		"bench": {ID: "bench", AssetID: "bench-wood", CurrentState: "default", Pos: sim.WorldPos{X: 100, Y: 100}},
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

// placeAtObjectPin moves the actor onto objID's loiter pin so an arrival
// resolves to it. The fixture gives each object a zero loiter offset, so the
// pin is the object's anchor tile (obj.Pos.Tile()); standing there puts the
// actor at Chebyshev 0 from the pin, inside LoiterAttributionTiles.
func placeAtObjectPin(t *testing.T, w *sim.World, actorID sim.ActorID, objID sim.VillageObjectID) {
	t.Helper()
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		obj := world.VillageObjects[objID]
		if obj == nil {
			return nil, fmt.Errorf("placeAtObjectPin: no object %q", objID)
		}
		actor := world.Actors[actorID]
		if actor == nil {
			return nil, fmt.Errorf("placeAtObjectPin: no actor %q", actorID)
		}
		actor.Pos = obj.Pos.Tile()
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("placeAtObjectPin: %v", err)
	}
}

// TestApplyObjectRefreshAtArrivalOwnedByOtherSkipped — LLM-50 D2: a non-owner
// arriving at an OWNED eat-in-place source gets no need drop, no hits, and no
// supply decrement. (The well is owned by someone other than hannah.)
func TestApplyObjectRefreshAtArrivalOwnedByOtherSkipped(t *testing.T) {
	w, cancel := buildRefreshTestWorld(t)
	defer cancel()

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.VillageObjects["well"].OwnerActorID = "prudence"
		return nil, nil
	}}); err != nil {
		t.Fatalf("set owner: %v", err)
	}

	placeAtObjectPin(t, w, "hannah", "well")
	res, err := w.Send(sim.ApplyObjectRefreshAtArrival("hannah"))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if r := res.(sim.ArrivalRefreshResult); len(r.Hits) != 0 {
		t.Errorf("owned-by-other well produced hits: %+v, want empty", r.Hits)
	}

	// Thirst unchanged (seeded 18); supply unchanged (seeded 3) — neither the
	// need drop nor the arrival decrement runs for a non-owner.
	if snap := w.Published().Actors["hannah"]; snap.Needs["thirst"] != 18 {
		t.Errorf("thirst=%d, want 18 (unchanged — not the owner)", snap.Needs["thirst"])
	}
	avail, _ := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return *world.VillageObjects["well"].Refreshes[0].AvailableQuantity, nil
	}})
	if avail.(int) != 3 {
		t.Errorf("well supply=%d, want 3 (untouched)", avail.(int))
	}
}

// TestApplyObjectRefreshAtArrivalWell covers the happy path: actor arrives
// at well, thirst drops, supply decrements by one, dwell credit stamped.
func TestApplyObjectRefreshAtArrivalWell(t *testing.T) {
	w, cancel := buildRefreshTestWorld(t)
	defer cancel()

	placeAtObjectPin(t, w, "hannah", "well")
	res, err := w.Send(sim.ApplyObjectRefreshAtArrival("hannah"))
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

	placeAtObjectPin(t, w, "hannah", "oak")
	res, _ := w.Send(sim.ApplyObjectRefreshAtArrival("hannah"))
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

	placeAtObjectPin(t, w, "hannah", "dry_bush")
	res, _ := w.Send(sim.ApplyObjectRefreshAtArrival("hannah"))
	r := res.(sim.ArrivalRefreshResult)
	if len(r.Hits) != 0 {
		t.Errorf("dry bush produced hits: %+v, want empty", r.Hits)
	}
	snap := w.Published().Actors["hannah"]
	if snap.Needs["hunger"] != 10 {
		t.Errorf("hunger changed: %d, want 10 (unchanged)", snap.Needs["hunger"])
	}
}

// TestApplyObjectRefreshAtArrivalYieldOnlySkipped covers the forage-to-sell row
// (LLM-24): arriving at a yield-only bush (amount=0, gatherable) applies NO
// need, stamps NO dwell credit, and does NOT decrement the gather supply — the
// stock is reserved for Gather (and refilled by the regen tick), not consumed
// in place.
func TestApplyObjectRefreshAtArrivalYieldOnlySkipped(t *testing.T) {
	w, cancel := buildRefreshTestWorld(t)
	defer cancel()

	placeAtObjectPin(t, w, "hannah", "sell_bush")
	res, err := w.Send(sim.ApplyObjectRefreshAtArrival("hannah"))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	r := res.(sim.ArrivalRefreshResult)
	if len(r.Hits) != 0 {
		t.Errorf("yield-only bush produced hits: %+v, want empty", r.Hits)
	}

	snap := w.Published().Actors["hannah"]
	if snap.Needs["hunger"] != 10 {
		t.Errorf("hunger changed: %d, want 10 (unchanged — no consume-in-place)", snap.Needs["hunger"])
	}

	// Supply untouched — arrival must not draw down a forage-to-sell row.
	avail, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return *world.VillageObjects["sell_bush"].Refreshes[0].AvailableQuantity, nil
		},
	})
	if avail.(int) != 3 {
		t.Errorf("sell_bush supply = %d, want 3 (unchanged on arrival)", avail.(int))
	}

	// No dwell credit — a yield-only row never stamps the eat-to-recover dwell.
	dwell, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			key := sim.DwellCreditKey{ObjectID: "sell_bush", Attribute: "hunger", Source: sim.DwellSourceObject}
			return world.Actors["hannah"].DwellCredits[key], nil
		},
	})
	if dwell.(*sim.DwellCredit) != nil {
		t.Errorf("yield-only bush stamped a dwell credit: %+v, want none", dwell)
	}
}

// TestObjectRefreshIsYieldOnly locks the mode readout (LLM-24): amount=0 +
// gatherable is yield-only; a need-bearing gather row (amount<0) is not; an
// amount=0 row that isn't gatherable is not (it's a misconfiguration).
func TestObjectRefreshIsYieldOnly(t *testing.T) {
	cases := []struct {
		name string
		row  sim.ObjectRefresh
		want bool
	}{
		{"forage-to-sell", sim.ObjectRefresh{Attribute: "hunger", Amount: 0, GatherItem: "berries"}, true},
		{"eat+pick", sim.ObjectRefresh{Attribute: "hunger", Amount: -8, GatherItem: "berries"}, false},
		{"zero-amount non-gather", sim.ObjectRefresh{Attribute: "hunger", Amount: 0}, false},
		{"need-only", sim.ObjectRefresh{Attribute: "hunger", Amount: -8}, false},
	}
	for _, c := range cases {
		if got := c.row.IsYieldOnly(); got != c.want {
			t.Errorf("%s: IsYieldOnly() = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestValidateObjectRefreshesYieldOnly covers the amount-rule relaxation
// (LLM-24): amount=0 is valid only on a gather source with no dwell; amount>0
// is always invalid; eat+pick (amount<0) stays valid. It also covers the
// nullable-attribute rule (LLM-264): a yield-only row needs no attribute, a
// need-bearing row does, and any attribute that IS present must be a known need.
func TestValidateObjectRefreshesYieldOnly(t *testing.T) {
	ip := func(v int) *int { return &v }
	cases := []struct {
		name    string
		rows    []*sim.ObjectRefresh
		wantErr bool
	}{
		{
			name:    "yield-only valid",
			rows:    []*sim.ObjectRefresh{{Attribute: "hunger", Amount: 0, GatherItem: "berries"}},
			wantErr: false,
		},
		{
			// LLM-264: the clean yield-only shape — no attribute, since the row
			// eases no need.
			name:    "yield-only without attribute valid",
			rows:    []*sim.ObjectRefresh{{Amount: 0, GatherItem: "water"}},
			wantErr: false,
		},
		{
			// LLM-264: a present attribute is still validated, whichever row type.
			name:    "yield-only with unknown attribute rejected",
			rows:    []*sim.ObjectRefresh{{Attribute: "bogus", Amount: 0, GatherItem: "berries"}},
			wantErr: true,
		},
		{
			name:    "eat+pick valid",
			rows:    []*sim.ObjectRefresh{{Attribute: "hunger", Amount: -8, GatherItem: "berries"}},
			wantErr: false,
		},
		{
			// LLM-264: a need-bearing row must still name the need it decrements.
			name:    "need-bearing without attribute rejected",
			rows:    []*sim.ObjectRefresh{{Amount: -8}},
			wantErr: true,
		},
		{
			name:    "zero amount without gather rejected",
			rows:    []*sim.ObjectRefresh{{Attribute: "hunger", Amount: 0}},
			wantErr: true,
		},
		{
			name:    "positive amount rejected",
			rows:    []*sim.ObjectRefresh{{Attribute: "hunger", Amount: 4, GatherItem: "berries"}},
			wantErr: true,
		},
		{
			name: "yield-only with dwell rejected",
			rows: []*sim.ObjectRefresh{{
				Attribute: "hunger", Amount: 0, GatherItem: "berries",
				DwellDelta: ip(-2), DwellPeriodMinutes: ip(30),
			}},
			wantErr: true,
		},
		{
			// LLM-264: two yield-only rows for the same gather item on one object are
			// rejected (mirrors the object_refresh_yield_key partial unique index).
			name: "duplicate yield-only gather item rejected",
			rows: []*sim.ObjectRefresh{
				{Amount: 0, GatherItem: "water"},
				{Amount: 0, GatherItem: "water"},
			},
			wantErr: true,
		},
		{
			// LLM-264: yield-only rows for DIFFERENT items on one object are fine.
			name: "distinct yield-only gather items valid",
			rows: []*sim.ObjectRefresh{
				{Amount: 0, GatherItem: "water"},
				{Amount: 0, GatherItem: "milk"},
			},
			wantErr: false,
		},
	}
	for _, c := range cases {
		err := sim.ValidateObjectRefreshes(c.rows)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err = %v, wantErr = %v", c.name, err, c.wantErr)
		}
	}
}

// TestApplyObjectRefreshAtArrivalOutOfRange covers the "no refresh object
// nearby" no-op.
func TestApplyObjectRefreshAtArrivalOutOfRange(t *testing.T) {
	w, cancel := buildRefreshTestWorld(t)
	defer cancel()

	// Actor stays at the tile origin (0,0) — far outside every object's
	// attribution radius — so nothing resolves.
	res, _ := w.Send(sim.ApplyObjectRefreshAtArrival("hannah"))
	r := res.(sim.ArrivalRefreshResult)
	if r.ObjectID != "" || len(r.Hits) != 0 {
		t.Errorf("out-of-range hit something: %+v", r)
	}
}

// TestApplyObjectRefreshAtArrivalUnknownActor covers the error branch.
func TestApplyObjectRefreshAtArrivalUnknownActor(t *testing.T) {
	w, cancel := buildRefreshTestWorld(t)
	defer cancel()

	_, err := w.Send(sim.ApplyObjectRefreshAtArrival("ghost"))
	if err == nil {
		t.Fatal("expected error for unknown actor")
	}
}

// TestApplyObjectRefreshAtArrivalIgnoresBareObjects covers the decorative-
// object pass-through: the bench sits at the same tile as the well, but it
// is unnamed (and assetless), so resolveLoiteringObject never considers it —
// the well resolves and applies. (The bench has no refresh rows anyway.)
func TestApplyObjectRefreshAtArrivalIgnoresBareObjects(t *testing.T) {
	w, cancel := buildRefreshTestWorld(t)
	defer cancel()

	placeAtObjectPin(t, w, "hannah", "well")
	res, _ := w.Send(sim.ApplyObjectRefreshAtArrival("hannah"))
	r := res.(sim.ArrivalRefreshResult)
	if r.ObjectID != "well" {
		t.Errorf("resolved object = %q, want well (bench has no refreshes)", r.ObjectID)
	}
}

// TestApplyObjectRefreshAtArrivalNamedBareObjectBlocks locks in the
// resolve-then-check semantics: when a NAMED, asset-backed object with NO
// refresh rows is the nearest loitering object, the arrival is a no-op even
// though a refresh-bearing well sits one tile away, still inside the
// attribution radius. v1 resolves the single loitering object first, then
// checks it for refresh rows — it does NOT skip past a refresh-less object to
// a farther refresh-bearing one. (Distinct from IgnoresBareObjects, where the
// bench is unnamed and thus invisible to the resolver entirely.)
func TestApplyObjectRefreshAtArrivalNamedBareObjectBlocks(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"bench-wood": {ID: "bench-wood"},
		"well-stone": {ID: "well-stone"},
	})
	zero := 0
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		// Named, asset-backed, NO refreshes — pin on the actor's tile (dist 0).
		"bench": {
			ID: "bench", DisplayName: "Bench", AssetID: "bench-wood", CurrentState: "default",
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
			Pos: sim.TileToWorld(sim.GridPoint{X: 100, Y: 100}),
		},
		// Named, asset-backed, HAS a refresh — pin one tile away (Chebyshev 1,
		// inside LoiterAttributionTiles), so it would apply if the nearer
		// bench didn't win the resolve.
		"well": {
			ID: "well", DisplayName: "Well", AssetID: "well-stone", CurrentState: "default",
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
			Pos:       sim.TileToWorld(sim.GridPoint{X: 101, Y: 100}),
			Refreshes: []*sim.ObjectRefresh{{Attribute: "thirst", Amount: -12}},
		},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {
			ID:    "hannah",
			Needs: map[sim.NeedKey]int{"thirst": 18},
			Pos:   sim.TilePos{X: 100, Y: 100}, // on the bench pin; the well is 1 tile away
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	res, _ := w.Send(sim.ApplyObjectRefreshAtArrival("hannah"))
	r := res.(sim.ArrivalRefreshResult)
	if r.ObjectID != "" || len(r.Hits) != 0 {
		t.Errorf("resolve-then-check failed: nearest object is the refresh-less bench, want no-op; got %+v", r)
	}
	// The farther well's refresh must NOT have applied through the bench.
	if got := w.Published().Actors["hannah"].Needs["thirst"]; got != 18 {
		t.Errorf("well refresh applied through the bench: thirst = %d, want 18 (unchanged)", got)
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
			Pos: sim.WorldPos{X: 0, Y: 0},
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
			Pos: sim.WorldPos{X: 0, Y: 0},
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

// TestRegenObjectRefreshFullKeepsAnchorCurrent is part of the LLM-103 regression:
// a finite periodic source that has sat full far longer than its period must keep
// its regen anchor current while full, so a later draw-down regrows a full period
// from the draw rather than snapping back on the next regen tick (a stale anchor
// would read elapsed >= period and refill instantly).
func TestRegenObjectRefreshFullKeepsAnchorCurrent(t *testing.T) {
	repo, handles := mem.NewRepository()
	stale := time.Now().UTC().Add(-72 * time.Hour) // 3 days idle, period 6h
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"bush": {
			ID: "bush", AssetID: "raspberry-bush", CurrentState: "berries",
			Pos: sim.WorldPos{X: 0, Y: 0},
			Refreshes: []*sim.ObjectRefresh{{
				Attribute:          "hunger",
				Amount:             -2,
				AvailableQuantity:  ip(3),
				MaxQuantity:        ip(3),
				RefreshMode:        sim.RefreshModePeriodic,
				RefreshPeriodHours: ip(6),
				LastRefreshAt:      tp(stale),
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

	send := func(fn func(*sim.World) (any, error)) any {
		v, err := w.Send(sim.Command{Fn: fn})
		if err != nil {
			t.Fatalf("command: %v", err)
		}
		return v
	}
	avail := func() int {
		return send(func(world *sim.World) (any, error) {
			return *world.VillageObjects["bush"].Refreshes[0].AvailableQuantity, nil
		}).(int)
	}

	// A regen pass over the FULL bush refills nothing but re-anchors it to now,
	// shedding the stale 3-day-old anchor.
	now := time.Now().UTC()
	if touched := send(func(world *sim.World) (any, error) {
		return sim.RegenObjectRefresh(world, now), nil
	}).(int); touched != 0 {
		t.Errorf("full-bush regen touched = %d, want 0", touched)
	}
	anchor, ok := send(func(world *sim.World) (any, error) {
		return world.VillageObjects["bush"].Refreshes[0].LastRefreshAt, nil
	}).(*time.Time)
	if !ok || anchor == nil || !anchor.Equal(now) {
		t.Fatalf("anchor = %v, want it advanced to %v while full", anchor, now)
	}

	// Simulate the bush already depleted after that anchored full-pass (a direct
	// set, not the gather path — the draw-down path is covered separately). The
	// anchor stays `now`, so regrow is measured from it: no refill just before the
	// period, a jump to max at the period.
	send(func(world *sim.World) (any, error) {
		world.VillageObjects["bush"].Refreshes[0].AvailableQuantity = ip(0)
		return nil, nil
	})
	send(func(world *sim.World) (any, error) {
		return sim.RegenObjectRefresh(world, now.Add(6*time.Hour-time.Nanosecond)), nil
	})
	if got := avail(); got != 0 {
		t.Errorf("bush supply just before the period = %d, want 0", got)
	}
	send(func(world *sim.World) (any, error) {
		return sim.RegenObjectRefresh(world, now.Add(6*time.Hour)), nil
	})
	if got := avail(); got != 3 {
		t.Errorf("bush supply at the period = %d, want 3 (regrew to max)", got)
	}
}

// TestDrawDownStockAnchorsRegrowAtHarvest covers the draw-down half of the
// LLM-103 fix (and the restart/boot race code_review flagged): a full finite
// periodic source carrying a stale anchor, harvested BEFORE any regen pass runs,
// must anchor regrow to the harvest instant so it does not snap back to max on
// the next regen tick. A draw from a non-full source leaves the anchor alone
// (its regrow is already running), and an infinite source is a no-op.
func TestDrawDownStockAnchorsRegrowAtHarvest(t *testing.T) {
	now := time.Now().UTC()
	stale := now.Add(-72 * time.Hour)

	// Full source, stale anchor: a draw stamps the anchor to the harvest instant.
	full := &sim.ObjectRefresh{
		Attribute: "hunger", Amount: -2,
		AvailableQuantity: ip(3), MaxQuantity: ip(3),
		RefreshMode: sim.RefreshModePeriodic, RefreshPeriodHours: ip(6),
		LastRefreshAt: tp(stale),
	}
	sim.DrawDownStock(full, 3, now)
	if *full.AvailableQuantity != 0 {
		t.Errorf("avail = %d, want 0 (picked clean)", *full.AvailableQuantity)
	}
	if full.LastRefreshAt == nil || !full.LastRefreshAt.Equal(now) {
		t.Errorf("anchor = %v, want stamped to harvest time %v", full.LastRefreshAt, now)
	}

	// Non-full source: a draw leaves the anchor unchanged (regrow already running).
	partial := &sim.ObjectRefresh{
		Attribute: "hunger", Amount: -2,
		AvailableQuantity: ip(2), MaxQuantity: ip(3),
		RefreshMode: sim.RefreshModePeriodic, RefreshPeriodHours: ip(6),
		LastRefreshAt: tp(stale),
	}
	sim.DrawDownStock(partial, 1, now)
	if *partial.AvailableQuantity != 1 {
		t.Errorf("avail = %d, want 1", *partial.AvailableQuantity)
	}
	if partial.LastRefreshAt == nil || !partial.LastRefreshAt.Equal(stale) {
		t.Errorf("anchor = %v, want unchanged %v (source was not full)", partial.LastRefreshAt, stale)
	}

	// Infinite source (a well): no finite supply to draw, no-op.
	well := &sim.ObjectRefresh{Attribute: "thirst", Amount: -12}
	sim.DrawDownStock(well, 1, now)
	if well.AvailableQuantity != nil {
		t.Errorf("infinite source gained a supply counter: %v", well.AvailableQuantity)
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
