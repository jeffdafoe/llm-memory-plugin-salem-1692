package sim_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// huddle_utterance_test.go — ZBBS-HOME-412: the transient huddle recent-
// conversation ring (the cross-tick "## Recent conversation here" source) and
// the SpeakTo hook that populates it for every speaker, NPC and PC alike.

func utteranceLine(i int) string { return fmt.Sprintf("line-%d", i) }

// AppendUtterance keeps the last MaxRecentUtterancesPerHuddle lines, oldest-
// first, dropping the oldest over the cap.
func TestHuddle_AppendUtterance_CapAndOrder(t *testing.T) {
	h := &sim.Huddle{ID: "h1"}
	base := time.Now()
	total := sim.MaxRecentUtterancesPerHuddle + 3
	for i := 0; i < total; i++ {
		h.AppendUtterance(sim.ActorID("ann"), "Ann", utteranceLine(i), base.Add(time.Duration(i)*time.Second))
	}
	if len(h.RecentUtterances) != sim.MaxRecentUtterancesPerHuddle {
		t.Fatalf("ring length: got %d, want %d (capped)", len(h.RecentUtterances), sim.MaxRecentUtterancesPerHuddle)
	}
	// Oldest-first; the first (total-cap) lines were dropped.
	if got, want := h.RecentUtterances[0].Text, utteranceLine(total-sim.MaxRecentUtterancesPerHuddle); got != want {
		t.Errorf("oldest retained line: got %q, want %q", got, want)
	}
	if got, want := h.RecentUtterances[len(h.RecentUtterances)-1].Text, utteranceLine(total-1); got != want {
		t.Errorf("newest line: got %q, want %q", got, want)
	}
}

// Empty text is ignored (defensive; the speak command already rejects it).
func TestHuddle_AppendUtterance_IgnoresEmpty(t *testing.T) {
	h := &sim.Huddle{ID: "h1"}
	h.AppendUtterance("ann", "Ann", "", time.Now())
	if len(h.RecentUtterances) != 0 {
		t.Errorf("empty text should not be recorded, got %d entries", len(h.RecentUtterances))
	}
}

// CloneHuddle deep-copies the ring so a published snapshot can't be mutated by a
// later world-goroutine append.
func TestCloneHuddle_IsolatesRecentUtterances(t *testing.T) {
	h := &sim.Huddle{ID: "h1"}
	h.AppendUtterance("ann", "Ann", "first", time.Now())
	clone := sim.CloneHuddle(h)
	h.AppendUtterance("ann", "Ann", "second", time.Now())
	if len(clone.RecentUtterances) != 1 || clone.RecentUtterances[0].Text != "first" {
		t.Errorf("clone must be isolated from later appends, got %+v", clone.RecentUtterances)
	}
}

// A committed speak records the utterance in the speaker's huddle ring — the
// populate side of "## Recent conversation here". PC and NPC both reach here via
// SpeakTo, so the single hook covers the player's lines too.
func TestSpeak_RecordsUtteranceInHuddleRing(t *testing.T) {
	const hid = sim.HuddleID("h1")
	w, cancel := buildSpeakTestWorld(t,
		actorSpec{id: "ann", displayName: "Ann", kind: sim.KindNPCShared, huddleID: hid},
		actorSpec{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: hid},
	)
	defer cancel()

	// buildSpeakTestWorld sets actor.CurrentHuddleID but does not create the
	// Huddle aggregate the ring lives on — seed it on the world goroutine.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Huddles[hid] = &sim.Huddle{ID: hid, Members: map[sim.ActorID]struct{}{"ann": {}, "bob": {}}}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed huddle: %v", err)
	}

	if _, err := w.Send(sim.SpeakTo("ann", "Good morrow, Bob.", "", true, time.Now())); err != nil {
		t.Fatalf("SpeakTo: %v", err)
	}

	v, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return append([]sim.Utterance(nil), world.Huddles[hid].RecentUtterances...), nil
	}})
	if err != nil {
		t.Fatalf("read ring: %v", err)
	}
	ring, _ := v.([]sim.Utterance)
	if len(ring) != 1 {
		t.Fatalf("ring length after one speak: got %d, want 1", len(ring))
	}
	if ring[0].SpeakerID != "ann" || ring[0].SpeakerName != "Ann" || ring[0].Text != "Good morrow, Bob." {
		t.Errorf("ring entry: got %+v", ring[0])
	}
}

// The PC's own lines are recorded too: the player's /pc/speak reaches SpeakTo in
// v2, so the single hook captures them — an NPC re-reading the ring next tick
// sees what the player just said (ZBBS-HOME-412). This is the "PC bridge for
// free" claim, pinned.
func TestSpeak_RecordsUtteranceInHuddleRing_PCSpeaker(t *testing.T) {
	const hid = sim.HuddleID("h1")
	w, cancel := buildSpeakTestWorld(t,
		actorSpec{id: "jeff", displayName: "Jeff", kind: sim.KindPC, huddleID: hid},
		actorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: hid},
	)
	defer cancel()

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Huddles[hid] = &sim.Huddle{ID: hid, Members: map[sim.ActorID]struct{}{"jeff": {}, "hannah": {}}}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed huddle: %v", err)
	}

	if _, err := w.Send(sim.SpeakTo("jeff", "Have you a room tonight?", "", true, time.Now())); err != nil {
		t.Fatalf("SpeakTo (PC): %v", err)
	}

	v, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return append([]sim.Utterance(nil), world.Huddles[hid].RecentUtterances...), nil
	}})
	if err != nil {
		t.Fatalf("read ring: %v", err)
	}
	ring, _ := v.([]sim.Utterance)
	if len(ring) != 1 || ring[0].SpeakerID != "jeff" || ring[0].Text != "Have you a room tonight?" {
		t.Fatalf("PC line must be recorded in the ring, got %+v", ring)
	}
}
