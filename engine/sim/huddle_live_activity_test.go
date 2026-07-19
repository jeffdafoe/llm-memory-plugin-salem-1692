package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// huddle_live_activity_test.go — LLM-467, end-to-end liveness stamping.
//
// HuddleIsLive reads Huddle.LastActivityAt, so the gate is only correct if every
// kind of conversational activity actually stamps that field. The unit tests in
// huddle_live_test.go construct huddles with timestamps directly and therefore
// cannot catch a MISSING stamp — these drive the real world commands instead
// (code_review, and the reason it was the approval blocker).
//
// This field already backs the 2h silence sweep, so a missed stamp was always a
// bug; what LLM-467 changes is the threshold at which one becomes visible. At
// two hours a dropped stamp hides, at five minutes it strands a live
// conversation. Hence the coverage.

// liveAfter reports whether the actor's huddle reads live at `now` under the
// default window, read on the world goroutine.
func liveAfter(t *testing.T, w *sim.World, id sim.ActorID, now time.Time) bool {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		h := world.Huddles[world.Actors[id].CurrentHuddleID]
		return sim.HuddleIsLive(h, now, sim.HuddleLiveWindowDefault), nil
	}})
	if err != nil {
		t.Fatalf("read liveness: %v", err)
	}
	return res.(bool)
}

// goDormant back-dates the actor's huddle so it reads dormant, letting each test
// below prove that its activity is what brings the huddle back.
func goDormant(t *testing.T, w *sim.World, id sim.ActorID, staleAt time.Time) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		h := world.Huddles[world.Actors[id].CurrentHuddleID]
		h.LastActivityAt = staleAt
		return nil, nil
	}}); err != nil {
		t.Fatalf("back-date huddle: %v", err)
	}
}

// seedTavernPair puts alice and bob in one tavern huddle and returns the clock
// they were joined at.
func seedTavernPair(t *testing.T, w *sim.World) time.Time {
	t.Helper()
	joinedAt := time.Unix(0, 0).UTC()
	for _, id := range []sim.ActorID{"alice", "bob"} {
		setActor(t, w, id, func(a *sim.Actor) {
			a.Kind = sim.KindNPCStateful
			a.InsideStructureID = "tavern"
		})
		sendT(t, w, sim.JoinHuddle(id, "tavern", "", joinedAt))
	}
	return joinedAt
}

func TestHuddleLiveness_JoinStampsActivity(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	joinedAt := seedTavernPair(t, w)

	if !liveAfter(t, w, "alice", joinedAt.Add(time.Minute)) {
		t.Errorf("a huddle joined a minute ago must read live")
	}
	if liveAfter(t, w, "alice", joinedAt.Add(time.Hour)) {
		t.Errorf("with no further activity the huddle must read dormant an hour on")
	}

	// A third party arriving is activity: it revives the dormant conversation.
	setActor(t, w, "charlie", func(a *sim.Actor) {
		a.Kind = sim.KindNPCStateful
		a.InsideStructureID = "tavern"
	})
	arrivesAt := joinedAt.Add(time.Hour)
	sendT(t, w, sim.JoinHuddle("charlie", "tavern", "", arrivesAt))

	if !liveAfter(t, w, "alice", arrivesAt.Add(time.Minute)) {
		t.Errorf("a peer joining must stamp activity and bring the huddle back to live")
	}
}

func TestHuddleLiveness_SpeechStampsActivity(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	joinedAt := seedTavernPair(t, w)

	spokeAt := joinedAt.Add(time.Hour)
	goDormant(t, w, "alice", joinedAt)
	if liveAfter(t, w, "alice", spokeAt) {
		t.Fatalf("precondition: the huddle must be dormant before the utterance")
	}

	if _, err := w.Send(sim.SpeakTo("alice", "Good morrow, Bob.", "", nil, false, spokeAt)); err != nil {
		t.Fatalf("SpeakTo: %v", err)
	}
	if !liveAfter(t, w, "alice", spokeAt.Add(time.Minute)) {
		t.Errorf("a spoken line must stamp activity — it is the primary liveness signal")
	}
	// Both parties read the same huddle, so the speaker's line revives it for the
	// listener too. That is the case the gate depends on: B must not be skipped
	// right after A speaks to it.
	if !liveAfter(t, w, "bob", spokeAt.Add(time.Minute)) {
		t.Errorf("the LISTENER's huddle must read live after being spoken to")
	}
}

func TestHuddleLiveness_PaymentStampsActivity(t *testing.T) {
	// The third activity type: a completed transaction, stamped via
	// touchHuddleProgress. A conversation can go quiet while business is being
	// done, and that is not dormancy.
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	joinedAt := seedTavernPair(t, w)
	setActor(t, w, "alice", func(a *sim.Actor) { a.Coins = 20 })

	paidAt := joinedAt.Add(time.Hour)
	goDormant(t, w, "alice", joinedAt)
	if liveAfter(t, w, "alice", paidAt) {
		t.Fatalf("precondition: the huddle must be dormant before the payment")
	}

	if _, err := w.Send(sim.Pay("alice", "bob", 3, "for the ale", paidAt)); err != nil {
		t.Fatalf("Pay: %v", err)
	}
	if !liveAfter(t, w, "alice", paidAt.Add(time.Minute)) {
		t.Errorf("a completed transaction must stamp activity")
	}
}

func TestPublishedSnapshotCarriesHuddleLiveWindow(t *testing.T) {
	// republish is the single Snapshot construction site, and it must resolve the
	// EFFECTIVE window — a world with the setting unset publishes the default,
	// never 0 (which perception would read as "always live" and quietly restore
	// the pre-LLM-467 behavior in production).
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	seedTavernPair(t, w)

	snap := w.Published()
	if snap == nil {
		t.Fatalf("no published snapshot")
	}
	if snap.HuddleLiveWindow != sim.HuddleLiveWindowDefault {
		t.Errorf("published HuddleLiveWindow = %v, want the default %v",
			snap.HuddleLiveWindow, sim.HuddleLiveWindowDefault)
	}
}
