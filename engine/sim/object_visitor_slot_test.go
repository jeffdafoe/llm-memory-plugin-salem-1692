package sim_test

import (
	"context"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// seedObjectSlotWorld seeds a world with one bare named prop ("oak", NOT
// Structure-bridged) whose loiter pin sits on its anchor tile (zero loiter
// offset), plus the supplied actors. Returns the running world and the pin.
func seedObjectSlotWorld(t *testing.T, actors map[sim.ActorID]*sim.Actor) (*sim.World, context.CancelFunc, sim.Position) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"oak-tree": {ID: "oak-tree", Category: "prop"},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"oak": {ID: "oak", DisplayName: "Oak", AssetID: "oak-tree",
			Pos: sim.WorldPos{X: 320, Y: 320}, LoiterOffsetX: intp(0), LoiterOffsetY: intp(0)},
	})
	if actors != nil {
		handles.Actors.Seed(actors)
	}
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel, anchorTile // zero loiter offset → pin == anchor tile
}

// pickObjSlot resolves an approach slot for actorID against object "oak",
// building a fresh WalkGrid inside the command.
func pickObjSlot(t *testing.T, w *sim.World, actorID sim.ActorID) (sim.Position, bool) {
	t.Helper()
	type result struct {
		pos sim.Position
		ok  bool
	}
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		grid, gerr := sim.BuildWalkGrid(world)
		if gerr != nil {
			return nil, gerr
		}
		p, ok := sim.PickObjectVisitorSlot(world, "oak", world.Actors[actorID], grid)
		return result{pos: p, ok: ok}, nil
	}})
	if err != nil {
		t.Fatalf("pickObjSlot command: %v", err)
	}
	r := res.(result)
	return r.pos, r.ok
}

// TestPickObjectVisitorSlot_ReturnsReachableSlot: a bare named prop resolves
// to a walkable tile within LoiterAttributionTiles of the pin, so an actor
// routed there resolves back to the same object on arrival.
func TestPickObjectVisitorSlot_ReturnsReachableSlot(t *testing.T) {
	w, cancel, pin := seedObjectSlotWorld(t, map[sim.ActorID]*sim.Actor{
		"weary": {ID: "weary", Pos: sim.TilePos{X: sim.PadX, Y: sim.PadY}},
	})
	defer cancel()

	got, ok := pickObjSlot(t, w, "weary")
	if !ok {
		t.Fatal("pickObjSlot(weary): ok=false, want a free ring slot")
	}
	if d := pin.Chebyshev(got); d > sim.LoiterAttributionTiles {
		t.Errorf("slot %+v is Chebyshev %d from pin %+v, want <= %d (so arrival re-resolves to the object)",
			got, d, pin, sim.LoiterAttributionTiles)
	}
}

// TestPickObjectVisitorSlot_MissingObject: an unknown object id is a clean
// ok=false, not a panic.
func TestPickObjectVisitorSlot_MissingObject(t *testing.T) {
	w, cancel, _ := seedObjectSlotWorld(t, map[sim.ActorID]*sim.Actor{
		"weary": {ID: "weary", Pos: sim.TilePos{X: sim.PadX, Y: sim.PadY}},
	})
	defer cancel()

	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		grid, gerr := sim.BuildWalkGrid(world)
		if gerr != nil {
			return nil, gerr
		}
		_, ok := sim.PickObjectVisitorSlot(world, "no-such-object", world.Actors["weary"], grid)
		return ok, nil
	}})
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	if res.(bool) {
		t.Error("expected ok=false for a missing object")
	}
}

// TestPickObjectVisitorSlot_NilActor: the unexported helper guards nil rather
// than panicking on actor.ID, matching pickVisitorSlot.
func TestPickObjectVisitorSlot_NilActor(t *testing.T) {
	w, cancel, _ := seedObjectSlotWorld(t, nil)
	defer cancel()

	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		grid, gerr := sim.BuildWalkGrid(world)
		if gerr != nil {
			return nil, gerr
		}
		_, ok := sim.PickObjectVisitorSlot(world, "oak", nil, grid)
		return ok, nil
	}})
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	if res.(bool) {
		t.Error("expected ok=false for a nil actor")
	}
}
