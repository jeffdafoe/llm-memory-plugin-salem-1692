package sim

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
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

	// World-pixel coordinates of the anchor point. WorldPos (geom.go); tile
	// coords via Pos.Tile(). Was an X/Y float64 pair — folded into WorldPos so
	// an object's pixel position can never be mixed with a tile coordinate.
	Pos WorldPos

	// Admin metadata.
	PlacedBy    string // llm-memory agent slug; "" if seeded by system
	DisplayName string // override for the catalog name; "" = use Asset.Name
	EntryPolicy EntryPolicy

	// OwnerActorID is the single owning actor of this object ("" if
	// unowned). A typed reference into World.Actors, not a stringly-typed
	// slug — ownership is one input to access, not the whole story.
	// For a STRUCTURE, entry under EntryPolicyOwner is a MEMBERSHIP check
	// (owner OR resident OR staff OR lodger — see structureMembershipAllows),
	// which is why a single owner suffices and co-ownership isn't needed:
	// a family enters their home by being residents (shared HomeStructureID),
	// not by co-owning it. For a gatherable/refreshable object (a berry bush),
	// it instead drives the strict owner-only gather/eat gate (LLM-50 D2 —
	// see OwnedByOther): owned ⇒ owner-only, unowned ⇒ commons.
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

	// Wear is the accumulated maintenance demand on an owned business (LLM-118,
	// generalized in LLM-247), accrued in proportion to the coin the owner turns
	// over at it (commitPayTransfer). Crossing the repair threshold warrants a
	// repair; crossing the degrade threshold closes it for trade until mended; a
	// repair resets it to 0. Durable (checkpointed). Zero for every object that
	// isn't a wearable business (see IsWearableStall / TagBusiness scope).
	Wear int

	// Refreshes — per-attribute need-decrement-on-arrival policies. Empty
	// for objects without refresh effects (decorative trees, plain benches).
	// Multi-attribute objects (a shaded oak refreshing both tiredness from
	// shade and hunger from acorns) carry one entry per attribute.
	Refreshes []*ObjectRefresh
}

// OwnedByOther reports whether this object has an owner other than actorID —
// the strict-owner gate (LLM-50 decision D2) for gather/eat on a placed
// VillageObject. An unowned object ("" owner) is commons (anyone may gather/
// eat), and the owner is always allowed; only a non-owner standing at an OWNED
// object is gated out.
//
// Deliberately NARROWER than the EntryPolicyOwner structure-entry rule (owner
// OR resident OR staff OR lodger — structureMembershipAllows): a bush has no
// residency/staff/lodging concept, so harvesting at an owned object is
// owner-only. Used by the four gather/eat touch-points (Gather command,
// ApplyObjectRefreshAtArrival, the gatherable cue, and the free-source list).
//
// Nil-safe: a nil object is treated as not-owned-by-anyone (commons), so a
// stray nil from a future caller degrades to "allowed" rather than panicking —
// consistent with the unowned-is-commons rule.
func (o *VillageObject) OwnedByOther(actorID ActorID) bool {
	if o == nil {
		return false
	}
	return o.OwnerActorID != "" && o.OwnerActorID != actorID
}

// IsFiniteGatherableSource reports whether the object is a FINITE gatherable
// source — a bush: you pick an exhaustible yield (a finite gather_item row) from
// it (LLM-87). This distinguishes a bush from a WELL, which is gatherable too
// (you can draw water) but INFINITE — no stock to exhaust. An NPC eats a bush via
// gather -> consume (so it does not auto-eat there on arrival), while a well keeps
// its arrival + dwell drink path. Nil-safe, mirroring OwnedByOther.
func (o *VillageObject) IsFiniteGatherableSource() bool {
	if o == nil {
		return false
	}
	for _, r := range o.Refreshes {
		if r != nil && r.IsGatherable() && r.IsFinite() {
			return true
		}
	}
	return false
}

// HasForageSourceFor reports whether the object carries a forage-to-sell refresh
// row for item — the object-side half of the forage warrant's actionability gate
// (actorRemembersForageSource, restock_tick.go). Shares ObjectRefresh.
// IsForageToSellFor with the perception cue so the warrant and the
// "## Your bushes to harvest" section agree on what's harvestable (LLM-90).
// Nil-safe, mirroring OwnedByOther / IsFiniteGatherableSource.
func (o *VillageObject) HasForageSourceFor(item ItemKind) bool {
	if o == nil {
		return false
	}
	for _, r := range o.Refreshes {
		if r.IsForageToSellFor(item) {
			return true
		}
	}
	return false
}

// ConfigWarnings returns one human-readable warning per village object that is
// misconfigured in a way the engine TOLERATES but an operator should fix. It is
// advisory only — never fatal — and is surfaced both at boot (logged) and on the
// umbilical /state read (so a defect introduced by a migration is visible without
// SSH access).
//
// Sole check today (LLM-60): a refresh-bearing object (a gather/eat source) with
// an empty DisplayName. The command-side resolver resolveLoiteringObject skips
// nameless objects, so neither the gather verb (Gather/StartHarvest) nor passive
// eat-on-arrival (ApplyObjectRefreshAtArrival) can resolve it — yet the perception
// cue (findGatherableCue) and the free-food list (gatherFreeSatiationSources) do
// NOT apply that name filter, so they keep advertising a source the engine then
// refuses, trapping an NPC in a gather/eat loop. Naming the object is the fix; the
// name requirement in the resolver is intentional (v1 "you are at X" attribution).
//
// Sorted by object id for a stable order across reads.
func ConfigWarnings(objects map[VillageObjectID]*VillageObject) []string {
	var warnings []string
	for id, obj := range objects {
		if obj == nil || len(obj.Refreshes) == 0 {
			continue
		}
		if strings.TrimSpace(obj.DisplayName) != "" {
			continue
		}
		kind := "eat-on-arrival source"
		for _, r := range obj.Refreshes {
			if r.IsGatherable() {
				kind = "gatherable source"
				break
			}
		}
		warnings = append(warnings, fmt.Sprintf(
			"village_object %s is a %s with no display_name — gather/eat-on-arrival cannot resolve it (resolveLoiteringObject skips nameless objects)",
			id, kind))
	}
	sort.Strings(warnings)
	return warnings
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
	// append([]string(nil), empty...) returns nil, which pgx encodes as SQL
	// NULL and the tags TEXT[] NOT NULL column rejects — aborting the whole
	// checkpoint. make([]string, 0, len) keeps the clone non-nil for an
	// empty source, matching the repo's "tags is always non-nil" invariant.
	cp.Tags = append(make([]string, 0, len(v.Tags)), v.Tags...)
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

// TagBusiness marks a village object — and, via the shared structure↔object id,
// the structure it co-locates with — as a place of trade where a worker can seek
// paid work (a shop, smithy, tavern, inn, market stall, or farm). Curated on
// objects through the editor's tag tool; read by the seek-work directional cue
// (LLM-152).
const TagBusiness = "business"

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

// SetStateResult is the outcome of a SetVillageObjectState (or
// ApplyScheduledFlip) command. Applied=true means the state actually
// changed. Applied=false means the command was a no-op — superseded by a
// newer pass of the flip's own subsystem (ApplyScheduledFlip only), or
// the object was already at the target state, or the object isn't in
// the world. The Reason field carries which.
type SetStateResult struct {
	Applied bool
	Reason  string // "applied" | "superseded" | "already_at_target" | "not_found" | "unknown_domain"
}

// SetVillageObjectState returns a Command that sets a village object's
// current_state to newState — unconditionally (no-op when already there).
// Scheduled phase/rotation flips go through ApplyScheduledFlip
// (world_phase.go), which layers the supersede-by-generation staleness
// check on top of this; the guard lived here as a guardGen param until
// ZBBS-HOME-447 moved it to the flip mechanism it belongs to.
//
// A successful apply emits VillageObjectStateChanged, which the httpapi hub
// translates to the object_state_changed client frame so clients update their
// sprites.
func SetVillageObjectState(id VillageObjectID, newState string) Command {
	return Command{
		Fn: func(w *World) (any, error) {
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
			obj.Pos = WorldPos{X: x, Y: y}
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

// ErrUnknownAsset is returned by CreateVillageObject when asset_id does not
// resolve in the loaded asset catalog (→ 400 at the HTTP layer).
var ErrUnknownAsset = errors.New("unknown asset")

// CreateObjectResult is the outcome of a CreateVillageObject command — the
// newly placed object (with its freshly minted id).
type CreateObjectResult struct {
	Object *VillageObject
}

// CreateVillageObject returns a Command that places a new village object of the
// given asset at (x, y) in absolute world-pixel space. Mirrors v1
// handleCreateVillageObject: the id is a fresh UUID, current_state comes from
// the asset's default state, and entry_policy defaults to open when the asset
// has a configured doorway (the placement is enterable) else closed (decorative
// — the editor can override per instance afterward). A non-empty attachedTo
// hangs the placement off an existing object as an overlay. Emits
// VillageObjectCreated → object_created so every client renders the new object.
// Returns ErrUnknownAsset (bad asset_id), ErrVillageObjectNotFound (bad
// attached_to), or ErrInvalidObjectPosition (non-finite coords).
func CreateVillageObject(assetID AssetID, x, y float64, attachedTo VillageObjectID, placedBy string) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) {
				return nil, ErrInvalidObjectPosition
			}
			asset, ok := w.Assets[assetID]
			if !ok || asset == nil {
				return nil, ErrUnknownAsset
			}
			if attachedTo != "" {
				if _, ok := w.VillageObjects[attachedTo]; !ok {
					return nil, ErrVillageObjectNotFound
				}
			}
			entryPolicy := EntryPolicyClosed
			if asset.DoorOffsetX != nil && asset.DoorOffsetY != nil {
				entryPolicy = EntryPolicyOpen
			}
			obj := &VillageObject{
				ID:           VillageObjectID(newUUIDv4()),
				AssetID:      assetID,
				CurrentState: asset.DefaultState,
				Pos:          WorldPos{X: x, Y: y},
				PlacedBy:     placedBy,
				EntryPolicy:  entryPolicy,
				AttachedTo:   attachedTo,
				Tags:         []string{},
			}
			w.VillageObjects[obj.ID] = obj
			w.emit(&VillageObjectCreated{
				ObjectID:     obj.ID,
				AssetID:      assetID,
				CurrentState: obj.CurrentState,
				X:            x,
				Y:            y,
				PlacedBy:     placedBy,
				EntryPolicy:  entryPolicy,
				AttachedTo:   attachedTo,
				At:           time.Now().UTC(),
			})
			return CreateObjectResult{Object: obj}, nil
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

// Admin metadata commands (SetVillageObjectOwner / SetVillageObjectLoiterOffset
// / SetVillageObjectEntryPolicy) back the editor's object-metadata write routes.
// Each runs on the world goroutine via a Command and mutates one VillageObject
// field. owner and entry policy emit NO bus event: they're admin-only labels the
// editing client re-reads from the save response, so a change needs no broadcast.
// Loiter offset DOES emit (VillageObjectLoiterOffsetChanged) since ZBBS-HOME-289
// put it in ObjectDTO — the loiter pin is editor-visible, so a live editor needs
// the change. Same restart-loss-until-checkpoint persistence as the other admin
// object commands — the next gen-marker SaveSnapshot converges the durable store.

// ErrOwnerActorNotFound is returned by SetVillageObjectOwner when a non-empty
// owner actor id does not resolve to a live actor (→ 422 at the HTTP layer).
// Clearing the owner (empty id) is always allowed and never returns this.
var ErrOwnerActorNotFound = errors.New("owner actor not found")

// ErrInvalidEntryPolicy is returned by SetVillageObjectEntryPolicy when the
// policy is not one of the four EntryPolicy values (→ 400 at the HTTP layer).
var ErrInvalidEntryPolicy = errors.New("invalid entry policy")

// SetOwnerResult / SetLoiterOffsetResult / SetEntryPolicyResult echo the applied
// value back to the HTTP layer. There's no broadcast for these metadata
// changes, so the command result is the only confirmation the editor gets.
type SetOwnerResult struct {
	ID           VillageObjectID
	OwnerActorID ActorID
}

type SetLoiterOffsetResult struct {
	ID VillageObjectID
	X  *int
	Y  *int
}

type SetEntryPolicyResult struct {
	ID          VillageObjectID
	EntryPolicy EntryPolicy
}

// SetVillageObjectOwner sets (or clears) a village object's owning actor.
// An empty ownerActorID clears ownership (unowned). A non-empty id must resolve
// to a live actor in World.Actors — OwnerActorID is a typed reference, so
// refusing a dangling id (ErrOwnerActorNotFound) keeps that reference honest.
// Returns ErrVillageObjectNotFound if the object is absent. Emits no event —
// owner is not in ObjectDTO, so the change is client-invisible.
func SetVillageObjectOwner(id VillageObjectID, ownerActorID ActorID) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			obj, ok := w.VillageObjects[id]
			if !ok {
				return nil, ErrVillageObjectNotFound
			}
			if ownerActorID != "" {
				if _, ok := w.Actors[ownerActorID]; !ok {
					return nil, ErrOwnerActorNotFound
				}
			}
			obj.OwnerActorID = ownerActorID
			return SetOwnerResult{ID: id, OwnerActorID: ownerActorID}, nil
		},
	}
}

// SetVillageObjectLoiterOffset sets (or clears) a village object's loiter
// offset — where loitering/visiting actors stand relative to its anchor tile,
// in tile units. A nil x or y clears that axis back to the catalog default (see
// EffectiveLoiterOffset). The HTTP layer enforces both-or-neither; this command
// faithfully applies whatever pointers it's given, because an axis-independent
// override is a legal world state. The pointed-to ints are copied so world
// state never aliases a caller-owned pointer. Returns ErrVillageObjectNotFound
// if the object is absent.
//
// Emits VillageObjectLoiterOffsetChanged (→ object_loiter_offset_changed) once
// loiter is in ObjectDTO (ZBBS-HOME-289) — the loiter pin is editor-visible, so
// a live editor updates on the change (unlike owner/entry-policy, which stay
// re-read-on-save). The event carries both the raw override and the resolved
// effective offset, computed via EffectiveLoiterOffset against the object's
// asset (nil-asset-safe).
func SetVillageObjectLoiterOffset(id VillageObjectID, x, y *int) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			obj, ok := w.VillageObjects[id]
			if !ok {
				return nil, ErrVillageObjectNotFound
			}
			obj.LoiterOffsetX = copyIntPtr(x)
			obj.LoiterOffsetY = copyIntPtr(y)
			effX, effY := EffectiveLoiterOffset(obj, w.Assets[obj.AssetID])
			w.emit(&VillageObjectLoiterOffsetChanged{
				ObjectID:               id,
				LoiterOffsetX:          obj.LoiterOffsetX,
				LoiterOffsetY:          obj.LoiterOffsetY,
				EffectiveLoiterOffsetX: effX,
				EffectiveLoiterOffsetY: effY,
				At:                     time.Now().UTC(),
			})
			return SetLoiterOffsetResult{ID: id, X: obj.LoiterOffsetX, Y: obj.LoiterOffsetY}, nil
		},
	}
}

// copyIntPtr returns a fresh pointer to a copy of *p, or nil when p is nil — so
// a stored pointer never aliases the caller's int.
func copyIntPtr(p *int) *int {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// SetVillageObjectEntryPolicy sets a village object's entry policy. policy must
// be one of the four EntryPolicy values ("" = type default, "open",
// "owner-only", "closed"); an unknown value is ErrInvalidEntryPolicy. Returns
// ErrVillageObjectNotFound if the object is absent. Emits no event — entry
// policy is not in ObjectDTO.
func SetVillageObjectEntryPolicy(id VillageObjectID, policy EntryPolicy) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			switch policy {
			case EntryPolicyDefault, EntryPolicyOpen, EntryPolicyOwner, EntryPolicyClosed:
				// valid
			default:
				return nil, ErrInvalidEntryPolicy
			}
			obj, ok := w.VillageObjects[id]
			if !ok {
				return nil, ErrVillageObjectNotFound
			}
			obj.EntryPolicy = policy
			return SetEntryPolicyResult{ID: id, EntryPolicy: policy}, nil
		},
	}
}

// Client-visible metadata commands (SetVillageObjectDisplayName / AddVillageObjectTag
// / RemoveVillageObjectTag) back the editor's display-name + tag write routes.
// Unlike the owner/loiter/entry-policy trio above, display_name and tags ARE in
// ObjectDTO, so a change is visible to connected clients and emits a per-field
// bus event the httpapi hub broadcasts (object_display_name_changed /
// village_object_tags_updated — WS seam settled with work, mail 6aad4f26). Same
// restart-loss-until-checkpoint persistence as the other admin object commands.
// Each follows setVillageObjectStateInline's discipline: a no-op (same name, or
// add/remove that doesn't change the set) mutates nothing and emits nothing.

// MaxVillageObjectDisplayNameLen caps a display name's rune length. A display
// name is a short label rendered in the client; 100 runes is generous for a
// place name without letting a pathological value bloat the DTO / WS frame.
const MaxVillageObjectDisplayNameLen = 100

// MaxVillageObjectTagLen caps a single tag's rune length. Tags are short
// identifier-like labels ("vendor", "lamplighter-stop"); 64 runes is ample.
const MaxVillageObjectTagLen = 64

// ErrInvalidDisplayName is returned by SetVillageObjectDisplayName when the
// (trimmed) name exceeds MaxVillageObjectDisplayNameLen or carries a control
// character (→ 400 at the HTTP layer). An empty name is valid — it clears the
// override.
var ErrInvalidDisplayName = errors.New("invalid display name")

// ErrInvalidTag is returned by AddVillageObjectTag / RemoveVillageObjectTag when
// the (trimmed) tag is empty, exceeds MaxVillageObjectTagLen, or carries a
// control character (→ 400 at the HTTP layer).
var ErrInvalidTag = errors.New("invalid tag")

// SetDisplayNameResult / SetTagsResult echo the post-mutation value back to the
// HTTP layer. SetTagsResult.Tags is the authoritative full set (a fresh copy,
// never the live world slice), matching the VillageObjectTagsUpdated payload.
type SetDisplayNameResult struct {
	ID          VillageObjectID
	DisplayName string
}

type SetTagsResult struct {
	ID   VillageObjectID
	Tags []string
}

// containsControlChar reports whether s carries any C0 control character or DEL.
// Display names and tags are client-visible persisted text; a control byte would
// be a typo at best and corrupt the DTO/WS frame at worst. Space (0x20) and
// printable runes pass; this is intentionally stricter than the speak/reason
// freeform fields (no \n\r\t exemption — a label is single-line).
func containsControlChar(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// SetVillageObjectDisplayName sets (or clears) a village object's display-name
// override. The name is trimmed; an empty result clears the override (the client
// falls back to the catalog name via EffectiveDisplayName). A non-empty name must
// be within MaxVillageObjectDisplayNameLen and free of control characters, else
// ErrInvalidDisplayName. Returns ErrVillageObjectNotFound if the object is
// absent. Emits VillageObjectDisplayNameChanged ONLY when the name actually
// changes — a same-name call is a no-op that emits nothing.
func SetVillageObjectDisplayName(id VillageObjectID, name string) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			trimmed := strings.TrimSpace(name)
			if utf8.RuneCountInString(trimmed) > MaxVillageObjectDisplayNameLen || containsControlChar(trimmed) {
				return nil, ErrInvalidDisplayName
			}
			obj, ok := w.VillageObjects[id]
			if !ok {
				return nil, ErrVillageObjectNotFound
			}
			if obj.DisplayName == trimmed {
				return SetDisplayNameResult{ID: id, DisplayName: trimmed}, nil
			}
			obj.DisplayName = trimmed
			w.emit(&VillageObjectDisplayNameChanged{
				ObjectID:    id,
				DisplayName: trimmed,
				At:          time.Now().UTC(),
			})
			return SetDisplayNameResult{ID: id, DisplayName: trimmed}, nil
		},
	}
}

// AddVillageObjectTag adds tag to a village object's per-instance tag set. The
// tag is trimmed and validated (non-empty, within MaxVillageObjectTagLen, no
// control characters; else ErrInvalidTag). Adding a tag already present is a
// no-op — the set stays deduplicated and no event fires. Returns
// ErrVillageObjectNotFound if the object is absent. On an actual add it emits
// VillageObjectTagsUpdated carrying the full post-mutation set.
func AddVillageObjectTag(id VillageObjectID, tag string) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			trimmed := strings.TrimSpace(tag)
			if trimmed == "" || utf8.RuneCountInString(trimmed) > MaxVillageObjectTagLen || containsControlChar(trimmed) {
				return nil, ErrInvalidTag
			}
			obj, ok := w.VillageObjects[id]
			if !ok {
				return nil, ErrVillageObjectNotFound
			}
			if obj.HasTag(trimmed) {
				return SetTagsResult{ID: id, Tags: append([]string(nil), obj.Tags...)}, nil
			}
			obj.Tags = append(obj.Tags, trimmed)
			tagsCopy := append([]string(nil), obj.Tags...)
			w.emit(&VillageObjectTagsUpdated{
				ObjectID: id,
				Tags:     tagsCopy,
				At:       time.Now().UTC(),
			})
			return SetTagsResult{ID: id, Tags: tagsCopy}, nil
		},
	}
}

// RemoveVillageObjectTag removes tag from a village object's tag set. The tag is
// trimmed and validated identically to AddVillageObjectTag (ErrInvalidTag on a
// bad value — so a malformed tag is a 400 whether you're adding or removing it).
// Removing a tag that isn't present is a no-op — no event fires. Returns
// ErrVillageObjectNotFound if the object is absent. On an actual removal it emits
// VillageObjectTagsUpdated carrying the full post-mutation set (an empty slice
// when the last tag was removed).
func RemoveVillageObjectTag(id VillageObjectID, tag string) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			trimmed := strings.TrimSpace(tag)
			if trimmed == "" || utf8.RuneCountInString(trimmed) > MaxVillageObjectTagLen || containsControlChar(trimmed) {
				return nil, ErrInvalidTag
			}
			obj, ok := w.VillageObjects[id]
			if !ok {
				return nil, ErrVillageObjectNotFound
			}
			if !obj.HasTag(trimmed) {
				return SetTagsResult{ID: id, Tags: append([]string(nil), obj.Tags...)}, nil
			}
			kept := make([]string, 0, len(obj.Tags))
			for _, t := range obj.Tags {
				if t != trimmed {
					kept = append(kept, t)
				}
			}
			obj.Tags = kept
			tagsCopy := append([]string(nil), kept...)
			w.emit(&VillageObjectTagsUpdated{
				ObjectID: id,
				Tags:     tagsCopy,
				At:       time.Now().UTC(),
			})
			return SetTagsResult{ID: id, Tags: tagsCopy}, nil
		},
	}
}

// SetRefreshesResult echoes the applied refresh set back to the HTTP layer. The
// rows are a fresh deep copy (never the live world slice), so the handler can
// serialize them off the world goroutine without racing the regen tick.
type SetRefreshesResult struct {
	ID        VillageObjectID
	Refreshes []*ObjectRefresh
}

// SetVillageObjectRefreshes replaces a village object's entire refresh-policy
// set. The incoming rows are validated by ValidateObjectRefreshes (which mirrors
// the object_refresh CHECK constraints) BEFORE any mutation, so an invalid set
// returns ErrInvalidRefresh and leaves the object untouched. An empty set clears
// all refresh policies.
//
// last_refresh_at is engine-managed, not caller-supplied: for an incoming row
// whose attribute matches an existing row with the SAME refresh_mode and
// refresh_period_hours, the existing regen anchor is preserved — an unrelated
// edit (amount, supply) shouldn't restart the regen schedule. Any other row
// (new, or with a changed mode/period) starts with a nil anchor so the regen
// tick re-anchors it on its next pass. This mirrors the v1 PUT .../refresh
// handler (engine/object_refresh_api.go).
//
// Returns ErrVillageObjectNotFound if the object is absent. Emits no event —
// refresh config is not in ObjectDTO, so the change is invisible to a connected
// client and needs no broadcast (the editor re-reads). Same restart-loss-until-
// checkpoint persistence as the other admin object commands.
func SetVillageObjectRefreshes(id VillageObjectID, rows []*ObjectRefresh) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if err := ValidateObjectRefreshes(rows); err != nil {
				return nil, err
			}
			obj, ok := w.VillageObjects[id]
			if !ok {
				return nil, ErrVillageObjectNotFound
			}
			// Index existing rows by attribute so an unchanged regen schedule
			// (mode + period) carries its anchor forward instead of resetting.
			// Guard against a nil persisted row (trusts world state, not the
			// already-validated input) so an admin edit can't panic on bad data.
			existing := make(map[NeedKey]*ObjectRefresh, len(obj.Refreshes))
			for _, r := range obj.Refreshes {
				if r == nil {
					continue
				}
				existing[r.Attribute] = r
			}
			next := make([]*ObjectRefresh, 0, len(rows))
			for _, r := range rows {
				clone := cloneObjectRefresh(r)
				// Only carry the anchor forward for a row that actually regens
				// (finite + a period): the regen tick ignores any other row, so
				// preserving its anchor would just leave dead state attached.
				prior, ok := existing[clone.Attribute]
				if ok && clone.IsFinite() && clone.RefreshPeriodHours != nil &&
					prior.RefreshMode == clone.RefreshMode &&
					intPtrEqual(prior.RefreshPeriodHours, clone.RefreshPeriodHours) {
					clone.LastRefreshAt = copyTimePtr(prior.LastRefreshAt)
				} else {
					clone.LastRefreshAt = nil
				}
				next = append(next, clone)
			}
			obj.Refreshes = next

			// Return a deep copy so the result, read off the world goroutine by
			// the HTTP handler, never aliases the live rows the regen tick mutates.
			snapshot := make([]*ObjectRefresh, len(next))
			for i, r := range next {
				snapshot[i] = cloneObjectRefresh(r)
			}
			return SetRefreshesResult{ID: id, Refreshes: snapshot}, nil
		},
	}
}
