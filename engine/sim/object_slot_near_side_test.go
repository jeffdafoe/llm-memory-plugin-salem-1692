package sim_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// LLM-944: a bush with no loiter offset and no door gets the default pin two
// tiles below it, so the pin's ring spans three rows: the row beside the bush,
// the pin's own row, and the row beyond. In the live village the pin row of
// four raspberry bushes is a moat, and an actor whose hash started the ring
// scan on the far row picked from across the water. The object slot picker
// now tries the slots nearest the object first.

// seedBushWorld seeds one 1x1 bush at the anchor tile (PadX+10, PadY+10) with
// the default pin (anchor + (0, 2)), the given rows of the pin's ring turned to
// deep water, and nActors actors parked far away. Returns the world, its cancel,
// the anchor and the actor ids.
func seedBushWorld(t *testing.T, waterRows []int, nActors int) (*sim.World, context.CancelFunc, sim.Position, []sim.ActorID) {
	t.Helper()
	anchor := sim.Position{X: sim.PadX + 10, Y: sim.PadY + 10}
	ter := makeAllGrassTerrain()
	for _, dy := range waterRows {
		for dx := -1; dx <= 1; dx++ {
			ter.Data[(anchor.Y+dy)*sim.MapW+anchor.X+dx] = sim.TerrainDeepWater
		}
	}
	actors := map[sim.ActorID]*sim.Actor{}
	ids := make([]sim.ActorID, nActors)
	for i := range ids {
		ids[i] = sim.ActorID(fmt.Sprintf("picker-%02d", i))
		actors[ids[i]] = &sim.Actor{ID: ids[i], Pos: sim.TilePos{X: sim.PadX, Y: sim.PadY}}
	}
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(ter)
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"bush-asset": {ID: "bush-asset", Category: "prop"},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		// No loiter offset and no door: the default pin branch.
		"bush": {ID: "bush", DisplayName: "Raspberry Bush", AssetID: "bush-asset", Pos: sim.WorldPos{X: 320, Y: 320}},
	})
	handles.Actors.Seed(actors)
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel, anchor, ids
}

// pickBushSlot resolves each actor's approach slot for "bush" in one command,
// with no reservations, so every actor sees the same empty ring.
func pickBushSlot(t *testing.T, w *sim.World, ids []sim.ActorID) []sim.Position {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		grid, gerr := sim.BuildWalkGrid(world)
		if gerr != nil {
			return nil, gerr
		}
		out := make([]sim.Position, len(ids))
		for i, id := range ids {
			p, ok := sim.PickObjectVisitorSlot(world, "bush", world.Actors[id], grid)
			if !ok {
				return nil, fmt.Errorf("no slot for %s", id)
			}
			out[i] = p
		}
		return out, nil
	}})
	if err != nil {
		t.Fatalf("pick command: %v", err)
	}
	return res.([]sim.Position)
}

// With water on the pin's row (the moat), every actor stands on the row
// beside the bush, never on the far row, and actors still spread across the
// near-row slots (the hash still breaks ties).
func TestObjectVisitorSlot_PrefersNearSideOfWater(t *testing.T) {
	w, cancel, anchor, ids := seedBushWorld(t, []int{2}, 24)
	defer cancel()

	seen := map[sim.Position]bool{}
	for i, p := range pickBushSlot(t, w, ids) {
		if p.Y != anchor.Y+1 {
			t.Errorf("%s: slot %+v is on row %+d from the bush, want +1 (the bush's side of the water)", ids[i], p, p.Y-anchor.Y)
		}
		seen[p] = true
	}
	if len(seen) < 2 {
		t.Errorf("24 actors used %d distinct near-row slots, want at least 2 (hash tie-break spreads them)", len(seen))
	}
}

// With no water, the nearest row still wins: the slot is within one tile of
// the bush and within LoiterAttributionTiles of the pin, so arrival and gather
// still attribute the actor to the bush.
func TestObjectVisitorSlot_NearestRowOnOpenGround(t *testing.T) {
	w, cancel, anchor, ids := seedBushWorld(t, nil, 12)
	defer cancel()

	pin := sim.Position{X: anchor.X, Y: anchor.Y + 2}
	for i, p := range pickBushSlot(t, w, ids) {
		if p.Chebyshev(anchor) != 1 {
			t.Errorf("%s: slot %+v is Chebyshev %d from the bush, want 1", ids[i], p, p.Chebyshev(anchor))
		}
		if d := p.Chebyshev(pin); d > sim.LoiterAttributionTiles {
			t.Errorf("%s: slot %+v is Chebyshev %d from the pin, want <= %d", ids[i], p, d, sim.LoiterAttributionTiles)
		}
	}
}

// When the near row and the pin row are both water, the far row is the only
// walkable part of the ring and is still used, as before.
func TestObjectVisitorSlot_FarRowWhenNearRowBlocked(t *testing.T) {
	w, cancel, anchor, ids := seedBushWorld(t, []int{1, 2}, 8)
	defer cancel()

	for i, p := range pickBushSlot(t, w, ids) {
		if p.Y != anchor.Y+3 {
			t.Errorf("%s: slot %+v is on row %+d, want +3 (the only walkable row)", ids[i], p, p.Y-anchor.Y)
		}
	}
}
