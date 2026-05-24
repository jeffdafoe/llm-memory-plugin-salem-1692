package sim

import (
	"log"
	"time"
)

// npc_rest_fallback.go — the homeless rest floor.
//
// RouteHomelessToRest is the safety net under the needs/sleep lifecycle: a
// bed-less, exhausted, off-shift NPC with no way to resolve its own tiredness
// (no home to auto-bed at, no rented room to lodge in) is walked to the
// nearest free tiredness-easing village object (a shade tree, a green) so it
// can dwell and recover. Deterministic by design — a broke exhausted vagrant
// crashing under a tree is not a cognition-worthy decision, so no LLM turn is
// spent on it; the locus of agency is upstream (the recovery_options "rent a
// room" / remedy cues that a solvent NPC almost always acts on before it ever
// reaches exhaustion). The floor fires regardless of coins precisely because
// it is the last resort.
//
// Wired into the sleep tick (runSleepTickIteration) AFTER the auto-bed pass,
// so an NPC that can bed at home or lodge at an inn is already handled and
// excluded here.

const restFallbackTirednessNeed = NeedKey("tiredness")

// RouteHomelessToRest returns a Command that walks every exhausted, bed-less,
// off-shift homeless NPC to a walkable approach tile beside the nearest free
// tiredness-easing village object. Level-triggered and idempotent: an NPC
// already standing at, or already en route to, its rest slot is skipped, so
// re-running the command tick after tick does not re-issue moves or thrash.
//
// The WalkGrid is built once on the first eligible actor and reused for every
// subsequent slot resolution this pass (skipped entirely when no actor is
// eligible). Returns the count of actors newly routed this pass.
func RouteHomelessToRest(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			nowMinute := localMinuteOfDay(w, now)
			redThreshold := w.Settings.NeedThresholds.Get(restFallbackTirednessNeed)
			var grid *WalkGrid
			routed := 0
			for _, a := range w.Actors {
				if !needsRestFallback(w, a, now, nowMinute, redThreshold) {
					continue
				}
				if grid == nil {
					g, err := buildWalkGrid(w)
					if err != nil {
						log.Printf("sim/npc_rest_fallback: build walk grid: %v", err)
						return routed, nil
					}
					grid = g
				}
				target, ok := nearestFreeRestSlot(w, a, grid)
				if !ok {
					continue
				}
				if a.Pos == target || alreadyHeadedToRest(a, target) {
					continue
				}
				if _, err := MoveActor(a.ID, NewPositionDestination(target), false, now).Fn(w); err != nil {
					log.Printf("sim/npc_rest_fallback: route %s -> rest %v: %v", a.ID, target, err)
					continue
				}
				routed++
			}
			return routed, nil
		},
	}
}

// needsRestFallback is the eligibility gate. Every condition must hold:
//
//   - an agent NPC (not a PC, decorative, or transient visitor object);
//   - no home (a homed NPC sleeps via the auto-bed arm);
//   - holds no active ledger lodging (a lodger beds at its rented inn);
//   - not already resting, warranted (mid-deliberation), or tick-in-flight;
//   - not in an active huddle (don't yank someone out of a conversation);
//   - tiredness at or past the red/exhausted threshold;
//   - off-shift — outside its own schedule, or outside the dawn/dusk day
//     window for an unscheduled NPC (so unscheduled NPCs rest at night and an
//     employed vagrant working late is not pulled off duty).
func needsRestFallback(w *World, a *Actor, now time.Time, nowMinute, redThreshold int) bool {
	if !isAgentNPC(a) {
		return false
	}
	if a.HomeStructureID != "" {
		return false
	}
	if actorHoldsActiveLodging(a, now) {
		return false
	}
	if actorIsResting(a, now) {
		return false
	}
	if a.WarrantedSince != nil || a.TickInFlight {
		return false
	}
	if actorInActiveHuddle(w, a) {
		return false
	}
	if a.Needs[restFallbackTirednessNeed] < redThreshold {
		return false
	}
	start, end, ok := effectiveShiftWindow(w, a)
	if !ok || minuteInShiftWindow(start, end, nowMinute) {
		return false
	}
	return true
}

// nearestFreeRestSlot resolves a walkable approach tile beside the nearest
// tiredness-easing object to the actor. Two stages: pick the nearest such
// object (nearestFreeTirednessObject), then resolve a stand-on slot beside its
// loiter pin (pickObjectVisitorSlot). The slot — not the object's own tile — is
// the move target, because a MoveDestinationPosition onto an unwalkable object
// footprint is rejected outright by resolvePathTarget (no route-to-adjacent
// fallback, unlike StructureVisit). The slot sits within LoiterAttributionTiles
// of the pin, so on arrival ApplyObjectRefreshAtArrival resolves back to this
// same object and applies its refresh.
//
// ok=false when no tiredness object exists, or the nearest one's eight slots
// AND its pin are all blocked. A blocked nearest object skips the actor this
// tick rather than falling through to a farther object — at Salem scale rest
// objects sit in open ground and all-blocked is a degenerate case; the next
// tick retries.
func nearestFreeRestSlot(w *World, actor *Actor, grid *WalkGrid) (Position, bool) {
	objID, ok := nearestFreeTirednessObject(w, actor.Pos)
	if !ok {
		return Position{}, false
	}
	return pickObjectVisitorSlot(w, objID, actor, grid)
}

// nearestFreeTirednessObject returns the id of the closest village object that
// currently eases tiredness (objectEasesTiredness), by squared tile distance
// to from. Ties break by smallest VillageObjectID for determinism across the
// map's random iteration order.
func nearestFreeTirednessObject(w *World, from TilePos) (VillageObjectID, bool) {
	var bestID VillageObjectID
	bestDist := 0
	found := false
	for id, obj := range w.VillageObjects {
		if obj == nil || !objectEasesTiredness(obj) {
			continue
		}
		ot := obj.Pos.Tile()
		dx := ot.X - from.X
		dy := ot.Y - from.Y
		d := dx*dx + dy*dy
		if !found || d < bestDist || (d == bestDist && id < bestID) {
			bestID, bestDist, found = id, d, true
		}
	}
	return bestID, found
}

// objectEasesTiredness reports whether obj carries a tiredness refresh row
// with supply available — i.e. dwelling at it actually recovers tiredness. A
// finite row whose AvailableQuantity has run dry (a depleted source) does not
// count.
func objectEasesTiredness(obj *VillageObject) bool {
	for _, r := range obj.Refreshes {
		if r == nil || r.Attribute != restFallbackTirednessNeed {
			continue
		}
		if r.IsFinite() && r.AvailableQuantity != nil && *r.AvailableQuantity <= 0 {
			continue
		}
		return true
	}
	return false
}

// alreadyHeadedToRest reports whether the actor's in-flight move already
// targets target — the idempotency guard that keeps a re-running router from
// re-issuing the same move every tick.
func alreadyHeadedToRest(a *Actor, target Position) bool {
	mi := a.MoveIntent
	return mi != nil &&
		mi.Destination.Kind == MoveDestinationPosition &&
		mi.Destination.Position != nil &&
		*mi.Destination.Position == target
}
