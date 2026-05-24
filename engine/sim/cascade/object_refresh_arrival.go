package cascade

import (
	"log"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// object_refresh_arrival.go — object-refresh-on-arrival wiring.
//
// Subscribes to sim.ActorArrived and calls sim.ApplyObjectRefreshAtArrival
// for the arriver. That command resolves the village object the actor is
// loitering at (resolveLoiteringObject, Chebyshev <= 1 to the loiter pin)
// and, if it carries refresh rows, applies them to the actor's needs,
// decrements finite supplies, and upserts any dwell credits. This is the
// v2 port of v1's applyObjectRefreshAtArrival reaction — the path by which
// arriving at a well/shade tree/food cart actually recovers a need.
//
// The command self-filters: an arrival anywhere that is NOT a named refresh
// object's loiter pin resolves to nothing and returns an empty result, so a
// blanket subscribe to every ActorArrived is correct — no actor-kind or
// intent pre-filter is needed. ActorArrived fires once on move completion
// (not per tile step), so an actor walking THROUGH a refresh pin en route
// elsewhere never triggers a spurious refresh.

// handleObjectRefreshArrival is the ActorArrived subscriber that applies
// object refresh on arrival. Registered via RegisterObjectRefreshArrival;
// runs synchronously on the world goroutine from inside sim.World.emit.
//
// Calling ApplyObjectRefreshAtArrival(...).Fn(w) directly here is safe and
// correct: subscribers dispatch inline from emit, so we are already on the
// world goroutine. Its need/dwell mutations and any consequent emits nest
// under the original ActorArrived's withRoot scope, carrying the arrival's
// EventID as their RootEventID.
//
// Event-freshness invariant: the command resolves off the actor's LIVE
// position (it takes only actorID), so if a same-tick subscriber moved the
// arriver after this event was stamped, the refresh would key off the
// wrong tile. Guard on FinalPosition match — if the arriver is no longer
// standing where the event says, skip; the next genuine arrival handles it.
//
// On command error (actor vanished between emit and dispatch): log and
// continue. An empty ArrivalRefreshResult (arrived somewhere with no
// refresh object) is the common no-op case and is not logged.
func handleObjectRefreshArrival(w *sim.World, evt sim.Event) {
	arrived, ok := evt.(*sim.ActorArrived)
	if !ok {
		return
	}
	arriver, ok := w.Actors[arrived.ActorID]
	if !ok {
		return
	}
	if arriver.Pos.X != arrived.FinalPosition.X ||
		arriver.Pos.Y != arrived.FinalPosition.Y {
		return
	}

	if _, err := sim.ApplyObjectRefreshAtArrival(arrived.ActorID).Fn(w); err != nil {
		log.Printf(
			"sim/cascade: object-refresh-on-arrival failed for %q: %v",
			arrived.ActorID, err,
		)
	}
}

// RegisterObjectRefreshArrival wires the object-refresh-on-arrival
// subscriber into the world. Must run on the world goroutine (call before
// World.Run or from inside a Command.Fn).
//
// Idempotency: registering twice dispatches the command twice per arrival.
// The second pass re-resolves the same loiter object and re-applies its
// refresh rows — NOT a no-op (needs would move twice, finite supplies
// decrement twice). Register exactly once; production wires it via
// RegisterProductionCascades.
func RegisterObjectRefreshArrival(w *sim.World) {
	if w == nil {
		panic("cascade: RegisterObjectRefreshArrival requires a non-nil world")
	}
	w.Subscribe(sim.SubscriberFunc(handleObjectRefreshArrival))
}
