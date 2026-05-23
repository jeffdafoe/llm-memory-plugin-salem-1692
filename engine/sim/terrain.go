package sim

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

// GridPoint is an internal-grid tile coordinate (NOT world-pixel). Alias of
// the canonical TilePos (geom.go) — kept as a name during the coordinate-type
// migration so existing pathfinding call sites compile unchanged.
type GridPoint = TilePos

// PathPoint is a world-pixel waypoint along a broadcast path. Alias of the
// canonical WorldPos (geom.go).
type PathPoint = WorldPos

// WorldToTile converts a world-pixel (wx, wy) to internal-grid tile coords.
// Thin wrapper over WorldPos.Tile() (geom.go) — single source of the floor
// formula. Retained for the existing positional callers; new code can use the
// method directly.
func WorldToTile(wx, wy float64) GridPoint {
	return WorldPos{X: wx, Y: wy}.Tile()
}

// TileToWorld returns the CENTER of a tile in world-pixel coords. The center
// is what NPCs walk toward — keeps them visually on the tile. Thin wrapper
// over TilePos.Center() (geom.go).
func TileToWorld(g GridPoint) PathPoint {
	return g.Center()
}
