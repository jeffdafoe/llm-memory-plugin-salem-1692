package sim

import "time"

// closed_business.go — ZBBS-HOME-353. Experiential "this business was shut"
// memory. A wandering NPC kept walking to an unattended shop/farm (the live
// John Ellis case: six trips in eight minutes to the keeperless Ellis Farm)
// because the restock/vendor perception cues name a supplier's WORKPLACE with no
// presence gate. The fix is experiential, not omniscient: when an NPC ARRIVES at
// a business and finds no keeper present, it remembers that — and perception
// annotates a later cue pointing there so the model deprioritizes it. The memory
// self-clears when the NPC next finds the business attended, and DECAYS after
// ClosedBusinessMemoryTTL so it retries rather than believing it shut forever.
//
// This is the CAPTURE half (an ActorArrived subscriber, additive — it does not
// touch the locomotion ticker). The SURFACE half lives in perception
// (build/render) and reads the actor's Observed store (ObservedClosed). The
// store itself is the unified observed-state memory in observed_state.go (LLM-80).

// ClosedBusinessMemoryTTL is how long a "found it shut" observation stays
// actionable before perception ignores it (ZBBS-HOME-353). After this the NPC
// re-checks the business rather than treating it as permanently closed. 4 game-
// hours (Jeff, 2026-05-30) — long enough to stop the same-session loop, short
// enough that a keeper returning within the day clears the avoidance.
const ClosedBusinessMemoryTTL = 4 * time.Hour

// handleClosedBusinessOnArrival is the ActorArrived subscriber that records (or
// clears) the arriving NPC's memory of a business being shut. It fires once per
// arrival and is a no-op for non-agent arrivals or arrivals that don't resolve
// to a worked structure other than the arriver's own workplace.
//
// "At a business" = the arrival resolved to a structure that is someone's
// workplace (has >=1 worker), reached either by entering it (FinalStructureID)
// or by standing at its loiter slot (a StructureVisit to an owner-only shop,
// where FinalStructureID is empty — the John Ellis path). "Keeper present" = at
// least one AWAKE worker of that structure is at it right now (inside it, or
// loitering at it); an asleep keeper does not count — innkeepers sleep at the
// inn, so an abed inn reads shut — but a keeper briefly on break (StateResting)
// still counts as open (the business is open, just quiet). LLM-126.
func handleClosedBusinessOnArrival(w *World, evt Event) {
	arr, ok := evt.(*ActorArrived)
	if !ok {
		return
	}
	a := w.Actors[arr.ActorID]
	if a == nil || !isAgentNPC(a) {
		return
	}

	structureID, ok := businessArrivedAt(w, a, arr)
	if !ok {
		return
	}

	if keeperPresentAt(w, structureID) {
		// Found it attended — clear any stale "shut" memory for this business.
		a.Observed.Clear(ObservedStateKey{StructureID: structureID, Condition: ObservedClosed})
		return
	}

	// Found it shut — remember it (stamped with the arrival time so perception
	// can decay the memory).
	a.Observed.Observe(ObservedStateKey{StructureID: structureID, Condition: ObservedClosed}, arr.At)
}

// businessArrivedAt resolves the worked structure the actor just arrived at, or
// ok=false when the arrival isn't at a business worth remembering. A structure
// counts only when it is someone ELSE's workplace: the arriver's own workplace
// is excluded (you don't observe your own shop as "shut" — you are its keeper),
// and a structure with no workers at all (a pure residence) is not a business.
func businessArrivedAt(w *World, a *Actor, arr *ActorArrived) (StructureID, bool) {
	// Entered the interior → FinalStructureID names it directly. Event-freshness:
	// only trust it when the actor's current structure still matches (a later
	// move could have superseded the arrival).
	if arr.FinalStructureID != "" && a.InsideStructureID == arr.FinalStructureID {
		return validBusiness(w, a, arr.FinalStructureID)
	}
	// Visited (stood at the loiter slot, outside) → resolve the structure from
	// the loiter pin. Structures share their id with their village_object
	// placement, so the resolved object id IS the structure id (validBusiness
	// requires w.Structures[id] != nil, so a resolved object that is NOT a
	// structure is rejected rather than mis-recorded).
	//
	// Event-freshness for the visit path: a StructureVisit arrival has an empty
	// FinalStructureID, so the inside-path's structure-match guard can't apply.
	// Instead require the actor to still be outdoors (InsideStructureID == "")
	// at the arrival position — a later move that entered a structure or walked
	// elsewhere fails this, so a superseded visit-arrival can't record off a
	// stale FinalPosition.
	if arr.FinalStructureID == "" && a.InsideStructureID == "" && a.Pos == arr.FinalPosition {
		if objID, ok := resolveLoiteringObject(w, arr.FinalPosition, LoiterAttributionTiles); ok {
			return validBusiness(w, a, StructureID(objID))
		}
	}
	return "", false
}

// validBusiness returns structureID when it resolves to a real structure that is
// a business (>=1 worker) and is NOT the arriver's own workplace; ok=false
// otherwise.
func validBusiness(w *World, a *Actor, structureID StructureID) (StructureID, bool) {
	if structureID == "" || structureID == a.WorkStructureID {
		return "", false
	}
	if w.Structures[structureID] == nil {
		return "", false
	}
	if !structureHasWorker(w, structureID) {
		return "", false // not a business — no one works here
	}
	return structureID, true
}

// structureHasWorker reports whether any actor has structureID as its workplace.
func structureHasWorker(w *World, structureID StructureID) bool {
	for _, worker := range w.Actors {
		if worker != nil && worker.WorkStructureID == structureID {
			return true
		}
	}
	return false
}

// keeperPresentAt reports whether any worker of structureID is currently TENDING
// it — at it (inside its interior, or loitering at its slot) AND awake. The awake
// gate is LLM-126: innkeepers sleep AT the inn, so without it an abed keeper reads
// as present and the arrival capture records the OPPOSITE of reality (it would
// even clear a stale "shut"). Only StateSleeping disqualifies — a keeper briefly
// on break (StateResting) still counts as present (the business is open, just
// quiet). A worker who has wandered off (the drifted Ellis Farm dairyer) is not
// present, so the business reads shut.
//
// Shares the per-worker rule with KeeperPresentInSnapshot (the read-path
// counterpart) via workerTendsStructure, so the write path (found-it-shut memory,
// cross-threshold huddle scope) and the read path (PC talk roster) can't drift on
// what "open" means.
func keeperPresentAt(w *World, structureID StructureID) bool {
	for _, worker := range w.Actors {
		if worker == nil {
			continue
		}
		if workerTendsStructure(w.VillageObjects, w.Assets, worker.WorkStructureID,
			worker.State, worker.InsideStructureID, worker.Pos, structureID) {
			return true
		}
	}
	return false
}

// keeperPresentInSnapshot is keeperPresentAt over a published Snapshot — the
// read-path counterpart that backs LoiterScopeConversableInSnapshot's shut-shop
// gate for the PC's cross-threshold conversational scope (httpapi
// pcAudienceStructure, LLM-359). The snapshot doesn't carry the asset catalog
// inline, so it is passed in — the same reference catalog ResolveLoiteringObject
// already needs. Shares workerTendsStructure with the live-world keeperPresentAt
// so the two agree on what "tending" means. Guards a nil snapshot (fail-closed:
// unknown ⇒ not tended) since it's reachable from an exported helper.
func keeperPresentInSnapshot(snap *Snapshot, assets map[AssetID]*Asset, structureID StructureID) bool {
	if snap == nil {
		return false
	}
	for _, a := range snap.Actors {
		if a == nil {
			continue
		}
		if workerTendsStructure(snap.VillageObjects, assets, a.WorkStructureID,
			a.State, a.InsideStructureID, a.Pos, structureID) {
			return true
		}
	}
	return false
}

// workerTendsStructure reports whether one worker — identified by its work anchor,
// wake state, and position — is currently tending structureID: it works there, is
// awake, and is physically at it (inside its interior, or loitering at its pin).
// The shared per-worker core of keeperPresentAt (live world) and
// KeeperPresentInSnapshot (published snapshot); pure over its map inputs so both
// callers run the identical rule (the ResolveLoiteringObject / ActorAtWorkpost
// dual-caller pattern).
func workerTendsStructure(objects map[VillageObjectID]*VillageObject, assets map[AssetID]*Asset,
	workStructureID StructureID, state ActorState, insideStructureID StructureID, pos TilePos,
	structureID StructureID) bool {
	if workStructureID != structureID {
		return false
	}
	if state == StateSleeping {
		return false // abed ⇒ not tending, though bedded down AT the inn (LLM-126)
	}
	if insideStructureID == structureID {
		return true
	}
	if objID, ok := ResolveLoiteringObject(objects, assets, pos, LoiterAttributionTiles); ok &&
		StructureID(objID) == structureID {
		return true
	}
	return false
}

// RegisterClosedBusinessSubscriber wires the shut-business-memory subscriber.
// Call before World.Run or from inside a Command (world-goroutine-safe). Mirrors
// RegisterSleepSubscriber — another ActorArrived subscriber. ZBBS-HOME-353.
func RegisterClosedBusinessSubscriber(w *World) {
	if w == nil {
		panic("sim: RegisterClosedBusinessSubscriber requires a non-nil world")
	}
	w.Subscribe(SubscriberFunc(handleClosedBusinessOnArrival))
}
