package sim

import "time"

// declined_work.go — LLM-198. Experiential "this employer turned me down for
// work" memory. A workless worker's seek-work directory (perception
// buildSeekWorkPlaces) lists every business nearest-first; without a memory of
// being refused, a worker walks straight back to the nearest shop that just
// declined its solicit_work offer and re-solicits the same employer. The fix is
// experiential, not omniscient: when an employer DECLINES a worker's labor
// offer, the worker remembers that business — and perception drops it from the
// worker's seek-work directory so the worker tries the next-nearest business
// instead. The memory DECAYS after DeclinedWorkMemoryTTL so the worker retries
// the following day rather than treating the door as shut forever.
//
// This is the CAPTURE half (a LaborResolved subscriber, additive). The SURFACE
// half lives in perception (workerRememberedDeclinedWork in build.go, read by
// buildSeekWorkPlaces). The store itself is the unified observed-state memory in
// observed_state.go (the ObservedDeclinedWork condition), the same decaying,
// restart-lossy store that backs ObservedClosed / ObservedOutOfStock.
//
// Complements LLM-181, which drops a CO-PRESENT declined employer from
// hasSolicitableAudience (stops the worker re-bidding while standing there).
// This stops the worker RETURNING — the directory no longer points it back.

// DeclinedWorkMemoryTTL is how long a "this employer declined me" observation
// suppresses that business from the worker's seek-work directory before
// perception lists it again (LLM-198). 12 game-hours (Jeff, 2026-06-30) — long
// enough to keep the worker off a refusing employer for the rest of the working
// day, short enough that the next day is a fresh chance.
const DeclinedWorkMemoryTTL = 12 * time.Hour

// handleDeclinedWorkOnResolved is the LaborResolved subscriber that records the
// soliciting worker's memory of an employer declining its labor offer. It fires
// only on the Declined terminal (not Completed/Expired/FailedUnavailable) and is
// a no-op when either party is gone or the employer keeps no business — there is
// no directory entry to suppress for an employer with no workplace.
//
// The memory is keyed by the EMPLOYER's WorkStructureID — the business the
// seek-work directory names and the worker walks to — so the surface check drops
// exactly that entry. An employer can decline away from its own shop (e.g. met
// at the Tavern); keying on its workplace still suppresses the right directory
// entry, because going to that shop to find that employer would be wasted.
func handleDeclinedWorkOnResolved(w *World, evt Event) {
	res, ok := evt.(*LaborResolved)
	if !ok || res.TerminalState != LaborTerminalStateDeclined {
		return
	}
	worker := w.Actors[res.WorkerID]
	if worker == nil || !isAgentNPC(worker) {
		return // only NPC workers perceive the seek-work directory
	}
	employer := w.Actors[res.EmployerID]
	if employer == nil || employer.WorkStructureID == "" {
		return // no employer workplace ⇒ nothing in the directory to drop
	}
	worker.Observed.Observe(
		ObservedStateKey{StructureID: employer.WorkStructureID, Condition: ObservedDeclinedWork},
		res.At,
	)
}

// RegisterDeclinedWorkSubscriber wires the declined-work-memory subscriber. Call
// before World.Run or from inside a Command (world-goroutine-safe). Mirrors
// RegisterClosedBusinessSubscriber — another observed-state capture subscriber.
// LLM-198.
func RegisterDeclinedWorkSubscriber(w *World) {
	if w == nil {
		panic("sim: RegisterDeclinedWorkSubscriber requires a non-nil world")
	}
	w.Subscribe(SubscriberFunc(handleDeclinedWorkOnResolved))
}
