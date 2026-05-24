package sim_test

import (
	"context"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// makeAllGrassTerrain returns a Terrain blob of MapW*MapH light-grass tiles.
func makeAllGrassTerrain() *sim.Terrain {
	data := make([]byte, sim.MapW*sim.MapH)
	for i := range data {
		data[i] = sim.TerrainLightGrass
	}
	return &sim.Terrain{Data: data}
}

// TestTerrainCost covers the surface-cost mapping.
func TestTerrainCost(t *testing.T) {
	cases := []struct {
		b    byte
		want uint8
	}{
		{sim.TerrainDirt, 1},
		{sim.TerrainCobblestone, 1},
		{sim.TerrainLightGrass, 3},
		{sim.TerrainDarkGrass, 3},
		{sim.TerrainShallowWater, 0},
		{sim.TerrainDeepWater, 0},
		{99, 1}, // unknown → walkable, cheap
	}
	for _, c := range cases {
		got := sim.TerrainCost(c.b)
		if got != c.want {
			t.Errorf("TerrainCost(%d) = %d, want %d", c.b, got, c.want)
		}
	}
}

// TestWorldTileRoundTrip covers the world↔tile conversion at the
// origin and at a few key offsets.
func TestWorldTileRoundTrip(t *testing.T) {
	cases := []struct {
		wx, wy   float64
		wantTile sim.GridPoint
	}{
		{0, 0, sim.GridPoint{X: sim.PadX, Y: sim.PadY}},
		{32, 32, sim.GridPoint{X: sim.PadX + 1, Y: sim.PadY + 1}},
		{-32, -32, sim.GridPoint{X: sim.PadX - 1, Y: sim.PadY - 1}},
		{16, 16, sim.GridPoint{X: sim.PadX, Y: sim.PadY}}, // within tile floors
	}
	for _, c := range cases {
		got := sim.WorldToTile(c.wx, c.wy)
		if got != c.wantTile {
			t.Errorf("WorldToTile(%v, %v) = %+v, want %+v", c.wx, c.wy, got, c.wantTile)
		}
	}

	// TileToWorld returns center. Round-trip a tile's center back through
	// WorldToTile should land on the same tile.
	tile := sim.GridPoint{X: sim.PadX + 5, Y: sim.PadY + 3}
	center := sim.TileToWorld(tile)
	back := sim.WorldToTile(center.X, center.Y)
	if back != tile {
		t.Errorf("round-trip: %+v → %v,%v → %+v", tile, center.X, center.Y, back)
	}
}

// TestBuildWalkGridBasic covers the all-grass case — every tile
// walkable at cost 3, no obstacles, no doors.
func TestBuildWalkGridBasic(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	gridRes, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return sim.BuildWalkGrid(world)
		},
	})
	g := gridRes.(*sim.WalkGrid)
	if !g.CanWalk(sim.PadX, sim.PadY) {
		t.Error("center tile not walkable on all-grass map")
	}
	if got := g.CostAt(sim.PadX, sim.PadY); got != 3 {
		t.Errorf("grass cost = %d, want 3", got)
	}
}

// TestBuildWalkGridObstacle covers obstacle stamping — a tree at a
// known position makes its anchor tile impassable, and the surrounding
// ring carries the overhang surcharge.
func TestBuildWalkGridObstacle(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"tree-maple": {
			ID:              "tree-maple",
			IsObstacle:      true,
			FootprintLeft:   0,
			FootprintRight:  0,
			FootprintTop:    0,
			FootprintBottom: 0,
		},
	})
	// Place a tree at world (320, 320) = tile (PadX+10, PadY+10).
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"tree-1": {ID: "tree-1", AssetID: "tree-maple", Pos: sim.WorldPos{X: 320, Y: 320}},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	gridRes, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return sim.BuildWalkGrid(world)
		},
	})
	g := gridRes.(*sim.WalkGrid)
	treeTile := sim.WorldToTile(320, 320)
	if g.CanWalk(treeTile.X, treeTile.Y) {
		t.Errorf("tree tile (%d,%d) should be impassable", treeTile.X, treeTile.Y)
	}
	// Adjacent tile carries overhang surcharge.
	if got := g.CostAt(treeTile.X+1, treeTile.Y); got != int(sim.OverhangSurcharge) {
		t.Errorf("overhang tile cost = %d, want %d", got, sim.OverhangSurcharge)
	}
}

// TestBuildWalkGridPassageOverridesWater covers the obstacles-then-
// passages stamping order: a bridge over deep water becomes walkable
// at cost 1.
func TestBuildWalkGridPassageOverridesWater(t *testing.T) {
	terrain := makeAllGrassTerrain()
	// Mark a 3-tile strip of deep water through the map at row PadY+5.
	for x := sim.PadX + 8; x <= sim.PadX+10; x++ {
		terrain.Data[(sim.PadY+5)*sim.MapW+x] = sim.TerrainDeepWater
	}

	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(terrain)
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"bridge": {
			ID:              "bridge",
			IsPassage:       true,
			FootprintLeft:   0,
			FootprintRight:  0,
			FootprintTop:    0,
			FootprintBottom: 0,
		},
	})
	// Place a bridge at the middle water tile: tile (PadX+9, PadY+5) = world (288, 160).
	bridgeWorld := sim.TileToWorld(sim.GridPoint{X: sim.PadX + 9, Y: sim.PadY + 5})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"bridge-1": {ID: "bridge-1", AssetID: "bridge", Pos: sim.WorldPos{X: bridgeWorld.X, Y: bridgeWorld.Y}},
	})

	w, _ := sim.LoadWorld(context.Background(), repo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	gridRes, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return sim.BuildWalkGrid(world)
		},
	})
	g := gridRes.(*sim.WalkGrid)
	// Bridge tile walkable (passage stamped 1 over water 0).
	if !g.CanWalk(sim.PadX+9, sim.PadY+5) {
		t.Errorf("bridge tile not walkable")
	}
	if got := g.CostAt(sim.PadX+9, sim.PadY+5); got != 1 {
		t.Errorf("bridge cost = %d, want 1", got)
	}
	// Adjacent water tile still impassable.
	if g.CanWalk(sim.PadX+8, sim.PadY+5) {
		t.Error("water tile next to bridge should still be impassable")
	}
}

// TestFindPathSimple covers a basic A* run on an open grass field.
func TestFindPathSimple(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	w, _ := sim.LoadWorld(context.Background(), repo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	pathRes, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			g, err := sim.BuildWalkGrid(world)
			if err != nil {
				return nil, err
			}
			return sim.FindPath(g,
				sim.GridPoint{X: sim.PadX, Y: sim.PadY},
				sim.GridPoint{X: sim.PadX + 5, Y: sim.PadY + 3}), nil
		},
	})
	path := pathRes.([]sim.GridPoint)
	if path == nil {
		t.Fatal("FindPath returned nil on open grass")
	}
	// Path length is 1 (start) + Manhattan distance (8 steps) = 9.
	if len(path) != 9 {
		t.Errorf("path length = %d, want 9 (start + 8 Manhattan steps)", len(path))
	}
	if path[0] != (sim.GridPoint{X: sim.PadX, Y: sim.PadY}) {
		t.Errorf("path[0] = %+v, want start", path[0])
	}
	if path[len(path)-1] != (sim.GridPoint{X: sim.PadX + 5, Y: sim.PadY + 3}) {
		t.Errorf("path[end] = %+v, want goal", path[len(path)-1])
	}
}

// TestFindPathSameStartGoal covers the trivial case.
func TestFindPathSameStartGoal(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	w, _ := sim.LoadWorld(context.Background(), repo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	pathRes, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			g, _ := sim.BuildWalkGrid(world)
			return sim.FindPath(g, sim.GridPoint{X: sim.PadX, Y: sim.PadY},
				sim.GridPoint{X: sim.PadX, Y: sim.PadY}), nil
		},
	})
	path := pathRes.([]sim.GridPoint)
	if len(path) != 1 {
		t.Errorf("same start/goal path length = %d, want 1", len(path))
	}
}

// TestFindPathRouteAroundObstacle covers detour: a wall of obstacles
// blocks the direct route, forcing a detour around.
func TestFindPathRouteAroundObstacle(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"wall": {ID: "wall", IsObstacle: true},
	})
	// Build a wall at column PadX+3, rows PadY..PadY+5 (6 tiles).
	objects := map[sim.VillageObjectID]*sim.VillageObject{}
	for y := sim.PadY; y <= sim.PadY+5; y++ {
		coord := sim.TileToWorld(sim.GridPoint{X: sim.PadX + 3, Y: y})
		objects[sim.VillageObjectID(string(rune('a'+y-sim.PadY)))] = &sim.VillageObject{
			ID:      sim.VillageObjectID(string(rune('a' + y - sim.PadY))),
			AssetID: "wall", Pos: sim.WorldPos{X: coord.X, Y: coord.Y},
		}
	}
	handles.VillageObjects.Seed(objects)
	w, _ := sim.LoadWorld(context.Background(), repo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	pathRes, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			g, _ := sim.BuildWalkGrid(world)
			return sim.FindPath(g,
				sim.GridPoint{X: sim.PadX + 1, Y: sim.PadY + 2},
				sim.GridPoint{X: sim.PadX + 6, Y: sim.PadY + 2}), nil
		},
	})
	path := pathRes.([]sim.GridPoint)
	if path == nil {
		t.Fatal("FindPath returned nil — should detour around wall")
	}
	// Should detour around the wall — path longer than Manhattan distance 5.
	if len(path) < 7 {
		t.Errorf("detour path length = %d, want >= 7", len(path))
	}
	// Confirm the path doesn't cross the wall column except above/below it.
	for _, p := range path {
		if p.X == sim.PadX+3 && p.Y >= sim.PadY && p.Y <= sim.PadY+5 {
			t.Errorf("path crossed wall at %+v", p)
		}
	}
}

// TestFindPathNoPath covers the unreachable case — surround the goal
// with impassable water.
func TestFindPathNoPath(t *testing.T) {
	terrain := makeAllGrassTerrain()
	// Ring of deep water around tile (PadX+10, PadY+10).
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			if dx == 0 && dy == 0 {
				continue
			}
			terrain.Data[(sim.PadY+10+dy)*sim.MapW+(sim.PadX+10+dx)] = sim.TerrainDeepWater
		}
	}
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(terrain)
	w, _ := sim.LoadWorld(context.Background(), repo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	pathRes, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			g, _ := sim.BuildWalkGrid(world)
			return sim.FindPath(g,
				sim.GridPoint{X: sim.PadX, Y: sim.PadY},
				sim.GridPoint{X: sim.PadX + 10, Y: sim.PadY + 10}), nil
		},
	})
	// FindPath returns a nil slice on no-path. The wrapping any
	// interface holds a typed-nil — guard via the cast value, not the
	// interface itself.
	path := pathRes.([]sim.GridPoint)
	if path != nil {
		t.Errorf("expected nil path to surrounded tile, got %d-step path", len(path))
	}
}

// TestFindPathToAdjacent covers walking up to an obstacle: the path
// ends on a cardinal neighbor of the goal, not on the goal itself.
func TestFindPathToAdjacent(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"well": {ID: "well", IsObstacle: true},
	})
	goalTile := sim.GridPoint{X: sim.PadX + 5, Y: sim.PadY + 5}
	goalWorld := sim.TileToWorld(goalTile)
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"well-1": {ID: "well-1", AssetID: "well", Pos: sim.WorldPos{X: goalWorld.X, Y: goalWorld.Y}},
	})

	w, _ := sim.LoadWorld(context.Background(), repo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	type adj struct {
		path     []sim.GridPoint
		neighbor sim.GridPoint
	}
	res, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			g, _ := sim.BuildWalkGrid(world)
			p, n := sim.FindPathToAdjacent(g,
				sim.GridPoint{X: sim.PadX, Y: sim.PadY},
				goalTile)
			return adj{path: p, neighbor: n}, nil
		},
	})
	a := res.(adj)
	if a.path == nil {
		t.Fatal("FindPathToAdjacent returned nil")
	}
	// Last tile in path should be the chosen neighbor, which should be
	// adjacent to the goal but not the goal itself.
	last := a.path[len(a.path)-1]
	if last == goalTile {
		t.Errorf("path ends on goal (impassable obstacle), not a neighbor")
	}
	dx := last.X - goalTile.X
	dy := last.Y - goalTile.Y
	if (dx*dx + dy*dy) != 1 {
		t.Errorf("last tile %+v not a cardinal neighbor of goal %+v", last, goalTile)
	}
	if a.neighbor != last {
		t.Errorf("returned neighbor %+v != last tile %+v", a.neighbor, last)
	}
}
