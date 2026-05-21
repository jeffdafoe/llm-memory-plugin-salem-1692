package sim

// Sprite / SpriteAnimation — in-memory port of the character-sprite catalog
// (engine/npcs.go NPCSprite / NPCSpriteAnim data types).
//
// Sprites are the character-render DEFINITIONS — "Woman A v00", "Old Man B
// v02". One Sprite is one sheet plus a set of (direction, animation) rows
// mapping into it. This is a SEPARATE catalog from Asset (asset.go): assets
// are placeable object/terrain art with a single src-rect per state, whereas
// character sprites use a row-indexed directional animation model
// (direction × animation → row_index), so the two cannot share a type.
//
// Actor.SpriteID references a Sprite by its npc_sprite.id UUID. The catalog
// is reference state: loaded once at startup, read directly off *World (no
// checkpoint path — editor CRUD writes the underlying tables and the world
// rebuilds the map wholesale via LoadAll). Same lifecycle as Asset/Terrain.

// SpriteID is the npc_sprite.id UUID (the table's primary key), held as a
// string. Actor.SpriteID references it and npc_sprite_animation.sprite_id
// FKs it. The human-readable label lives in Sprite.Name, not here.
type SpriteID string

// Sprite is the catalog entry for one character sprite sheet. FrameWidth /
// FrameHeight are the per-frame pixel dimensions; the client slices the
// sheet into a grid of those cells and plays the rows named by Animations.
type Sprite struct {
	ID          SpriteID
	Name        string
	Sheet       string
	FrameWidth  int
	FrameHeight int
	PackID      *string
	Pack        *TilesetPack
	Animations  []SpriteAnimation
}

// SpriteAnimation is one (direction, animation) mapping into a sprite sheet.
// RowIndex is the 0-indexed sheet row; frames run left-to-right from column 0
// through FrameCount-1. FrameRate is frames per second.
//
// Direction is one of north/south/east/west and Animation is idle/walk
// (enforced by CHECK constraints on npc_sprite_animation). Kept as plain
// strings here — the client speaks these tokens verbatim and the engine
// never branches on them.
type SpriteAnimation struct {
	Direction  string
	Animation  string
	RowIndex   int
	FrameCount int
	FrameRate  float64
}
