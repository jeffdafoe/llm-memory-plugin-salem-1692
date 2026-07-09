package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// eco_conclude_sweep_test.go — LLM-334. Bounded conversation arcs while
// unwatched: the eco-conclude sweep stamps EcoUnwatchedSince on active huddles
// while eco is engaged and silently concludes them once the stamp outlives the
// arc, clearing members' social-only warrant cycles so the conclusion sticks.
// Commerce-carrying and player-touched huddles are never concluded.

// enableEcoArc turns eco mode + the arc on with the given arc length.
func enableEcoArc(t *testing.T, w *sim.World, arc time.Duration) {
	t.Helper()
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.EcoEnabled = true
		world.Settings.EcoConversationMax = arc
		return nil, nil
	}})
}

// ecoStamp reads a huddle's EcoUnwatchedSince off the world goroutine.
func ecoStamp(t *testing.T, w *sim.World, id sim.HuddleID) *time.Time {
	t.Helper()
	v := sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		h, ok := world.Huddles[id]
		if !ok || h == nil {
			return (*time.Time)(nil), nil
		}
		return h.EcoUnwatchedSince, nil
	}})
	stamp, _ := v.(*time.Time)
	return stamp
}

// stampFreshPC makes an actor a player with fresh presence, so AudienceActive
// reads true.
func stampFreshPC(t *testing.T, w *sim.World, id sim.ActorID, now time.Time) {
	t.Helper()
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors[id]
		a.Kind = sim.KindPC
		seen := now
		a.LastPCSeenAt = &seen
		return nil, nil
	}})
}

func TestEcoConcludeSweep_ConcludesUnwatchedPastArc(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	sink := wireLoopTelemetry(t, w)
	t0 := time.Now().UTC()

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", t0)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", t0))
	enableEcoArc(t, w, 3*time.Minute)
	appendUtterance(t, w, h, "alice", "A quiet evening, isn't it?", t0)
	appendUtterance(t, w, h, "bob", "Aye, that it is.", t0.Add(time.Minute))

	// Pending social-only cycle on alice (the beat the conclusion must silence)
	// and a mixed cycle on bob (must survive whole).
	sendT(t, w, sim.StampWarrant("alice", sim.WarrantMeta{
		TriggerActorID: "bob",
		Reason:         sim.BasicWarrantReason{K: sim.WarrantKindNPCSpoke},
		SourceEventID:  11,
		OccurredAt:     t0,
	}, t0))
	sendT(t, w, sim.StampWarrant("bob", sim.WarrantMeta{
		TriggerActorID: "alice",
		Reason:         sim.BasicWarrantReason{K: sim.WarrantKindNeedThreshold},
		SourceEventID:  12,
		OccurredAt:     t0,
	}, t0))

	// Pass 1: stamps the arc clock, concludes nothing.
	sendT(t, w, sim.EvaluateEcoConcludeSweep(t0))
	if huddleConcludedAt(t, w, h) != nil {
		t.Fatal("first sight must stamp, not conclude")
	}
	if ecoStamp(t, w, h) == nil {
		t.Fatal("first unwatched pass should stamp EcoUnwatchedSince")
	}

	// Pass 2 past the arc: concluded, silent, sticky.
	t1 := t0.Add(3 * time.Minute)
	sendT(t, w, sim.EvaluateEcoConcludeSweep(t1))
	if huddleConcludedAt(t, w, h) == nil {
		t.Fatal("an unwatched huddle past its arc should be concluded")
	}
	var reasons []string
	for _, rec := range sink.snapshot() {
		if rec.Kind == "stuck" {
			reasons = append(reasons, rec.Detail["reason"])
		}
	}
	if len(reasons) == 0 {
		t.Fatal("eco conclusion should emit stuck telemetry per member")
	}
	for _, r := range reasons {
		if r != "eco_conclude" {
			t.Errorf("telemetry reason = %q, want eco_conclude", r)
		}
	}
	pendingKinds := func(id sim.ActorID) []sim.WarrantKind {
		v := sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
			var kinds []sim.WarrantKind
			for _, m := range world.Actors[id].Warrants {
				kinds = append(kinds, m.Kind())
			}
			return kinds, nil
		}})
		kinds, _ := v.([]sim.WarrantKind)
		return kinds
	}
	if got := pendingKinds("alice"); len(got) != 0 {
		t.Errorf("alice pending warrants = %v, want none (social-only cycle cleared)", got)
	}
	if got := pendingKinds("bob"); len(got) == 0 {
		t.Error("bob's mixed cycle must survive the clear whole")
	}
}

func TestEcoConcludeSweep_AudienceClearsStampsAndBlocksConcludes(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	t0 := time.Now().UTC()

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", t0)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", t0))
	enableEcoArc(t, w, 3*time.Minute)
	appendUtterance(t, w, h, "alice", "Evening.", t0)

	// Unwatched pass stamps the arc.
	sendT(t, w, sim.EvaluateEcoConcludeSweep(t0))
	if ecoStamp(t, w, h) == nil {
		t.Fatal("unwatched pass should stamp")
	}

	// A player shows up: the pass clears every stamp and concludes nothing —
	// even at a time far past the old stamp's arc.
	stampFreshPC(t, w, "charlie", t0.Add(4*time.Minute))
	sendT(t, w, sim.EvaluateEcoConcludeSweep(t0.Add(4*time.Minute)))
	if huddleConcludedAt(t, w, h) != nil {
		t.Fatal("a watched pass must never conclude")
	}
	if ecoStamp(t, w, h) != nil {
		t.Error("a watched pass should clear the arc stamp (fresh arc when eco re-engages)")
	}
}

func TestEcoConcludeSweep_PCAttendedClearsStamp(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	t0 := time.Now().UTC()

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", t0)).(sim.JoinHuddleResult).HuddleID
	enableEcoArc(t, w, 3*time.Minute)
	sendT(t, w, sim.EvaluateEcoConcludeSweep(t0))
	if ecoStamp(t, w, h) == nil {
		t.Fatal("unwatched pass should stamp")
	}

	// A recent PC line in the huddle (can outlive the presence stamp by ~2 min)
	// clears the arc even while the world-level audience reads absent.
	setHuddlePCUtterance(t, w, h, t0.Add(time.Minute))
	sendT(t, w, sim.EvaluateEcoConcludeSweep(t0.Add(2*time.Minute)))
	if ecoStamp(t, w, h) != nil {
		t.Error("a player-attended huddle must have its arc stamp cleared")
	}
	if huddleConcludedAt(t, w, h) != nil {
		t.Error("a player-attended huddle must not be concluded")
	}
}

func TestEcoConcludeSweep_CommerceGuard(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	t0 := time.Now().UTC()

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", t0)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", t0))
	enableEcoArc(t, w, 3*time.Minute)

	// Arm the arc, then open a live negotiation in the huddle.
	sendT(t, w, sim.EvaluateEcoConcludeSweep(t0))
	stagePayTerminal(t, w, 1, h, "alice", "bob", "bread", sim.PayLedgerStatePending, time.Time{})

	// Far past the arc: the pending deal re-stamps instead of concluding.
	t1 := t0.Add(10 * time.Minute)
	sendT(t, w, sim.EvaluateEcoConcludeSweep(t1))
	if huddleConcludedAt(t, w, h) != nil {
		t.Fatal("a huddle with a pending pay-ledger entry must not be concluded")
	}
	if stamp := ecoStamp(t, w, h); stamp == nil || !stamp.Equal(t1) {
		t.Errorf("commerce guard should push the arc stamp to now, got %v", stamp)
	}

	// Deal resolves; a member still holds a commerce-commitment warrant —
	// same guard, the arc keeps restarting.
	clearPayLedger(t, w)
	sendT(t, w, sim.StampWarrant("bob", sim.WarrantMeta{
		TriggerActorID: "alice",
		Reason:         sim.BasicWarrantReason{K: sim.WarrantKindPayOffer},
		SourceEventID:  21,
		OccurredAt:     t1,
	}, t1))
	t2 := t1.Add(10 * time.Minute)
	sendT(t, w, sim.EvaluateEcoConcludeSweep(t2))
	if huddleConcludedAt(t, w, h) != nil {
		t.Fatal("a huddle whose member holds a commerce warrant must not be concluded")
	}
	if stamp := ecoStamp(t, w, h); stamp == nil || !stamp.Equal(t2) {
		t.Errorf("member-warrant guard should push the arc stamp to now, got %v", stamp)
	}

	// Commerce done: the arc finally runs and the huddle concludes.
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["bob"].Warrants = nil
		return nil, nil
	}})
	t3 := t2.Add(3 * time.Minute)
	sendT(t, w, sim.EvaluateEcoConcludeSweep(t3))
	if huddleConcludedAt(t, w, h) == nil {
		t.Error("with commerce settled the arc should conclude the huddle")
	}
}

func TestEcoConcludeSweep_DisabledNoop(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	t0 := time.Now().UTC()

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", t0)).(sim.JoinHuddleResult).HuddleID

	// Arc 0 = off: nothing stamps, nothing concludes, and a stale stamp from an
	// earlier enabled stretch is cleared.
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.EcoEnabled = true
		world.Settings.EcoConversationMax = 0
		stamp := t0.Add(-time.Hour)
		world.Huddles[h].EcoUnwatchedSince = &stamp
		return nil, nil
	}})
	sendT(t, w, sim.EvaluateEcoConcludeSweep(t0))
	if huddleConcludedAt(t, w, h) != nil {
		t.Fatal("arc 0 must disable the sweep")
	}
	if ecoStamp(t, w, h) != nil {
		t.Error("a disabled pass should clear stale stamps")
	}

	// EcoEnabled false: same posture.
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.EcoEnabled = false
		world.Settings.EcoConversationMax = 3 * time.Minute
		stamp := t0.Add(-time.Hour)
		world.Huddles[h].EcoUnwatchedSince = &stamp
		return nil, nil
	}})
	sendT(t, w, sim.EvaluateEcoConcludeSweep(t0))
	if huddleConcludedAt(t, w, h) != nil {
		t.Fatal("eco disabled must disable the sweep")
	}
	if ecoStamp(t, w, h) != nil {
		t.Error("an eco-off pass should clear stale stamps")
	}
}

func TestSetEcoMode_ConversationMax(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()

	arc := 120
	res, err := sendT2(t, w, sim.SetEcoMode(nil, nil, nil, &arc))
	if err != nil {
		t.Fatalf("SetEcoMode(conversation_max): %v", err)
	}
	out := res.(sim.EcoModeSettingsResult)
	if out.ConversationMaxSeconds != 120 {
		t.Errorf("ConversationMaxSeconds = %d, want 120", out.ConversationMaxSeconds)
	}

	neg := -5
	if _, err := sendT2(t, w, sim.SetEcoMode(nil, nil, nil, &neg)); err == nil {
		t.Error("a negative conversation_max must be rejected")
	}

	zero := 0
	if _, err := sendT2(t, w, sim.SetEcoMode(nil, nil, nil, &zero)); err != nil {
		t.Errorf("conversation_max 0 (the off-switch) must be valid: %v", err)
	}
}

// sendT2 is sendT with the error returned instead of fatal'd, for rejection
// assertions.
func sendT2(t *testing.T, w *sim.World, cmd sim.Command) (any, error) {
	t.Helper()
	return w.Send(cmd)
}
