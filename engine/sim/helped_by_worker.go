package sim

import "time"

// helped_by_worker.go — LLM-228. An employer's experiential memory that a
// specific worker COMPLETED a paid job for them. The employer-side, person-keyed
// counterpart to declined_work.go's ObservedDeclinedWork ("this employer turned
// me down"): here the EMPLOYER remembers a worker who helped.
//
// When that same worker solicits the employer again, perception surfaces the
// memory at the "## Work offers awaiting your decision" section ("You remember X
// lending you a hand recently…"), so the re-hire choice is informed by the past
// benefit rather than by an engine-authored hire-value pitch at the decision
// point. Such a pitch shipped in #690 and was pulled the same day in #691 — NPCs
// hire willingly with no tangible-benefit argument, and a standing value line
// above the accept/decline footer nudged every affordable offer toward accept,
// against the no-nudging principle. The better legibility surface is experiential
// instead: after the benefit has actually happened, let the keeper recall it.
//
// This is the CAPTURE half (a LaborResolved subscriber, additive). The SURFACE
// half lives in perception (buildLaborOffersForMe reads the memory into
// LaborOfferView.HelpedBeforeRecently; renderLaborOffers renders the recall
// line). The store itself is the unified observed-state memory in
// observed_state.go (the ObservedHelpedByWorker condition) — the same decaying,
// restart-lossy store that backs ObservedClosed / ObservedDeclinedWork, here
// carrying a PEER-keyed rather than structure-keyed belief.

// HelpedByWorkerMemoryTTL is how long an employer remembers a worker's completed
// job before perception stops surfacing the returning-helper recall (LLM-228).
// 36 game-hours — long enough that the worker who helped yesterday is still
// recalled when they solicit again today, short enough that the recall reads as
// a recent beat rather than a permanent fixture. Warmer than DeclinedWorkMemoryTTL
// (12h), which keeps a worker off a refusing employer only for the working day;
// a good turn is worth remembering a little longer than a brush-off.
const HelpedByWorkerMemoryTTL = 36 * time.Hour

// handleHelpedByWorkerOnResolved is the LaborResolved subscriber that records an
// employer's memory of a worker who completed a paid job for them. It fires ONLY
// on the Completed terminal — the one terminal where the reward actually
// transferred (labor_settle.go settleCompletedLabor). A stiffed job
// (FailedUnavailable with work performed: the worker finished but the employer
// could no longer cover the reward) does NOT stamp — "you got more done" must not
// read over an unpaid settle, and that aggrieved beat is already carried by the
// InteractionLeftWorkerUnpaid relationship fact. Declined / Expired never reach
// Completed either.
//
// The memory lives on the EMPLOYER's Observed store, keyed by the WORKER's
// PeerID — the mirror of ObservedDeclinedWork, which lives on the worker keyed by
// the employer's workplace. No workplace is involved here: this belief is about a
// person (the returning worker), so it needs no structure. A no-op when the
// employer is gone or is not an NPC that perceives (a PC employer carries its own
// continuity through the player, and a decorative actor never takes a turn).
func handleHelpedByWorkerOnResolved(w *World, evt Event) {
	res, ok := evt.(*LaborResolved)
	if !ok || res.TerminalState != LaborTerminalStateCompleted {
		return
	}
	employer := w.Actors[res.EmployerID]
	if employer == nil || !isAgentNPC(employer) {
		return // only NPC employers perceive the decision-section recall
	}
	if res.WorkerID == "" {
		return // no worker to remember (defensive; a Completed offer always has one)
	}
	employer.Observed.Observe(
		ObservedStateKey{PeerID: res.WorkerID, Condition: ObservedHelpedByWorker},
		res.At,
	)
}

// RegisterHelpedByWorkerSubscriber wires the helped-by-worker-memory subscriber.
// Call before World.Run or from inside a Command (world-goroutine-safe). Mirrors
// RegisterDeclinedWorkSubscriber — another observed-state capture subscriber.
// LLM-228.
func RegisterHelpedByWorkerSubscriber(w *World) {
	if w == nil {
		panic("sim: RegisterHelpedByWorkerSubscriber requires a non-nil world")
	}
	w.Subscribe(SubscriberFunc(handleHelpedByWorkerOnResolved))
}
