package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// degeneracy_throttle_test.go — LLM-94 Stage-2. The surgical warrant throttle in
// EvaluateReactors: a DegeneracyThrottled actor whose due warrant cycle is all-
// ambient has its wake pushed out by the backoff (and a `deferred`/gate=degeneracy
// record written); a salient warrant in the cycle fires normally; a sub-throttle
// stage and a disabled observer both leave it un-throttled. Reuses buildPR3aWorld
// / seedDueWarrant / subscribeReactorTicks (reactor_pr3a_test.go) + inspectActor
// (reactor_test.go), same package.

// armDegeneracy enables/disables the observer and sets the actor's stage + the
// throttle backoff so a single EvaluateReactors(now) exercises the throttle gate.
func armDegeneracy(t *testing.T, w *sim.World, id sim.ActorID, stage sim.DegeneracyStage, backoff time.Duration, enabled bool) {
	t.Helper()
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		if enabled {
			world.Settings.DegeneracyThinAfterTicks = 5
		} else {
			world.Settings.DegeneracyThinAfterTicks = 0
		}
		world.Settings.DegeneracyThrottleBackoff = backoff
		world.Actors[id].DegenStage = stage
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("armDegeneracy: %v", err)
	}
}

func TestEvaluateReactors_DegeneracyThrottle_DefersAmbientCycle(t *testing.T) {
	w, cancel, tel := buildPR3aWorld(t)
	defer cancel()
	now := time.Now().UTC()

	armDegeneracy(t, w, "alice", sim.DegeneracyThrottled, 3*time.Minute, true)
	seedDueWarrant(t, w, "alice", []sim.WarrantMeta{
		{Reason: sim.IdleBackstopWarrantReason{}},
	}, now)
	emitted := subscribeReactorTicks(t, w)

	_, _ = w.Send(sim.EvaluateReactors(now))

	if len(*emitted) != 0 {
		t.Errorf("throttled ambient cycle: want 0 emits, got %d", len(*emitted))
	}
	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if a.WarrantedSince == nil {
			t.Error("throttle cleared the warrant — it must stay OPEN")
		}
		if a.TickInFlight {
			t.Error("TickInFlight set despite throttle deferral (nothing consumed)")
		}
		// Pushed out by exactly the backoff (the throttle push carries no jitter).
		want := now.Add(3 * time.Minute)
		if a.WarrantDueAt == nil || !a.WarrantDueAt.Equal(want) {
			t.Errorf("WarrantDueAt not pushed by the throttle backoff: got %v want %v", a.WarrantDueAt, want)
		}
	})
	sawDeferred := false
	for _, r := range tel.snapshot() {
		if r.Kind == "deferred" && r.ActorID == "alice" && r.Detail["gate"] == "degeneracy" {
			sawDeferred = true
		}
	}
	if !sawDeferred {
		t.Error("expected a `deferred`/gate=degeneracy telemetry record for alice")
	}
}

func TestEvaluateReactors_DegeneracyThrottle_PassesSalientCycle(t *testing.T) {
	w, cancel, _ := buildPR3aWorld(t)
	defer cancel()
	now := time.Now().UTC()

	armDegeneracy(t, w, "alice", sim.DegeneracyThrottled, 3*time.Minute, true)
	// A salient warrant in the cycle (a PC spoke) — the throttle must step aside
	// even though an ambient idle-backstop rides alongside it.
	seedDueWarrant(t, w, "alice", []sim.WarrantMeta{
		{Reason: sim.IdleBackstopWarrantReason{}},
		{Reason: sim.PCSpeechWarrantReason{SpeechID: 7, Speaker: "player"}},
	}, now)
	emitted := subscribeReactorTicks(t, w)

	_, _ = w.Send(sim.EvaluateReactors(now))
	if len(*emitted) != 1 {
		t.Errorf("a salient cycle must fire through the throttle: emit count = %d, want 1", len(*emitted))
	}
}

func TestEvaluateReactors_DegeneracyThrottle_DisabledLiftsThrottle(t *testing.T) {
	w, cancel, _ := buildPR3aWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// A Throttled stage left over from when the observer was on, but it is now
	// OFF — the throttle must not apply, or the actor would never tick, never
	// reach updateDegeneracy, and stay deferred forever.
	armDegeneracy(t, w, "alice", sim.DegeneracyThrottled, 3*time.Minute, false)
	seedDueWarrant(t, w, "alice", []sim.WarrantMeta{
		{Reason: sim.IdleBackstopWarrantReason{}},
	}, now)
	emitted := subscribeReactorTicks(t, w)

	_, _ = w.Send(sim.EvaluateReactors(now))
	if len(*emitted) != 1 {
		t.Errorf("a disabled observer must not throttle: emit count = %d, want 1", len(*emitted))
	}
}

func TestEvaluateReactors_DegeneracyThrottle_FlaggedStageNotThrottled(t *testing.T) {
	w, cancel, _ := buildPR3aWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// Stage-1 (Flagged) is below the throttle threshold — only Throttled defers.
	armDegeneracy(t, w, "alice", sim.DegeneracyFlagged, 3*time.Minute, true)
	seedDueWarrant(t, w, "alice", []sim.WarrantMeta{
		{Reason: sim.IdleBackstopWarrantReason{}},
	}, now)
	emitted := subscribeReactorTicks(t, w)

	_, _ = w.Send(sim.EvaluateReactors(now))
	if len(*emitted) != 1 {
		t.Errorf("a flagged (not throttled) actor must not be throttled: emit count = %d, want 1", len(*emitted))
	}
}
