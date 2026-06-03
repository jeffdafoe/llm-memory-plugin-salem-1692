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
// This file BOTH maintains the edge (set on speak, clear on reply / leave —
// WORK-370 1/2) AND, below the "the gate" banner, consumes it (WORK-370 2/2):
// the sim.Speak backstop that rejects an idle re-pitch with the new-news / PC /
// whole-huddle carve-outs, plus the lazy PC-vs-NPC liveness windows. The
// perception turn-line + act-now-coda swap read the same edge off the published
// snapshot (perception/build.go buildTurnState).
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

// --- ZBBS-WORK-370 (2/2): the gate ----------------------------------------
//
// The edge above is now READ. Two layers consume it: the sim.Speak backstop
// (this file's helpers) rejects an idle re-pitch, and perception build renders
// a turn-line + swaps the act-now coda. Both apply the same liveness window so
// the rendered nudge and the hard reject agree on when an edge has lapsed.

// Default conversation turn-state liveness windows. After this long without a
// reply, an actor's outgoing await-edge to the silent party is treated as
// stale: the backstop stops suppressing a re-initiation and perception drops
// the "wait for their reply" line, so a conversation with an unresponsive party
// re-opens rather than locking up. Keyed on the ADDRESSEE's kind (Fork 3) — a
// human player is slow (reads + types), an NPC answers at tick speed — so the
// two windows differ by design. Both overridable via WorldSettings
// (pc_await_reply_window_seconds / npc_await_reply_window_seconds); these are
// the fallbacks when unset, the same lazy-default posture as
// DefaultPCPresenceStaleAfter / defaultMinReactorTickGap.
const (
	DefaultPCAwaitReplyWindow  = 5 * time.Minute
	DefaultNPCAwaitReplyWindow = 60 * time.Second
)

// awaitReplyWindow returns the liveness window for an await-reply edge whose
// ADDRESSEE is of the given kind, applying the WorldSettings override when set
// (>0) and the Default*AwaitReplyWindow fallback otherwise. PC addressees get
// the long window; every NPC kind gets the short one.
func (w *World) awaitReplyWindow(addresseeKind ActorKind) time.Duration {
	if addresseeKind == KindPC {
		if w.Settings.PCAwaitReplyWindow > 0 {
			return w.Settings.PCAwaitReplyWindow
		}
		return DefaultPCAwaitReplyWindow
	}
	if w.Settings.NPCAwaitReplyWindow > 0 {
		return w.Settings.NPCAwaitReplyWindow
	}
	return DefaultNPCAwaitReplyWindow
}

// hasLiveAwaitEdge reports whether this actor holds an outgoing await-reply edge
// to `addressee` that is still live at `now` under `window` — i.e. it addressed
// `addressee` within the window and hasn't yet been answered. A missing edge,
// or one older than the window, is not live. window <= 0 means "no expiry
// configured" → an existing edge counts as live (the hand-built-snapshot
// posture; a loaded world always resolves a positive window).
func (a *Actor) hasLiveAwaitEdge(addressee ActorID, now time.Time, window time.Duration) bool {
	stamp, ok := a.awaitingReplyFrom[addressee]
	if !ok {
		return false
	}
	if window <= 0 {
		return true
	}
	return now.Sub(stamp) < window
}

// Note: there is deliberately NO "is the speaker being awaited by someone"
// helper. The backstop needs no explicit responding carve-out — see the gate in
// SpeakTo: the per-pair edge invariant (a speak clears every incoming edge
// against the speaker) makes A->B and B->A mutually exclusive, so a live
// outgoing edge to the addressee already implies the addressee is not awaiting
// the speaker. A genuine reply is allowed implicitly.

// cloneAwaitingReplyFrom deep-copies an await-reply edge map for the published
// snapshot, so snapshot readers never alias the live Actor's mutable map.
// Returns nil for an empty/nil source (the common no-pending-turn case) so the
// snapshot field stays nil exactly when the live field is.
func cloneAwaitingReplyFrom(src map[ActorID]time.Time) map[ActorID]time.Time {
	if len(src) == 0 {
		return nil
	}
	out := make(map[ActorID]time.Time, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
