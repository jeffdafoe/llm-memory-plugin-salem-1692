package sim

import "time"

// seek_work_visited.go — LLM-563. Experiential "I called there looking for work
// just now" memory, the no-event sibling of closed_business.go / declined_work.go
// / no_hiring.go.
//
// A workless worker's seek-work directory (perception buildSeekWorkPlaces) drops
// a business the worker has learned something bad about — shut (ObservedClosed),
// an explicit refusal (ObservedDeclinedWork), a keeper on break
// (ObservedNoHiring). Every one of those requires a REJECTION event. A visit
// where nothing happened at all — the keeper was there, hireable, and the ask
// simply went nowhere — stamped nothing, so the business stayed in the directory
// at full standing and the same door could be walked to again immediately,
// indefinitely (the live Patience Walker case: the General Store four times in
// twelve minutes, 21 business trips in three hours with nothing learned).
//
// The fix is a memory of the VISIT itself: arriving at a business while
// job-hunting stamps ObservedSeekWorkVisited, whatever came of the call. Unlike
// its three siblings this memory does not DROP the business — "I was just there"
// is far weaker evidence than a refusal, and dropping on it would empty the
// whole directory after one tour of town while the seek-work impulse kept
// nagging with no destination to offer. Instead perception RANKS visited
// businesses after untried ones (least-recently-visited first), so the directory
// converges on doors not yet knocked and a business the worker just left cannot
// sit at the top of the list.
//
// This is the CAPTURE half (an ActorArrived subscriber, additive). The SURFACE
// half lives in perception (workerRememberedVisited in build.go, read by
// buildSeekWorkPlaces for ordering). The store is the unified observed-state
// memory (observed_state.go, the ObservedSeekWorkVisited condition).

// SeekWorkVisitedMemoryTTL is how long a "called there looking for work"
// observation keeps a business ranked behind untried ones before perception
// restores its normal nearest-first standing (LLM-563). The weakest evidence
// tier — "not just now", not "refused" — so it matches NoHiringMemoryTTL (2h)
// rather than the 4h shut / 12h declined memories. From the live data: the
// observed re-visit churn ran a full tour of six businesses in well under two
// hours, so 2h covers a complete ask-around round with margin while still
// letting the worker retry the same doors within the day. 2 game-hours — tunable.
const SeekWorkVisitedMemoryTTL = 2 * time.Hour

// handleSeekWorkVisitedOnArrival is the ActorArrived subscriber that records a
// WORKLESS worker's memory of having called at a business. It fires once per
// arrival and is a no-op for anyone who does not consult the seek-work directory
// (non-agents, non-workers, employed workers) or for arrivals that don't resolve
// to a business other than the arriver's own workplace — the same gate as
// handleNoHiringOnArrival, since buildSeekWorkPlaces is the sole reader.
//
// The stamp is UNCONDITIONAL on a qualifying arrival: "I was there" is true
// whatever state the business was in. When the visit also earns a stronger
// memory (shut / no-hiring), that sibling's DROP outranks this one's ranking for
// its own TTL — the visited stamp just keeps the business deprioritised after
// the stronger belief clears or decays. There is no self-clear: a re-visit
// refreshes the stamp (Observe overwrites), and the memory otherwise just decays
// on its TTL.
func handleSeekWorkVisitedOnArrival(w *World, evt Event) {
	arr, ok := evt.(*ActorArrived)
	if !ok {
		return
	}
	a := w.Actors[arr.ActorID]
	if a == nil || !isAgentNPC(a) || !actorIsWorker(a) || actorHasResolvableWorkplace(w, a) {
		return
	}
	structureID, ok := businessArrivedAt(w, a, arr)
	if !ok {
		return
	}
	a.Observed.Observe(ObservedStateKey{StructureID: structureID, Condition: ObservedSeekWorkVisited}, arr.At)
}

// RegisterSeekWorkVisitedSubscriber wires the visited-business-memory subscriber.
// Call before World.Run or from inside a Command (world-goroutine-safe). Mirrors
// RegisterNoHiringSubscriber — another observed-state capture subscriber. LLM-563.
func RegisterSeekWorkVisitedSubscriber(w *World) {
	if w == nil {
		panic("sim: RegisterSeekWorkVisitedSubscriber requires a non-nil world")
	}
	w.Subscribe(SubscriberFunc(handleSeekWorkVisitedOnArrival))
}
