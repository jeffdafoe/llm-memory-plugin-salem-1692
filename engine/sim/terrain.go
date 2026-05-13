package sim

import "math"

// Terrain — in-memory port of village_terrain.go data shape.
//
// The terrain is a fixed-size grid of bytes (one byte per tile) loaded
// once at startup as reference state. The client uses a pad offset to
// map world-pixel coords to internal grid coords (world (0,0) is at
// internal tile (PadX, PadY)).

const (
	// TileSize is world pixels per tile. Both client and engine assume
	// this — coordinate math runs in tile units after dividing by it.
	TileSize = 32.0

	// PadX / PadY map world (0,0) to internal tile (PadX, PadY).
	// Matches client-side terrain rendering offset.
	PadX = 60
	PadY = 112

	// MapW / MapH are the internal grid dimensions in tiles. Matches the
	// terrain blob's expected byte count (MapW * MapH).
	MapW = 200
	MapH = 180
)

// Terrain types — one byte per tile in the data blob. Names mirror
// legacy's terrainCost branches.
const (
	TerrainDirt         byte = 1
	TerrainLightGrass   byte = 2
	TerrainDarkGrass    byte = 3
	TerrainCobblestone  byte = 4
	TerrainShallowWater byte = 5
	TerrainDeepWater    byte = 6
)

// Terrain holds the raw per-tile terrain bytes. Data is row-major,
// length must equal MapW * MapH.
type Terrain struct {
	Data []byte // length MapW * MapH; one tile per byte
}

// GridPoint is an internal-grid tile coordinate (NOT world-pixel).
type GridPoint struct {
	X int
	Y int
}

// PathPoint is a world-pixel waypoint along a broadcast path.
type PathPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// WorldToTile converts a world-pixel (wx, wy) to internal-grid tile
// coords. Uses floor so any point within a tile's footprint maps to
// that tile.
func WorldToTile(wx, wy float64) GridPoint {
	return GridPoint{
		X: PadX + int(math.Floor(wx/TileSize)),
		Y: PadY + int(math.Floor(wy/TileSize)),
	}
}

// TileToWorld returns the CENTER of a tile in world-pixel coords. The
// center is what NPCs walk toward — keeps them visually on the tile.
func TileToWorld(g GridPoint) PathPoint {
	return PathPoint{
		X: float64(g.X-PadX)*TileSize + TileSize/2,
		Y: float64(g.Y-PadY)*TileSize + TileSize/2,
	}
}
