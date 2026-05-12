package sim

// Asset / AssetState / AssetSlot / AssetLight / TilesetPack — in-memory port
// of the asset catalog (engine/assets.go data types).
//
// Assets are the DEFINITIONS of placeable things — "Maple Tree", "Market
// Stall (Wood)". An Asset has one or more visual variants (AssetState rows)
// per sheet/source-rect, optionally tagged ('day-active', 'night-active',
// 'lamplighter-target', 'occupied', 'notice-board', etc.) and optionally
// light-emitting (AssetLight for the PointLight2D parameters).
//
// VillageObject is the per-placement INSTANCE — see village_object.go.
//
// Reference state (loaded at startup, hot-reload on SIGHUP). Editor CRUD
// admin endpoints live in the HTTP-layer port; this file is the runtime
// data model only.

// AssetID is a slug-style identifier ("tree-maple", "stall-wood", etc.).
type AssetID string

// AssetStateID is the SERIAL primary key from asset_state.
type AssetStateID int

// TilesetPack groups asset sheets that came from one source tileset.
type TilesetPack struct {
	ID   string
	Name string
	URL  *string
}

// AssetSlot defines a named attachment point on an asset (e.g. campfire's
// "top" slot where a pot can be placed). Overlay assets declare which slot
// they fit via Asset.FitsSlot.
type AssetSlot struct {
	SlotName string
	OffsetX  int
	OffsetY  int
}

// Asset is the catalog entry for one logical placeable thing.
type Asset struct {
	ID           AssetID
	Name         string
	Category     string // "tree" | "nature" | "structure" | "prop"
	DefaultState string
	AnchorX      float64
	AnchorY      float64
	Layer        string // "objects" | "above"
	PackID       *string
	FitsSlot     *string
	ZIndex       int  // Godot CanvasItem z; <0 renders below NPCs
	IsObstacle   bool // pathfind treats as blocked
	IsPassage    bool // bridges / passages that override IsObstacle for movement

	// Per-side footprint counts (tiles from anchor in each cardinal
	// direction, anchor tile always included).
	FootprintLeft   int
	FootprintRight  int
	FootprintTop    int
	FootprintBottom int

	// Door tile offset in tiles from placement origin (nullable for
	// non-structures or structures without a door). Home-routing falls
	// back to findPathToAdjacent when nil.
	DoorOffsetX *int
	DoorOffsetY *int

	// VisibleWhenInside hides/shows the villager sprite when inside=true.
	// Default false hides (plain houses); true for see-through stalls.
	VisibleWhenInside bool

	// StandOffsetX/Y is a pure-render position offset for NPCs inside a
	// visible_when_inside structure. NPC walks to the door tile;
	// post-arrival the client repositions them to anchor + stand_offset.
	StandOffsetX *int
	StandOffsetY *int

	// TransitionSpreadSeconds — used by the phase-change flip mechanism
	// to scatter per-object state changes uniformly in [0, N) seconds
	// rather than all firing instantly. Zero = immediate.
	TransitionSpreadSeconds int

	Pack   *TilesetPack
	States []AssetState
	Slots  []AssetSlot
}

// FindState returns the AssetState with the given state name, or nil.
func (a *Asset) FindState(state string) *AssetState {
	for i := range a.States {
		if a.States[i].State == state {
			return &a.States[i]
		}
	}
	return nil
}

// StateForTag returns the first AssetState carrying tag (deterministic by
// ID), or nil if no state on this asset has the tag. Used by world_phase
// to resolve "what's the day-active state for this asset?".
func (a *Asset) StateForTag(tag string) *AssetState {
	var best *AssetState
	for i := range a.States {
		for _, t := range a.States[i].Tags {
			if t == tag {
				s := &a.States[i]
				if best == nil || s.ID < best.ID {
					best = s
				}
				break
			}
		}
	}
	return best
}

// AssetState is one visual variant of an asset. Animated states have
// FrameCount > 1 (frames consecutive horizontally in the sheet).
type AssetState struct {
	ID         AssetStateID
	State      string // "default" | "open" | "closed" | "lit" | "unlit" | ...
	Sheet      string
	SrcX       int
	SrcY       int
	SrcW       int
	SrcH       int
	FrameCount int
	FrameRate  float64
	Tags       []string // 'day-active', 'lamplighter-target', 'occupied', ...
	Light      *AssetLight
}

// HasTag returns true if this state carries tag.
func (s *AssetState) HasTag(tag string) bool {
	for _, t := range s.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// AssetLight describes the PointLight2D parameters for a light-emitting
// state. Only lit states have a populated Light field; the client attaches
// a PointLight2D to the sprite at runtime.
type AssetLight struct {
	Color            string  // hex #RRGGBB
	Radius           int     // world pixels
	Energy           float64 // brightness multiplier
	OffsetX          int     // light center offset from sprite origin
	OffsetY          int
	FlickerAmplitude float64 // 0 = steady
	FlickerPeriodMs  int
}
