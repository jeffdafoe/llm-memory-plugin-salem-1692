package sim

import (
	"sort"
	"time"
)

// labor_arrival.go — LLM-229. The ActorArrived subscriber that starts a hired
// worker's work window once they (and the owner) are at the employer's
// workplace.
//
// AcceptWork flips an off-site hire to LaborStateEnRoute and walks the worker to
// the employer's post instead of starting the work in place (labor_commands.go).
// The work window must not start until the worker is actually AT the post WITH
// the owner present — a worker never enters an establishment ahead of its owner.
// Two arrivals drive the flip to Working:
//
//   - the WORKER arrives at the workplace: if the owner is already there, start
//     the job; if not, wait at the loiter pin (EnRouteWaiting) for the owner.
//   - the OWNER arrives at their own workplace: pull in any workers of theirs
//     who were waiting at the loiter — send each inside, which fires another
//     worker-arrival that starts the job.
//
// A worker who arrives at the loiter pin of an interior shop with the owner
// present is sent inside first (sendWorkerToWorkplace → enter); it is the SECOND
// arrival (now inside) that starts the job. For a doorless stall the staff pin
// IS the post, so the first arrival with the owner present starts it.
//
// The bounded-wait backstop (labor_settle.go) voids an EnRoute offer that never
// reaches this flip — a walk deadlock, or an owner who never shows — past its
// EnRouteDeadline, so nothing here needs a timeout of its own.

// handleLaborArrivalOnArrival is the ActorArrived subscriber that advances an
// EnRoute labor offer toward Working (LLM-229). It reacts to two arrivals: the
// worker reaching the workplace, and the employer reaching their own post (which
// releases workers waiting on them). Non-labor arrivals fall through cheaply (an
// empty ledger, or an arriver who holds no EnRoute offer as worker or employer).
// Runs on the world goroutine — w.emit dispatches subscribers synchronously, and
// the walk this issues emits only ActorMoveStarted (the resulting ActorArrived
// fires on a later locomotion tick, so there is no re-entrant arrival cascade).
func handleLaborArrivalOnArrival(w *World, evt Event) {
	arr, ok := evt.(*ActorArrived)
	if !ok {
		return
	}
	if len(w.LaborLedger) == 0 {
		return
	}
	// The arriver as WORKER: their own relocation reached a destination.
	if offer := workerEnRouteOffer(w, arr.ActorID); offer != nil {
		advanceEnRouteOffer(w, offer, arr.At)
	}
	// The arriver as EMPLOYER: reaching their own post releases workers who were
	// waiting at the loiter for them to show. LaborID order so the pull-in (and
	// any resulting speech / action-log beat) is deterministic, not map-ordered.
	for _, offer := range employerEnRouteOffers(w, arr.ActorID) {
		advanceEnRouteOffer(w, offer, arr.At)
	}
}

// advanceEnRouteOffer moves one EnRoute offer forward given the current
// positions of its worker and employer: start the work window when the worker is
// at the post with the owner present; send the worker in when the owner is
// present but the worker is still outside; otherwise mark the worker as waiting
// at the loiter for the owner (perception phrasing). Safe to call on either
// party's arrival, and a no-op once the offer has left EnRoute.
// World-goroutine-only.
func advanceEnRouteOffer(w *World, offer *LaborOffer, at time.Time) {
	if offer.State != LaborStateEnRoute {
		return
	}
	worker := w.Actors[offer.WorkerID]
	employer := w.Actors[offer.EmployerID]
	if worker == nil || employer == nil {
		return // a vanished party — the bounded-wait backstop voids the offer
	}
	ws := employer.WorkStructureID
	if ws == "" {
		return // no post to reach — the backstop voids it
	}
	ownerPresent := actorAtWorkpost(w, employer, ws)
	switch {
	case ownerPresent && actorAtWorkpost(w, worker, ws):
		// Worker at the post with the owner present — the work begins.
		startLaborWork(w, offer, worker, employer, at)
	case ownerPresent:
		// The owner is here but the worker isn't at the post yet (arrived at the
		// loiter of an interior shop, or the owner just showed while they waited)
		// — send them in. The enter-arrival that follows starts the job.
		offer.EnRouteWaiting = false
		sendWorkerToWorkplace(w, worker, employer, false, at)
	default:
		// Owner absent. A worker who has reached the workplace waits at the loiter
		// for the owner; a still-walking worker just keeps walking. Flag the
		// arrived-and-waiting case so the self-state reads "waiting for X" rather
		// than "on your way to X."
		if atWorkplaceVicinity(w, worker, ws) {
			offer.EnRouteWaiting = true
		}
	}
}

// atWorkplaceVicinity reports whether the worker has reached the employer's
// workplace — inside it, at its staff pin, or standing at its outdoor loiter pin
// (the wait spot). Distinct from actorAtWorkpost, which for an interior shop
// requires being INSIDE: a worker waiting at the door for the owner is at the
// vicinity but not yet at the post. World-goroutine-only.
func atWorkplaceVicinity(w *World, a *Actor, ws StructureID) bool {
	if actorAtWorkpost(w, a, ws) {
		return true
	}
	pin, ok := effectiveLoiterTile(w, ws)
	if !ok {
		return false
	}
	return a.Pos.Chebyshev(pin) <= LoiterAttributionTiles
}

// workerEnRouteOffer returns workerID's EnRoute offer, or nil. At most one live
// job per worker (workerHasLiveJob gates accept), so the first match wins.
// World-goroutine-only.
func workerEnRouteOffer(w *World, workerID ActorID) *LaborOffer {
	for _, o := range w.LaborLedger {
		if o != nil && o.State == LaborStateEnRoute && o.WorkerID == workerID {
			return o
		}
	}
	return nil
}

// employerEnRouteOffers returns employerID's EnRoute offers, sorted by LaborID so
// releasing waiting workers on the employer's arrival is deterministic (an
// employer can have several — John Ellis hired two). Nil when none.
// World-goroutine-only.
func employerEnRouteOffers(w *World, employerID ActorID) []*LaborOffer {
	var ids []LaborID
	for id, o := range w.LaborLedger {
		if o != nil && o.State == LaborStateEnRoute && o.EmployerID == employerID {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]*LaborOffer, 0, len(ids))
	for _, id := range ids {
		out = append(out, w.LaborLedger[id])
	}
	return out
}

// RegisterLaborArrivalSubscriber wires the LLM-229 en-route → working arrival
// subscriber. Call before World.Run or from inside a Command (world-goroutine-
// safe). Mirrors RegisterGatherTargetSubscriber — another ActorArrived subscriber.
func RegisterLaborArrivalSubscriber(w *World) {
	if w == nil {
		panic("sim: RegisterLaborArrivalSubscriber requires a non-nil world")
	}
	w.Subscribe(SubscriberFunc(handleLaborArrivalOnArrival))
}
