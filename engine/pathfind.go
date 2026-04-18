package main

// Grid A* pathfinding over the world tile grid.
//
// The terrain blob from village_terrain is an internal grid of width×height
// bytes, one byte per tile. The client uses a pad (pad_x=60, pad_y=112) to
// map internal tiles to world-pixel coords: world (0,0) corresponds to
// internal tile (pad_x, pad_y). We mirror that pad here so pathfinding works
// in the same coordinate space.
//
// Walkability: tiles are impassable if their terrain byte is shallow/deep
// water (types 5 and 6), OR if a placed village_object whose asset has
// is_obstacle = true occupies the tile. Objects are treated as occupying the
// single tile containing their world position — good enough for trees and
// small props; misses some spread on larger buildings. Refine later.
//
// A* uses 4-connected neighbors (no diagonals) and Manhattan heuristic.
// Step cost is per-tile (see terrainCost): roads (dirt, cobblestone) and
// bridge surfaces are 1; grass is 3 so NPCs visibly prefer roads when a
// reasonable detour exists.

import (
	"container/heap"
	"context"
	"fmt"
	"math"
)

const (
	tileSize = 32.0 // world pixels per tile
	padX     = 60   // world (0,0) → internal tile (padX, padY)
	padY     = 112
	mapW     = 200
	mapH     = 180
)

// gridPoint is an internal-grid tile coordinate.
type gridPoint struct {
	X, Y int
}

// pathPoint is a world-pixel waypoint in the broadcast path.
type pathPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// walkGrid holds per-tile A* step cost for a single path query. Built
// fresh each pathfind so we see current terrain + current object
// placements. 0 = impassable; positive values are the cost of stepping
// ONTO that tile. Higher cost discourages routing through that terrain.
//
// Surface costs (terrainCost): dirt and cobblestone = 1 (roads/paths,
// preferred), grass = 3 (off-road, NPCs avoid when a short-ish road
// detour exists). Water = 0. Bridge passage tiles get cost 1 so NPCs
// treat them like a continuation of the road. Heuristic stays Manhattan
// × 1 — admissible because min cost is 1.
type walkGrid struct {
	cost []uint8 // row-major, size mapW*mapH; 0 = impassable
}

func (g *walkGrid) canWalk(x, y int) bool {
	if x < 0 || x >= mapW || y < 0 || y >= mapH {
		return false
	}
	return g.cost[y*mapW+x] > 0
}

func (g *walkGrid) costAt(x, y int) int {
	if x < 0 || x >= mapW || y < 0 || y >= mapH {
		return 0
	}
	return int(g.cost[y*mapW+x])
}

// terrainCost maps a terrain byte to its step cost. Tuned so a 3-tile
// road detour beats a 1-tile shortcut across grass — strong enough that
// NPCs visibly prefer roads, gentle enough that grass isn't off-limits
// when the road would be a long way around.
func terrainCost(b byte) uint8 {
	switch b {
	case 1, 4: // dirt, cobblestone
		return 1
	case 2, 3: // light_grass, dark_grass
		return 3
	case 5, 6: // shallow_water, deep_water
		return 0
	default:
		return 1 // unknown terrain — assume walkable, cheap
	}
}

// loadWalkGrid reads terrain + obstacle-tagged village_objects and builds a
// walkability grid. Called per pathfind so it reflects current state.
func (app *App) loadWalkGrid(ctx context.Context) (*walkGrid, error) {
	var terrain []byte
	if err := app.DB.QueryRow(ctx,
		`SELECT data FROM village_terrain WHERE id = 1`,
	).Scan(&terrain); err != nil {
		return nil, fmt.Errorf("load terrain: %w", err)
	}
	if len(terrain) != mapW*mapH {
		return nil, fmt.Errorf("terrain size mismatch: got %d, want %d", len(terrain), mapW*mapH)
	}

	g := &walkGrid{cost: make([]uint8, mapW*mapH)}
	for i, b := range terrain {
		g.cost[i] = terrainCost(b)
	}

	// Mark obstacle / passage objects' footprints. Order matters: obstacles
	// stamp impassable first, passages stamp walkable second so a bridge
	// always wins over the water it spans (or any other obstacle below it).
	//
	// Footprint is per-side tile counts (footprint_left/right/top/bottom)
	// extending out from the anchor TILE. Anchor tile is always part of
	// the footprint, so a {0,0,0,0} asset blocks just its anchor tile.
	// All four sides are tunable from the editor's drag-resize border,
	// which is why this is per-side rather than width/height.
	rows, err := app.DB.Query(ctx,
		`SELECT o.x, o.y,
		        a.footprint_left, a.footprint_right, a.footprint_top, a.footprint_bottom,
		        a.is_passage
		 FROM village_object o
		 JOIN asset a ON a.id = o.asset_id
		 WHERE (a.is_obstacle = TRUE OR a.is_passage = TRUE) AND o.attached_to IS NULL
		 ORDER BY a.is_passage`, // obstacles (FALSE) come before passages (TRUE)
	)
	if err != nil {
		return nil, fmt.Errorf("load obstacles: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var wx, wy float64
		var fLeft, fRight, fTop, fBottom int
		var isPassage bool
		if err := rows.Scan(&wx, &wy, &fLeft, &fRight, &fTop, &fBottom, &isPassage); err != nil {
			continue
		}
		ax, ay := worldToTile(wx, wy)
		// Passages stamp cost = 1 (treat the bridge surface as a road).
		// Obstacles stamp 0 (impassable).
		var stamp uint8 = 0
		if isPassage {
			stamp = 1
		}
		for ty := ay - fTop; ty <= ay+fBottom; ty++ {
			if ty < 0 || ty >= mapH {
				continue
			}
			for tx := ax - fLeft; tx <= ax+fRight; tx++ {
				if tx < 0 || tx >= mapW {
					continue
				}
				g.cost[ty*mapW+tx] = stamp
			}
		}
	}
	return g, nil
}

// worldToTile converts a world-pixel (x, y) to internal-grid tile coords.
// Uses floor so any point within a tile's footprint maps to that tile.
func worldToTile(wx, wy float64) (int, int) {
	return padX + int(math.Floor(wx/tileSize)), padY + int(math.Floor(wy/tileSize))
}

// tileToWorld returns the CENTER of a tile in world-pixel coords. The center
// is what NPCs walk toward — keeps them visually on the tile.
func tileToWorld(tx, ty int) pathPoint {
	return pathPoint{
		X: float64(tx-padX)*tileSize + tileSize/2,
		Y: float64(ty-padY)*tileSize + tileSize/2,
	}
}

// aStarItem is a node in the A* priority queue.
type aStarItem struct {
	pos   gridPoint
	gCost int // actual cost from start
	fCost int // g + h
	index int // heap index
}

type aStarQueue []*aStarItem

func (q aStarQueue) Len() int            { return len(q) }
func (q aStarQueue) Less(i, j int) bool  { return q[i].fCost < q[j].fCost }
func (q aStarQueue) Swap(i, j int)       { q[i], q[j] = q[j], q[i]; q[i].index = i; q[j].index = j }
func (q *aStarQueue) Push(x interface{}) { item := x.(*aStarItem); item.index = len(*q); *q = append(*q, item) }
func (q *aStarQueue) Pop() interface{}   { old := *q; n := len(old); x := old[n-1]; *q = old[:n-1]; return x }

// findPathToAdjacent finds the shortest path from start to any walkable tile
// adjacent to goal (goal itself may be impassable — typical for lamps/trees
// you want to walk TO but not onto). Returns (path, neighbor_tile_reached)
// or (nil, zero) if no neighbor is both walkable and reachable. Tries the 4
// cardinal neighbors and keeps the shortest result.
func findPathToAdjacent(g *walkGrid, start, goal gridPoint) ([]gridPoint, gridPoint) {
	candidates := []gridPoint{
		{goal.X, goal.Y + 1}, // south (preferred approach from below)
		{goal.X, goal.Y - 1},
		{goal.X - 1, goal.Y},
		{goal.X + 1, goal.Y},
	}
	var best []gridPoint
	var bestNeighbor gridPoint
	bestLen := -1
	for _, n := range candidates {
		if !g.canWalk(n.X, n.Y) {
			continue
		}
		path := findPath(g, start, n)
		if path == nil {
			continue
		}
		if bestLen < 0 || len(path) < bestLen {
			best = path
			bestNeighbor = n
			bestLen = len(path)
		}
	}
	return best, bestNeighbor
}

// findPath returns a tile-path from start to goal (inclusive of both).
// Returns nil if no path exists. Manhattan heuristic, 4-connected.
//
// If start is not walkable (NPC standing on water somehow) we still allow
// starting there — only neighbors and the goal need to pass walkability.
func findPath(g *walkGrid, start, goal gridPoint) []gridPoint {
	if start == goal {
		return []gridPoint{start}
	}
	if !g.canWalk(goal.X, goal.Y) {
		return nil
	}

	manhattan := func(a, b gridPoint) int {
		dx := a.X - b.X
		dy := a.Y - b.Y
		if dx < 0 {
			dx = -dx
		}
		if dy < 0 {
			dy = -dy
		}
		return dx + dy
	}

	openSet := &aStarQueue{}
	heap.Init(openSet)
	startItem := &aStarItem{pos: start, gCost: 0, fCost: manhattan(start, goal)}
	heap.Push(openSet, startItem)

	cameFrom := map[gridPoint]gridPoint{}
	gScore := map[gridPoint]int{start: 0}
	closed := map[gridPoint]bool{}

	neighbors := []gridPoint{{0, -1}, {0, 1}, {-1, 0}, {1, 0}}

	for openSet.Len() > 0 {
		current := heap.Pop(openSet).(*aStarItem)
		if current.pos == goal {
			// Reconstruct path.
			path := []gridPoint{goal}
			cursor := goal
			for cursor != start {
				cursor = cameFrom[cursor]
				path = append([]gridPoint{cursor}, path...)
			}
			return path
		}
		closed[current.pos] = true

		for _, d := range neighbors {
			next := gridPoint{current.pos.X + d.X, current.pos.Y + d.Y}
			if closed[next] {
				continue
			}
			if !g.canWalk(next.X, next.Y) {
				continue
			}
			tentativeG := current.gCost + g.costAt(next.X, next.Y)
			if existing, ok := gScore[next]; ok && tentativeG >= existing {
				continue
			}
			cameFrom[next] = current.pos
			gScore[next] = tentativeG
			heap.Push(openSet, &aStarItem{
				pos:   next,
				gCost: tentativeG,
				fCost: tentativeG + manhattan(next, goal),
			})
		}
	}
	return nil // no path
}
