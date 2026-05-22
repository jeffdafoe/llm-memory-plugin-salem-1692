package sim

// occupancy.go — derived structure occupancy state (ZBBS-070 port of legacy
// engine/world_phase.go + npc_behaviors.go occupancy logic).
//
// Some structures have an "occupied" visual variant: a tavern's windows glow
// when patrons are inside, an inn lights up at night when guests are sleeping.
// The occupied flag is DERIVED from the headcount inside the structure plus two
// per-asset knobs, and drives which AssetState the placed object renders.
//
// Reference: shared/notes/codebase/salem/occupancy. v2 differences from v1:
//   - Occupancy is keyed off the in-memory actorsByStructure index (no COUNT
//     query) — a structure's id IS its placement object's id (shared-identity
//     bridge, structure_anchors.go), so headcount and the object resolve from
//     the same StructureID/VillageObjectID.
//   - The occupied/unoccupied visual is expressed as AssetStates tagged
//     'occupied' / 'unoccupied' (asset.go), resolved via Asset.StateForTag —
//     the same tag-driven model the day/night phase flip uses.
//
// Derived flag (matches v1):
//
//	occupied = (count >= asset.OccupiedMinCount)
//	       AND (NOT asset.OccupiedNightOnly OR world phase == night)
//
// Recomputed on two triggers, both on the world goroutine:
//   - per arrival/departure — setActorInsideStructure (locomotion_ticker.go)
//     recomputes the structure left and the structure entered;
//   - per phase transition — ApplyPhaseTransition (world_phase.go) sweeps the
//     night-only structures, whose flag can change on the day↔night boundary
//     with no actor moving.
//
// A real flip emits VillageObjectStateChanged → object_state_changed, so the
// client re-renders the new state.

import "time"

// Asset-state tags marking the occupied / unoccupied visual variants.
const (
	TagOccupied   = "occupied"
	TagUnoccupied = "unoccupied"
)

// refreshStructureOccupancyState recomputes the occupied/unoccupied visual
// state for the structure backed by structureID and applies it if it changed.
// No-op when the structure has no placement object, its asset is missing from
// the catalog, or the asset isn't occupancy-tracked (it must carry BOTH an
// 'occupied'- and an 'unoccupied'-tagged state — otherwise there's no defined
// pair to toggle between, so the structure simply doesn't participate). A real
// flip emits VillageObjectStateChanged via setVillageObjectStateInline.
//
// MUST be called from inside a Command.Fn (reads/writes world maps, emits).
func refreshStructureOccupancyState(w *World, structureID StructureID) {
	obj, ok := w.VillageObjects[VillageObjectID(structureID)]
	if !ok {
		return
	}
	asset, ok := w.Assets[obj.AssetID]
	if !ok {
		return
	}
	occupiedState := asset.StateForTag(TagOccupied)
	unoccupiedState := asset.StateForTag(TagUnoccupied)
	if occupiedState == nil || unoccupiedState == nil {
		return // not occupancy-tracked
	}

	// Headcount (ZBBS-HOME-284 #2). For "active presence" structures (NOT
	// night-only — shops, taverns where occupied == open for business),
	// sleeping / on-break actors don't count, so a home==work keeper going to
	// bed darkens the structure. For night-only structures (inns where occupied
	// == guests lodging) everyone counts — the inn is lit precisely because
	// guests are (sleeping) inside. Safe to exclude now that the sleep lifecycle
	// re-triggers this recompute on the bed-down (executeNPCSleep) and wake
	// (wakeNPC) transitions, so the count can't go stale when a rest window
	// opens or closes.
	now := time.Now().UTC()
	count := 0
	for id := range w.actorsByStructure[structureID] {
		a := w.Actors[id]
		if a == nil {
			continue
		}
		if !asset.OccupiedNightOnly && actorIsResting(a, now) {
			continue
		}
		count++
	}
	occupied := count >= asset.OccupiedMinCount &&
		(!asset.OccupiedNightOnly || w.Phase == PhaseNight)

	target := unoccupiedState.State
	if occupied {
		target = occupiedState.State
	}
	if obj.CurrentState == target {
		return
	}
	setVillageObjectStateInline(w, obj, target)
}

// refreshNightOnlyOccupancyStates recomputes occupancy for every night-only
// occupancy-tracked structure. Run at a phase transition: a night-only
// structure's occupied flag can flip on the day↔night boundary with no actor
// moving (an inn full of sleeping guests goes from unlit by day to lit at
// dusk), so the per-arrival hook alone wouldn't catch it. Non-night-only
// structures depend only on headcount, which doesn't change at a boundary, so
// they're left to the arrival/departure hook.
//
// MUST be called from inside a Command.Fn.
func refreshNightOnlyOccupancyStates(w *World) {
	for objID, obj := range w.VillageObjects {
		asset, ok := w.Assets[obj.AssetID]
		if !ok || !asset.OccupiedNightOnly {
			continue
		}
		refreshStructureOccupancyState(w, StructureID(objID))
	}
}
