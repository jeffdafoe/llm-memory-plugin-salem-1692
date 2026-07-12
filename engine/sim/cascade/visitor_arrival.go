package cascade

import (
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// visitor_arrival.go — LLM-379. Records where a traveler ACTUALLY goes on his rounds.
// The engine no longer chooses a visitor's stops (that was the v1 circuit fighting his
// own move_to); he navigates himself with move_to. This subscriber watches
// sim.ActorArrived and, when a visitor lands at a keeper-business, marks it visited
// (sim.RecordVisitorArrival) so the "## Your rounds" cue can render it back and he
// routes onward instead of repeating a shop. All gating — visitor? on his rounds?
// keeper present? not the inn? — lives in the Command, validated against LIVE actor
// state, so a stale event degrades to a no-op rather than acting on event coordinates.
//
// DestStructureID (not FinalStructureID) is the signal: a walk INTO an open shop and a
// doorstep/knock at a shut-door shop's visitor slot both name the shop there, and both
// put the traveler co-present with the keeper for a trade — the arrival huddle
// (business_arrival.go) forms in either case.

// handleVisitorArrival is the ActorArrived subscriber. Calling the Command's Fn
// directly is safe: subscribers dispatch inline from emit on the world goroutine (same
// posture as handleBusinessArrival).
func handleVisitorArrival(w *sim.World, evt sim.Event) {
	arrived, ok := evt.(*sim.ActorArrived)
	if !ok {
		return
	}
	if arrived.DestStructureID == "" {
		return // not a structure-targeted arrival — nothing to record
	}
	_, _ = sim.RecordVisitorArrival(arrived.ActorID, arrived.DestStructureID).Fn(w)
}

// RegisterVisitorArrival wires the visitor rounds-recording subscriber. Must run on the
// world goroutine (before World.Run or inside a Command.Fn).
//
// Idempotency: a double registration's second dispatch re-records an already-visited
// shop, which appendUniqueStructure dedupes — a no-op, mirroring RegisterBusinessArrival.
func RegisterVisitorArrival(w *sim.World) {
	if w == nil {
		panic("cascade: RegisterVisitorArrival requires a non-nil world")
	}
	w.Subscribe(sim.SubscriberFunc(handleVisitorArrival))
}
