package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// peerSet flattens a HuddlePartReason peer list for order-insensitive
// comparison — the stamp sites collect peers from map iteration, so order
// is not part of the contract.
func peerSet(ids []sim.ActorID) map[sim.ActorID]bool {
	set := make(map[sim.ActorID]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

func assertPeerIDs(t *testing.T, label string, got []sim.ActorID, want ...sim.ActorID) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: PeerIDs = %v, want %v", label, got, want)
	}
	set := peerSet(got)
	for _, id := range want {
		if !set[id] {
			t.Errorf("%s: PeerIDs = %v, missing %q", label, got, id)
		}
	}
}

// TestHuddlePartReason_JoinAndLeaveNamePeers covers the LLM-438 stamp
// retrofit in huddle_commands.go: the actor's own HuddleJoined warrant
// carries the members already in the huddle on arrival, and the HuddleLeft
// warrant carries the members left behind — the payload the perception
// layer renders as "You left the conversation with <peers>." A first
// joiner (nobody there yet) carries an empty peer list, which renders as
// the bare pre-LLM-438 sentence.
func TestHuddlePartReason_JoinAndLeaveNamePeers(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now))
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now.Add(time.Second)))
	sendT(t, w, sim.JoinHuddle("charlie", "tavern", "", now.Add(2*time.Second)))

	aliceJoined := findWarrant(readWarrants(t, w, "alice"), sim.WarrantKindHuddleJoined)
	aliceReason, ok := aliceJoined.Reason.(sim.HuddlePartReason)
	if !ok {
		t.Fatalf("alice joined Reason type = %T, want HuddlePartReason", aliceJoined.Reason)
	}
	assertPeerIDs(t, "alice (first joiner)", aliceReason.PeerIDs)

	bobJoined := findWarrant(readWarrants(t, w, "bob"), sim.WarrantKindHuddleJoined)
	bobReason, ok := bobJoined.Reason.(sim.HuddlePartReason)
	if !ok {
		t.Fatalf("bob joined Reason type = %T, want HuddlePartReason", bobJoined.Reason)
	}
	assertPeerIDs(t, "bob joined", bobReason.PeerIDs, "alice")

	charlieJoined := findWarrant(readWarrants(t, w, "charlie"), sim.WarrantKindHuddleJoined)
	charlieReason, ok := charlieJoined.Reason.(sim.HuddlePartReason)
	if !ok {
		t.Fatalf("charlie joined Reason type = %T, want HuddlePartReason", charlieJoined.Reason)
	}
	assertPeerIDs(t, "charlie joined", charlieReason.PeerIDs, "alice", "bob")

	// Alice walks away — her own departure warrant names the two she left.
	sendT(t, w, sim.LeaveHuddle("alice", now.Add(time.Minute)))

	aliceLeft := findWarrant(readWarrants(t, w, "alice"), sim.WarrantKindHuddleLeft)
	leftReason, ok := aliceLeft.Reason.(sim.HuddlePartReason)
	if !ok {
		t.Fatalf("alice left Reason type = %T, want HuddlePartReason", aliceLeft.Reason)
	}
	assertPeerIDs(t, "alice left", leftReason.PeerIDs, "bob", "charlie")
}

// TestHuddlePartReason_StartOutdoorHuddle covers the commands_move.go stamp
// site: with no reason override, each participant's default HuddleJoined
// warrant carries the members already joined before them in participant
// order — first participant none, second the first.
func TestHuddlePartReason_StartOutdoorHuddle(t *testing.T) {
	w, cancel, _ := buildOutdoorTestWorld(t)
	defer cancel()

	now := time.Now().UTC()
	if _, err := w.Send(sim.StartOutdoorHuddle([]sim.ActorID{"ann", "ben"}, outdoorAnchor, 3, nil, now)); err != nil {
		t.Fatalf("StartOutdoorHuddle: %v", err)
	}

	annJoined := findWarrant(readWarrants(t, w, "ann"), sim.WarrantKindHuddleJoined)
	annReason, ok := annJoined.Reason.(sim.HuddlePartReason)
	if !ok {
		t.Fatalf("ann Reason type = %T, want HuddlePartReason", annJoined.Reason)
	}
	assertPeerIDs(t, "ann (first participant)", annReason.PeerIDs)

	benJoined := findWarrant(readWarrants(t, w, "ben"), sim.WarrantKindHuddleJoined)
	benReason, ok := benJoined.Reason.(sim.HuddlePartReason)
	if !ok {
		t.Fatalf("ben Reason type = %T, want HuddlePartReason", benJoined.Reason)
	}
	assertPeerIDs(t, "ben joined", benReason.PeerIDs, "ann")
}
