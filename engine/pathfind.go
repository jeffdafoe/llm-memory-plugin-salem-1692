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
// A* uses 4-connected neighbors (no diagonals) and Manhattan heuristic. Cost
// per step = 1.

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

// walkGrid holds per-tile passability for a single path query. Built fresh
// each pathfind so we see current terrain + current object placements.
type walkGrid struct {
	walkable []bool // row-major, size mapW*mapH
}

func (g *walkGrid) canWalk(x, y int) bool {
	if x < 0 || x >= mapW || y < 0 || y >= mapH {
		return false
	}
	return g.walkable[y*mapW+x]
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

	g := &walkGrid{walkable: make([]bool, mapW*mapH)}
	for i, b := range terrain {
		// Types 5 (shallow water) and 6 (deep water) are impassable.
		g.walkable[i] = b != 5 && b != 6
	}

	// Mark obstacle objects' tiles impassable.
	rows, err := app.DB.Query(ctx,
		`SELECT o.x, o.y FROM village_object o
		 JOIN asset a ON a.id = o.asset_id
		 WHERE a.is_obstacle = TRUE AND o.attached_to IS NULL`,
	)
	if err != nil {
		return nil, fmt.Errorf("load obstacles: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var wx, wy float64
		if err := rows.Scan(&wx, &wy); err != nil {
			continue
		}
		tx, ty := worldToTile(wx, wy)
		if tx >= 0 && tx < mapW && ty >= 0 && ty < mapH {
			g.walkable[ty*mapW+tx] = false
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
			tentativeG := current.gCost + 1
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
