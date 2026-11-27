package sim_test

import (
	"context"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// painted_bridge_walkgrid_test.go — LLM-968k: each placed bridge over its
// crossing, before and after the painted-bridge migration, through the
// engine's own walk grid. Each case is a live crossing: a north-south stream
// (tile columns x0..x1, every row) under an east-west road on two rows.
// Before: the pack bridge's passage footprint 2,1,2,1 and door (0, -1), at the
// bridge's placed position. After: footprint 2,2,1,0 (the deck: the two road
// rows, a bank tile each side of the stream at its widest), no door, at the
// tile centre the migration moves it to — the same tile for A, a row north
// for B, a column west for C (at a negative y).

type bridgeCrossing struct {
	name           string
	x0, x1         int // the stream's columns
	roadY0, roadY1 int // the road's two rows
	oldPos         sim.WorldPos
	newTileX       int
	newTileY       int
}

var bridgeCrossings = []bridgeCrossing{
	{"A", 61, 63, 5, 6, sim.WorldPos{X: 2015.90002441406, Y: 220.351501464844}, 62, 6},
	{"B", 62, 63, 21, 22, sim.WorldPos{X: 2033.89990234375, Y: 753.802124023438}, 63, 22},
	{"C", 63, 65, -28, -27, sim.WorldPos{X: 2083.84912109375, Y: -832.535522460938}, 64, -27},
}

// bridgeGrid builds the walk grid for one bridge with the given asset values
// at pos over the crossing's stream and road.
func bridgeGrid(t *testing.T, c bridgeCrossing, asset *sim.Asset, pos sim.WorldPos) *sim.WalkGrid {
	t.Helper()
	terrain := makeAllGrassTerrain()
	for y := 0; y < sim.MapH; y++ {
		for x := sim.PadX + c.x0; x <= sim.PadX+c.x1; x++ {
			terrain.Data[y*sim.MapW+x] = sim.TerrainShallowWater
		}
	}
	for y := c.roadY0; y <= c.roadY1; y++ {
		for x := c.x0 - 12; x <= c.x1+12; x++ {
			if x < c.x0 || x > c.x1 {
				terrain.Data[(sim.PadY+y)*sim.MapW+sim.PadX+x] = sim.TerrainCobblestone
			}
		}
	}
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(terrain)
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{asset.ID: asset})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"bridge-1": {ID: "bridge-1", AssetID: asset.ID, Pos: pos, CurrentState: "default"},
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
// lets a walker onto, within six rows of the road.
func walkableWater(g *sim.WalkGrid, c bridgeCrossing) map[[2]int]bool {
	out := map[[2]int]bool{}
	for y := c.roadY0 - 6; y <= c.roadY1+6; y++ {
		for x := c.x0; x <= c.x1; x++ {
			if g.CanWalk(sim.PadX+x, sim.PadY+y) {
				out[[2]int{x, y}] = true
			}
		}
	}
	return out
}

func TestPaintedBridgeCarriesTheRoadAndOnlyTheRoad(t *testing.T) {
	for _, c := range bridgeCrossings {
		t.Run(c.name, func(t *testing.T) {
			before := bridgeGrid(t, c, &sim.Asset{
				ID: "bridge", IsPassage: true,
				FootprintLeft: 2, FootprintRight: 1, FootprintTop: 2, FootprintBottom: 1,
				DoorOffsetX: intPtr(0), DoorOffsetY: intPtr(-1),
			}, c.oldPos)
			after := bridgeGrid(t, c, &sim.Asset{
				ID: "bridge", IsPassage: true,
				FootprintLeft: 2, FootprintRight: 2, FootprintTop: 1, FootprintBottom: 0,
			}, sim.WorldPos{X: float64(c.newTileX*32 + 16), Y: float64(c.newTileY*32 + 16)})

			// After: the stream is walkable on the road rows, all the way
			// across, and nowhere else.
			got := walkableWater(after, c)
			for y := c.roadY0 - 6; y <= c.roadY1+6; y++ {
				for x := c.x0; x <= c.x1; x++ {
					want := y >= c.roadY0 && y <= c.roadY1
					if got[[2]int{x, y}] != want {
						t.Errorf("stream tile (%d, %d) walkable = %v, want %v", x, y, got[[2]int{x, y}], want)
					}
				}
			}
			// Every stream tile walkable after was walkable before.
			old := walkableWater(before, c)
			for tile := range got {
				if !old[tile] {
					t.Errorf("stream tile %v is newly walkable", tile)
				}
			}
			// The road crosses on both grids, over water only on its rows.
			west := sim.GridPoint{X: sim.PadX + c.x0 - 4, Y: sim.PadY + c.roadY1}
			east := sim.GridPoint{X: sim.PadX + c.x1 + 4, Y: sim.PadY + c.roadY1}
			for label, g := range map[string]*sim.WalkGrid{"before": before, "after": after} {
				path := sim.FindPath(g, west, east)
				if path == nil {
					t.Fatalf("%s: no path across the stream", label)
				}
				for _, p := range path {
					x, y := p.X-sim.PadX, p.Y-sim.PadY
					if x >= c.x0 && x <= c.x1 && (y < c.roadY0 || y > c.roadY1) {
						t.Errorf("%s: the path crosses the water off the road at (%d, %d)", label, x, y)
					}
				}
			}
		})
	}
}
