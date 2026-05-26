package sim_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// buildGatherTestWorld seeds a fixture for the gather verb (ZBBS-WORK-328):
//   - well: thirst refresh, INFINITE supply, GatherItem="water" (unbounded,
//     gather always succeeds — the v1 well model)
//   - bush: hunger refresh, FINITE supply (available=2 of max=4), continuous
//     regen, GatherItem="berries" (bounded — depletes and refills)
//   - dry_bush: hunger refresh, FINITE depleted (available=0), GatherItem="berries"
//   - oak: hunger refresh, infinite, NO GatherItem (consume-in-place only —
//     proves a refresh row without GatherItem isn't gatherable)
//   - bench: no refreshes (decorative — proves resolve-then-check)
//
// Item catalog seeds water + berries so resolveItemKind succeeds.
func buildGatherTestWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()

	handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		"water": {
			Name:     "water",
			Category: sim.ItemCategoryDrink,
			Satisfies: []sim.ItemSatisfaction{
				{Attribute: "thirst", Immediate: 8},
			},
		},
		"berries": {
			Name:     "berries",
			Category: sim.ItemCategoryFood,
			Satisfies: []sim.ItemSatisfaction{
				{Attribute: "hunger", Immediate: 4},
			},
		},
	})

	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"well-stone":   {ID: "well-stone", Name: "Old Well"},
		"bush-berries": {ID: "bush-berries", Name: "Berry Bush"},
		"tree-oak":     {ID: "tree-oak", Name: "Oak"},
		"bench-wood":   {ID: "bench-wood", Name: "Bench"},
	})
	zero := 0
	ip := func(v int) *int { return &v }
	tp := func(t time.Time) *time.Time { return &t }
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"well": {
			ID: "well", DisplayName: "Old Well", AssetID: "well-stone", CurrentState: "default",
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
			Pos: sim.WorldPos{X: 100, Y: 100},
			Refreshes: []*sim.ObjectRefresh{
				{Attribute: "thirst", Amount: -12, GatherItem: "water"}, // infinite
			},
		},
		"bush": {
			ID: "bush", DisplayName: "Berry Bush", AssetID: "bush-berries", CurrentState: "default",
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
			Pos: sim.WorldPos{X: 500, Y: 500},
			Refreshes: []*sim.ObjectRefresh{
				{
					Attribute:          "hunger",
					Amount:             -8,
					AvailableQuantity:  ip(2),
					MaxQuantity:        ip(4),
					RefreshMode:        sim.RefreshModeContinuous,
					RefreshPeriodHours: ip(6),
					LastRefreshAt:      tp(time.Now().UTC()),
					GatherItem:         "berries",
				},
			},
		},
		"dry_bush": {
			ID: "dry_bush", DisplayName: "Picked Bush", AssetID: "bush-berries", CurrentState: "default",
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
					GatherItem:         "berries",
				},
			},
		},
		"oak": {
			ID: "oak", DisplayName: "Oak", AssetID: "tree-oak", CurrentState: "default",
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
			Pos: sim.WorldPos{X: 1500, Y: 1500},
			Refreshes: []*sim.ObjectRefresh{
				{Attribute: "hunger", Amount: -4}, // no GatherItem
			},
		},
		"bench": {
			ID: "bench", DisplayName: "Bench", AssetID: "bench-wood", CurrentState: "default",
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
			Pos: sim.WorldPos{X: 2000, Y: 2000},
		},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {ID: "hannah", LLMAgent: "hannah-innkeeper"},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

// placeAt moves the actor onto objID's loiter pin (zero offset = anchor tile).
func placeAt(t *testing.T, w *sim.World, actorID sim.ActorID, objID sim.VillageObjectID) {
	t.Helper()
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		obj := world.VillageObjects[objID]
		if obj == nil {
			return nil, fmt.Errorf("placeAt: no object %q", objID)
		}
		actor := world.Actors[actorID]
		if actor == nil {
			return nil, fmt.Errorf("placeAt: no actor %q", actorID)
		}
		actor.Pos = obj.Pos.Tile()
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("placeAt(%s): %v", objID, err)
	}
}

// inventoryOf reads actorID's quantity of kind off the live world.
func inventoryOf(t *testing.T, w *sim.World, actorID sim.ActorID, kind sim.ItemKind) int {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		actor := world.Actors[actorID]
		if actor == nil {
			return 0, fmt.Errorf("inventoryOf: no actor %q", actorID)
		}
		return actor.Inventory[kind], nil
	}})
	if err != nil {
		t.Fatalf("inventoryOf: %v", err)
	}
	return res.(int)
}

func TestGather_InfiniteWell_AlwaysSucceeds(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	placeAt(t, w, "hannah", "well")

	res, err := w.Send(sim.Gather("hannah", 3, time.Now().UTC()))
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	gr := res.(sim.GatherResult)
	if gr.Item != "water" || gr.Qty != 3 {
		t.Errorf("got Item=%q Qty=%d, want water/3", gr.Item, gr.Qty)
	}
	if gr.SourceName != "Old Well" {
		t.Errorf("SourceName=%q, want Old Well", gr.SourceName)
	}
	if got := inventoryOf(t, w, "hannah", "water"); got != 3 {
		t.Errorf("inventory water=%d, want 3", got)
	}
}

func TestGather_FiniteBush_DecrementsAndClamps(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	placeAt(t, w, "hannah", "bush")

	// bush has 2 available; ask for 5 → clamps to 2.
	res, err := w.Send(sim.Gather("hannah", 5, time.Now().UTC()))
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	gr := res.(sim.GatherResult)
	if gr.Qty != 2 {
		t.Errorf("Qty=%d, want 2 (clamped to available)", gr.Qty)
	}
	if got := inventoryOf(t, w, "hannah", "berries"); got != 2 {
		t.Errorf("inventory berries=%d, want 2", got)
	}

	// Now empty — a second gather rejects as depleted.
	_, err = w.Send(sim.Gather("hannah", 1, time.Now().UTC()))
	if !errors.Is(err, sim.ErrGatherableDepleted) {
		t.Errorf("second gather err=%v, want ErrGatherableDepleted", err)
	}
}

func TestGather_DepletedBush_Rejects(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	placeAt(t, w, "hannah", "dry_bush")

	_, err := w.Send(sim.Gather("hannah", 1, time.Now().UTC()))
	if !errors.Is(err, sim.ErrGatherableDepleted) {
		t.Errorf("err=%v, want ErrGatherableDepleted", err)
	}
}

func TestGather_NonGatherableRefreshObject_Rejects(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	placeAt(t, w, "hannah", "oak") // has a refresh row but no GatherItem

	_, err := w.Send(sim.Gather("hannah", 1, time.Now().UTC()))
	if !errors.Is(err, sim.ErrNoGatherSource) {
		t.Errorf("err=%v, want ErrNoGatherSource", err)
	}
}

func TestGather_NoSourceHere_Rejects(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	placeAt(t, w, "hannah", "bench") // decorative, no refreshes

	_, err := w.Send(sim.Gather("hannah", 1, time.Now().UTC()))
	if !errors.Is(err, sim.ErrNoGatherSource) {
		t.Errorf("err=%v, want ErrNoGatherSource", err)
	}
}

func TestGather_DefaultsQtyToOne(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	placeAt(t, w, "hannah", "well")

	res, err := w.Send(sim.Gather("hannah", 0, time.Now().UTC())) // qty<1 → 1
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if gr := res.(sim.GatherResult); gr.Qty != 1 {
		t.Errorf("Qty=%d, want 1 (default)", gr.Qty)
	}
}

func TestGather_UnknownActor_Errors(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()

	_, err := w.Send(sim.Gather("ghost", 1, time.Now().UTC()))
	if err == nil {
		t.Fatal("want error for unknown actor, got nil")
	}
}
