package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// buildDensePlotWorld seeds two of "prue"'s forage-to-sell berry bushes at the
// SAME tile — "ripe_bush" (4 ripe) and "dry_bush" (0) — plus a forage policy low
// on berries. This is the dense interleaved plot where the old single-nearest
// resolution handed her the zeroed bush (LLM-93). NOTE the ids: "dry_bush" sorts
// before "ripe_bush", so the lowest-id tie-break alone would pick the dry one.
func buildDensePlotWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		"berries": {Name: "berries", Category: sim.ItemCategoryFood,
			Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger", Immediate: 4}}},
	})
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{"bush": {ID: "bush", Name: "Berry Bush"}})
	z := 0
	q := func(v int) *int { return &v }
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"ripe_bush": {ID: "ripe_bush", DisplayName: "Berry Bush", AssetID: "bush", OwnerActorID: "prue",
			LoiterOffsetX: &z, LoiterOffsetY: &z, Pos: sim.WorldPos{X: 0, Y: 0},
			Refreshes: []*sim.ObjectRefresh{{Attribute: "hunger", Amount: 0, GatherItem: "berries", AvailableQuantity: q(4), MaxQuantity: q(4)}}},
		"dry_bush": {ID: "dry_bush", DisplayName: "Berry Bush", AssetID: "bush", OwnerActorID: "prue",
			LoiterOffsetX: &z, LoiterOffsetY: &z, Pos: sim.WorldPos{X: 0, Y: 0},
			Refreshes: []*sim.ObjectRefresh{{Attribute: "hunger", Amount: 0, GatherItem: "berries", AvailableQuantity: q(0), MaxQuantity: q(4)}}},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		// Pad origin so the actor's tile aligns with the bushes at WorldPos{0,0}
		// (world→tile adds the pad offset — same convention as the move_to fixtures).
		"prue": {ID: "prue", LLMAgent: "prue", Kind: sim.KindNPCStateful,
			Pos: sim.TilePos{X: sim.PadX, Y: sim.PadY}, Inventory: map[sim.ItemKind]int{"berries": 0},
			RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{{Item: "berries", Source: sim.RestockSourceForage, Max: 10}}}},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	w.Settings.RestockReorderPct = 25
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

func gatheredObject(t *testing.T, res any) sim.VillageObjectID {
	t.Helper()
	gr, ok := res.(sim.GatherResult)
	if !ok {
		t.Fatalf("Gather result is %T, want sim.GatherResult", res)
	}
	return gr.ObjectID
}

// TestGather_DensePlot_SkipsDepletedForRipe: standing among co-located bushes, the
// gather skips the (lower-id) depleted one for the ripe one — no more looping on
// "the source is depleted right now" while a ripe bush sits at the same tile.
func TestGather_DensePlot_SkipsDepletedForRipe(t *testing.T) {
	w, cancel := buildDensePlotWorld(t)
	defer cancel()
	res, err := w.Send(sim.Gather("prue", 1, time.Now().UTC()))
	if err != nil {
		t.Fatalf("gather should pick the co-located ripe bush, got error: %v", err)
	}
	if id := gatheredObject(t, res); id != "ripe_bush" {
		t.Errorf("gathered from %q, want ripe_bush (the depleted dry_bush must be skipped)", id)
	}
}

// TestGather_DensePlot_HonorsWalkedToTarget: with BOTH bushes ripe (so lowest-id
// would pick dry_bush), the bush she deliberately walked to wins.
func TestGather_DensePlot_HonorsWalkedToTarget(t *testing.T) {
	w, cancel := buildDensePlotWorld(t)
	defer cancel()
	// Refill dry_bush and stamp ripe_bush as the walked-to target.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		four := 4
		world.VillageObjects["dry_bush"].Refreshes[0].AvailableQuantity = &four
		world.Actors["prue"].GatherTargetObjectID = "ripe_bush"
		return nil, nil
	}}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	res, err := w.Send(sim.Gather("prue", 1, time.Now().UTC()))
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	if id := gatheredObject(t, res); id != "ripe_bush" {
		t.Errorf("gathered from %q, want ripe_bush (the walked-to target must win over lower-id dry_bush)", id)
	}
}
