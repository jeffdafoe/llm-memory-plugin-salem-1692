package sim_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// turn_state_gate_test.go — ZBBS-WORK-370 (2/2) the backstop: sim.SpeakTo
// rejects an NPC's idle re-pitch of a peer it already addressed and is still
// awaiting a reply from, with the new-news / responding / PC / window carve-
// outs. Reuses buildSpeakTestWorld / captureSpoke from the sibling test files
// (same sim_test package). The gate reads pre-speak turn-state, so each case
// opens the edge with a first speak (hasNewNews=true) before probing the gate.

var gateBase = time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)

// TestTurnStateGate_IdleRepitchRejected — the core case: an NPC that already
// spoke to a peer and awaits their reply, re-addressing them on a tick with no
// new news, is rejected.
func TestTurnStateGate_IdleRepitchRejected(t *testing.T) {
	w, stop := buildSpeakTestWorld(t,
		actorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1"},
		actorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	// First address opens hannah's awaiting-reply edge toward ezekiel.
	if _, err := w.Send(sim.SpeakTo("hannah", "Care for bread?", "Ezekiel", nil, true, gateBase)); err != nil {
		t.Fatalf("first speak (opens edge): %v", err)
	}
	captured := captureSpoke(t, w)
	// Idle re-pitch a second later, no new news → rejected.
	_, err := w.Send(sim.SpeakTo("hannah", "Still want bread?", "Ezekiel", nil, false, gateBase.Add(time.Second)))
	if err == nil {
		t.Fatal("idle re-pitch should be rejected by the turn-state backstop")
	}
	if !strings.Contains(err.Error(), "awaiting their reply") {
		t.Errorf("reject reason = %q, want it to mention awaiting their reply", err.Error())
	}
	if len(*captured) != 0 {
		t.Errorf("a rejected speak must not emit a Spoke event; got %d", len(*captured))
	}
}

// TestTurnStateGate_NewNewsExempt — the same re-pitch is allowed when the tick
// carries fresh news (a real event behind it), so a legitimate follow-up
// ("here is your bread") commits.
func TestTurnStateGate_NewNewsExempt(t *testing.T) {
	w, stop := buildSpeakTestWorld(t,
		actorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1"},
		actorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	if _, err := w.Send(sim.SpeakTo("hannah", "Care for bread?", "Ezekiel", nil, true, gateBase)); err != nil {
		t.Fatalf("first speak: %v", err)
	}
	// hasNewNews=true → exempt even while awaiting the reply.
	if _, err := w.Send(sim.SpeakTo("hannah", "Here is your bread.", "Ezekiel", nil, true, gateBase.Add(time.Second))); err != nil {
		t.Errorf("new-news follow-up should be allowed, got: %v", err)
	}
}

// TestTurnStateGate_ReplyToAddresserAllowed — replying to a peer that addressed
// you is allowed even with no new news. The speaker holds no outgoing edge to
// that peer (the peer's utterance, as the awaited party, is what one would be
// replying to), so the gate's hasLiveAwaitEdge check is false — a genuine reply
// passes implicitly, no explicit "responding" carve-out needed.
func TestTurnStateGate_ReplyToAddresserAllowed(t *testing.T) {
	w, stop := buildSpeakTestWorld(t,
		actorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1"},
		actorSpec{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	// bob addresses hannah → bob awaits hannah; hannah holds no edge toward bob.
	if _, err := w.Send(sim.SpeakTo("bob", "Hannah, a word?", "Hannah", nil, true, gateBase)); err != nil {
		t.Fatalf("bob->hannah: %v", err)
	}
	// hannah replies to bob, no news → allowed (she has no live edge toward bob).
	if _, err := w.Send(sim.SpeakTo("hannah", "Aye, Bob?", "Bob", nil, false, gateBase.Add(time.Second))); err != nil {
		t.Errorf("a reply to the peer that addressed you should be allowed, got: %v", err)
	}
}

// TestTurnStateGate_ThirdPartyEdgeDoesNotExempt — regression for the bug
// code_review caught: an unrelated incoming edge must NOT exempt an idle
// re-pitch of a DIFFERENT peer. hannah awaits ezekiel; bob then addresses hannah
// (an edge from a third party); hannah's re-pitch of ezekiel with no news is
// still rejected — bob awaiting hannah is irrelevant to ezekiel.
func TestTurnStateGate_ThirdPartyEdgeDoesNotExempt(t *testing.T) {
	w, stop := buildSpeakTestWorld(t,
		actorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1"},
		actorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1"},
		actorSpec{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	if _, err := w.Send(sim.SpeakTo("hannah", "Care for bread?", "Ezekiel", nil, true, gateBase)); err != nil {
		t.Fatalf("hannah->ezekiel: %v", err)
	}
	// bob addresses hannah. bob speaking only clears edges against bob, so
	// hannah's edge toward ezekiel survives.
	if _, err := w.Send(sim.SpeakTo("bob", "Hannah, a word?", "Hannah", nil, true, gateBase.Add(time.Second))); err != nil {
		t.Fatalf("bob->hannah: %v", err)
	}
	// hannah re-pitches ezekiel, no news → still rejected (the bob->hannah edge
	// is unrelated to whether hannah may re-address ezekiel).
	_, err := w.Send(sim.SpeakTo("hannah", "Still want bread?", "Ezekiel", nil, false, gateBase.Add(2*time.Second)))
	if err == nil {
		t.Fatal("an idle re-pitch of ezekiel must be rejected despite an unrelated bob->hannah edge")
	}
	if !strings.Contains(err.Error(), "awaiting their reply") {
		t.Errorf("reject reason = %q, want it to mention awaiting their reply", err.Error())
	}
}

// TestTurnStateGate_PCNeverGated — a PC speaker is never subject to the
// backstop, even re-addressing the same peer with no news (a human may say
// whatever, whenever).
func TestTurnStateGate_PCNeverGated(t *testing.T) {
	w, stop := buildSpeakTestWorld(t,
		actorSpec{id: "jeff", displayName: "Jeff", kind: sim.KindPC, huddleID: "h1"},
		actorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	if _, err := w.Send(sim.SpeakTo("jeff", "Ezekiel, hello.", "Ezekiel", nil, true, gateBase)); err != nil {
		t.Fatalf("first PC speak: %v", err)
	}
	// Even with hasNewNews=false and a live edge, the PC is exempt via the Kind
	// check.
	if _, err := w.Send(sim.SpeakTo("jeff", "Ezekiel, you there?", "Ezekiel", nil, false, gateBase.Add(time.Second))); err != nil {
		t.Errorf("PC speak must never be gated, got: %v", err)
	}
}

// TestTurnStateGate_WindowExpiryReopens — once the addressee-kind window
// lapses, the awaiting edge is no longer live and a re-initiation is allowed
// (anti-lockup), so a conversation with an unresponsive party can resume.
func TestTurnStateGate_WindowExpiryReopens(t *testing.T) {
	w, stop := buildSpeakTestWorld(t,
		actorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1"},
		actorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	if _, err := w.Send(sim.SpeakTo("hannah", "Care for bread?", "Ezekiel", nil, true, gateBase)); err != nil {
		t.Fatalf("first speak: %v", err)
	}
	// Within the default 60s NPC window → still gated.
	if _, err := w.Send(sim.SpeakTo("hannah", "Bread?", "Ezekiel", nil, false, gateBase.Add(30*time.Second))); err == nil {
		t.Error("within the window, the re-pitch should still be gated")
	}
	// Past the window → the edge lapsed → allowed.
	if _, err := w.Send(sim.SpeakTo("hannah", "Bread, perhaps?", "Ezekiel", nil, false, gateBase.Add(2*time.Minute))); err != nil {
		t.Errorf("after the window lapses, the re-initiation should be allowed, got: %v", err)
	}
}

// TestTurnStateGate_WholeHuddleNotGated — a no-addressee (whole-huddle)
// utterance opens no directed edge and is never gated, even while the speaker
// awaits a specific peer.
func TestTurnStateGate_WholeHuddleNotGated(t *testing.T) {
	w, stop := buildSpeakTestWorld(t,
		actorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1"},
		actorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	if _, err := w.Send(sim.SpeakTo("hannah", "Care for bread?", "Ezekiel", nil, true, gateBase)); err != nil {
		t.Fatalf("first speak: %v", err)
	}
	// Whole-huddle (no `to`, no vocative) → addressedID empty → not gated.
	if _, err := w.Send(sim.SpeakTo("hannah", "A fine morning to all.", "", nil, false, gateBase.Add(time.Second))); err != nil {
		t.Errorf("a whole-huddle utterance must not be gated, got: %v", err)
	}
}
