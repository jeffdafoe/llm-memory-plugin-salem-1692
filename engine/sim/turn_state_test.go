package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// turn_state_test.go — ZBBS-WORK-370 turn-state edge maintenance: sim.Speak
// opens the speaker->addressee awaiting-reply edge and clears any edge the
// speaker was awaited on; huddle leave/conclude drop the edges. The gate that
// READS these edges (the sim.Speak backstop + perception turn-line) is covered
// separately in turn_state_gate_test.go; this file pins only the edge
// bookkeeping the gate relies on.

// awaitingReplies reads the speaker's live turn-state map on the world
// goroutine (via a Command) and returns a copy — the edge lives on the live
// Actor; the published snapshot carries a deep-cloned copy (ActorSnapshot.
// AwaitingReplyFrom) that perception build reads.
func awaitingReplies(t *testing.T, w *sim.World, speaker sim.ActorID) map[sim.ActorID]time.Time {
	t.Helper()
	v := sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		out := map[sim.ActorID]time.Time{}
		if a := world.Actors[speaker]; a != nil {
			for k, ts := range sim.ActorAwaitingReplyFrom(a) {
				out[k] = ts
			}
		}
		return out, nil
	}})
	return v.(map[sim.ActorID]time.Time)
}

func awaitingReplyAt(t *testing.T, w *sim.World, speaker, addressee sim.ActorID) (time.Time, bool) {
	t.Helper()
	ts, ok := awaitingReplies(t, w, speaker)[addressee]
	return ts, ok
}

func awaitingReplyCount(t *testing.T, w *sim.World, speaker sim.ActorID) int {
	t.Helper()
	return len(awaitingReplies(t, w, speaker))
}

// --- speak-path mutations (buildSpeakTestWorld) -------------------------

// TestTurnState_SpeakToAddresseeSetsEdge — speaking to a resolved addressee
// opens the speaker's awaiting-reply edge toward them (and only them).
func TestTurnState_SpeakToAddresseeSetsEdge(t *testing.T) {
	w, stop := buildSpeakTestWorld(t,
		actorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1"},
		actorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1"},
		actorSpec{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	at := time.Now().UTC()
	if _, err := w.Send(sim.SpeakTo("hannah", "Good morrow.", "Ezekiel", true, at)); err != nil {
		t.Fatalf("SpeakTo: %v", err)
	}
	ts, ok := awaitingReplyAt(t, w, "hannah", "ezekiel")
	if !ok {
		t.Fatal("hannah should be awaiting a reply from ezekiel")
	}
	if !ts.Equal(at) {
		t.Errorf("edge stamp = %v, want %v", ts, at)
	}
	if _, ok := awaitingReplyAt(t, w, "hannah", "bob"); ok {
		t.Error("hannah should NOT be awaiting a reply from the unaddressed bystander bob")
	}
}

// TestTurnState_WholeHuddleSpeakOpensNoEdge — a no-addressee (whole-huddle)
// utterance opens no directed edge.
func TestTurnState_WholeHuddleSpeakOpensNoEdge(t *testing.T) {
	w, stop := buildSpeakTestWorld(t,
		actorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1"},
		actorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	if _, err := w.Send(sim.Speak("hannah", "Good morrow all.", time.Now().UTC())); err != nil {
		t.Fatalf("Speak: %v", err)
	}
	if n := awaitingReplyCount(t, w, "hannah"); n != 0 {
		t.Errorf("hannah edge count = %d, want 0 (whole-huddle)", n)
	}
}

// TestTurnState_AwaitedPartySpeakingClearsEdge — any utterance by the awaited
// party is the reply: it clears the awaiter's edge toward it.
func TestTurnState_AwaitedPartySpeakingClearsEdge(t *testing.T) {
	w, stop := buildSpeakTestWorld(t,
		actorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1"},
		actorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	t1 := time.Now().UTC()
	if _, err := w.Send(sim.SpeakTo("hannah", "Good morrow.", "Ezekiel", true, t1)); err != nil {
		t.Fatalf("SpeakTo: %v", err)
	}
	if _, ok := awaitingReplyAt(t, w, "hannah", "ezekiel"); !ok {
		t.Fatal("precondition: hannah should await ezekiel")
	}
	// Ezekiel replies — any utterance counts, no addressee needed.
	if _, err := w.Send(sim.Speak("ezekiel", "And to you.", t1.Add(time.Second))); err != nil {
		t.Fatalf("Speak: %v", err)
	}
	if _, ok := awaitingReplyAt(t, w, "hannah", "ezekiel"); ok {
		t.Error("ezekiel spoke — hannah's edge toward ezekiel should be cleared")
	}
}

// --- leave / conclude cleanup (buildHuddleTestWorld) --------------------

// TestTurnState_ConcludeHuddleDropsEdges — concluding the conversation clears
// every member's pending turn edges.
func TestTurnState_ConcludeHuddleDropsEdges(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()

	now := time.Now().UTC()
	sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now))
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))
	sendT(t, w, sim.SpeakTo("alice", "Good morrow.", "Bob", true, now.Add(time.Second)))
	if _, ok := awaitingReplyAt(t, w, "alice", "bob"); !ok {
		t.Fatal("precondition: alice should await bob")
	}

	huddleID := sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors["alice"].CurrentHuddleID, nil
	}}).(sim.HuddleID)
	sendT(t, w, sim.ConcludeHuddle(huddleID, now.Add(2*time.Second)))
	if n := awaitingReplyCount(t, w, "alice"); n != 0 {
		t.Errorf("alice edge count after conclude = %d, want 0", n)
	}
}

// TestTurnState_LeaveByAwaitedPeerClearsRemainingEdge — when the awaited peer
// leaves, the member that was awaiting it is no longer owed a reply.
func TestTurnState_LeaveByAwaitedPeerClearsRemainingEdge(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()

	now := time.Now().UTC()
	sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now))
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))
	sendT(t, w, sim.SpeakTo("alice", "Good morrow.", "Bob", true, now.Add(time.Second)))
	if _, ok := awaitingReplyAt(t, w, "alice", "bob"); !ok {
		t.Fatal("precondition: alice should await bob")
	}

	sendT(t, w, sim.LeaveHuddle("bob", now.Add(2*time.Second)))
	if _, ok := awaitingReplyAt(t, w, "alice", "bob"); ok {
		t.Error("bob left — alice's edge toward bob should be cleared")
	}
}

// TestTurnState_LeaveByAwaiterDropsOwnEdge — the leaver's own pending edges go
// with it.
func TestTurnState_LeaveByAwaiterDropsOwnEdge(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()

	now := time.Now().UTC()
	sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now))
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))
	sendT(t, w, sim.SpeakTo("alice", "Good morrow.", "Bob", true, now.Add(time.Second)))
	if _, ok := awaitingReplyAt(t, w, "alice", "bob"); !ok {
		t.Fatal("precondition: alice should await bob")
	}

	sendT(t, w, sim.LeaveHuddle("alice", now.Add(2*time.Second)))
	if n := awaitingReplyCount(t, w, "alice"); n != 0 {
		t.Errorf("alice edge count after leaving = %d, want 0", n)
	}
}
