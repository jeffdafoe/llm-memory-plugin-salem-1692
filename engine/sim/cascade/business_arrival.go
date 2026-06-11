package cascade

import (
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// business_arrival.go — ZBBS-HOME-425. Indoor-arrival hospitality trigger:
// subscribes to sim.ActorArrived and, for an arrival INSIDE a structure,
// runs the arrival business-huddle bootstrap. All gating (conversational
// kind, ghost-PC staleness, at-post receptive keeper present, no-keeper
// no-op) lives in sim.EnsureArrivalBusinessHuddle, which validates against
// LIVE actor state — a stale event degrades to a no-op rather than acting
// on event coordinates. The outdoor counterpart is arrival_encounter.go;
// the two split on FinalStructureID and never both fire for one event.
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
		return // outdoor — the encounter cascade owns it
	}
	// The Command never returns an error (it logs and degrades internally),
	// so there is nothing to handle here.
	_, _ = sim.EnsureArrivalBusinessHuddle(arrived.ActorID, arrived.At).Fn(w)
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
