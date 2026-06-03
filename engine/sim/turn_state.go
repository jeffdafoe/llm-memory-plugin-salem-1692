package sim

import "time"

// turn_state.go — ZBBS-WORK-370 conversation turn-state: the directed
// addressed/awaiting-reply edge between huddle participants.
//
// The problem this exists for: "may I speak?" in v2 is still answered by WORLD
// state (a warrant fired because speech was heard nearby / a peer is present),
// never by DIALOGUE state (did the party I addressed actually reply yet). So
// NPCs talk over each other, re-pitch a customer who hasn't answered, and chase
// an order every tick. The reactor may still WAKE an actor; turn-state decides
// whether a woken actor SPEAKS.
//
// The primitive is a single per-actor directed edge, `Actor.awaitingReplyFrom`
// (addressee -> when I last addressed them), and everything derives from it:
//
//   - "I am waiting on T"        -> my own awaitingReplyFrom[T] is live.
//   - "it is my turn / I'm owed
//     a reply from P"            -> some peer P holds awaitingReplyFrom[me].
//
// There is no second "addressed_by" map — storing both directions would let
// the two views drift; the inbound view is always derived by scanning peers.
//
// This file maintains the edge (set on speak, clear on reply / leave). The GATE
// that consumes it — the perception turn-line plus the sim.Speak backstop that
// rejects an idle re-pitch, with the new-news exemption — and the lazy
// time-window expiry (PC vs NPC liveness windows) land in the follow-up slice.
// Until then the edge is maintained but read by nothing, exactly like the
// Spoke.AddressedID seam it rides on (WORK-369).
//
// State lives on the SPEAKER, ephemeral like heardSpeechMisses and the rest of
// the reactor bookkeeping (wiped on LoadWorld; a post-restart conversation
// simply starts with no pending turns — a UX wrinkle, not a correctness
// failure). This is the deliberate generalization of HOME-331's heardSpeechMiss
// cell: same per-(actor, interlocutor) shape, same two mutation callsites
// (sim.Speak sets/clears; departure drops), upgraded from a miss-count into the
// full directed turn edge. All methods MUST run on the world goroutine.

// awaitReply records that this actor (the speaker) just addressed `addressee`
// and is now awaiting their reply, stamped at `now`. A later utterance by
// `addressee` clears it (satisfyAwaitedReplyFrom). No-op for an empty addressee
// — a whole-huddle / no-one-specific utterance (Spoke.AddressedID == "") opens
// no directed edge. Lazily allocates the map.
func (a *Actor) awaitReply(addressee ActorID, now time.Time) {
	if addressee == "" {
		return
	}
	if a.awaitingReplyFrom == nil {
		a.awaitingReplyFrom = make(map[ActorID]time.Time, 1)
	}
	a.awaitingReplyFrom[addressee] = now
}

// satisfyAwaitedReplyFrom clears any "I am awaiting a reply from `speaker`"
// edge on this actor — called for every huddle peer when `speaker` speaks,
// since ANY utterance by the awaited party is itself the reply that takes the
// turn (no addressee match needed). delete on an absent key is a no-op.
func (a *Actor) satisfyAwaitedReplyFrom(speaker ActorID) {
	delete(a.awaitingReplyFrom, speaker)
}

// dropAwaitingReplies clears this actor's entire outgoing edge set — used when
// the actor leaves its huddle or the huddle concludes. An actor is only ever in
// one huddle, so every edge it holds points at a member of that huddle;
// dissolving the conversation makes them all moot. The reciprocal cleanup
// (removing this actor from peers' maps) is done by the leave/conclude callers.
func (a *Actor) dropAwaitingReplies() {
	a.awaitingReplyFrom = nil
}
