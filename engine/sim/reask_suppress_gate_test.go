package sim_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// reask_suppress_gate_test.go — LLM-232 backstop: sim.SpeakTo rejects an NPC's
// minutes-scale re-ask of the sole awake peer of a two-body huddle when that
// peer has said nothing back, with the PC / new-news / peer-replied / window /
// multi-peer carve-outs. Unlike the WORK-370 gate (turn_state_gate_test.go),
// this reads the huddle's RecentUtterances ring, so the world must carry a real
// huddle — buildSpeakTestWorld seeds none, which is exactly why the gate stays
// inert there and leaves those tests unchanged. Reuses gateBase / captureSpoke
// from the sibling sim_test files.

// buildReaskWorld seeds the actors AND a huddle containing them, so SpeakTo
// records into the ring the LLM-232 backstop reads. All actors are StateIdle
// (awake) unless the caller passes a pre-set State.
func buildReaskWorld(t *testing.T, huddleID sim.HuddleID, actors ...*sim.Actor) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	seed := make(map[sim.ActorID]*sim.Actor, len(actors))
	members := make(map[sim.ActorID]struct{}, len(actors))
	for _, a := range actors {
		if a.RecentActions == nil {
			a.RecentActions = sim.NewRingBuffer[sim.Action](4)
		}
		a.CurrentHuddleID = huddleID
		seed[a.ID] = a
		members[a.ID] = struct{}{}
	}
	handles.Actors.Seed(seed)
	handles.Huddles.Seed(map[sim.HuddleID]*sim.Huddle{
		huddleID: {ID: huddleID, Members: members, StartedAt: gateBase},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	return w, func() { cancel(); <-done }
}

func reaskActor(id sim.ActorID, name string, kind sim.ActorKind) *sim.Actor {
	return &sim.Actor{ID: id, DisplayName: name, Kind: kind, State: sim.StateIdle}
}

// TestReaskGate_UndirectedRejected — the specimen: an undirected proposal opens
// no WORK-370 edge, so a re-ask 90s later (past the 60s window, no new news, the
// sole peer silent) is caught only by the LLM-232 backstop.
func TestReaskGate_UndirectedRejected(t *testing.T) {
	w, stop := buildReaskWorld(t, "h1",
		reaskActor("john", "John Ellis", sim.KindNPCShared),
		reaskActor("patience", "Patience Walker", sim.KindNPCShared),
	)
	defer stop()
	captured := captureSpoke(t, w)

	if _, err := w.Send(sim.SpeakTo("john", "I could trade cheese for carrots.", "", nil, true, gateBase)); err != nil {
		t.Fatalf("first proposal (opens no edge, records the ring): %v", err)
	}
	if len(*captured) != 1 {
		t.Fatalf("first proposal should emit; got %d", len(*captured))
	}
	_, err := w.Send(sim.SpeakTo("john", "So — carrots for cheese?", "", nil, false, gateBase.Add(90*time.Second)))
	if err == nil {
		t.Fatal("undirected re-ask of a silent sole peer should be rejected")
	}
	if !strings.Contains(err.Error(), "haven't answered") {
		t.Errorf("reject reason = %q, want it to mention they haven't answered", err.Error())
	}
	if len(*captured) != 1 {
		t.Errorf("the rejected re-ask must not emit; got %d total", len(*captured))
	}
}

// TestReaskGate_PeerReplyClears — once the peer answers, a follow-up is allowed
// (any utterance from the peer takes the turn).
func TestReaskGate_PeerReplyClears(t *testing.T) {
	w, stop := buildReaskWorld(t, "h1",
		reaskActor("john", "John Ellis", sim.KindNPCShared),
		reaskActor("patience", "Patience Walker", sim.KindNPCShared),
	)
	defer stop()

	if _, err := w.Send(sim.SpeakTo("john", "Carrots for cheese?", "", nil, true, gateBase)); err != nil {
		t.Fatalf("john proposal: %v", err)
	}
	if _, err := w.Send(sim.SpeakTo("patience", "Perhaps later.", "", nil, true, gateBase.Add(10*time.Second))); err != nil {
		t.Fatalf("patience reply: %v", err)
	}
	// john follows up after her reply, no new news → allowed (she spoke since).
	if _, err := w.Send(sim.SpeakTo("john", "Fair enough.", "", nil, false, gateBase.Add(20*time.Second))); err != nil {
		t.Errorf("a follow-up after the peer replied should be allowed, got: %v", err)
	}
}

// TestReaskGate_WindowLapseReopens — a re-ask past ReaskSuppressWindow is
// allowed so a genuinely dropped conversation can re-open.
func TestReaskGate_WindowLapseReopens(t *testing.T) {
	w, stop := buildReaskWorld(t, "h1",
		reaskActor("john", "John Ellis", sim.KindNPCShared),
		reaskActor("patience", "Patience Walker", sim.KindNPCShared),
	)
	defer stop()

	if _, err := w.Send(sim.SpeakTo("john", "Carrots for cheese?", "", nil, true, gateBase)); err != nil {
		t.Fatalf("john proposal: %v", err)
	}
	// Within the 3-minute window → still gated.
	if _, err := w.Send(sim.SpeakTo("john", "Carrots?", "", nil, false, gateBase.Add(time.Minute))); err == nil {
		t.Error("within the window, the re-ask should still be gated")
	}
	// Past the window → allowed.
	if _, err := w.Send(sim.SpeakTo("john", "Carrots, perhaps?", "", nil, false, gateBase.Add(4*time.Minute))); err != nil {
		t.Errorf("after the window lapses, the re-ask should be allowed, got: %v", err)
	}
}

// TestReaskGate_NewNewsExempt — a tick carrying fresh news (a real event behind
// it) exempts the re-ask, so an event-driven follow-up still commits.
func TestReaskGate_NewNewsExempt(t *testing.T) {
	w, stop := buildReaskWorld(t, "h1",
		reaskActor("john", "John Ellis", sim.KindNPCShared),
		reaskActor("patience", "Patience Walker", sim.KindNPCShared),
	)
	defer stop()

	if _, err := w.Send(sim.SpeakTo("john", "Carrots for cheese?", "", nil, true, gateBase)); err != nil {
		t.Fatalf("john proposal: %v", err)
	}
	if _, err := w.Send(sim.SpeakTo("john", "Here, the cheese is fresh.", "", nil, true, gateBase.Add(90*time.Second))); err != nil {
		t.Errorf("a new-news follow-up should be allowed, got: %v", err)
	}
}

// TestReaskGate_TwoAwakePeersNotGated — with two awake peers the single-peer
// gate stays out (ambiguous turn); an undirected re-ask is allowed.
func TestReaskGate_TwoAwakePeersNotGated(t *testing.T) {
	w, stop := buildReaskWorld(t, "h1",
		reaskActor("john", "John Ellis", sim.KindNPCShared),
		reaskActor("patience", "Patience Walker", sim.KindNPCShared),
		reaskActor("bob", "Bob", sim.KindNPCShared),
	)
	defer stop()

	if _, err := w.Send(sim.SpeakTo("john", "Anyone got carrots?", "", nil, true, gateBase)); err != nil {
		t.Fatalf("john proposal: %v", err)
	}
	if _, err := w.Send(sim.SpeakTo("john", "Carrots, anyone?", "", nil, false, gateBase.Add(90*time.Second))); err != nil {
		t.Errorf("with two awake peers the undirected re-ask should be allowed, got: %v", err)
	}
}

// TestReaskGate_PCSpeakerExempt — a human player is never gated, even re-asking
// a silent sole peer with no new news.
func TestReaskGate_PCSpeakerExempt(t *testing.T) {
	w, stop := buildReaskWorld(t, "h1",
		reaskActor("jeff", "Jeff", sim.KindPC),
		reaskActor("patience", "Patience Walker", sim.KindNPCShared),
	)
	defer stop()

	if _, err := w.Send(sim.SpeakTo("jeff", "Carrots for cheese?", "", nil, true, gateBase)); err != nil {
		t.Fatalf("first PC line: %v", err)
	}
	if _, err := w.Send(sim.SpeakTo("jeff", "Still keen on a trade?", "", nil, false, gateBase.Add(90*time.Second))); err != nil {
		t.Errorf("a PC speaker must never be gated, got: %v", err)
	}
}
