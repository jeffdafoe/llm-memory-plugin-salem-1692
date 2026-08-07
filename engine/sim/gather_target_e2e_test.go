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

// buildForeignPinPlotWorld seeds the LLM-618 geometry: "moses" stands on a legal
// visitor slot of his OWN ripe bush, and that slot is Chebyshev 0 from a neighbouring
// bush owned by someone else.
//
//	actor tile      = pad origin
//	foreign_bush pin = pad origin        (cheb 0 — the strict nearest)
//	mine_bush pin    = pad origin +(1,1) (cheb 1 — a legal ring slot away)
//
// This is the live shape: an ObjectVisit parks the walker on one of the eight ring
// slots around its destination's pin, and a ring slot can be another object's pin.
// Both bushes are ripe, so stock is not the variable under test — ownership of the
// tile is.
func buildForeignPinPlotWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		"wheat": {Name: "wheat", Category: sim.ItemCategoryMaterial},
	})
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{"wheat_plant": {ID: "wheat_plant", Name: "Wheat"}})
	z, one := 0, 1
	q := func(v int) *int { return &v }
	ripe := func() []*sim.ObjectRefresh {
		return []*sim.ObjectRefresh{{Attribute: "hunger", Amount: 0, GatherItem: "wheat",
			AvailableQuantity: q(3), MaxQuantity: q(3)}}
	}
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"mine_bush": {ID: "mine_bush", DisplayName: "Wheat", AssetID: "wheat_plant", OwnerActorID: "moses",
			LoiterOffsetX: &one, LoiterOffsetY: &one, Pos: sim.WorldPos{X: 0, Y: 0}, Refreshes: ripe()},
		"foreign_bush": {ID: "foreign_bush", DisplayName: "Wheat", AssetID: "wheat_plant", OwnerActorID: "wendy",
			LoiterOffsetX: &z, LoiterOffsetY: &z, Pos: sim.WorldPos{X: 0, Y: 0}, Refreshes: ripe()},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"moses": {ID: "moses", LLMAgent: "moses", Kind: sim.KindNPCStateful,
			Pos: sim.TilePos{X: sim.PadX, Y: sim.PadY}, Inventory: map[sim.ItemKind]int{"wheat": 0},
			RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{{Item: "wheat", Source: sim.RestockSourceForage, Max: 30}}}},
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

// TestGather_ForeignPinOnRingSlot_WalkedToTargetWins — LLM-618. A grower parked on a
// ring slot of the bush he walked to may harvest it, even though that slot is another
// actor's loiter pin. Before the walked-to bypass the strict-nearest scan resolved the
// foreign bush and rejected with ErrNotYourSource, which also stripped the `gather`
// tool from his turn (it is advertised only when this resolution yields a source) —
// Moses James re-walked the same wheat for two hours with no picking verb.
func TestGather_ForeignPinOnRingSlot_WalkedToTargetWins(t *testing.T) {
	w, cancel := buildForeignPinPlotWorld(t)
	defer cancel()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["moses"].GatherTargetObjectID = "mine_bush"
		return nil, nil
	}}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	res, err := w.Send(sim.Gather("moses", 1, time.Now().UTC()))
	if err != nil {
		t.Fatalf("gather at his own walked-to bush should succeed, got: %v", err)
	}
	if id := gatheredObject(t, res); id != "mine_bush" {
		t.Errorf("gathered from %q, want mine_bush", id)
	}
}

// TestGather_ForeignPinOnRingSlot_NoTargetStillRejects is the foil that keeps the
// bypass honest: the SAME geometry with no walked-to target still meets the unchanged
// nearest-scan and rejects. Without this arm the fix would be indistinguishable from
// deleting the poacher gate — standing at a stranger's bush and reaching past it must
// still fail.
func TestGather_ForeignPinOnRingSlot_NoTargetStillRejects(t *testing.T) {
	w, cancel := buildForeignPinPlotWorld(t)
	defer cancel()
	_, err := w.Send(sim.Gather("moses", 1, time.Now().UTC()))
	if !errors.Is(err, sim.ErrNotYourSource) {
		t.Errorf("err=%v, want ErrNotYourSource (no walked-to target — the foreign pin still owns the tile)", err)
	}
}

// TestGather_ForeignPinOnRingSlot_ForeignTargetStillRejects — the bypass keys on the
// target being one the actor MAY take from, not merely on a target being set. Walking
// deliberately to another's bush is exactly the poaching case the gate exists for.
func TestGather_ForeignPinOnRingSlot_ForeignTargetStillRejects(t *testing.T) {
	w, cancel := buildForeignPinPlotWorld(t)
	defer cancel()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["moses"].GatherTargetObjectID = "foreign_bush"
		return nil, nil
	}}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err := w.Send(sim.Gather("moses", 1, time.Now().UTC()))
	if !errors.Is(err, sim.ErrNotYourSource) {
		t.Errorf("err=%v, want ErrNotYourSource (the walked-to target is another's bush)", err)
	}
}

// TestGather_ForeignPinOnRingSlot_DepartureClearsTheBypass — LLM-618, raised by
// code_review. The bypass must mean "the source I walked to", so the walked-to target
// must not outlive the visit. Here Moses holds mine_bush as his target, then commits a
// move that never arrives; from the very same tile the gate is back in force. Without
// the clear in MoveActor the stale id would keep bypassing a foreign pin he had
// already walked away from — a real weakening of the poacher gate, and invisible,
// because the actor never physically moves in this fixture.
func TestGather_ForeignPinOnRingSlot_DepartureClearsTheBypass(t *testing.T) {
	w, cancel := buildForeignPinPlotWorld(t)
	defer cancel()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["moses"].GatherTargetObjectID = "mine_bush"
		return nil, nil
	}}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// Walk off somewhere else. The move is accepted (that is what clears the
	// target); it never completes, which is exactly the stale case.
	away := sim.TilePos{X: sim.PadX + 12, Y: sim.PadY + 12}
	if _, err := w.Send(sim.MoveActor("moses", sim.NewPositionDestination(away), false, time.Now().UTC())); err != nil {
		t.Fatalf("move away: %v", err)
	}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["moses"]
		if a.GatherTargetObjectID != "" {
			return nil, fmt.Errorf("committing a move must clear the gather target, got %q", a.GatherTargetObjectID)
		}
		// Drop the in-flight intent so the walk-incompatible gate is not what
		// rejects below — the nearest-scan must be, on the unchanged tile.
		a.MoveIntent = nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("post-move state: %v", err)
	}
	_, err := w.Send(sim.Gather("moses", 1, time.Now().UTC()))
	if !errors.Is(err, sim.ErrNotYourSource) {
		t.Errorf("err=%v, want ErrNotYourSource (the walked-to target did not survive the departure)", err)
	}
}

// TestGather_ForeignPinOnRingSlot_StopMoveLeavesTheBypassClosed — LLM-618, code_review
// round 2. The sibling above proves MoveActor clears the target, but it then drops the
// MoveIntent by hand to isolate the poacher-gate assertion, so it would still pass if a
// real halt path later restored or re-stamped GatherTargetObjectID. This one runs the
// production mechanism end to end: commit the move, then StopMove — the actual halt —
// and require the target to still be empty and the gate to still reject.
func TestGather_ForeignPinOnRingSlot_StopMoveLeavesTheBypassClosed(t *testing.T) {
	w, cancel := buildForeignPinPlotWorld(t)
	defer cancel()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["moses"].GatherTargetObjectID = "mine_bush"
		return nil, nil
	}}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	away := sim.TilePos{X: sim.PadX + 12, Y: sim.PadY + 12}
	if _, err := w.Send(sim.MoveActor("moses", sim.NewPositionDestination(away), false, time.Now().UTC())); err != nil {
		t.Fatalf("move away: %v", err)
	}
	if _, err := w.Send(sim.StopMove("moses", time.Now().UTC())); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["moses"]
		if a.MoveIntent != nil {
			return nil, fmt.Errorf("StopMove should clear the intent, still in flight")
		}
		if a.GatherTargetObjectID != "" {
			return nil, fmt.Errorf("halting a walk must not restore the gather target, got %q", a.GatherTargetObjectID)
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("post-stop state: %v", err)
	}
	_, err := w.Send(sim.Gather("moses", 1, time.Now().UTC()))
	if !errors.Is(err, sim.ErrNotYourSource) {
		t.Errorf("err=%v, want ErrNotYourSource (a halted walk leaves no walked-to target)", err)
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
