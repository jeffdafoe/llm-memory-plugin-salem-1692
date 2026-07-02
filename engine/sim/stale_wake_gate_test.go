package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// stale_wake_gate_test.go — LLM-233. The staleness-decay gate in
// EvaluateReactors end-to-end: a repeat all-ambient cycle against an
// unchanged situation is deferred (warrant stays open, due pushed to the
// decayed re-wake time, `deferred`/gate=stale_wake telemetry); any real
// change, a salient warrant, a Force, an elapsed backoff, or a disabled gate
// all emit normally. Reuses buildPR3aWorld / seedDueWarrant /
// subscribeReactorTicks (reactor_pr3a_test.go) + inspectActor
// (reactor_test.go), the same harness as the degeneracy throttle tests.

// armStaleWake enables the decay gate at the given base.
func armStaleWake(t *testing.T, w *sim.World, base time.Duration) {
	t.Helper()
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.StaleWakeDecayBase = base
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("armStaleWake: %v", err)
	}
}

// finishInFlightTick clears the in-flight marker an emitted tick left on the
// actor so a re-seeded warrant cycle can reach the evaluator again (the gate
// tests never run a real LLM tick to completion).
func finishInFlightTick(t *testing.T, w *sim.World, id sim.ActorID) {
	t.Helper()
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors[id]
		a.TickInFlight = false
		a.TickAttemptID = ""
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("finishInFlightTick: %v", err)
	}
}

func restockMeta() sim.WarrantMeta {
	return sim.WarrantMeta{Reason: sim.RestockWarrantReason{Item: "carrots", Source: sim.RestockSourceBuy}}
}

func staleWakeDeferrals(tel *recordingTelemetry) int {
	n := 0
	for _, r := range tel.snapshot() {
		if r.Kind == "deferred" && r.Detail["gate"] == "stale_wake" {
			n++
		}
	}
	return n
}

func TestEvaluateReactors_StaleWake_DefersRepeatAmbientCycle(t *testing.T) {
	w, cancel, tel := buildPR3aWorld(t)
	defer cancel()
	armStaleWake(t, w, time.Minute)
	emitted := subscribeReactorTicks(t, w)
	t0 := time.Now().UTC()

	// First ambient wake: no ledger yet — emits at full rate and records.
	seedDueWarrant(t, w, "alice", []sim.WarrantMeta{restockMeta()}, t0)
	_, _ = w.Send(sim.EvaluateReactors(t0))
	if len(*emitted) != 1 {
		t.Fatalf("first ambient wake: want 1 emit, got %d", len(*emitted))
	}
	inspectActor(t, w, "alice", func(a *sim.Actor) {
		e := a.StaleWake[sim.WarrantKindRestock]
		if e == nil || e.Streak != 1 {
			t.Fatalf("ledger after first emit: %+v, want streak 1", e)
		}
	})
	finishInFlightTick(t, w, "alice")

	// Same warrant kind, nothing about alice's situation changed, 30s later:
	// deferred to t0 + 2m (streak 1 → 2·base), warrant left OPEN.
	t1 := t0.Add(30 * time.Second)
	seedDueWarrant(t, w, "alice", []sim.WarrantMeta{restockMeta()}, t1)
	_, _ = w.Send(sim.EvaluateReactors(t1))
	if len(*emitted) != 1 {
		t.Fatalf("repeat unchanged wake: want it deferred (still 1 emit), got %d", len(*emitted))
	}
	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if a.WarrantedSince == nil {
			t.Error("deferral cleared the warrant — it must stay OPEN")
		}
		if a.WarrantDueAt == nil || !a.WarrantDueAt.Equal(t0.Add(2*time.Minute)) {
			t.Errorf("due pushed to %v, want %v (lastEmit + 2·base)", a.WarrantDueAt, t0.Add(2*time.Minute))
		}
	})
	if got := staleWakeDeferrals(tel); got != 1 {
		t.Errorf("stale_wake deferred telemetry records = %d, want 1", got)
	}

	// Past the decayed re-wake time: emits again, streak advances to 2.
	t2 := t0.Add(2*time.Minute + time.Second)
	seedDueWarrant(t, w, "alice", []sim.WarrantMeta{restockMeta()}, t2)
	_, _ = w.Send(sim.EvaluateReactors(t2))
	if len(*emitted) != 2 {
		t.Fatalf("post-backoff wake: want 2 emits, got %d", len(*emitted))
	}
	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if e := a.StaleWake[sim.WarrantKindRestock]; e == nil || e.Streak != 2 {
			t.Errorf("ledger after second emit: %+v, want streak 2", e)
		}
	})
}

func TestEvaluateReactors_StaleWake_SituationChangeLiftsDecay(t *testing.T) {
	w, cancel, _ := buildPR3aWorld(t)
	defer cancel()
	armStaleWake(t, w, time.Minute)
	emitted := subscribeReactorTicks(t, w)
	t0 := time.Now().UTC()

	seedDueWarrant(t, w, "alice", []sim.WarrantMeta{restockMeta()}, t0)
	_, _ = w.Send(sim.EvaluateReactors(t0))
	finishInFlightTick(t, w, "alice")

	// A coin arrives — the situation fingerprint changes, so the repeat wake
	// 30s later passes at full rate (and the ledger resets to the new
	// fingerprint at streak 1).
	_, _ = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["alice"].Coins++
		return nil, nil
	}})
	t1 := t0.Add(30 * time.Second)
	seedDueWarrant(t, w, "alice", []sim.WarrantMeta{restockMeta()}, t1)
	_, _ = w.Send(sim.EvaluateReactors(t1))
	if len(*emitted) != 2 {
		t.Fatalf("changed-situation wake: want 2 emits, got %d", len(*emitted))
	}
	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if e := a.StaleWake[sim.WarrantKindRestock]; e == nil || e.Streak != 1 {
			t.Errorf("ledger after change: %+v, want fresh streak 1", e)
		}
	})
}

func TestEvaluateReactors_StaleWake_SalientAndForcedBypass(t *testing.T) {
	w, cancel, _ := buildPR3aWorld(t)
	defer cancel()
	armStaleWake(t, w, time.Minute)
	emitted := subscribeReactorTicks(t, w)
	t0 := time.Now().UTC()

	seedDueWarrant(t, w, "alice", []sim.WarrantMeta{restockMeta()}, t0)
	_, _ = w.Send(sim.EvaluateReactors(t0))
	finishInFlightTick(t, w, "alice")

	// A cycle carrying a SALIENT warrant is never touched by the gate, even
	// though the restock kind alone would be stale.
	t1 := t0.Add(20 * time.Second)
	seedDueWarrant(t, w, "alice", []sim.WarrantMeta{
		restockMeta(),
		{Reason: sim.BasicWarrantReason{K: sim.WarrantKindHuddlePeerJoined}},
	}, t1)
	_, _ = w.Send(sim.EvaluateReactors(t1))
	if len(*emitted) != 2 {
		t.Fatalf("salient-carrying cycle: want 2 emits, got %d", len(*emitted))
	}
	finishInFlightTick(t, w, "alice")

	// A forced ambient warrant (operator nudge) bypasses too. Note the min
	// tick gap is also Force-bypassed, so this fires immediately after.
	t2 := t1.Add(time.Second)
	forced := restockMeta()
	forced.Force = true
	seedDueWarrant(t, w, "alice", []sim.WarrantMeta{forced}, t2)
	_, _ = w.Send(sim.EvaluateReactors(t2))
	if len(*emitted) != 3 {
		t.Fatalf("forced ambient cycle: want 3 emits, got %d", len(*emitted))
	}

	// Neither the salient-carrying cycle nor the forced one advanced the
	// ledger: Force and salient bypass ENTIRELY — the operator nudge must not
	// deepen the streak or push LastEmitAt (extending a later deferral).
	inspectActor(t, w, "alice", func(a *sim.Actor) {
		e := a.StaleWake[sim.WarrantKindRestock]
		if e == nil || e.Streak != 1 || !e.LastEmitAt.Equal(t0) {
			t.Errorf("ledger after salient+forced emits: %+v, want streak 1 lastEmit %v (untouched)", e, t0)
		}
	})
}

func TestEvaluateReactors_StaleWake_DisabledIsInert(t *testing.T) {
	w, cancel, tel := buildPR3aWorld(t)
	defer cancel()
	// Gate disabled (zero base — the WorldSettings zero value).
	emitted := subscribeReactorTicks(t, w)
	t0 := time.Now().UTC()

	seedDueWarrant(t, w, "alice", []sim.WarrantMeta{restockMeta()}, t0)
	_, _ = w.Send(sim.EvaluateReactors(t0))
	finishInFlightTick(t, w, "alice")

	t1 := t0.Add(10 * time.Second)
	seedDueWarrant(t, w, "alice", []sim.WarrantMeta{restockMeta()}, t1)
	_, _ = w.Send(sim.EvaluateReactors(t1))
	if len(*emitted) != 2 {
		t.Fatalf("disabled gate: want 2 emits, got %d", len(*emitted))
	}
	if got := staleWakeDeferrals(tel); got != 0 {
		t.Errorf("disabled gate wrote %d stale_wake deferrals, want 0", got)
	}
	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if a.StaleWake != nil {
			t.Error("disabled gate populated the ledger, want nil")
		}
	})
}
