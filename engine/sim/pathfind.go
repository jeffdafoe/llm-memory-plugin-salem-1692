package sim

import (
	"container/heap"
	"fmt"
)

// Pathfinding — in-memory port of engine/pathfind.go.
//
// Grid A* over the world tile grid (MapW × MapH). Walkability and
// step cost are computed at query time from current terrain + current
// village_object placements (obstacles + passages + doors).
//
// SURFACE COSTS (TerrainCost):
//   dirt, cobblestone     1   roads/paths, preferred
//   light/dark grass      3   off-road, NPCs detour onto roads when possible
//   shallow/deep water    0   impassable
//
// OBSTACLE STAMPING:
//   - Asset.IsObstacle = true tiles the asset's footprint as
//     impassable (cost 0).
//   - Asset.IsPassage = true stamps cost 1 (bridge surface, treated
//     like a road).
//   - Asset.DoorOffsetX/Y punches a walkable hole through the
//     obstacle footprint, plus carves a 1-tile-wide corridor to the
//     nearest footprint edge so an interior door is reachable.
//   - VillageObject.AttachedTo non-empty → skip (overlay attachments
//     don't stamp obstacles).
//
// OVERHANG SURCHARGE: the 1-tile ring around an obstacle gets cost 8
// so planners prefer wider routes around buildings. Mana Seed sprites
// have decorative wall/roof art that extends a tile beyond the
// collision footprint; paths threading the immediate ring visually
// clip the wall art. Surcharge 8 is meaningfully higher than grass
// (3) but doesn't dominate distance — a single surcharge tile costs
// about as much as a 2-tile grass detour.
//
// A* USES Manhattan heuristic, 4-connected neighbors. Heuristic is
// admissible because min step cost is 1.

// OverhangSurcharge is the cost stamped on the 1-tile ring around an
// obstacle's footprint. Higher than grass (3), lower than impassable.
const OverhangSurcharge uint8 = 8

// WalkGrid is per-tile A* step cost for one path query. 0 = impassable;
// positive values are the cost of stepping ONTO that tile.
type WalkGrid struct {
	cost []uint8 // row-major, size MapW*MapH
}

// CanWalk reports whether (x, y) is in bounds and walkable.
func (g *WalkGrid) CanWalk(x, y int) bool {
	if x < 0 || x >= MapW || y < 0 || y >= MapH {
		return false
	}
	return g.cost[y*MapW+x] > 0
}

// CostAt returns the step cost for entering (x, y). 0 for impassable
// or out-of-bounds.
func (g *WalkGrid) CostAt(x, y int) int {
	if x < 0 || x >= MapW || y < 0 || y >= MapH {
		return 0
	}
	return int(g.cost[y*MapW+x])
}

// TerrainCost maps a terrain byte to its A* step cost.
func TerrainCost(b byte) uint8 {
	switch b {
	case TerrainDirt, TerrainCobblestone:
		return 1
	case TerrainLightGrass, TerrainDarkGrass:
		return 3
	case TerrainShallowWater, TerrainDeepWater:
		return 0
	default:
		return 1 // unknown terrain — assume walkable, cheap
	}
}

// buildWalkGrid constructs a WalkGrid from the current world state:
// terrain bytes + asset-stamped obstacles/passages + door corridor
// carve-outs.
//
// MUST BE CALLED FROM INSIDE A Command.Fn (or from LoadWorld init) —
// reads w.Terrain, w.Assets, w.VillageObjects directly without going
// through the world goroutine. Pure read; doesn't mutate state.
//
// Returns nil + error if terrain isn't loaded or is the wrong size.
//
// Unexported by design: an exported `*World`-taking helper is too easy to
// misuse from future HTTP handlers; the discipline that callers MUST be
// inside a Command.Fn lives only in this doc-comment. Keep it internal.
func buildWalkGrid(w *World) (*WalkGrid, error) {
	if w.Terrain == nil {
		return nil, fmt.Errorf("terrain not loaded")
	}
	if len(w.Terrain.Data) != MapW*MapH {
		return nil, fmt.Errorf("terrain size mismatch: got %d, want %d",
			len(w.Terrain.Data), MapW*MapH)
	}

	g := &WalkGrid{cost: make([]uint8, MapW*MapH)}
	for i, b := range w.Terrain.Data {
		g.cost[i] = TerrainCost(b)
	}

	// Two-pass stamping: obstacles first, then passages (so a bridge
	// overrides the water it spans). Mirrors legacy ORDER BY a.is_passage.
	type doorTile struct {
		x, y                           int
		fpMinX, fpMaxX, fpMinY, fpMaxY int
	}
	var doorTiles []doorTile

	stampPass := func(isPassage bool) {
		for _, obj := range w.VillageObjects {
			if obj.AttachedTo != "" {
				continue // overlay attachments don't stamp
			}
			asset, ok := w.Assets[obj.AssetID]
			if !ok {
				continue
			}
			if isPassage != asset.IsPassage {
				continue
			}
			if !isPassage && !asset.IsObstacle {
				continue
			}
			anchor := obj.Pos.Tile()
			var stamp uint8 = 0
			if isPassage {
				stamp = 1
			}
			for ty := anchor.Y - asset.FootprintTop; ty <= anchor.Y+asset.FootprintBottom; ty++ {
				if ty < 0 || ty >= MapH {
					continue
				}
				for tx := anchor.X - asset.FootprintLeft; tx <= anchor.X+asset.FootprintRight; tx++ {
					if tx < 0 || tx >= MapW {
						continue
					}
					g.cost[ty*MapW+tx] = stamp
				}
			}

			// Overhang surcharge — skip for passages (bridges have no
			// overhang).
			if !isPassage {
				for ty := anchor.Y - asset.FootprintTop - 1; ty <= anchor.Y+asset.FootprintBottom+1; ty++ {
					if ty < 0 || ty >= MapH {
						continue
					}
					for tx := anchor.X - asset.FootprintLeft - 1; tx <= anchor.X+asset.FootprintRight+1; tx++ {
						if tx < 0 || tx >= MapW {
							continue
						}
						// Skip footprint interior — already stamped 0.
						if tx >= anchor.X-asset.FootprintLeft && tx <= anchor.X+asset.FootprintRight &&
							ty >= anchor.Y-asset.FootprintTop && ty <= anchor.Y+asset.FootprintBottom {
							continue
						}
						existing := g.cost[ty*MapW+tx]
						// Don't overwrite impassable (water, another
						// building's footprint) or already-surcharged tiles.
						if existing == 0 || existing >= OverhangSurcharge {
							continue
						}
						g.cost[ty*MapW+tx] = OverhangSurcharge
					}
				}
			}

			if asset.DoorOffsetX != nil && asset.DoorOffsetY != nil {
				doorTiles = append(doorTiles, doorTile{
					x:      anchor.X + *asset.DoorOffsetX,
					y:      anchor.Y + *asset.DoorOffsetY,
					fpMinX: anchor.X - asset.FootprintLeft,
					fpMaxX: anchor.X + asset.FootprintRight,
					fpMinY: anchor.Y - asset.FootprintTop,
					fpMaxY: anchor.Y + asset.FootprintBottom,
				})
			}
		}
	}
	stampPass(false) // obstacles first
	stampPass(true)  // then passages

	// Door carve-outs. Each door is a walkable hole; doors inside the
	// footprint get a 1-tile corridor to the nearest edge so they're
	// reachable. Tie-break order: W > E > N > S. The Mana Seed sprite
	// south-door convention reads better visually, but always picking
	// south carves long internal corridors for buildings with non-south
	// doors (Blacksmith stall). Closest-edge tie-break is the
	// least-disruptive compromise — see legacy comment block for the
	// history.
	stampWalk := func(tx, ty int) {
		if tx < 0 || tx >= MapW || ty < 0 || ty >= MapH {
			return
		}
		g.cost[ty*MapW+tx] = 1
	}
	for _, d := range doorTiles {
		stampWalk(d.x, d.y)
		if d.x < d.fpMinX || d.x > d.fpMaxX || d.y < d.fpMinY || d.y > d.fpMaxY {
			continue // door outside footprint, no corridor needed
		}
		distW := d.x - d.fpMinX
		distE := d.fpMaxX - d.x
		distN := d.y - d.fpMinY
		distS := d.fpMaxY - d.y
		minDist := distW
		dir := "w"
		if distE < minDist {
			minDist = distE
			dir = "e"
		}
		if distN < minDist {
			minDist = distN
			dir = "n"
		}
		if distS < minDist {
			dir = "s"
		}
		switch dir {
		case "w":
			for tx := d.x - 1; tx >= d.fpMinX; tx-- {
				stampWalk(tx, d.y)
			}
		case "e":
			for tx := d.x + 1; tx <= d.fpMaxX; tx++ {
				stampWalk(tx, d.y)
			}
		case "n":
			for ty := d.y - 1; ty >= d.fpMinY; ty-- {
				stampWalk(d.x, ty)
			}
		case "s":
			for ty := d.y + 1; ty <= d.fpMaxY; ty++ {
				stampWalk(d.x, ty)
			}
		}
	}

	return g, nil
}

// FindPath returns a tile-path from start to goal (inclusive of both),
// or nil when no path exists. Manhattan heuristic, 4-connected.
//
// If start is not walkable (an NPC standing on water somehow) we still
// allow starting there — only neighbors and the goal need to pass
// walkability.
func FindPath(g *WalkGrid, start, goal GridPoint) []GridPoint {
	if start == goal {
		return []GridPoint{start}
	}
	if !g.CanWalk(goal.X, goal.Y) {
		return nil
	}

	manhattan := func(a, b GridPoint) int {
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
	heap.Push(openSet, &aStarItem{pos: start, gCost: 0, fCost: manhattan(start, goal)})

	cameFrom := map[GridPoint]GridPoint{}
	gScore := map[GridPoint]int{start: 0}
	closed := map[GridPoint]bool{}

	neighbors := []GridPoint{{0, -1}, {0, 1}, {-1, 0}, {1, 0}}

	for openSet.Len() > 0 {
		current := heap.Pop(openSet).(*aStarItem)
		if current.pos == goal {
			// Reconstruct path.
			path := []GridPoint{goal}
			cursor := goal
			for cursor != start {
				cursor = cameFrom[cursor]
				path = append([]GridPoint{cursor}, path...)
			}
			return path
		}
		closed[current.pos] = true

		for _, d := range neighbors {
			next := GridPoint{current.pos.X + d.X, current.pos.Y + d.Y}
			if closed[next] {
				continue
			}
			if !g.CanWalk(next.X, next.Y) {
				continue
			}
			tentativeG := current.gCost + g.CostAt(next.X, next.Y)
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
	return nil
}

// FindPathToAdjacent finds the shortest path from start to any walkable
// cardinal neighbor of goal. Returns (path, neighbor) — the chosen
// neighbor, or (nil, zero) if no neighbor is both walkable and
// reachable. Used to walk up TO an obstacle (well, building wall)
// rather than through it.
//
// Preference order on ties: south first (matches Mana Seed sprite
// convention), then north, west, east — but only by len() shortest,
// so the actual approach direction depends on the map.
func FindPathToAdjacent(g *WalkGrid, start, goal GridPoint) ([]GridPoint, GridPoint) {
	candidates := []GridPoint{
		{goal.X, goal.Y + 1}, // south
		{goal.X, goal.Y - 1}, // north
		{goal.X - 1, goal.Y},
		{goal.X + 1, goal.Y},
	}
	var best []GridPoint
	var bestNeighbor GridPoint
	bestLen := -1
	for _, n := range candidates {
		if !g.CanWalk(n.X, n.Y) {
			continue
		}
		path := FindPath(g, start, n)
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

// aStarItem is a node in the A* priority queue.
type aStarItem struct {
	pos   GridPoint
	gCost int // actual cost from start
	fCost int // g + h
	index int // heap index
}

type aStarQueue []*aStarItem

func (q aStarQueue) Len() int           { return len(q) }
func (q aStarQueue) Less(i, j int) bool { return q[i].fCost < q[j].fCost }
func (q aStarQueue) Swap(i, j int) {
	q[i], q[j] = q[j], q[i]
	q[i].index = i
	q[j].index = j
}
func (q *aStarQueue) Push(x interface{}) {
	item := x.(*aStarItem)
	item.index = len(*q)
	*q = append(*q, item)
}
func (q *aStarQueue) Pop() interface{} {
	old := *q
	n := len(old)
	x := old[n-1]
	*q = old[:n-1]
	return x
}
