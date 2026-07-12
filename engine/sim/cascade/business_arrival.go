package cascade

import (
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// business_arrival.go — ZBBS-HOME-425. Business-arrival hospitality trigger:
// subscribes to sim.ActorArrived and runs the arrival business-huddle
// bootstrap for two staffed-business cases — an arrival INSIDE the structure
// (walk into an open shop), and (LLM-384) a non-knock arrival that VISITS a
// structure from its loiter pin OUTSIDE it (a market stall has no interior; an
// owner-only shop turns a non-member's enter into a loiter visit). All gating
// (conversational kind, ghost-PC staleness, at-post receptive keeper present,
// live loiter/inside scope, no-keeper no-op) lives in
// sim.EnsureArrivalBusinessHuddle, which validates against LIVE actor state —
// a stale event degrades to a no-op rather than acting on event coordinates.
// A knock (owner-only, PC-driven) routes to the knock bootstrap instead; a
// plain open-ground arrival belongs to the encounter cascade
// (arrival_encounter.go, which skips both knocked and stall-loiter arrivers).
//
// The bootstrap's keeper-first join order makes the resulting
// HuddleJoined(arriver) carry the keeper in OtherMembers, which is what
// the businessowner greet subscriber keys on — so a customer walking into
// a staffed business is greeted with no LLM call, exactly the hospitality
// substrate's purpose.

// handleBusinessArrival is the ActorArrived subscriber. Calling the
// Command's Fn directly is safe: subscribers dispatch inline from emit on
// the world goroutine (same posture as handleArrivalEncounter).
func handleBusinessArrival(w *sim.World, evt sim.Event) {
	arrived, ok := evt.(*sim.ActorArrived)
	if !ok {
		return
	}
	if arrived.FinalStructureID == "" {
		// Outdoor arrival. A knock (ZBBS-HOME-445) lands here by definition —
		// the knocker stops at the loiter slot, outside — and DestStructureID
		// names the knocked structure; the knock bootstrap forms the
		// across-the-doorway service huddle.
		if arrived.Knocked && arrived.DestStructureID != "" {
			_, _ = sim.EnsureKnockServiceHuddle(arrived.ActorID, arrived.DestStructureID, arrived.At).Fn(w)
			return
		}
		// LLM-384: a non-knock arrival that walked to a structure as a VISIT
		// stops at its loiter pin, OUTSIDE it — a market stall has no interior,
		// and an NPC turned away from an owner-only shop's membership gate
		// loiters rather than enters. The patron is conversationally scoped
		// across the threshold to the keeper working within (ZBBS-HOME-378),
		// but LLM-375 excludes an open-stall loiterer from the outdoor encounter
		// (arrival_encounter.go), so without this nothing forms the keeper's
		// huddle until the patron transacts — and the patron then speaks FIRST,
		// robbing the inside keeper of the HuddleJoined greet turn (the engine
		// greet was dropped for VA keepers, ZBBS-HOME-461). Form the keeper's
		// structure huddle here, keeper-first, the loiter analog of the indoor
		// bootstrap below. EnsureArrivalBusinessHuddle re-validates the live
		// loiter scope and no-ops when no at-post keeper is present, so a visit
		// to a keeperless structure (a home, a shut shop) stays silent.
		if arrived.DestStructureID != "" {
			_, _ = sim.EnsureArrivalBusinessHuddle(arrived.ActorID, arrived.DestStructureID, arrived.At).Fn(w)
		}
		return
	}
	// The event's final structure rides into the Command, which verifies the
	// actor is still inside it (stale-arrival guard). The Command never
	// returns an error (it logs and degrades internally).
	_, _ = sim.EnsureArrivalBusinessHuddle(arrived.ActorID, arrived.FinalStructureID, arrived.At).Fn(w)
}

// RegisterBusinessArrival wires the indoor-arrival hospitality subscriber.
// Must run on the world goroutine (before World.Run or inside a
// Command.Fn).
//
// Idempotency: a double registration's second dispatch observes the
// arriver already huddled (the first dispatch joined them), so
// EnsureArrivalBusinessHuddle's CurrentHuddleID pre-filter short-circuits
// — a no-op, mirroring RegisterEncounter's posture.
func RegisterBusinessArrival(w *sim.World) {
	if w == nil {
		panic("cascade: RegisterBusinessArrival requires a non-nil world")
	}
	w.Subscribe(sim.SubscriberFunc(handleBusinessArrival))
}
