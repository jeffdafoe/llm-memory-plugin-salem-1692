package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// LLM-170 — a clique that churns huddles among the same members at the same
// structure must carry its conversation across the re-formation: the recent-
// utterance ring (no cross-huddle re-greeting) and the loop state (so the loop
// sweep can still conclude a churned loop).

// loopingRingAmong builds the Walker livelock ring with the given speakers cycled
// through it, so the carry-over's speaker-derived member set matches real actors.
func loopingRingAmong(speakers []sim.ActorID, now time.Time) []sim.Utterance {
	ring := loopingLines(now)
	for i := range ring {
		s := speakers[i%len(speakers)]
		ring[i].SpeakerID = s
		ring[i].SpeakerName = string(s)
	}
	return ring
}

// huddleRing reads a huddle's RecentUtterances off the world goroutine.
func huddleRing(t *testing.T, w *sim.World, id sim.HuddleID) []sim.Utterance {
	t.Helper()
	v := sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		h, ok := world.Huddles[id]
		if !ok || h == nil {
			return []sim.Utterance(nil), nil
		}
		return append([]sim.Utterance(nil), h.RecentUtterances...), nil
	}})
	ring, _ := v.([]sim.Utterance)
	return ring
}

// huddleLastProgress reads a huddle's LastProgressAt off the world goroutine.
func huddleLastProgress(t *testing.T, w *sim.World, id sim.HuddleID) time.Time {
	t.Helper()
	v := sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		h, ok := world.Huddles[id]
		if !ok || h == nil {
			return time.Time{}, nil
		}
		return h.LastProgressAt, nil
	}})
	ts, _ := v.(time.Time)
	return ts
}

// appendUtterance appends one spoken line to a huddle's ring on the world goroutine
// — the test stand-in for the clique resuming its loop after re-forming.
func appendUtterance(t *testing.T, w *sim.World, id sim.HuddleID, speaker sim.ActorID, text string, at time.Time) {
	t.Helper()
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		h, ok := world.Huddles[id]
		if !ok || h == nil {
			t.Fatalf("huddle %q not found when appending utterance", id)
		}
		h.AppendUtterance(speaker, string(speaker), text, at)
		return nil, nil
	}})
}

// drainHuddle removes every listed member so the last leave concludes the huddle —
// the conclude+re-form churn the carry-over targets.
func drainHuddle(t *testing.T, w *sim.World, members []sim.ActorID, now time.Time) {
	t.Helper()
	for _, m := range members {
		sendT(t, w, sim.LeaveHuddle(m, now))
	}
}

// TestConversationCarryover_SeedsRingForReturningSpeaker: a structure huddle
// concludes, then the same speaker re-forms it within the window → the fresh huddle
// inherits the recent-utterance ring (so peers are not re-greeted as strangers).
func TestConversationCarryover_SeedsRingForReturningSpeaker(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	h1 := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))
	ring := loopingRingAmong([]sim.ActorID{"alice", "bob"}, now)
	setHuddleLoopState(t, w, h1, ring, nil, time.Time{})

	drainHuddle(t, w, []sim.ActorID{"alice", "bob"}, now)
	if huddleConcludedAt(t, w, h1) == nil {
		t.Fatal("h1 should be concluded after draining all members")
	}

	now2 := now.Add(30 * time.Second)
	h2 := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now2)).(sim.JoinHuddleResult).HuddleID
	if h2 == h1 {
		t.Fatal("re-form should mint a NEW huddle")
	}
	if got := huddleRing(t, w, h2); len(got) != len(ring) {
		t.Fatalf("re-formed huddle ring = %d utterances, want %d (carried)", len(got), len(ring))
	}
}

// TestConversationCarryover_NewSpeakerStartsFresh: a speaker who was NOT part of the
// concluded conversation forms a fresh huddle — the prior exchange is not injected.
func TestConversationCarryover_NewSpeakerStartsFresh(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	h1 := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))
	setHuddleLoopState(t, w, h1, loopingRingAmong([]sim.ActorID{"alice", "bob"}, now), nil, time.Time{})
	drainHuddle(t, w, []sim.ActorID{"alice", "bob"}, now)

	now2 := now.Add(30 * time.Second)
	h2 := sendT(t, w, sim.JoinHuddle("charlie", "tavern", "", now2)).(sim.JoinHuddleResult).HuddleID
	if got := huddleRing(t, w, h2); len(got) != 0 {
		t.Errorf("a new speaker's huddle ring = %d utterances, want 0 (fresh conversation)", len(got))
	}
}

// TestConversationCarryover_ExpiredWindowStartsFresh: a re-formation after the
// continuity window is a separate conversation — no ring is carried.
func TestConversationCarryover_ExpiredWindowStartsFresh(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	h1 := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))
	setHuddleLoopState(t, w, h1, loopingRingAmong([]sim.ActorID{"alice", "bob"}, now), nil, time.Time{})
	drainHuddle(t, w, []sim.ActorID{"alice", "bob"}, now)

	beyond := now.Add(sim.HuddleContinuityWindowDefault + time.Minute)
	h2 := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", beyond)).(sim.JoinHuddleResult).HuddleID
	if got := huddleRing(t, w, h2); len(got) != 0 {
		t.Errorf("re-form past the window ring = %d utterances, want 0 (fresh conversation)", len(got))
	}
}

// TestConversationCarryover_ReJoinIsNotProgress: a returning member's re-join must
// NOT stamp LastProgressAt (that would reset the loop spell every churn cycle); a
// genuinely-new participant's join still stamps progress.
func TestConversationCarryover_ReJoinIsNotProgress(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	baseline := now.Add(-10 * time.Minute)

	h1 := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))
	setHuddleLoopState(t, w, h1, loopingRingAmong([]sim.ActorID{"alice", "bob"}, now), nil, baseline)
	drainHuddle(t, w, []sim.ActorID{"alice", "bob"}, now)

	now2 := now.Add(30 * time.Second)
	h2 := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now2)).(sim.JoinHuddleResult).HuddleID
	if got := huddleLastProgress(t, w, h2); !got.Equal(baseline) {
		t.Errorf("returning speaker re-form LastProgressAt = %v, want carried baseline %v", got, baseline)
	}
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now2)) // returning member, no stamp
	if got := huddleLastProgress(t, w, h2); !got.Equal(baseline) {
		t.Errorf("returning member join LastProgressAt = %v, want unchanged baseline %v", got, baseline)
	}
	sendT(t, w, sim.JoinHuddle("charlie", "tavern", "", now2)) // genuinely new → progress
	if got := huddleLastProgress(t, w, h2); !got.Equal(now2) {
		t.Errorf("new participant join LastProgressAt = %v, want stamped now2 %v", got, now2)
	}
}

// TestConversationCarryover_JoinAfterDivergenceIsProgress: once a genuinely-new
// participant has joined a re-formed huddle, it has diverged from the old clique, so
// a LATER join even by a former member counts as composition progress — it must NOT
// inherit the old loop baseline.
func TestConversationCarryover_JoinAfterDivergenceIsProgress(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	baseline := now.Add(-10 * time.Minute)

	h1 := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))
	setHuddleLoopState(t, w, h1, loopingRingAmong([]sim.ActorID{"alice", "bob"}, now), nil, baseline)
	drainHuddle(t, w, []sim.ActorID{"alice", "bob"}, now)

	now2 := now.Add(30 * time.Second)
	h2 := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now2)).(sim.JoinHuddleResult).HuddleID // returning → baseline
	if got := huddleLastProgress(t, w, h2); !got.Equal(baseline) {
		t.Fatalf("alice re-form LastProgressAt = %v, want carried baseline %v", got, baseline)
	}
	t3 := now2.Add(5 * time.Second)
	sendT(t, w, sim.JoinHuddle("charlie", "tavern", "", t3)) // genuinely new → diverges the huddle
	if got := huddleLastProgress(t, w, h2); !got.Equal(t3) {
		t.Fatalf("new participant join LastProgressAt = %v, want stamped %v", got, t3)
	}
	t4 := now2.Add(10 * time.Second)
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", t4)) // former member, but the huddle has diverged → progress
	if got := huddleLastProgress(t, w, h2); !got.Equal(t4) {
		t.Errorf("former member joining a diverged huddle LastProgressAt = %v, want stamped %v (not the old baseline)", got, t4)
	}
}

// TestLeaveHuddle_DoesNotStampProgress: a leave from a surviving huddle no longer
// counts as progress (LLM-170 — only a transaction or a new participant does), so a
// remaining clique can still be caught looping.
func TestLeaveHuddle_DoesNotStampProgress(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	baseline := now.Add(-10 * time.Minute)

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))
	setHuddleLoopState(t, w, h, loopingRingAmong([]sim.ActorID{"alice", "bob"}, now), nil, baseline)

	sendT(t, w, sim.LeaveHuddle("alice", now.Add(time.Second))) // bob remains; huddle survives
	if huddleConcludedAt(t, w, h) != nil {
		t.Fatal("huddle should survive a non-last leave")
	}
	if got := huddleLastProgress(t, w, h); !got.Equal(baseline) {
		t.Errorf("LastProgressAt after a leave = %v, want unchanged baseline %v (a leave is not progress)", got, baseline)
	}
}

// TestHuddleLoopContentPresent_DurableAcrossStaleness: the durable onset condition
// stays true when a repetitive ring goes quiet (so the spell survives a churn gap),
// while the live armed predicate flips false — the split that makes the gate span a
// re-formation.
func TestHuddleLoopContentPresent_DurableAcrossStaleness(t *testing.T) {
	now := time.Now().UTC()
	s := sim.WorldSettings{HuddleLoopTimeout: time.Minute, HuddleLoopRepeatPercent: 60}
	h := &sim.Huddle{RecentUtterances: loopingLines(now)}

	stale := now.Add(5 * time.Minute) // newest line is now 5 min old
	if !sim.HuddleLoopContentPresent(s, h) {
		t.Error("a quiet repetitive ring should still be content-present (durable onset condition)")
	}
	if sim.HuddleConversationLooping(s, h, stale) {
		t.Error("a quiet repetitive ring must NOT read as live-looping (silence sweep's domain)")
	}
}

// TestHuddleLoopSweep_ConcludesChurnedLoop is the headline LLM-170 regression: a
// looping huddle that DISSOLVES and RE-FORMS among the same members before the
// persistence gate elapses is still concluded — the carried loop clock makes the
// gate span the churn instead of restarting each cycle.
func TestHuddleLoopSweep_ConcludesChurnedLoop(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	enableLoopSweep(t, w, 2*time.Minute, 60)

	h1 := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))

	onset := now.Add(-100 * time.Second)        // looping, but 20s short of the 2m gate
	staleProgress := now.Add(-10 * time.Minute) // progress older than the repetitive ring
	setHuddleLoopState(t, w, h1, loopingRingAmong([]sim.ActorID{"alice", "bob"}, now), &onset, staleProgress)

	// Not yet concluded: the gate has not elapsed.
	sendT(t, w, sim.EvaluateHuddleLoopSweep(now))
	if huddleConcludedAt(t, w, h1) != nil {
		t.Fatal("loop should not be concluded before the persistence gate elapses")
	}

	// The clique disperses and reconvenes — a fresh huddle, ~30s later.
	drainHuddle(t, w, []sim.ActorID{"alice", "bob"}, now)
	now2 := now.Add(30 * time.Second)
	h2 := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now2)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now2))
	if h2 == h1 {
		t.Fatal("re-form should mint a NEW huddle")
	}
	// They resume the loop (a fresh live line lands in the carried ring).
	appendUtterance(t, w, h2, "alice", "Let's go to the market", now2)

	// now2 - onset = 130s >= the 2m gate, and the loop is live again → concluded,
	// even though it ran across two different huddles.
	sendT(t, w, sim.EvaluateHuddleLoopSweep(now2))
	if huddleConcludedAt(t, w, h2) == nil {
		t.Error("a churned loop should be concluded — the carried clock spans the re-formation")
	}
}

// TestHuddleLoopSweep_ChurnConcludeResetsCarriedClock: when the loop sweep itself
// concludes a huddle, the carry-over keeps the ring (re-greeting fix) but resets the
// loop clock, so a re-form gets a fresh gate rather than being re-concluded instantly.
func TestHuddleLoopSweep_ChurnConcludeResetsCarriedClock(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	enableLoopSweep(t, w, 2*time.Minute, 60)

	h1 := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))
	onset := now.Add(-3 * time.Minute) // already past the gate
	staleProgress := now.Add(-10 * time.Minute)
	setHuddleLoopState(t, w, h1, loopingRingAmong([]sim.ActorID{"alice", "bob"}, now), &onset, staleProgress)

	sendT(t, w, sim.EvaluateHuddleLoopSweep(now))
	if huddleConcludedAt(t, w, h1) == nil {
		t.Fatal("a sustained loop should be concluded by the sweep")
	}

	v := sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		ls, ok := sim.CarryoverLoopingSince(world, "tavern")
		if !ok {
			t.Fatal("expected a carry-over after the sweep concluded the loop")
		}
		return ls, nil
	}})
	if ls, _ := v.(*time.Time); ls != nil {
		t.Errorf("loop-sweep conclude should reset the carried loop clock, got %v", ls)
	}
}
