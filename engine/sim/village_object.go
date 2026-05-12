package sim

// VillageObject — per-placement instance of an Asset on the village map.
// In-memory port of the legacy village_object + village_object_tag tables.
//
// Hot state: current_state mutates at phase transitions, occupancy refresh,
// admin override, owner change, etc. Position (X, Y) and loiter offsets can
// change via admin "move" or "set loiter offset". Tags are per-instance
// (different from AssetState.Tags which are per-state). Display name, owner,
// attached-to, entry-policy are admin-settable.
//
// Per-aggregate persistence: VillageObjectsRepo owns village_object +
// village_object_tag together; the repo loads and saves them as one entity.

// VillageObjectID is a UUID (gen_random_uuid in PG).
type VillageObjectID string

// EntryPolicy controls who may enter a structure object. Mirrors the
// legacy entry_policy column values.
type EntryPolicy string

const (
	EntryPolicyDefault EntryPolicy = ""           // type-driven default
	EntryPolicyOpen    EntryPolicy = "open"       // anyone may enter
	EntryPolicyOwner   EntryPolicy = "owner-only" // only owner + lodgers
	EntryPolicyClosed  EntryPolicy = "closed"     // no one enters
)

// VillageObject is one placement on the village map.
type VillageObject struct {
	ID           VillageObjectID
	AssetID      AssetID
	CurrentState string

	// World coordinates (pixels) of the anchor point. tileSize=32; tile
	// coords are derived via X/tileSize floor.
	X float64
	Y float64

	// Admin metadata.
	PlacedBy    string // llm-memory agent slug; "" if seeded by system
	Owner       string // llm-memory agent slug; "" if unowned
	DisplayName string // override for the catalog name; "" = use Asset.Name
	EntryPolicy EntryPolicy

	// AttachedTo links overlay objects (lamp on a wagon, etc.) to their
	// parent VillageObject. Empty for top-level placements.
	AttachedTo VillageObjectID

	// Loiter offset — where visitors / loitering NPCs stand relative to
	// the object's anchor tile, in tile units. Nil = use catalog default.
	LoiterOffsetX *int
	LoiterOffsetY *int

	// Per-instance tags. Different from AssetState.Tags (which are
	// per-state on the catalog). Examples: "vendor", "innkeeper",
	// "lamplighter-stop", etc.
	Tags []string

	// AvailableQuantity is the runtime stock counter for objects with
	// produce/refresh policies (gatherables, vendor inventory, etc.).
	// Mutated by object_refresh + produce_tick subsystems (ported later).
	// Zero is the safe default for objects without a stock concept.
	AvailableQuantity int
}

// HasTag returns true if this instance carries tag.
func (o *VillageObject) HasTag(tag string) bool {
	for _, t := range o.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// EffectiveDisplayName returns DisplayName if set, otherwise the supplied
// catalog name (Asset.Name). Convenience for rendering — callsites usually
// have the Asset in hand alongside the VillageObject.
func (o *VillageObject) EffectiveDisplayName(catalogName string) string {
	if o.DisplayName != "" {
		return o.DisplayName
	}
	return catalogName
}

// EffectiveLoiterOffset returns the loiter offset (X, Y), falling back to
// the supplied catalog defaults if either field is nil. Returned as int
// values rather than pointers for callsite ergonomics.
func (o *VillageObject) EffectiveLoiterOffset(catalogX, catalogY int) (int, int) {
	x := catalogX
	y := catalogY
	if o.LoiterOffsetX != nil {
		x = *o.LoiterOffsetX
	}
	if o.LoiterOffsetY != nil {
		y = *o.LoiterOffsetY
	}
	return x, y
}
