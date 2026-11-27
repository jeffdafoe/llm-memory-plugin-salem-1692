package sim_test

import (
	"context"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// painted_wagon_walkgrid_test.go — LLM-968j: the live Covered Wagon's tiles
// before and after the painted-wagons migration, through the engine's own
// walk grid. Before: footprint 2,2,4,1 with its door (-2, -2) carved walkable,
// at its placed position. After: footprint 2,2,1,1, no door, at its anchor
// tile's centre. The migration claims no tile becomes blocked; this holds it
// to that, door carving included.

// wagonGrid builds the walk grid for one covered wagon with the given asset
// values at pos, and returns it with the wagon's anchor tile.
func wagonGrid(t *testing.T, asset *sim.Asset, pos sim.WorldPos) (*sim.WalkGrid, sim.GridPoint) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{asset.ID: asset})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"covered-wagon-1": {ID: "covered-wagon-1", AssetID: asset.ID, Pos: pos, CurrentState: "default"},
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
	return res.(*sim.WalkGrid), sim.WorldToTile(pos.X, pos.Y)
}

func TestPaintedCoveredWagonBlocksNoNewTile(t *testing.T) {
	before, anchorBefore := wagonGrid(t, &sim.Asset{
		ID: "covered-wagon", IsObstacle: true,
		FootprintLeft: 2, FootprintRight: 2, FootprintTop: 4, FootprintBottom: 1,
		DoorOffsetX: intPtr(-2), DoorOffsetY: intPtr(-2),
	}, sim.WorldPos{X: 898.426330566406, Y: 187.881195068359})
	after, anchorAfter := wagonGrid(t, &sim.Asset{
		ID: "covered-wagon", IsObstacle: true,
		FootprintLeft: 2, FootprintRight: 2, FootprintTop: 1, FootprintBottom: 1,
	}, sim.WorldPos{X: 912, Y: 176})
	if anchorBefore != anchorAfter {
		t.Fatalf("the move changed the anchor tile: %v -> %v", anchorBefore, anchorAfter)
	}
	a := anchorAfter
	blockedBefore, blockedAfter, freed := 0, 0, 0
	// Every tile within reach of either footprint, its overhang ring and
	// the door's corridor.
	for dy := -7; dy <= 4; dy++ {
		for dx := -5; dx <= 5; dx++ {
			x, y := a.X+dx, a.Y+dy
			wasBlocked, isBlocked := !before.CanWalk(x, y), !after.CanWalk(x, y)
			if isBlocked && !wasBlocked {
				t.Errorf("tile (%+d, %+d) from the anchor is newly blocked", dx, dy)
			}
			if wasBlocked {
				blockedBefore++
			}
			if isBlocked {
				blockedAfter++
				inNew := dx >= -2 && dx <= 2 && dy >= -1 && dy <= 1
				if !inNew {
					t.Errorf("tile (%+d, %+d) is blocked outside the new footprint", dx, dy)
				}
			}
			if wasBlocked && !isBlocked {
				freed++
			}
		}
	}
	// Before: the 5x6 footprint less the door tile; after: the 5x3 box.
	if blockedBefore != 29 || blockedAfter != 15 || freed != 14 {
		t.Errorf("blocked before %d (want 29), after %d (want 15), freed %d (want 14)", blockedBefore, blockedAfter, freed)
	}
	if !before.CanWalk(a.X-2, a.Y-2) {
		t.Error("the old door tile (-2, -2) should be walkable before")
	}
}
