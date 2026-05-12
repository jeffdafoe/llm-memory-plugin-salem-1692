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
	// Mutated by object_refresh + produce_tick subsystems.
	// Zero is the safe default for objects without a stock concept.
	AvailableQuantity int

	// Refreshes — per-attribute need-decrement-on-arrival policies. Empty
	// for objects without refresh effects (decorative trees, plain benches).
	// Multi-attribute objects (a shaded oak refreshing both tiredness from
	// shade and hunger from acorns) carry one entry per attribute.
	Refreshes []*ObjectRefresh
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

// SetStateResult is the outcome of a SetVillageObjectState command.
// Applied=true means the state actually changed. Applied=false means the
// command was a no-op — either superseded by a newer generation, or the
// object was already at the target state, or the object isn't in the
// world. The Reason field carries which.
type SetStateResult struct {
	Applied bool
	Reason  string // "applied" | "superseded" | "already_at_target" | "not_found"
}

// SetVillageObjectState returns a Command that sets a village object's
// current_state to newState. If guardGen is non-zero, the command also
// checks World.WorldEventGen and aborts (Applied=false, Reason="superseded")
// when the current generation has advanced past guardGen — this is how
// scheduled flips from a previous phase transition stay clean when a newer
// transition has already fired.
//
// guardGen=0 disables the check (admin overrides, occupancy refresh that
// just-happened, etc.).
//
// TODO: when the Hub/WS layer ports, broadcast object_state_changed on
// successful apply so clients update their sprites.
func SetVillageObjectState(id VillageObjectID, newState string, guardGen uint64) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if guardGen != 0 && guardGen != w.WorldEventGen.Load() {
				return SetStateResult{Applied: false, Reason: "superseded"}, nil
			}
			obj, ok := w.VillageObjects[id]
			if !ok {
				return SetStateResult{Applied: false, Reason: "not_found"}, nil
			}
			if obj.CurrentState == newState {
				return SetStateResult{Applied: false, Reason: "already_at_target"}, nil
			}
			obj.CurrentState = newState
			return SetStateResult{Applied: true, Reason: "applied"}, nil
		},
	}
}
