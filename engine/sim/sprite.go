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

	// Behaviors are engine-behavior slugs carried by the SPRITE (npc_sprite.
	// behaviors jsonb, LLM-579): placing an actor with the sprite is the whole
	// authoring flow for the behavior — no per-actor attribute grant. Slugs
	// compose: a duck carries BehaviorWaterfowl (how it moves) and
	// BehaviorAmbient (what it counts as).
	Behaviors []string

	// RenderScale is the client-side draw scale for this sprite (npc_sprite.
	// render_scale, LLM-580). The engine never reads it — it rides the catalog
	// so the client can size a duck (1.0) differently from a villager (2.0)
	// without hardcoding species. NOT NULL DEFAULT 2.0 in the schema; the
	// client guards <= 0 back to its default for test-seeded zero values.
	RenderScale float64
}

// BehaviorWaterfowl marks a sprite whose decorative actors are driven by the
// waterfowl wander (LLM-579): swim the connected water region they were
// dropped into, with occasional shore excursions.
const BehaviorWaterfowl = "waterfowl"

// BehaviorAmbient marks a sprite whose actors are village SCENERY: they move,
// but nothing they do is part of the village's record. Their comings and
// goings stay out of the action log, and they are named neither on the
// atmosphere roster nor in its activity digest (LLM-593).
//
// It answers a different question from ActorKind, and the two must not be
// conflated. Kind answers "can it deliberate?" — a KindDecorative actor has no
// LLM, so it cannot advance a huddle, hold coins, or work a shift, and the
// gates for those correctly test Kind. BehaviorAmbient answers "is it part of
// village life?" The lamplighter, washerwoman and town crier are all
// KindDecorative — the engine walks them because they have no volition of
// their own — yet they tour the village and they speak, so their doings
// belong in the record. Until the ducks arrived both questions had the same
// answer, which is how one predicate came to serve both.
//
// Carried by the SPRITE, so a new animal needs no code: tag the sprite and
// every gate below follows. Orthogonal to the movement slug — a duck is
// ["waterfowl", "ambient"], a future chicken is ["ambient"] plus whatever
// drives it.
const BehaviorAmbient = "ambient"

// HasBehavior reports whether the sprite carries the given behavior slug.
func (s *Sprite) HasBehavior(slug string) bool {
	if s == nil {
		return false
	}
	for _, b := range s.Behaviors {
		if b == slug {
			return true
		}
	}
	return false
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
