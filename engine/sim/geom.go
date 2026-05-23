package sim

import "math"

// geom.go — typed coordinate system for the village grid.
//
// See shared/notes/codebase/salem-engine-v2/coordinates for the full
// convention. The engine works in TWO coordinate spaces, kept distinct at the
// type level so mixing them is a COMPILE error rather than a silently-wrong
// runtime calc (the recurring tile-vs-pixel unit bug):
//
//   - TilePos  — a padded internal-grid tile index. World (0,0) maps to tile
//     (PadX, PadY). This is the space Actor.Pos, the WalkGrid, and pathfinding
//     work in.
//   - WorldPos — a world-pixel coordinate. This is the space VillageObject.Pos
//     and the client wire use.
//
// You cannot subtract or compare across spaces directly; convert explicitly
// with WorldPos.Tile() / TilePos.Center(). The legacy free functions
// WorldToTile / TileToWorld now delegate to these methods (terrain.go) and the
// legacy point types GridPoint / Position / PathPoint are aliases of the
// canonical types (so existing call sites keep compiling during migration).

// TilePos is a padded internal-grid tile coordinate. The PadX/PadY offset is
// baked in (world (0,0) is tile (PadX, PadY)) — the same convention the
// WalkGrid and Actor.Pos use, so a WorldPos.Tile() result is directly
// comparable to an actor's position.
type TilePos struct {
	X int
	Y int
}

// WorldPos is a world-pixel coordinate. The json tags match the retired
// PathPoint so anything that serialized a PathPoint serializes a WorldPos
// identically.
type WorldPos struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// TileOffset is a RELATIVE tile delta — e.g. an asset's door or loiter offset
// that combines with an absolute TilePos via Add. Distinct from TilePos so a
// relative offset can never be mistaken for an absolute position.
type TileOffset struct {
	DX int
	DY int
}

// Tile converts a world-pixel coordinate to its padded grid tile, flooring so
// any pixel within a tile's footprint maps to that tile. Matches the legacy
// WorldToTile exactly.
func (w WorldPos) Tile() TilePos {
	return TilePos{
		X: PadX + int(math.Floor(w.X/TileSize)),
		Y: PadY + int(math.Floor(w.Y/TileSize)),
	}
}

// Dist returns the Euclidean pixel distance between two world positions.
func (w WorldPos) Dist(o WorldPos) float64 {
	dx := w.X - o.X
	dy := w.Y - o.Y
	return math.Sqrt(dx*dx + dy*dy)
}

// Center returns the world-pixel CENTER of the tile — what actors walk toward,
// keeping them visually on the tile. Matches the legacy TileToWorld.
func (t TilePos) Center() WorldPos {
	return WorldPos{
		X: float64(t.X-PadX)*TileSize + TileSize/2,
		Y: float64(t.Y-PadY)*TileSize + TileSize/2,
	}
}

// Origin returns the world-pixel top-left corner of the tile.
func (t TilePos) Origin() WorldPos {
	return WorldPos{
		X: float64(t.X-PadX) * TileSize,
		Y: float64(t.Y-PadY) * TileSize,
	}
}

// Chebyshev returns the king's-move distance (max of the per-axis deltas) in
// tiles — the "within N tiles, diagonals included" metric loiter-pin and
// arrival resolution use.
func (t TilePos) Chebyshev(o TilePos) int {
	dx := absInt(t.X - o.X)
	dy := absInt(t.Y - o.Y)
	if dx > dy {
		return dx
	}
	return dy
}

// Manhattan returns the 4-direction grid distance (sum of the per-axis deltas).
func (t TilePos) Manhattan(o TilePos) int {
	return absInt(t.X-o.X) + absInt(t.Y-o.Y)
}

// Add applies a relative tile offset, yielding a new absolute position.
func (t TilePos) Add(off TileOffset) TilePos {
	return TilePos{X: t.X + off.DX, Y: t.Y + off.DY}
}

// Equal reports whether two tile positions are the same tile.
func (t TilePos) Equal(o TilePos) bool {
	return t.X == o.X && t.Y == o.Y
}

// absInt is the int absolute value (math.Abs is float64-only).
func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
