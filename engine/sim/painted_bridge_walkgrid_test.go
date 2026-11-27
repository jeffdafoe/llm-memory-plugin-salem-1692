package sim_test

import (
	"context"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// painted_bridge_walkgrid_test.go — LLM-968k: a bridge over a stream, before
// and after the painted-bridge migration, through the engine's own walk grid.
// The terrain is crossing A as it is live: a stream three tiles wide (tiles
// x 61..63) under an east-west road on rows 5 and 6. Before: the pack bridge's
// passage footprint 2,1,2,1 and door (0, -1), at A's placed position. After:
// footprint 2,2,1,0 (the deck: the two road rows, the stream and a bank tile
// each side), no door, at its anchor tile's centre.

const streamX0, streamX1 = 61, 63

// bridgeGrid builds the walk grid for one bridge with the given asset values
// at pos over the stream-and-road terrain.
func bridgeGrid(t *testing.T, asset *sim.Asset, pos sim.WorldPos) *sim.WalkGrid {
	t.Helper()
	terrain := makeAllGrassTerrain()
	for y := 0; y < sim.MapH; y++ {
		for x := sim.PadX + streamX0; x <= sim.PadX+streamX1; x++ {
			terrain.Data[y*sim.MapW+x] = sim.TerrainShallowWater
		}
	}
	for _, y := range []int{5, 6} {
		for x := 50; x <= 75; x++ {
			if x < streamX0 || x > streamX1 {
				terrain.Data[(sim.PadY+y)*sim.MapW+sim.PadX+x] = sim.TerrainCobblestone
			}
		}
	}
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(terrain)
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{asset.ID: asset})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"bridge-a": {ID: "bridge-a", AssetID: asset.ID, Pos: pos, CurrentState: "default"},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.BuildWalkGrid(world)
	}})
	if err != nil {
		t.Fatalf("BuildWalkGrid: %v", err)
	}
	return res.(*sim.WalkGrid)
}

// walkableWater lists the stream tiles (village tile coordinates) the grid
// lets a walker onto, rows 0..12.
func walkableWater(g *sim.WalkGrid) map[[2]int]bool {
	out := map[[2]int]bool{}
	for y := 0; y <= 12; y++ {
		for x := streamX0; x <= streamX1; x++ {
			if g.CanWalk(sim.PadX+x, sim.PadY+y) {
				out[[2]int{x, y}] = true
			}
		}
	}
	return out
}

func TestPaintedBridgeCarriesTheRoadAndOnlyTheRoad(t *testing.T) {
	before := bridgeGrid(t, &sim.Asset{
		ID: "bridge", IsPassage: true,
		FootprintLeft: 2, FootprintRight: 1, FootprintTop: 2, FootprintBottom: 1,
		DoorOffsetX: intPtr(0), DoorOffsetY: intPtr(-1),
	}, sim.WorldPos{X: 2015.90002441406, Y: 220.351501464844})
	after := bridgeGrid(t, &sim.Asset{
		ID: "bridge", IsPassage: true,
		FootprintLeft: 2, FootprintRight: 2, FootprintTop: 1, FootprintBottom: 0,
	}, sim.WorldPos{X: 62*32 + 16, Y: 6*32 + 16})

	// After: the stream is walkable on the two road rows and nowhere else.
	got := walkableWater(after)
	for y := 0; y <= 12; y++ {
		for x := streamX0; x <= streamX1; x++ {
			want := y == 5 || y == 6
			if got[[2]int{x, y}] != want {
				t.Errorf("stream tile (%d, %d) walkable = %v, want %v", x, y, got[[2]int{x, y}], want)
			}
		}
	}
	// Every stream tile walkable after was walkable before: the deck opens
	// nothing new.
	old := walkableWater(before)
	for tile := range got {
		if !old[tile] {
			t.Errorf("stream tile %v is newly walkable", tile)
		}
	}
	// The road crosses, keeping to its rows over the water.
	west, east := sim.GridPoint{X: sim.PadX + 57, Y: sim.PadY + 6}, sim.GridPoint{X: sim.PadX + 67, Y: sim.PadY + 6}
	for label, g := range map[string]*sim.WalkGrid{"before": before, "after": after} {
		path := sim.FindPath(g, west, east)
		if path == nil {
			t.Fatalf("%s: no path across the stream", label)
		}
		for _, p := range path {
			x, y := p.X-sim.PadX, p.Y-sim.PadY
			if x >= streamX0 && x <= streamX1 && y != 5 && y != 6 {
				t.Errorf("%s: the path crosses the water off the road at (%d, %d)", label, x, y)
			}
		}
	}
}
