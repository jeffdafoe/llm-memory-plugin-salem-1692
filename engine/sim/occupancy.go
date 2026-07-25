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
// There are two semantics behind the one flag, and which one applies is a
// property of the structure, not of the trigger (LLM-534):
//
//   - A BUSINESS reads occupied when someone is MINDING it — businessTendedAt,
//     the same predicate perception uses to tell an NPC a place is open. This is
//     the "open for business" question.
//   - Everything else reads occupied by HEADCOUNT, as v1 did:
//     occupied = (count >= asset.OccupiedMinCount)
//     AND (NOT asset.OccupiedNightOnly OR world phase == night)
//
// Recomputed on these triggers, all on the world goroutine:
//   - per arrival/departure — setActorInsideStructure (locomotion_ticker.go)
//     recomputes the structure left and the structure entered;
//   - per phase transition — ApplyPhaseTransition (world_phase.go) sweeps the
//     night-only structures, whose flag can change on the day↔night boundary
//     with no actor moving;
//   - per locomotion tick that moved anyone, per teleport, on a workplace
//     reassignment, on a labor settle, on bed-down / wake, and once at load —
//     refreshActivePresenceOccupancyStates sweeps every non-night-only tracked
//     structure, because the tended flag turns on inputs the inside-structure
//     chokepoint cannot see (see that function for the full list and why each
//     call site is there).
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

	target := unoccupiedState.State
	if structureReadsOccupied(w, structureID, asset) {
		target = occupiedState.State
	}
	if obj.CurrentState == target {
		return
	}
	setVillageObjectStateInline(w, obj, target)
}

// structureReadsOccupied answers the occupied question for one structure under
// whichever of the two semantics its asset carries.
//
// A business that is not night-only means "open for business", and the answer is
// businessTendedAt — is anyone MINDING the place, its own awake keeper or a hired
// hand on a live job for that keeper, inside its interior or standing at its
// loiter pin. Sharing that predicate with perception is the whole point (LLM-534):
// the sprite and the cue an NPC reads are now the same claim, so they cannot
// disagree. They did — an interior-less market stall's loiter pin sits OUTSIDE its
// footprint (Ellis Farm: footprint y ends at 43, pin at (41,44)), and the
// footprint is impassable but for the door tile, so a keeper at her post was never
// in actorsByStructure and the stall could not render open while she worked it.
// The stalls that looked fine each have a common room, so their keeper ENTERS and
// lands on the door tile, which IS a footprint tile — the mechanism only ever
// worked by that accident of geometry.
//
// OccupiedMinCount does not apply to the tended reading: "someone is minding it"
// is not a headcount, and no business asset sets the knob above 1. It still
// governs every headcount structure.
//
// Everything else keeps the v1 headcount (ZBBS-HOME-284 #2). For a non-night-only
// headcount structure, sleeping / on-break actors don't count, so a home==work
// keeper going to bed darkens it. For night-only structures (occupied == guests
// lodging) everyone counts — the inn is lit precisely because guests are
// (sleeping) inside. Safe to exclude because the sleep lifecycle re-triggers this
// recompute on the bed-down (executeNPCSleep) and wake (wakeNPC) transitions, so
// the count can't go stale when a rest window opens or closes.
//
// MUST be called from inside a Command.Fn.
func structureReadsOccupied(w *World, structureID StructureID, asset *Asset) bool {
	if !asset.OccupiedNightOnly && structureHasWorker(w, structureID) {
		return businessTendedAt(w, structureID)
	}

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
	return count >= asset.OccupiedMinCount &&
		(!asset.OccupiedNightOnly || w.Phase == PhaseNight)
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

// refreshActivePresenceOccupancyStates recomputes occupancy for every
// occupancy-tracked structure that is NOT night-only — the twin of
// refreshNightOnlyOccupancyStates, covering the other half of the catalog
// (LLM-534). Businesses among them read through businessTendedAt; the rest read by
// headcount. The sweep does not care which: it re-derives each structure and
// structureReadsOccupied decides.
//
// Deliberately NOT filtered to businesses. Filtering on structureHasWorker looks
// like an obvious cheap win and is a bug: a structure that just STOPPED being a
// business (its last worker reassigned) is precisely the one whose art is now
// wrong, and a business-filtered sweep cannot see it. Sweeping the whole
// non-night-only set is what makes "both ends of a change" true without any caller
// naming either end.
//
// The sweep exists because tendedness turns on inputs the setActorInsideStructure
// chokepoint cannot see:
//
//   - The keeper's POSITION matters while she is outdoors, at her loiter pin. An
//     outdoor→outdoor step never changes InsideStructureID, so it never reaches
//     that chokepoint — walking up to an interior-less stall, or away from one, is
//     invisible to it.
//   - WHO KEEPS the place matters: SetActorWorkStructure can make a structure a
//     business or stop it being one, with no one moving.
//   - A hired hand's LaborStateWorking transitions matter, and the ledger is not
//     the actor index.
//   - The keeper's WAKE state matters, and the sleep hooks refresh
//     a.InsideStructureID, which is "" for a keeper bedded down outdoors.
//
// Sweeping beats a trigger per input: a missed trigger leaves a stall showing the
// wrong art until something unrelated moves. Cost is the asset-tag test over
// w.VillageObjects, then a re-derive for the handful that carry both tags.
//
// Call sites, each the point where one of those inputs can change:
//
//   - EvaluateLocomotion, after the mover loop — every walk, coalesced to one pass
//     per tick. Its no-movers early return means an idle village pays nothing.
//   - updateInsideStructureIDFromTileOwnership — the non-walk position flip
//     (teleport / operator set-position). A teleported actor has no MoveIntent, so
//     the locomotion tick would take that early return and never sweep.
//   - setActorStructure — the engine's only WorkStructureID write.
//   - EvaluateLaborLedgerSweep, on a completed job — a hand can stop working and
//     stand still, so movement can't be relied on.
//   - executeNPCSleep / wakeNPC — guards; see the note at those call sites, which
//     explains why no caller reaches them with an outdoor sleeper today.
//   - FinalizeLoad — the art is derived but stored, so a checkpointed state that no
//     longer matches the world converges before the first publish.
//
// MUST be called from inside a Command.Fn.
func refreshActivePresenceOccupancyStates(w *World) {
	for objID, obj := range w.VillageObjects {
		if obj == nil {
			continue
		}
		asset, ok := w.Assets[obj.AssetID]
		if !ok || asset == nil || asset.OccupiedNightOnly {
			continue
		}
		if asset.StateForTag(TagOccupied) == nil || asset.StateForTag(TagUnoccupied) == nil {
			continue
		}
		refreshStructureOccupancyState(w, StructureID(objID))
	}
}
