package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// restock_forage_degraded_e2e_test.go — LLM-634, the end-to-end arm. The unit
// test (restock_tick_degraded_test.go) proves the forage WAKE fires for a
// degraded keeper; this drives the whole errand on a running world so an
// overlooked degrade gate anywhere downstream — the move_to resolver, the walk,
// the harvest command, the completion sweep — would fail it: a keeper whose own
// business is worn past degrade, low on a forage item he remembers an owned bush
// for, is woken, walks to the bush on the cue's move_to handle, gathers, and the
// stock lands in his inventory. This is the chain that went dark live on
// 2026-08-15 when the sole water gatherer's Mill degraded.

// buildDegradedForagerWorld seeds a grass map with "joseph" at the pad, his own
// "mill" (a wearable business, worn past degrade) a few tiles south, and his own
// "berry_bush" (finite, yield-only forage-to-sell, 10 ripe) a few tiles east on a
// clear grass path. He holds 0 berries against a `forage berries` cap of 20 and
// remembers the bush as a gather:berries place (what LLM-77 ownership-seeding
// records), which is the forage warrant's actionability precondition.
func buildDegradedForagerWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		"berries": {Name: "berries", Category: sim.ItemCategoryFood, Capabilities: []string{"portable"}},
	})
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"bush": {ID: "bush", Category: "prop"},
		"shop": {ID: "shop", Category: "prop"},
	})
	zero := 0
	stock := 10
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		// Object Pos is UNPADDED world pixels — WorldPos.Tile() adds the pad — so
		// tile (0,4) here is 4 tiles south of the actor standing at the pad origin.
		"mill": {
			ID: "mill", AssetID: "shop", DisplayName: "Mill",
			Pos:           sim.WorldPos{X: 0, Y: 4 * 32},
			OwnerActorID:  "joseph",
			Tags:          []string{sim.TagBusiness},
			Wear:          650,
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
		},
		"berry_bush": {
			ID: "berry_bush", AssetID: "bush", DisplayName: "Berry Bush",
			Pos:           sim.WorldPos{X: 4 * 32, Y: 0}, // 4 tiles east of the pad origin
			OwnerActorID:  "joseph",
			EntryPolicy:   sim.EntryPolicyClosed,
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
			Refreshes: []*sim.ObjectRefresh{
				{Attribute: "hunger", Amount: 0, GatherItem: "berries", AvailableQuantity: &stock},
			},
		},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"joseph": {
			ID: "joseph", DisplayName: "Joseph Scott", Kind: sim.KindNPCStateful, LLMAgent: "joseph-agent",
			Pos:       sim.TilePos{X: sim.PadX, Y: sim.PadY},
			Inventory: map[sim.ItemKind]int{"berries": 0},
			RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
				{Item: "berries", Source: sim.RestockSourceForage, Max: 20},
			}},
			KnownPlaces: map[sim.PlaceRef]*sim.KnownPlace{
				"berry_bush": {Ref: "berry_bush", Kind: sim.PlaceKindObject, Affordances: []string{"gather:berries"}},
			},
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	// Set before Run starts — no world goroutine yet, so a direct write is safe
	// (and a Send here would block forever).
	w.Settings.RestockReorderPct = sim.DefaultRestockReorderPct
	w.Settings.StallWearRepairThreshold = 400
	w.Settings.StallWearDegradeThreshold = 600
	w.Settings.StallDegradedProducePct = sim.DefaultStallDegradedProducePct
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

func TestDegradedKeeper_ForageErrandEndToEnd(t *testing.T) {
	w, cancel := buildDegradedForagerWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// Precondition: the fixture really is degraded, so the test can never
	// silently pass on the healthy-shop case that always worked.
	degraded, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.StallDegraded(sim.OwnedWearableStall(world.VillageObjects, "joseph"), world.Settings.StallWearDegradeThreshold), nil
	}})
	if err != nil {
		t.Fatalf("read degrade: %v", err)
	}
	if !degraded.(bool) {
		t.Fatal("fixture drift: joseph's mill must be worn past the degrade threshold")
	}

	// 1. The wake: the restock producer stamps a FORAGE warrant on the degraded keeper.
	res, err := w.Send(sim.EvaluateRestock(now))
	if err != nil {
		t.Fatalf("EvaluateRestock: %v", err)
	}
	if res.(int) != 1 {
		t.Fatalf("stamped = %d, want 1 (a degraded keeper is still woken to gather)", res.(int))
	}
	reason, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		for _, m := range world.Actors["joseph"].Warrants {
			if r, ok := m.Reason.(sim.RestockWarrantReason); ok {
				return r, nil
			}
		}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("read warrants: %v", err)
	}
	r, ok := reason.(sim.RestockWarrantReason)
	if !ok {
		t.Fatal("no RestockWarrantReason stamped on the degraded keeper")
	}
	if r.Item != "berries" || r.Source != sim.RestockSourceForage {
		t.Fatalf("warrant = %+v, want {berries forage}", r)
	}

	// 2. The errand: the cue's move_to handle (the bush id in structure_id) routes
	//    a walk for the degraded owner, and the walk lands at the bush.
	if _, err := w.Send(sim.MoveToStructure("joseph", "berry_bush", now)); err != nil {
		t.Fatalf("MoveToStructure(berry_bush): %v", err)
	}
	for i := 0; i < 60; i++ {
		if _, err := w.Send(sim.EvaluateLocomotion(now.Add(time.Duration(i) * time.Second))); err != nil {
			t.Fatalf("EvaluateLocomotion: %v", err)
		}
		if moveIntentOf(t, w, "joseph") == nil {
			break
		}
	}
	if mi := moveIntentOf(t, w, "joseph"); mi != nil {
		t.Fatalf("still walking after 60 ticks: %+v", mi)
	}
	arrived, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["joseph"]
		return a.Pos.Chebyshev(world.VillageObjects["berry_bush"].Pos.Tile()) <= sim.LoiterAttributionTiles, nil
	}})
	if err != nil {
		t.Fatalf("read position: %v", err)
	}
	if !arrived.(bool) {
		t.Fatal("the walk did not end at the bush")
	}

	// 3. The gather: the harvest starts for the degraded owner and its yield lands
	//    at completion — the shelf refills without the mend.
	startRes, err := w.Send(sim.StartHarvest("joseph", 20))
	if err != nil {
		t.Fatalf("StartHarvest: %v", err)
	}
	sr := startRes.(sim.SourceActivityStartResult)
	if !sr.Started || sr.ObjectID != "berry_bush" {
		t.Fatalf("start result = %+v, want Started @ berry_bush", sr)
	}
	if n := forceComplete(t, w); n != 1 {
		t.Fatalf("completed = %d, want 1", n)
	}
	if got := inventoryOf(t, w, "joseph", "berries"); got != 10 {
		t.Errorf("berries = %d, want 10 (the bush picked clean into the degraded keeper's stock)", got)
	}
	if got := availOf(t, w, "berry_bush"); got != 0 {
		t.Errorf("bush supply = %d, want 0", got)
	}
}
