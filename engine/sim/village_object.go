package sim

import (
	"errors"
	"math"
	"sort"
	"time"
)

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
	EntryPolicyOwner   EntryPolicy = "owner-only" // members only — see structureMembershipAllows
	EntryPolicyClosed  EntryPolicy = "closed"     // no one enters (no interior)
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
	DisplayName string // override for the catalog name; "" = use Asset.Name
	EntryPolicy EntryPolicy

	// OwnerActorID is the single owning actor of this structure ("" if
	// unowned). A typed reference into World.Actors, not a stringly-typed
	// slug — ownership is one input to entry access, not the whole story.
	// Entry for an EntryPolicyOwner structure is a MEMBERSHIP check
	// (owner OR resident OR staff OR lodger — see structureMembershipAllows),
	// which is why a single owner suffices and co-ownership isn't needed:
	// a family enters their home by being residents (shared
	// HomeStructureID), not by co-owning it.
	OwnerActorID ActorID

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

// CloneVillageObject returns a deep copy suitable for publication via
// Snapshot or for the mem-repo serialization boundary. Tags slice and
// Refreshes slice (plus each ObjectRefresh AND its scalar pointer
// fields) are cloned so world-side mutations do not leak through the
// copy AND so snapshot readers cannot mutate world state via the
// pointer fields.
//
// Scalar pointer fields on ObjectRefresh (AvailableQuantity, MaxQuantity,
// RefreshPeriodHours, LastRefreshAt, DwellDelta, DwellPeriodMinutes) are
// individually deep-copied. Aliasing them would let a snapshot reader
// write `*snap.VillageObjects[id].Refreshes[0].AvailableQuantity = N` and
// mutate the int reachable from the live world — code_review caught this
// in the first cleanup pass.
func CloneVillageObject(v *VillageObject) *VillageObject {
	if v == nil {
		return nil
	}
	cp := *v
	if v.Tags != nil {
		cp.Tags = append([]string(nil), v.Tags...)
	}
	if v.Refreshes != nil {
		cp.Refreshes = make([]*ObjectRefresh, len(v.Refreshes))
		for i, r := range v.Refreshes {
			if r == nil {
				continue
			}
			rc := *r
			if r.AvailableQuantity != nil {
				x := *r.AvailableQuantity
				rc.AvailableQuantity = &x
			}
			if r.MaxQuantity != nil {
				x := *r.MaxQuantity
				rc.MaxQuantity = &x
			}
			if r.RefreshPeriodHours != nil {
				x := *r.RefreshPeriodHours
				rc.RefreshPeriodHours = &x
			}
			if r.LastRefreshAt != nil {
				t := *r.LastRefreshAt
				rc.LastRefreshAt = &t
			}
			if r.DwellDelta != nil {
				x := *r.DwellDelta
				rc.DwellDelta = &x
			}
			if r.DwellPeriodMinutes != nil {
				x := *r.DwellPeriodMinutes
				rc.DwellPeriodMinutes = &x
			}
			cp.Refreshes[i] = &rc
		}
	}
	return &cp
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
			setVillageObjectStateInline(w, obj, newState)
			return SetStateResult{Applied: true, Reason: "applied"}, nil
		},
	}
}

// setVillageObjectStateInline mutates obj.CurrentState to newState and emits
// VillageObjectStateChanged (which the httpapi hub translates to the
// object_state_changed client frame). The single mutate+emit path shared by
// SetVillageObjectState and the occupancy reactor (occupancy.go) — callers are
// responsible for the no-op short-circuit (newState == current) so this never
// emits a spurious same-state change.
//
// MUST be called from inside a Command.Fn (mutates world state + emits).
func setVillageObjectStateInline(w *World, obj *VillageObject, newState string) {
	prev := obj.CurrentState
	obj.CurrentState = newState
	w.emit(&VillageObjectStateChanged{
		ObjectID:  obj.ID,
		FromState: prev,
		ToState:   newState,
		At:        time.Now().UTC(),
	})
}

// Admin object commands (MoveVillageObject / DeleteVillageObject) back the
// admin/editor write routes. Both run on the world goroutine via a Command and,
// on success, emit a bus event the httpapi hub broadcasts (object_moved /
// object_deleted). Neither writes through to Postgres directly: the next
// gen-marker checkpoint UPSERTs the moved row and prunes the deleted one via
// its delete-not-present sweep, so the durable store converges on the next
// SaveSnapshot. A crash before that checkpoint loses the move/delete — the same
// restart-loss posture every other in-memory mutation has.

// ErrVillageObjectNotFound is returned by the admin object commands when no
// village object has the given id (→ 404 at the HTTP layer).
var ErrVillageObjectNotFound = errors.New("village object not found")

// ErrInvalidObjectPosition is returned by MoveVillageObject when the target
// coordinate is non-finite (NaN or ±Inf). The HTTP layer rejects these before
// the command runs, but the command is exported and guards the invariant for
// any direct caller — a NaN/Inf coordinate would corrupt JSON serialization and
// the checkpoint (→ 400 at the HTTP layer).
var ErrInvalidObjectPosition = errors.New("invalid object position")

// ErrVillageObjectIsStructure is returned by DeleteVillageObject when the
// target object backs a Structure. A building is the shared-identity bridge
// (structure_anchors.go): its StructureID and VillageObjectID are the same
// UUID, so deleting the placement would orphan the live Structure aggregate
// (occupants bound via Inside/Home/WorkStructureID, ownership, anchored
// huddles). The command refuses; tearing down a structure is a separate,
// larger operation (→ 422 at the HTTP layer).
var ErrVillageObjectIsStructure = errors.New("village object backs a structure")

// MoveObjectResult is the outcome of a MoveVillageObject command — the object
// id and its new absolute world-pixel anchor.
type MoveObjectResult struct {
	ID VillageObjectID
	X  float64
	Y  float64
}

// MoveVillageObject returns a Command that repositions a village object to
// (x, y), absolute world-pixel anchor coordinates (the same space ObjectDTO
// emits, NOT actor tile coords). Returns ErrVillageObjectNotFound if no object
// has the id. On success it mutates X/Y in place and emits VillageObjectMoved →
// the object_moved broadcast.
//
// Moves only the targeted object. An overlay attached to it (AttachedTo) is
// rendered by the client at the parent's anchor plus the asset slot offset, so
// a parent move carries its overlays on screen without moving their rows here;
// independent repositioning of an attached overlay is left to a follow-up if a
// live run shows it's needed.
func MoveVillageObject(id VillageObjectID, x, y float64) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) {
				return nil, ErrInvalidObjectPosition
			}
			obj, ok := w.VillageObjects[id]
			if !ok {
				return nil, ErrVillageObjectNotFound
			}
			obj.X = x
			obj.Y = y
			w.emit(&VillageObjectMoved{
				ObjectID: id,
				X:        x,
				Y:        y,
				At:       time.Now().UTC(),
			})
			return MoveObjectResult{ID: id, X: x, Y: y}, nil
		},
	}
}

// DeleteObjectResult is the outcome of a DeleteVillageObject command.
// DeletedIDs lists every object removed — the target plus any overlay objects
// transitively attached to it — with children before the parent they hung off.
type DeleteObjectResult struct {
	DeletedIDs []VillageObjectID
}

// DeleteVillageObject returns a Command that removes a village object and every
// overlay object attached to it (transitively, mirroring the pg attached_to
// ON DELETE CASCADE so the in-memory world and a later checkpoint agree).
// Returns ErrVillageObjectNotFound if the object is absent, or
// ErrVillageObjectIsStructure if it backs a Structure (refused — see that
// error). On success it deletes the rows from World.VillageObjects and emits
// one VillageObjectDeleted per removed id → object_deleted broadcasts, children
// first.
func DeleteVillageObject(id VillageObjectID) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if _, ok := w.VillageObjects[id]; !ok {
				return nil, ErrVillageObjectNotFound
			}
			if _, ok := w.Structures[StructureID(id)]; ok {
				return nil, ErrVillageObjectIsStructure
			}
			removed := deleteObjectCascade(w, id)
			now := time.Now().UTC()
			for _, rid := range removed {
				w.emit(&VillageObjectDeleted{ObjectID: rid, At: now})
			}
			return DeleteObjectResult{DeletedIDs: removed}, nil
		},
	}
}

// deleteObjectCascade removes root and every object transitively attached to it
// from w.VillageObjects, returning the removed ids in post-order: every
// descendant is emitted before the object it is attached to, so a delete always
// reports (and broadcasts) a child overlay before its parent. It builds the
// attached_to adjacency (parent → children) up front so the map is never
// mutated mid-range; children slices are sorted for a deterministic delete/emit
// order, and a seen-set makes a pathological attached_to cycle (which the
// schema's self-FK doesn't structurally prevent) terminate.
func deleteObjectCascade(w *World, root VillageObjectID) []VillageObjectID {
	children := make(map[VillageObjectID][]VillageObjectID)
	for id, obj := range w.VillageObjects {
		if obj != nil {
			children[obj.AttachedTo] = append(children[obj.AttachedTo], id)
		}
	}
	for parent := range children {
		kids := children[parent]
		sort.Slice(kids, func(i, j int) bool { return kids[i] < kids[j] })
	}

	seen := make(map[VillageObjectID]bool)
	var removed []VillageObjectID
	var visit func(VillageObjectID)
	visit = func(id VillageObjectID) {
		if seen[id] {
			return
		}
		seen[id] = true
		for _, childID := range children[id] {
			visit(childID)
		}
		delete(w.VillageObjects, id)
		removed = append(removed, id)
	}
	visit(root)
	return removed
}
