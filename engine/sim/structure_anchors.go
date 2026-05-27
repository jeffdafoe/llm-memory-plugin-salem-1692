package sim

import (
	"hash/fnv"
	"log"
)

// structure_anchors.go — PR 4 structure-anchor layer.
//
// Carries v1's structure-anchor system (door / loiter pin / visitor
// slots) onto the v2 substrate. Reference for the v1 algorithms:
// shared/notes/codebase/salem/structure-lookups.
//
// SHARED-IDENTITY BRIDGE (option A, locked 2026-05-14). A building is
// represented by BOTH a Structure (interior rooms, the thing actors bind
// to via Inside/Home/WorkStructureID) AND a VillageObject (sprite
// placement, footprint, loiter offset, entry policy) that share the same
// UUID — i.e. for any building, its StructureID and VillageObjectID are
// the same string. The anchor data PR 4 needs (door offset, footprint,
// loiter offset) lives on the VillageObject and its Asset; it is NOT
// duplicated onto Structure. villageObjectForStructure crosses that
// bridge. v1 had a single village_object row per building, so this keeps
// one source of truth the same way v1 did.

// visitorSlotOffsets is the king's-move ring around a structure's loiter
// pin. An arriving visitor stands on one of these eight tiles; the pin
// tile itself is NOT a slot — it marks the gathering CENTRE. The order is
// fixed and load-bearing: pickVisitorSlot scans from a per-actor
// hash-derived start index through this slice in order, so changing the
// order changes which slot a given actor lands on.
var visitorSlotOffsets = [8]Position{
	{X: -1, Y: -1}, {X: 0, Y: -1}, {X: 1, Y: -1},
	{X: -1, Y: 0}, {X: 1, Y: 0},
	{X: -1, Y: 1}, {X: 0, Y: 1}, {X: 1, Y: 1},
}

// villageObjectForStructure resolves the shared-identity bridge: returns
// the VillageObject and its Asset for a building's StructureID. ok=false
// when no VillageObject shares the structure's ID, or its Asset is not in
// the catalog — callers treat that as "this structure has no usable
// placement" and fail the operation rather than guessing an anchor.
//
// MUST be called from inside a Command.Fn (reads w.VillageObjects,
// w.Assets). Unexported by design — see buildWalkGrid for the rationale.
func villageObjectForStructure(w *World, structureID StructureID) (*VillageObject, *Asset, bool) {
	vobj, ok := w.VillageObjects[VillageObjectID(structureID)]
	if !ok {
		return nil, nil, false
	}
	asset, ok := w.Assets[vobj.AssetID]
	if !ok {
		return nil, nil, false
	}
	return vobj, asset, true
}

// computeLoiterTile resolves a structure's loiter pin — the gathering
// CENTRE tile, not a stand-on tile. Resolution order matches v1's
// effectiveLoiterTile (engine/village_objects.go):
//
//  1. Per-instance loiter offset, when both axes are set on the
//     VillageObject (the editor sets them as a pair — a dragged pin).
//  2. Else the asset's door offset, one tile south of the door.
//  3. Else (0, FootprintBottom + 2) — two tiles below the visible
//     footprint.
//
// Pure function (no world access) so a future v2 editor port can share
// the exact computation the engine uses — the green loiter pin the
// editor draws then lands precisely where the engine parks visitors.
//
// Precondition: vobj and asset must both be non-nil — the function
// dereferences both and intentionally does not guard against nil (a nil
// here is bad data, not a runtime condition; a silent zero-position
// fallback would hide it). Production callers reach it through
// villageObjectForStructure, which guarantees both are non-nil.
func computeLoiterTile(vobj *VillageObject, asset *Asset) Position {
	anchor := vobj.Pos.Tile()
	switch {
	case vobj.LoiterOffsetX != nil && vobj.LoiterOffsetY != nil:
		return Position{X: anchor.X + *vobj.LoiterOffsetX, Y: anchor.Y + *vobj.LoiterOffsetY}
	case asset.DoorOffsetX != nil && asset.DoorOffsetY != nil:
		return Position{X: anchor.X + *asset.DoorOffsetX, Y: anchor.Y + *asset.DoorOffsetY + 1}
	default:
		return Position{X: anchor.X, Y: anchor.Y + asset.FootprintBottom + 2}
	}
}

// EffectiveLoiterOffset returns the resolved loiter offset in TILE units
// relative to the object's anchor tile, applying the same per-instance →
// door-offset → footprint fallback as computeLoiterTile. This is the offset the
// object read DTO ships (effective_loiter_offset_x/y) and the
// object_loiter_offset_changed event carries, so the editor renders its loiter
// pin exactly where the engine parks visitors — single source of truth, no
// client-side reimplementation of the fallback formula (ZBBS-HOME-289).
//
// asset may be nil (a dangling asset_id): the full door/footprint fallback can't
// run, so it returns the per-instance override if present, else (0, 0). One bad
// asset reference must not break the objects read or the event.
//
// The nil-asset branch deliberately mirrors computeLoiterTile's BOTH-or-nothing
// gate (case 1 requires both axes set): a one-axis-only override is treated as
// "no override" — the same way the real resolver treats it — and resolves to the
// (0, 0) best-effort here rather than a per-axis blend. Honoring a partial
// override would diverge from where the engine actually parks visitors and break
// the single-source-of-truth invariant. (A partial override isn't reachable via
// the set-loiter-offset route, which enforces both-or-neither; it only arises
// from direct/loaded world state.)
func EffectiveLoiterOffset(vobj *VillageObject, asset *Asset) (int, int) {
	if asset == nil {
		if vobj.LoiterOffsetX != nil && vobj.LoiterOffsetY != nil {
			return *vobj.LoiterOffsetX, *vobj.LoiterOffsetY
		}
		return 0, 0
	}
	pin := computeLoiterTile(vobj, asset)
	anchor := vobj.Pos.Tile()
	return pin.X - anchor.X, pin.Y - anchor.Y
}

// effectiveLoiterTile resolves the loiter pin for a building by its
// StructureID, crossing the shared-identity bridge. ok=false when the
// structure has no VillageObject placement (see villageObjectForStructure).
//
// MUST be called from inside a Command.Fn. Unexported by design.
func effectiveLoiterTile(w *World, structureID StructureID) (Position, bool) {
	vobj, asset, ok := villageObjectForStructure(w, structureID)
	if !ok {
		return Position{}, false
	}
	return computeLoiterTile(vobj, asset), true
}

// ResolveMoveTargetTile returns the goal tile an actor's current MoveIntent
// is walking toward, resolved grid-free for operator introspection (the
// umbilical /agent view). Best-effort by destination kind:
//
//   - Position       → the exact target tile.
//   - StructureEnter → the door tile (structureEntryTile).
//   - StructureVisit → the loiter pin (effectiveLoiterTile) — the CENTRE the
//     eight visitor slots ring. The exact slot needs a live WalkGrid; the pin
//     is enough to show where the actor is headed and whether the target is
//     off-grid (the locomotion-debug use case this exists for).
//
// ok=false when the actor has no MoveIntent, or a structure target has no
// resolvable placement / door. MUST be called from inside a Command.Fn (the
// structure resolvers read w.VillageObjects / w.Assets).
func ResolveMoveTargetTile(w *World, a *Actor) (TilePos, bool) {
	if a == nil || a.MoveIntent == nil {
		return TilePos{}, false
	}
	dest := a.MoveIntent.Destination
	switch dest.Kind {
	case MoveDestinationPosition:
		if dest.Position == nil {
			return TilePos{}, false
		}
		return *dest.Position, true
	case MoveDestinationStructureEnter:
		if dest.StructureID == nil {
			return TilePos{}, false
		}
		return structureEntryTile(w, *dest.StructureID)
	case MoveDestinationStructureVisit:
		if dest.StructureID == nil {
			return TilePos{}, false
		}
		return effectiveLoiterTile(w, *dest.StructureID)
	default:
		return TilePos{}, false
	}
}

// Loiter-attribution tolerances, in king's-move (Chebyshev) tiles. Ported
// from v1's two reverse-lookup callers, which shared the loiter-pin formula
// but used different radii:
//   - LoiterAttributionTiles (1): v1 resolveLoiteringStructure — "standing
//     AT" a pin (object-refresh arrival, dwell still-here, the NPC "you are
//     at X" perception line). Chebyshev <= 1 = the pin tile or its 8
//     king's-move visitor slots — the exact inverse of pickVisitorSlot.
//   - AudienceScopeTiles (2): v1 actorStructureScope outdoor audience scope —
//     its 64px ring (= 2 tiles) for the conversational/talk-panel scope a
//     loitering PC hears.
const (
	LoiterAttributionTiles = 1
	AudienceScopeTiles     = 2
)

// ResolveLoiteringObject returns the id of the NAMED village object whose
// loiter pin (computeLoiterTile) is closest to actorTile within maxCheb
// king's-move tiles, or ("", false) when none qualifies. This is the v2 port
// of v1's resolveLoiteringStructure (engine/village_objects.go:184) — the
// inverse of pickVisitorSlot: given the tile an actor occupies, identify which
// loiter pin owns it.
//
// "Named" (DisplayName != "") mirrors v1's `display_name IS NOT NULL` filter,
// so it matches any named building OR prop a visitor can stand at (a well, a
// shade oak) — NOT only Structure-bridged buildings. An object whose asset_id
// doesn't resolve in the catalog is skipped (v1 JOINed asset, so an
// unresolvable asset never matched).
//
// Ties (equal Chebyshev distance) break by the smallest VillageObjectID so the
// result is deterministic regardless of the map's random iteration order (v1's
// `ORDER BY chebyshev_dist LIMIT 1` left exact ties to Postgres; v2 pins it).
//
// Pure over its inputs so both the live world (pass w.VillageObjects, w.Assets
// from inside a Command.Fn) and the published snapshot (snap.VillageObjects +
// the reference asset catalog) can call it — the read surface (pc/me audience
// scope) reads the snapshot; the in-engine consumers (object-refresh, dwell,
// NPC-route arrival) read the live world.
func ResolveLoiteringObject(objects map[VillageObjectID]*VillageObject, assets map[AssetID]*Asset, actorTile TilePos, maxCheb int) (VillageObjectID, bool) {
	var bestID VillageObjectID
	bestDist := 0
	found := false
	for id, vobj := range objects {
		if vobj == nil || vobj.DisplayName == "" {
			continue
		}
		asset, ok := assets[vobj.AssetID]
		if !ok || asset == nil {
			continue
		}
		d := computeLoiterTile(vobj, asset).Chebyshev(actorTile)
		if d > maxCheb {
			continue
		}
		if !found || d < bestDist || (d == bestDist && id < bestID) {
			bestID, bestDist, found = id, d, true
		}
	}
	return bestID, found
}

// resolveLoiteringObject is the live-world convenience wrapper over
// ResolveLoiteringObject. MUST be called from inside a Command.Fn (reads
// w.VillageObjects / w.Assets).
func resolveLoiteringObject(w *World, actorTile TilePos, maxCheb int) (VillageObjectID, bool) {
	return ResolveLoiteringObject(w.VillageObjects, w.Assets, actorTile, maxCheb)
}

// structureEntryTile returns the tile an actor must reach to count as
// "inside" structureID for a StructureEnter move: the structure's door
// tile (placement anchor + the asset's door offset).
//
// In v2's WalkGrid model the door tile is the sole walkable footprint
// tile — buildWalkGrid stamps the rest of the footprint impassable and
// carves a corridor only to the door — so the door tile is both the
// pathfinding goal and, once the actor stands on it, the tile-ownership
// signal that flips InsideStructureID.
//
// ok=false when the structure has no VillageObject placement, or its
// asset declares no door offset. A doorless structure cannot be entered;
// the caller rejects the StructureEnter (such a structure should be
// targeted with StructureVisit instead).
//
// This is the v2 form of the PR 4 design note's closestReachableInteriorTile
// helper — under the shared-identity bridge there is exactly one reachable
// interior tile (the door), so no "closest of many" search is needed.
//
// MUST be called from inside a Command.Fn. Unexported by design.
func structureEntryTile(w *World, structureID StructureID) (Position, bool) {
	vobj, asset, ok := villageObjectForStructure(w, structureID)
	if !ok {
		return Position{}, false
	}
	if asset.DoorOffsetX == nil || asset.DoorOffsetY == nil {
		return Position{}, false
	}
	anchor := vobj.Pos.Tile()
	return Position{X: anchor.X + *asset.DoorOffsetX, Y: anchor.Y + *asset.DoorOffsetY}, true
}

// structureContainingTile returns the StructureID whose footprint
// contains pos, or ok=false when pos sits inside no structure's
// footprint. A "structure" here is a VillageObject that also has a
// Structure entry under the shared-identity bridge — trees, wells, and
// other placements that aren't structures never flip InsideStructureID.
//
// The footprint is the asset's per-side extent around the placement
// anchor (the same rectangle buildWalkGrid stamps). Linear in
// VillageObject count — fine at Salem scale; a tile→structure index can
// be added if it ever shows up in a profile.
//
// MUST be called from inside a Command.Fn (reads w.VillageObjects,
// w.Structures, w.Assets). Unexported by design.
func structureContainingTile(w *World, pos Position) (StructureID, bool) {
	for vobjID, vobj := range w.VillageObjects {
		sid := StructureID(vobjID)
		if _, isStructure := w.Structures[sid]; !isStructure {
			continue
		}
		asset, ok := w.Assets[vobj.AssetID]
		if !ok {
			continue
		}
		anchor := vobj.Pos.Tile()
		if pos.X >= anchor.X-asset.FootprintLeft && pos.X <= anchor.X+asset.FootprintRight &&
			pos.Y >= anchor.Y-asset.FootprintTop && pos.Y <= anchor.Y+asset.FootprintBottom {
			return sid, true
		}
	}
	return "", false
}

// pickVisitorSlot picks the tile an arriving visitor stands on: one of
// the eight visitorSlotOffsets around the structure's loiter pin.
//
// Selection is deterministic per actor — the same actor targeting the
// same structure always starts its scan from the same slot, so an actor
// re-resolving its destination tick after tick does not thrash between
// slots. From that hash-derived start, the eight slots are scanned in
// fixed order; the first slot that is both traversable (per grid) and
// not already occupied by another actor wins. When all eight are
// blocked, the loiter pin tile itself is the last resort — but only if
// the pin is itself stand-able (walkable and unoccupied), since
// returning an unwalkable or occupied tile would have resolvePathTarget
// report success on a destination the mover can never finish at.
//
// ok=false when the structure has no VillageObject placement, OR when
// every one of the eight slots AND the loiter pin are blocked — the
// caller (MoveActor / the ticker) treats that as a clean reject rather
// than accepting a move that can never make progress.
//
// The caller supplies the WalkGrid so the locomotion ticker can build it
// once per tick and reuse it across every moving actor rather than
// rebuilding per slot resolution. (This differs from the PR 4 design
// note's gridless signature; passing the grid in is the per-tick-replan
// model's natural shape.)
//
// MUST be called from inside a Command.Fn. Unexported by design.
func pickVisitorSlot(w *World, structureID StructureID, actor *Actor, grid *WalkGrid) (Position, bool) {
	if actor == nil {
		return Position{}, false
	}
	pin, ok := effectiveLoiterTile(w, structureID)
	if !ok {
		return Position{}, false
	}
	return pickVisitorSlotAtPin(w, pin, actor, grid, nil, string(structureID))
}

// pickObjectVisitorSlot is the object-keyed sibling of pickVisitorSlot: it
// picks a walkable, unoccupied stand-on tile around a village object's loiter
// pin so an actor can be routed TO that object. pickVisitorSlot resolves its
// pin through the structure bridge (effectiveLoiterTile); a bare named prop
// (a shade tree, a well) has no Structure entry, so this resolves the pin
// directly via computeLoiterTile instead.
//
// The returned tile sits within LoiterAttributionTiles of the pin (a king's-
// move slot is Chebyshev 1; the pin fallback is Chebyshev 0), so on arrival
// ApplyObjectRefreshAtArrival resolves back to this same object.
//
// ok=false when the object or its asset is missing from the world, or when
// every one of the eight slots AND the pin are blocked.
//
// MUST be called from inside a Command.Fn. Unexported by design.
func pickObjectVisitorSlot(w *World, objID VillageObjectID, actor *Actor, grid *WalkGrid) (Position, bool) {
	if actor == nil {
		return Position{}, false
	}
	vobj, ok := w.VillageObjects[objID]
	if !ok || vobj == nil {
		return Position{}, false
	}
	asset, ok := w.Assets[vobj.AssetID]
	if !ok || asset == nil {
		return Position{}, false
	}
	return pickObjectVisitorSlotAvoiding(w, objID, actor, grid, nil)
}

// pickObjectVisitorSlotAvoiding is pickObjectVisitorSlot with an additional
// reserved set: any slot already in reserved is rejected as if occupied. A
// caller resolving slots for MANY actors in one pass — before any of them has
// physically moved — uses this to keep two actors from being assigned the same
// tile (tileOccupiedByOtherActor only sees current positions, not the in-flight
// MoveIntents the pass has just issued). reserved may be nil (no reservations).
//
// MUST be called from inside a Command.Fn. Unexported by design.
func pickObjectVisitorSlotAvoiding(w *World, objID VillageObjectID, actor *Actor, grid *WalkGrid, reserved map[Position]struct{}) (Position, bool) {
	if actor == nil {
		return Position{}, false
	}
	vobj, ok := w.VillageObjects[objID]
	if !ok || vobj == nil {
		return Position{}, false
	}
	asset, ok := w.Assets[vobj.AssetID]
	if !ok || asset == nil {
		return Position{}, false
	}
	pin := computeLoiterTile(vobj, asset)
	return pickVisitorSlotAtPin(w, pin, actor, grid, reserved, string(objID))
}

// pickVisitorSlotAtPin is the shared ring-scan core behind pickVisitorSlot
// (structure-keyed) and pickObjectVisitorSlot (object-keyed). Given a resolved
// loiter pin, it scans the eight visitorSlotOffsets from a per-actor
// hash-derived start (so a re-resolving actor doesn't thrash between slots)
// and returns the first slot that is both traversable and unoccupied. When all
// eight are blocked, the pin tile itself is the last resort — but only if the
// pin is itself stand-able, since returning an unwalkable or occupied tile
// would have resolvePathTarget report success on a destination the mover can
// never finish at.
//
// label identifies the anchor (a structure or object id) in the all-blocked
// log lines. ok=false when every slot AND the pin are blocked.
//
// reserved, when non-nil, rejects any candidate already in the set — for a
// caller resolving slots for many actors in one pass before any has moved (see
// pickObjectVisitorSlotAvoiding). nil means no reservations (the per-tick
// single-actor callers).
//
// MUST be called from inside a Command.Fn. Unexported by design.
func pickVisitorSlotAtPin(w *World, pin Position, actor *Actor, grid *WalkGrid, reserved map[Position]struct{}, label string) (Position, bool) {
	n := len(visitorSlotOffsets)
	start := int(hashActorID(actor.ID) % uint32(n))
	for i := 0; i < n; i++ {
		off := visitorSlotOffsets[(start+i)%n]
		slot := Position{X: pin.X + off.X, Y: pin.Y + off.Y}
		if !grid.CanWalk(slot.X, slot.Y) {
			continue
		}
		if tileOccupiedByOtherActor(w, slot, actor.ID) {
			continue
		}
		if _, taken := reserved[slot]; taken {
			continue
		}
		return slot, true
	}
	// All eight ring slots are blocked. Fall back to the loiter pin
	// itself — but only if it is actually stand-able. An unwalkable or
	// occupied pin returned here would be accepted by resolvePathTarget
	// and then soft-block forever at the final step; failing resolution
	// instead lets MoveActor reject cleanly.
	if _, pinTaken := reserved[pin]; !pinTaken &&
		grid.CanWalk(pin.X, pin.Y) && !tileOccupiedByOtherActor(w, pin, actor.ID) {
		log.Printf("pickVisitorSlotAtPin: all 8 visitor slots blocked for %s; "+
			"falling back to loiter pin %+v (admin should relocate the pin)", label, pin)
		return pin, true
	}
	log.Printf("pickVisitorSlotAtPin: all 8 visitor slots AND the loiter pin %+v are blocked "+
		"for %s; no visitor tile available (admin should relocate the pin)", pin, label)
	return Position{}, false
}

// hashActorID hashes an ActorID to a stable uint32 (FNV-1a over the raw
// ID bytes). pickVisitorSlot uses it to derive a per-actor starting slot
// — stable across ticks and process restarts, which is what keeps slot
// selection from thrashing.
func hashActorID(id ActorID) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return h.Sum32()
}

// tileOccupiedByOtherActor reports whether any actor other than exceptID
// currently stands on pos. Linear in actor count — fine at Salem scale (a
// few dozen actors); a position index would help if actor counts ever
// grow large.
//
// MUST be called from inside a Command.Fn (reads w.Actors). Unexported by
// design.
func tileOccupiedByOtherActor(w *World, pos Position, exceptID ActorID) bool {
	for id, a := range w.Actors {
		if id == exceptID {
			continue
		}
		if a.Pos.Equal(pos) {
			return true
		}
	}
	return false
}
