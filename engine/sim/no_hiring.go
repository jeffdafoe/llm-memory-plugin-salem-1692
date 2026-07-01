package sim

import "time"

// no_hiring.go — LLM-210. Experiential "I couldn't get hired here right now"
// memory, the resting-keeper sibling of closed_business.go / declined_work.go.
//
// A workless worker's seek-work directory (perception buildSeekWorkPlaces) lists
// every business nearest-first, already dropping the ones the worker remembers
// SHUT (ObservedClosed — keeper absent/asleep) or DECLINED (ObservedDeclinedWork —
// an explicit refusal). A keeper on BREAK (StateResting) fell between the two: it
// counts as "present" so the arrival never records the business shut, yet it is
// partitioned out of the solicitable audience (CoPresentResting) so solicit_work
// is never offered and the worker never earns a decline memory. The business
// stayed in the directory forever and the worker looped to it — the live Lewis
// Walker case: home <-> General Store, keeper Josiah on break the whole time.
//
// The fix is experiential, not omniscient: when a worker ARRIVES at a business and
// finds a keeper present but not hireable (on break), it remembers that — and
// perception drops the business from the seek-work directory until the memory
// decays, steering the worker to a business with an available keeper instead. The
// memory self-clears the moment the worker next finds a hireable keeper there.
//
// This is the CAPTURE half (an ActorArrived subscriber, additive). The SURFACE
// half lives in perception (workerRememberedNoHiring in build.go, read by
// buildSeekWorkPlaces). The store is the unified observed-state memory
// (observed_state.go, the ObservedNoHiring condition).

// NoHiringMemoryTTL is how long a "keeper was on break, no work here" observation
// suppresses that business from the worker's seek-work directory before perception
// lists it again (LLM-210). Shorter than the 4h shut / 12h declined memories: a
// break ends soon, so the worker should retry sooner than for a shut shop or an
// outright refusal. 2 game-hours — tunable.
const NoHiringMemoryTTL = 2 * time.Hour

// handleNoHiringOnArrival is the ActorArrived subscriber that records (or clears)
// the arriving worker's memory of a business having no keeper available to hire.
// It fires once per arrival and is a no-op for non-agent arrivals or arrivals that
// don't resolve to a business other than the arriver's own workplace
// (businessArrivedAt, shared with the closed-business capture).
//
// Keeper states, at the resolved business:
//   - a hireable keeper present (awake, not on break) → clear any stale memory (self-heal);
//   - a keeper present but on break (StateResting) → remember it (the resting-keeper gap);
//   - no keeper present (absent/asleep) → left to handleClosedBusinessOnArrival, which
//     records ObservedClosed; that already drops the business from the directory, so a
//     separate no-hiring stamp would be redundant. Any stale no-hiring memory just decays.
func handleNoHiringOnArrival(w *World, evt Event) {
	arr, ok := evt.(*ActorArrived)
	if !ok {
		return
	}
	a := w.Actors[arr.ActorID]
	if a == nil || !isAgentNPC(a) {
		return // only NPC workers perceive the seek-work directory
	}

	structureID, ok := businessArrivedAt(w, a, arr)
	if !ok {
		return
	}
	if !keeperPresentAt(w, structureID) {
		return // keeperless ⇒ handleClosedBusinessOnArrival's ObservedClosed covers it
	}

	key := ObservedStateKey{StructureID: structureID, Condition: ObservedNoHiring}
	if hireableKeeperPresentAt(w, structureID) {
		a.Observed.Clear(key) // a keeper is here and free to take on work — belief cleared
		return
	}
	// A keeper is here but on break: present (so not "shut"), yet not solicitable.
	a.Observed.Observe(key, arr.At)
}

// hireableKeeperPresentAt is the stricter sibling of keeperPresentAt
// (closed_business.go): it reports whether structureID has a keeper who could take
// on a worker RIGHT NOW — present (inside it, or loitering at its slot) AND both
// awake and NOT on break. keeperPresentAt counts a keeper on StateResting as
// present ("open, just quiet" — the right sense for lodging/consumption), but a
// resting keeper is partitioned out of the solicitable audience, so for HIRING it
// does not count. Only the state gate differs from keeperPresentAt.
func hireableKeeperPresentAt(w *World, structureID StructureID) bool {
	for _, worker := range w.Actors {
		if worker == nil || worker.WorkStructureID != structureID {
			continue
		}
		if worker.State == StateSleeping || worker.State == StateResting {
			continue // abed or on break ⇒ cannot take on a worker now
		}
		if worker.InsideStructureID == structureID {
			return true
		}
		if objID, ok := resolveLoiteringObject(w, worker.Pos, LoiterAttributionTiles); ok &&
			StructureID(objID) == structureID {
			return true
		}
	}
	return false
}

// RegisterNoHiringSubscriber wires the no-hiring-memory subscriber. Call before
// World.Run or from inside a Command (world-goroutine-safe). Mirrors
// RegisterDeclinedWorkSubscriber — another observed-state capture subscriber. LLM-210.
func RegisterNoHiringSubscriber(w *World) {
	if w == nil {
		panic("sim: RegisterNoHiringSubscriber requires a non-nil world")
	}
	w.Subscribe(SubscriberFunc(handleNoHiringOnArrival))
}
