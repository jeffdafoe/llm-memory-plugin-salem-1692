package sim_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// reactor_pr3a_test.go — PR 3a substrate amendments: source-aware warrant
// stamping (the three dedup paths + the zero-sourced bypass), the in-
// flight / recently-consumed source-key bookkeeping, the MinReactorTickGap
// pacing floor, the tick admission gate, and the terminal-status warrant
// policy in CompleteReactorTick.

// ---- test doubles -----------------------------------------------------

// fakeAdmission is a TickAdmissionController with a settable verdict.
type fakeAdmission struct{ admit bool }

func (f *fakeAdmission) CanAdmit() bool { return f.admit }

// recordingTelemetry captures TickTelemetryRecords for assertions.
type recordingTelemetry struct {
	mu      sync.Mutex
	records []sim.TickTelemetryRecord
}

func (r *recordingTelemetry) WriteTickTelemetry(rec sim.TickTelemetryRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, rec)
}

func (r *recordingTelemetry) snapshot() []sim.TickTelemetryRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sim.TickTelemetryRecord(nil), r.records...)
}

// buildPR3aWorld seeds a running world with one actor ("alice") and the
// same tight reactor settings buildReactorTestWorld uses, but exposes the
// recording telemetry sink so admission-deferral tests can assert the
// `deferred` record. MinReactorTickGap / AdmissionBackoff are left unset
// so their defaults apply.
func buildPR3aWorld(t *testing.T) (*sim.World, context.CancelFunc, *recordingTelemetry) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"alice": {ID: "alice", DisplayName: "Alice"},
	})
	tel := &recordingTelemetry{}
	repo.TickTelemetry = tel

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Settings.ReactorJitterMin = 10 * time.Millisecond
			world.Settings.ReactorJitterMax = 11 * time.Millisecond
			world.Settings.ReactorEvaluatorCadence = 5 * time.Millisecond
			world.Settings.MaxWarrantsPerActor = 16
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	return w, cancel, tel
}

// seedDueWarrant hand-stamps a single due (now-1ms) warrant cycle on the
// actor so an immediate EvaluateReactors(now) considers it.
func seedDueWarrant(t *testing.T, w *sim.World, id sim.ActorID, metas []sim.WarrantMeta, now time.Time) {
	t.Helper()
	_, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			a := world.Actors[id]
			since := now.Add(-50 * time.Millisecond)
			due := now.Add(-time.Millisecond)
			a.WarrantedSince = &since
			a.WarrantDueAt = &due
			a.Warrants = append([]sim.WarrantMeta(nil), metas...)
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("seedDueWarrant: %v", err)
	}
}

// subscribeReactorTicks registers a recorder for ReactorTickDue events.
func subscribeReactorTicks(t *testing.T, w *sim.World) *[]*sim.ReactorTickDue {
	t.Helper()
	got := &[]*sim.ReactorTickDue{}
	var mu sync.Mutex
	_, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
				if e, ok := evt.(*sim.ReactorTickDue); ok {
					mu.Lock()
					*got = append(*got, e)
					mu.Unlock()
				}
			}))
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("subscribeReactorTicks: %v", err)
	}
	return got
}

// ---- source-aware tryStampWarrant: the three dedup paths --------------

// TestTryStampWarrant_OpenCycleDedup covers dedup path 1: a second warrant
// with the same WarrantSourceKey already pending in the open cycle is not
// appended.
func TestTryStampWarrant_OpenCycleDedup(t *testing.T) {
	w, cancel, _ := buildPR3aWorld(t)
	defer cancel()
	now := time.Now().UTC()

	_, _ = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["alice"]
		meta := sim.WarrantMeta{
			TriggerActorID: "bob",
			Reason:         sim.NPCSpeechWarrantReason{SpeechID: 7, Speaker: "bob"},
			SourceEventID:  sim.EventID(7),
			RootEventID:    sim.EventID(7),
		}
		sim.TryStampWarrant(world, a, meta, now)
		sim.TryStampWarrant(world, a, meta, now) // same (Kind, Discriminator)
		if len(a.Warrants) != 1 {
			t.Errorf("open-cycle dedup: Warrants len = %d, want 1", len(a.Warrants))
		}
		return nil, nil
	}})
}

// TestTryStampWarrant_DistinctSourceEventStillStamps covers that two
// warrants of the same kind under the SAME root but with DIFFERENT
// SourceEventIDs are distinct developments — both stamp. Dedup never keys
// on RootEventID.
func TestTryStampWarrant_DistinctSourceEventStillStamps(t *testing.T) {
	w, cancel, _ := buildPR3aWorld(t)
	defer cancel()
	now := time.Now().UTC()

	_, _ = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["alice"]
		base := sim.WarrantMeta{
			TriggerActorID: "bob",
			RootEventID:    sim.EventID(3), // same root for both
		}
		m1 := base
		m1.Reason = sim.NPCSpeechWarrantReason{SpeechID: 7, Speaker: "bob"}
		m1.SourceEventID = sim.EventID(7)
		m2 := base
		m2.Reason = sim.NPCSpeechWarrantReason{SpeechID: 8, Speaker: "bob"}
		m2.SourceEventID = sim.EventID(8)
		sim.TryStampWarrant(world, a, m1, now)
		sim.TryStampWarrant(world, a, m2, now)
		if len(a.Warrants) != 2 {
			t.Errorf("distinct SourceEventID under same root: Warrants len = %d, want 2", len(a.Warrants))
		}
		return nil, nil
	}})
}

// TestTryStampWarrant_ZeroSourceBypassesDedup covers design_review's
// required bypass test: two same-kind warrants with SourceEventID == 0
// ("not event-sourced") must NOT be suppressed — dedup must never key on
// WarrantKind + EventID(0). Both stamps land.
func TestTryStampWarrant_ZeroSourceBypassesDedup(t *testing.T) {
	w, cancel, _ := buildPR3aWorld(t)
	defer cancel()
	now := time.Now().UTC()

	_, _ = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["alice"]
		// Identical metas, SourceEventID left at the zero value.
		meta := sim.WarrantMeta{
			TriggerActorID: "bob",
			Reason:         sim.BasicWarrantReason{K: sim.WarrantKindHuddleJoined},
		}
		if sim.EventSourced(meta) {
			t.Fatal("meta with SourceEventID == 0 must report eventSourced() == false")
		}
		sim.TryStampWarrant(world, a, meta, now)
		sim.TryStampWarrant(world, a, meta, now)
		if len(a.Warrants) != 2 {
			t.Errorf("zero-sourced warrants must bypass dedup: Warrants len = %d, want 2", len(a.Warrants))
		}
		return nil, nil
	}})
}

// TestTryStampWarrant_InFlightDedup covers dedup path 2: a warrant whose
// WarrantSourceKey was consumed into the actor's in-flight tick attempt is
// suppressed — the actor is already addressing that exact source.
func TestTryStampWarrant_InFlightDedup(t *testing.T) {
	w, cancel, _ := buildPR3aWorld(t)
	defer cancel()
	now := time.Now().UTC()

	_, _ = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["alice"]
		key := sim.WarrantSourceKey{Kind: sim.WarrantKindNPCSpoke, Discriminator: 7}
		sim.SetActorInFlightSourceKeys(a, map[sim.WarrantSourceKey]struct{}{key: {}})

		meta := sim.WarrantMeta{
			Reason:        sim.NPCSpeechWarrantReason{SpeechID: 7, Speaker: "bob"},
			SourceEventID: sim.EventID(7),
		}
		sim.TryStampWarrant(world, a, meta, now)
		if a.WarrantedSince != nil || len(a.Warrants) != 0 {
			t.Errorf("in-flight dedup: warrant with an in-flight source key must be suppressed (since=%v warrants=%d)",
				a.WarrantedSince, len(a.Warrants))
		}

		// A DIFFERENT source event of the same kind is a new development —
		// it must still stamp.
		meta.Reason = sim.NPCSpeechWarrantReason{SpeechID: 8, Speaker: "bob"}
		meta.SourceEventID = sim.EventID(8)
		sim.TryStampWarrant(world, a, meta, now)
		if a.WarrantedSince == nil || len(a.Warrants) != 1 {
			t.Errorf("distinct source event must still stamp past the in-flight set (warrants=%d)", len(a.Warrants))
		}
		return nil, nil
	}})
}

// TestTryStampWarrant_RecentlyConsumedDedup covers dedup path 3: a warrant
// whose WarrantSourceKey is in the recently-consumed set is suppressed
// while inside the TTL window, and stamps again once the entry has aged
// past recentlyConsumedTTL.
func TestTryStampWarrant_RecentlyConsumedDedup(t *testing.T) {
	w, cancel, _ := buildPR3aWorld(t)
	defer cancel()
	base := time.Now().UTC()

	_, _ = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["alice"]
		key := sim.WarrantSourceKey{Kind: sim.WarrantKindNPCSpoke, Discriminator: 7}
		sim.RememberConsumedSourceKey(a, key, base)

		meta := sim.WarrantMeta{
			Reason:        sim.NPCSpeechWarrantReason{SpeechID: 7, Speaker: "bob"},
			SourceEventID: sim.EventID(7),
		}
		// Within the TTL — suppressed.
		sim.TryStampWarrant(world, a, meta, base.Add(time.Minute))
		if a.WarrantedSince != nil {
			t.Error("recently-consumed within TTL must suppress the warrant")
		}
		// Past the TTL — no longer suppressed.
		sim.TryStampWarrant(world, a, meta, base.Add(sim.RecentlyConsumedTTL+time.Second))
		if a.WarrantedSince == nil || len(a.Warrants) != 1 {
			t.Errorf("recently-consumed past TTL must NOT suppress (warrants=%d)", len(a.Warrants))
		}
		return nil, nil
	}})
}

// ---- recently-consumed set: TTL sweep, cap eviction, LoadWorld reset ---

// TestRememberConsumedSourceKey_CapEviction covers the hard cap: inserting
// well past recentlyConsumedCap distinct fresh keys keeps the set at the
// cap, evicting oldest-by-insertion first.
func TestRememberConsumedSourceKey_CapEviction(t *testing.T) {
	a := &sim.Actor{ID: "alice"}
	base := time.Now().UTC()

	total := sim.RecentlyConsumedCap + 25
	for i := 0; i < total; i++ {
		key := sim.WarrantSourceKey{Kind: sim.WarrantKindNPCSpoke, Discriminator: uint64(i + 1)}
		sim.RememberConsumedSourceKey(a, key, base.Add(time.Duration(i)*time.Millisecond))
	}

	got := sim.ActorRecentlyConsumedSourceKeys(a)
	if len(got) != sim.RecentlyConsumedCap {
		t.Errorf("recently-consumed len = %d, want exactly cap %d", len(got), sim.RecentlyConsumedCap)
	}
	// The very first (oldest) key must have been evicted; the last
	// (newest) must remain.
	oldest := sim.WarrantSourceKey{Kind: sim.WarrantKindNPCSpoke, Discriminator: 1}
	if _, ok := got[oldest]; ok {
		t.Error("oldest-by-insertion key should have been evicted")
	}
	newest := sim.WarrantSourceKey{Kind: sim.WarrantKindNPCSpoke, Discriminator: uint64(total)}
	if _, ok := got[newest]; !ok {
		t.Error("newest key should still be present")
	}
}

// TestRememberConsumedSourceKey_TTLSweep covers expired-first eviction:
// when the set is at cap and every entry has aged past recentlyConsumedTTL,
// the next insert sweeps all stale entries before inserting.
func TestRememberConsumedSourceKey_TTLSweep(t *testing.T) {
	a := &sim.Actor{ID: "alice"}
	now := time.Now().UTC()
	stale := now.Add(-2 * sim.RecentlyConsumedTTL)

	for i := 0; i < sim.RecentlyConsumedCap; i++ {
		key := sim.WarrantSourceKey{Kind: sim.WarrantKindNPCSpoke, Discriminator: uint64(i + 1)}
		sim.RememberConsumedSourceKey(a, key, stale)
	}
	// The set is at cap and entirely stale; this insert triggers the
	// expired-first sweep.
	fresh := sim.WarrantSourceKey{Kind: sim.WarrantKindNPCSpoke, Discriminator: 9999}
	sim.RememberConsumedSourceKey(a, fresh, now)

	got := sim.ActorRecentlyConsumedSourceKeys(a)
	if len(got) != 1 {
		t.Errorf("after TTL sweep len = %d, want 1 (all stale swept, one fresh)", len(got))
	}
	if _, ok := got[fresh]; !ok {
		t.Error("the fresh key must remain after the sweep")
	}
}

// TestLoadWorld_WipesPR3aReactorState covers that the PR 3a dedup
// bookkeeping (inFlightSourceKeys, recentlyConsumedSourceKeys) is
// ephemeral — wiped on LoadWorld like the rest of the reactor state.
func TestLoadWorld_WipesPR3aReactorState(t *testing.T) {
	repo, handles := mem.NewRepository()
	a := &sim.Actor{ID: "alice"}
	sim.SetActorInFlightSourceKeys(a, map[sim.WarrantSourceKey]struct{}{
		{Kind: sim.WarrantKindNPCSpoke, Discriminator: 1}: {},
	})
	sim.SetActorRecentlyConsumedSourceKeys(a, map[sim.WarrantSourceKey]time.Time{
		{Kind: sim.WarrantKindNPCSpoke, Discriminator: 2}: time.Now(),
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{"alice": a})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	la := w.Actors["alice"]
	if sim.ActorInFlightSourceKeys(la) != nil {
		t.Error("inFlightSourceKeys survived LoadWorld")
	}
	if sim.ActorRecentlyConsumedSourceKeys(la) != nil {
		t.Error("recentlyConsumedSourceKeys survived LoadWorld")
	}
}

// ---- MinReactorTickGap pacing floor -----------------------------------

// TestEvaluateReactors_MinReactorTickGapEnforced covers the always-on
// per-actor pacing floor: an actor that ticked more recently than
// MinReactorTickGap (default 5s) does not emit — its WarrantDueAt is
// pushed to the gap boundary and the warrant stays open. A Force warrant
// bypasses the floor.
func TestEvaluateReactors_MinReactorTickGapEnforced(t *testing.T) {
	w, cancel, _ := buildPR3aWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// Simulate a prior tick 1s ago — well inside the 5s default gap.
	_, _ = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["alice"]
		a.RecentReactorTicks = sim.NewRingBuffer[time.Time](8)
		a.RecentReactorTicks.Push(now.Add(-1 * time.Second))
		return nil, nil
	}})
	seedDueWarrant(t, w, "alice", []sim.WarrantMeta{
		{Reason: sim.BasicWarrantReason{K: sim.WarrantKindNPCSpoke}},
	}, now)
	emitted := subscribeReactorTicks(t, w)

	_, _ = w.Send(sim.EvaluateReactors(now))
	if len(*emitted) != 0 {
		t.Errorf("MinReactorTickGap: actor ticked 1s ago, want no emit; got %d", len(*emitted))
	}
	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if a.WarrantedSince == nil {
			t.Error("MinReactorTickGap cleared the warrant (it must stay open)")
		}
		if a.WarrantDueAt == nil || !a.WarrantDueAt.After(now) {
			t.Errorf("WarrantDueAt not pushed forward by the gap: %v", a.WarrantDueAt)
		}
	})

	// A Force warrant bypasses the floor — it emits even inside the gap.
	seedDueWarrant(t, w, "alice", []sim.WarrantMeta{
		{Force: true, Reason: sim.BasicWarrantReason{K: sim.WarrantKindAdmin}},
	}, now)
	_, _ = w.Send(sim.EvaluateReactors(now))
	if len(*emitted) != 1 {
		t.Errorf("Force warrant must bypass MinReactorTickGap: emit count = %d, want 1", len(*emitted))
	}
}

// ---- tick admission gate ----------------------------------------------

// TestEvaluateReactors_AdmissionGate covers Option-A admit-before-consume:
// when CanAdmit() is false the evaluator emits nothing, leaves the
// warrants OPEN, pushes WarrantDueAt by AdmissionBackoff, and writes a
// `deferred` telemetry record. When CanAdmit() flips true the warrant
// fires normally.
func TestEvaluateReactors_AdmissionGate(t *testing.T) {
	w, cancel, tel := buildPR3aWorld(t)
	defer cancel()
	now := time.Now().UTC()

	adm := &fakeAdmission{admit: false}
	_, _ = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.SetTickAdmissionController(adm)
		return nil, nil
	}})
	seedDueWarrant(t, w, "alice", []sim.WarrantMeta{
		{Reason: sim.BasicWarrantReason{K: sim.WarrantKindNPCSpoke}},
	}, now)
	emitted := subscribeReactorTicks(t, w)

	// Admission denied.
	_, _ = w.Send(sim.EvaluateReactors(now))
	if len(*emitted) != 0 {
		t.Errorf("admission denied: want 0 emits, got %d", len(*emitted))
	}
	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if a.WarrantedSince == nil {
			t.Error("admission denial cleared the warrant — it must stay OPEN")
		}
		if a.TickInFlight {
			t.Error("TickInFlight set despite admission denial (nothing should be consumed)")
		}
		if a.WarrantDueAt == nil || !a.WarrantDueAt.After(now) {
			t.Errorf("WarrantDueAt not pushed by AdmissionBackoff: %v", a.WarrantDueAt)
		}
	})
	// A `deferred` telemetry record was written for the actor.
	recs := tel.snapshot()
	sawDeferred := false
	for _, r := range recs {
		if r.Kind == "deferred" && r.ActorID == "alice" {
			sawDeferred = true
		}
	}
	if !sawDeferred {
		t.Errorf("expected a `deferred` telemetry record for alice; got %+v", recs)
	}

	// Admission granted — the still-open warrant now fires. Evaluate at a
	// time past the pushed WarrantDueAt.
	adm.admit = true
	_, _ = w.Send(sim.EvaluateReactors(now.Add(time.Second)))
	if len(*emitted) != 1 {
		t.Errorf("admission granted: warrant should fire; emit count = %d, want 1", len(*emitted))
	}
}

// TestEvaluateReactors_DefaultAlwaysAdmit covers that with no admission
// controller installed the default alwaysAdmit lets the evaluator run —
// a due warrant fires with no handler wired.
func TestEvaluateReactors_DefaultAlwaysAdmit(t *testing.T) {
	w, cancel, _ := buildPR3aWorld(t)
	defer cancel()
	now := time.Now().UTC()

	seedDueWarrant(t, w, "alice", []sim.WarrantMeta{
		{Reason: sim.BasicWarrantReason{K: sim.WarrantKindNPCSpoke}},
	}, now)
	emitted := subscribeReactorTicks(t, w)

	_, _ = w.Send(sim.EvaluateReactors(now))
	if len(*emitted) != 1 {
		t.Errorf("default alwaysAdmit: due warrant should fire; emit count = %d, want 1", len(*emitted))
	}
}

// TestEvaluateReactors_NeedInterruptsBreakAndStamps covers the ZBBS-HOME-329
// #3/#6 emit-path behavior: an actor on a still-running scheduled break with a
// due red-need warrant has the break ENDED (endBreak) when the tick is emitted,
// and LastTickedAt is stamped to the evaluator's now. The eligibility unit
// tests cover the gate; this pins the actual state mutation at the chokepoint.
func TestEvaluateReactors_NeedInterruptsBreakAndStamps(t *testing.T) {
	w, cancel, _ := buildPR3aWorld(t)
	defer cancel()
	now := time.Now().UTC()
	future := now.Add(time.Hour)

	// Put alice on a scheduled break that still has time to run.
	_, _ = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["alice"]
		a.State = sim.StateResting
		a.BreakUntil = &future
		return nil, nil
	}})
	// A due red-need warrant — the thing that should cut the break short.
	seedDueWarrant(t, w, "alice", []sim.WarrantMeta{
		{Reason: sim.NeedThresholdWarrantReason{Need: "hunger"}},
	}, now)
	emitted := subscribeReactorTicks(t, w)

	_, _ = w.Send(sim.EvaluateReactors(now))
	if len(*emitted) != 1 {
		t.Fatalf("need warrant on a break should fire a tick; emit count = %d, want 1", len(*emitted))
	}
	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if !a.TickInFlight {
			t.Error("TickInFlight = false, want true (tick emitted)")
		}
		if a.State == sim.StateResting {
			t.Error("State still StateResting; endBreak should have reset it to idle")
		}
		if a.BreakUntil != nil {
			t.Errorf("BreakUntil = %v, want nil (break ended on interrupt)", a.BreakUntil)
		}
		if a.LastTickedAt == nil || !a.LastTickedAt.Equal(now) {
			t.Errorf("LastTickedAt = %v, want %v (stamped at emit)", a.LastTickedAt, now)
		}
	})
}

// TestEvaluateReactors_PCSpeechInterruptsBreakAndStamps is the ZBBS-HOME-377
// emit-path counterpart of the need case above: an actor on a still-running
// scheduled break with a due PC-speech warrant has the break actually ENDED
// (endBreak) when the tick emits — so the keeper a player is addressing leaves
// rest and answers, not merely passes the eligibility gate. The unit tests
// (reactor_test.go) cover the gate; this pins the state mutation at the
// chokepoint (code_review #2 — the highest-value addition).
func TestEvaluateReactors_PCSpeechInterruptsBreakAndStamps(t *testing.T) {
	w, cancel, _ := buildPR3aWorld(t)
	defer cancel()
	now := time.Now().UTC()
	future := now.Add(time.Hour)

	// Put alice on a scheduled break that still has time to run.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["alice"]
		a.State = sim.StateResting
		a.BreakUntil = &future
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed break: %v", err)
	}
	// A due PC-speech warrant — a player addressing alice in person.
	seedDueWarrant(t, w, "alice", []sim.WarrantMeta{
		{Reason: sim.PCSpeechWarrantReason{SpeechID: 1, Speaker: "player"}},
	}, now)
	emitted := subscribeReactorTicks(t, w)

	if _, err := w.Send(sim.EvaluateReactors(now)); err != nil {
		t.Fatalf("EvaluateReactors: %v", err)
	}
	if len(*emitted) != 1 {
		t.Fatalf("PC-speech warrant on a break should fire a tick; emit count = %d, want 1", len(*emitted))
	}
	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if !a.TickInFlight {
			t.Error("TickInFlight = false, want true (tick emitted)")
		}
		if a.State == sim.StateResting {
			t.Error("State still StateResting; endBreak should have reset it to idle")
		}
		if a.BreakUntil != nil {
			t.Errorf("BreakUntil = %v, want nil (break ended on PC-speech interrupt)", a.BreakUntil)
		}
		if a.LastTickedAt == nil || !a.LastTickedAt.Equal(now) {
			t.Errorf("LastTickedAt = %v, want %v (stamped at emit)", a.LastTickedAt, now)
		}
	})
}

// ---- in-flight key recording at emit ----------------------------------

// TestEvaluateReactors_RecordsInFlightSourceKeys covers that the consumed
// event-sourced warrants' WarrantSourceKeys land in actor.inFlightSourceKeys
// at ReactorTickDue emit (and a zero-sourced warrant contributes no key).
func TestEvaluateReactors_RecordsInFlightSourceKeys(t *testing.T) {
	w, cancel, _ := buildPR3aWorld(t)
	defer cancel()
	now := time.Now().UTC()

	seedDueWarrant(t, w, "alice", []sim.WarrantMeta{
		{Reason: sim.NPCSpeechWarrantReason{SpeechID: 11, Speaker: "bob"}, SourceEventID: sim.EventID(11)},
		{Reason: sim.ArrivalWarrantReason{AttemptID: 12}, SourceEventID: sim.EventID(12)},
		{Reason: sim.BasicWarrantReason{K: sim.WarrantKindHuddleJoined}}, // zero-discriminator — no key
	}, now)
	emitted := subscribeReactorTicks(t, w)

	_, _ = w.Send(sim.EvaluateReactors(now))
	if len(*emitted) != 1 {
		t.Fatalf("expected 1 emit, got %d", len(*emitted))
	}
	inspectActor(t, w, "alice", func(a *sim.Actor) {
		keys := sim.ActorInFlightSourceKeys(a)
		if len(keys) != 2 {
			t.Fatalf("inFlightSourceKeys len = %d, want 2 (zero-sourced warrant contributes none)", len(keys))
		}
		want := []sim.WarrantSourceKey{
			{Kind: sim.WarrantKindNPCSpoke, Discriminator: 11},
			{Kind: sim.WarrantKindArrived, Discriminator: 12},
		}
		for _, k := range want {
			if _, ok := keys[k]; !ok {
				t.Errorf("inFlightSourceKeys missing %+v", k)
			}
		}
	})
}

// ---- CompleteReactorTick terminal-status warrant policy ---------------

// completeWithStatus arranges an in-flight attempt for alice with the
// given consumed source keys, then runs CompleteReactorTick with the
// given terminal status + carried-forward warrants, and returns nothing —
// callers inspect the actor afterwards.
func completeWithStatus(
	t *testing.T, w *sim.World,
	inFlight []sim.WarrantSourceKey,
	status sim.TickTerminalStatus,
	carried []sim.WarrantMeta,
	now time.Time,
) {
	t.Helper()
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["alice"]
		a.TickInFlight = true
		a.TickAttemptID = "tk-pr3a"
		set := make(map[sim.WarrantSourceKey]struct{}, len(inFlight))
		for _, k := range inFlight {
			set[k] = struct{}{}
		}
		sim.SetActorInFlightSourceKeys(a, set)
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("arrange in-flight attempt: %v", err)
	}
	if _, err := w.Send(sim.CompleteReactorTick("alice", "tk-pr3a",
		sim.TickResult{TerminalStatus: status, UnaddressedWarrants: carried}, now)); err != nil {
		t.Fatalf("CompleteReactorTick: %v", err)
	}
}

// TestCompleteReactorTick_SuccessMovesAddressedKeys covers the success
// branch of the terminal-status policy: the consumed source keys move into
// the recently-consumed set, and the in-flight markers clear.
func TestCompleteReactorTick_SuccessMovesAddressedKeys(t *testing.T) {
	w, cancel, _ := buildPR3aWorld(t)
	defer cancel()
	now := time.Now().UTC()

	k1 := sim.WarrantSourceKey{Kind: sim.WarrantKindNPCSpoke, Discriminator: 21}
	k2 := sim.WarrantSourceKey{Kind: sim.WarrantKindArrived, Discriminator: 22}
	completeWithStatus(t, w, []sim.WarrantSourceKey{k1, k2}, sim.TickStatusSuccess, nil, now)

	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if a.TickInFlight || a.TickAttemptID != "" {
			t.Error("in-flight markers not cleared on completion")
		}
		if sim.ActorInFlightSourceKeys(a) != nil {
			t.Error("inFlightSourceKeys not cleared on completion")
		}
		rc := sim.ActorRecentlyConsumedSourceKeys(a)
		if _, ok := rc[k1]; !ok {
			t.Error("success: addressed key k1 not moved to recently-consumed")
		}
		if _, ok := rc[k2]; !ok {
			t.Error("success: addressed key k2 not moved to recently-consumed")
		}
	})
}

// TestCompleteReactorTick_FailedBeforeRenderMovesNothing covers the
// failed-before-render branch: the actor never perceived the stimulus, so
// no consumed key moves into recently-consumed.
func TestCompleteReactorTick_FailedBeforeRenderMovesNothing(t *testing.T) {
	w, cancel, _ := buildPR3aWorld(t)
	defer cancel()
	now := time.Now().UTC()

	k1 := sim.WarrantSourceKey{Kind: sim.WarrantKindNPCSpoke, Discriminator: 31}
	completeWithStatus(t, w, []sim.WarrantSourceKey{k1}, sim.TickStatusFailedBeforeRender, nil, now)

	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if a.TickInFlight {
			t.Error("in-flight marker not cleared")
		}
		if len(sim.ActorRecentlyConsumedSourceKeys(a)) != 0 {
			t.Error("failed-before-render must move nothing into recently-consumed")
		}
	})
}

// TestCompleteReactorTick_CarryForwardReopensAndExcludes covers the carry-
// forward path: an UnaddressedWarrant is re-opened directly onto the actor
// (bypassing tryStampWarrant's dedup), and its source key is excluded from
// the recently-consumed move — so it can fire again — while the genuinely
// addressed key still moves into recently-consumed.
func TestCompleteReactorTick_CarryForwardReopensAndExcludes(t *testing.T) {
	w, cancel, _ := buildPR3aWorld(t)
	defer cancel()
	now := time.Now().UTC()

	addressed := sim.WarrantSourceKey{Kind: sim.WarrantKindNPCSpoke, Discriminator: 41}
	carriedKey := sim.WarrantSourceKey{Kind: sim.WarrantKindArrived, Discriminator: 42}
	// The carried warrant's sourceKey() must equal carriedKey.
	carriedMeta := sim.WarrantMeta{
		Reason:        sim.ArrivalWarrantReason{AttemptID: 42},
		SourceEventID: sim.EventID(42),
	}
	completeWithStatus(t, w,
		[]sim.WarrantSourceKey{addressed, carriedKey},
		sim.TickStatusSuccess,
		[]sim.WarrantMeta{carriedMeta},
		now)

	inspectActor(t, w, "alice", func(a *sim.Actor) {
		rc := sim.ActorRecentlyConsumedSourceKeys(a)
		if _, ok := rc[addressed]; !ok {
			t.Error("the genuinely addressed key should move into recently-consumed")
		}
		if _, ok := rc[carriedKey]; ok {
			t.Error("a carried-forward key must be EXCLUDED from recently-consumed")
		}
		// The carried warrant is re-opened directly — a fresh warrant cycle.
		if a.WarrantedSince == nil || a.WarrantDueAt == nil {
			t.Fatal("carry-forward did not re-open a warrant cycle")
		}
		if len(a.Warrants) != 1 || a.Warrants[0].SourceEventID != sim.EventID(42) {
			t.Errorf("carried warrant not re-opened onto the actor: %+v", a.Warrants)
		}
	})
}

// TestCompleteReactorTick_StaleLeavesAttemptUntouched covers that a
// completion for a superseded attempt touches nothing — the live attempt's
// in-flight markers and source keys are left intact.
func TestCompleteReactorTick_StaleLeavesAttemptUntouched(t *testing.T) {
	w, cancel, _ := buildPR3aWorld(t)
	defer cancel()
	now := time.Now().UTC()

	k1 := sim.WarrantSourceKey{Kind: sim.WarrantKindNPCSpoke, Discriminator: 51}
	_, _ = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["alice"]
		a.TickInFlight = true
		a.TickAttemptID = "tk-current"
		sim.SetActorInFlightSourceKeys(a, map[sim.WarrantSourceKey]struct{}{k1: {}})
		return nil, nil
	}})

	res, err := w.Send(sim.CompleteReactorTick("alice", "tk-stale",
		sim.TickResult{TerminalStatus: sim.TickStatusSuccess}, now))
	if err != nil {
		t.Fatalf("CompleteReactorTick: %v", err)
	}
	if !res.(sim.CompleteReactorTickResult).Stale {
		t.Error("expected Stale=true for a mismatched AttemptID")
	}
	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if !a.TickInFlight || a.TickAttemptID != "tk-current" {
			t.Error("stale completion disturbed the live attempt's in-flight markers")
		}
		if _, ok := sim.ActorInFlightSourceKeys(a)[k1]; !ok {
			t.Error("stale completion disturbed the live attempt's in-flight source keys")
		}
		if len(sim.ActorRecentlyConsumedSourceKeys(a)) != 0 {
			t.Error("stale completion moved keys into recently-consumed")
		}
	})
}

// TestCompleteReactorTick_IdleActorEmptyAttemptIsStale covers code_review
// R1: a stray completion against an idle actor with an empty attemptID
// must return Stale=true and run NO warrant policy. The zero value of
// TickAttemptID is also "", so without the TickInFlight / non-empty
// guards such a call would match an idle actor and wrongly re-open
// warrants / mutate recently-consumed state.
func TestCompleteReactorTick_IdleActorEmptyAttemptIsStale(t *testing.T) {
	w, cancel, _ := buildPR3aWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// alice is idle: TickInFlight false, TickAttemptID "" (zero value), no
	// warrant cycle. A buggy completion carrying UnaddressedWarrants must
	// not be able to inject them.
	carried := sim.WarrantMeta{
		Reason:        sim.BasicWarrantReason{K: sim.WarrantKindArrived},
		SourceEventID: sim.EventID(77),
	}
	res, err := w.Send(sim.CompleteReactorTick("alice", "",
		sim.TickResult{TerminalStatus: sim.TickStatusSuccess, UnaddressedWarrants: []sim.WarrantMeta{carried}}, now))
	if err != nil {
		t.Fatalf("CompleteReactorTick: %v", err)
	}
	if !res.(sim.CompleteReactorTickResult).Stale {
		t.Error("idle actor + empty attemptID: expected Stale=true")
	}
	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if a.WarrantedSince != nil || len(a.Warrants) != 0 {
			t.Errorf("stray completion injected warrants onto an idle actor: since=%v warrants=%d",
				a.WarrantedSince, len(a.Warrants))
		}
		if len(sim.ActorRecentlyConsumedSourceKeys(a)) != 0 {
			t.Error("stray completion mutated recently-consumed state on an idle actor")
		}
		if a.TickInFlight {
			t.Error("stray completion set TickInFlight on an idle actor")
		}
	})
}

// TestCloneActor_PR3aFieldsRoundTrip covers that CloneActor deep-copies
// the PR 3a additions — the WarrantMeta source fields carried in Warrants,
// the inFlightSourceKeys set, and the recentlyConsumedSourceKeys set — so
// a mutation on one side cannot leak to the other across the clone
// boundary the mem (and future pg) repos impose.
func TestCloneActor_PR3aFieldsRoundTrip(t *testing.T) {
	now := time.Now().UTC()
	since := now.Add(-time.Second)
	due := now.Add(time.Second)
	k1 := sim.WarrantSourceKey{Kind: sim.WarrantKindNPCSpoke, Discriminator: 1}
	k2 := sim.WarrantSourceKey{Kind: sim.WarrantKindArrived, Discriminator: 2}

	orig := &sim.Actor{
		ID:             "alice",
		WarrantedSince: &since,
		WarrantDueAt:   &due,
		Warrants: []sim.WarrantMeta{
			{
				TriggerActorID: "bob",
				Reason:         sim.BasicWarrantReason{K: sim.WarrantKindNPCSpoke},
				SourceEventID:  sim.EventID(1),
				RootEventID:    sim.EventID(1),
				SourceActorID:  "bob",
				HuddleID:       "h1",
				SceneID:        "sc1",
				OccurredAt:     now,
			},
		},
	}
	sim.SetActorInFlightSourceKeys(orig, map[sim.WarrantSourceKey]struct{}{k1: {}})
	sim.SetActorRecentlyConsumedSourceKeys(orig, map[sim.WarrantSourceKey]time.Time{k2: now})

	clone := sim.CloneActor(orig)

	// WarrantMeta source fields are preserved on the clone.
	if len(clone.Warrants) != 1 {
		t.Fatalf("clone Warrants len = %d, want 1", len(clone.Warrants))
	}
	cw := clone.Warrants[0]
	if cw.SourceEventID != 1 || cw.RootEventID != 1 || cw.SourceActorID != "bob" ||
		cw.HuddleID != "h1" || cw.SceneID != "sc1" || !cw.OccurredAt.Equal(now) {
		t.Errorf("WarrantMeta source fields not preserved on clone: %+v", cw)
	}
	// The Warrants slice is independent — a mutation on the original does
	// not leak to the clone.
	orig.Warrants[0].SourceEventID = sim.EventID(999)
	if clone.Warrants[0].SourceEventID != 1 {
		t.Error("Warrants slice aliased — original mutation leaked to clone")
	}

	// inFlightSourceKeys: the clone has the entry, and the maps are
	// independent — deleting from the original must not affect the clone.
	if _, ok := sim.ActorInFlightSourceKeys(clone)[k1]; !ok {
		t.Fatal("clone missing inFlightSourceKeys entry k1")
	}
	delete(sim.ActorInFlightSourceKeys(orig), k1)
	if _, ok := sim.ActorInFlightSourceKeys(clone)[k1]; !ok {
		t.Error("inFlightSourceKeys aliased — deleting from original leaked to clone")
	}

	// recentlyConsumedSourceKeys: same independence check.
	if _, ok := sim.ActorRecentlyConsumedSourceKeys(clone)[k2]; !ok {
		t.Fatal("clone missing recentlyConsumedSourceKeys entry k2")
	}
	delete(sim.ActorRecentlyConsumedSourceKeys(orig), k2)
	if _, ok := sim.ActorRecentlyConsumedSourceKeys(clone)[k2]; !ok {
		t.Error("recentlyConsumedSourceKeys aliased — deleting from original leaked to clone")
	}
}

// TestCloneActor_SocialFields verifies the #4 social persistence fields survive
// CloneActor: the config values (tag/start/end) are preserved, and the mutated
// SocialLastBoundaryAt cursor is deep-cloned (a distinct pointer) like the other
// *time.Time cursors — so a published snapshot can't alias the live boundary
// stamp the social mover advances.
func TestCloneActor_SocialFields(t *testing.T) {
	start := 1080
	end := 1320
	bound := time.Now().UTC()
	orig := &sim.Actor{
		ID:                   "deco",
		SocialTag:            "tavern_evening",
		SocialStartMin:       &start,
		SocialEndMin:         &end,
		SocialLastBoundaryAt: &bound,
	}

	clone := sim.CloneActor(orig)

	if clone.SocialTag != "tavern_evening" {
		t.Errorf("SocialTag = %q, want tavern_evening", clone.SocialTag)
	}
	if clone.SocialStartMin == nil || *clone.SocialStartMin != 1080 {
		t.Errorf("SocialStartMin = %v, want 1080", clone.SocialStartMin)
	}
	if clone.SocialEndMin == nil || *clone.SocialEndMin != 1320 {
		t.Errorf("SocialEndMin = %v, want 1320", clone.SocialEndMin)
	}
	if clone.SocialLastBoundaryAt == nil || !clone.SocialLastBoundaryAt.Equal(bound) {
		t.Fatalf("SocialLastBoundaryAt = %v, want %v", clone.SocialLastBoundaryAt, bound)
	}
	// Deep-cloned: a distinct pointer from the original, so advancing the live
	// actor's boundary stamp can't leak into a published snapshot.
	if clone.SocialLastBoundaryAt == orig.SocialLastBoundaryAt {
		t.Error("SocialLastBoundaryAt aliased — want a deep-cloned distinct pointer")
	}
}

// TestTerminalStatusAddresses covers the per-status classification: which
// terminal statuses count their addressed keys as "consumed" (move into
// recently-consumed) and which carry everything forward.
func TestTerminalStatusAddresses(t *testing.T) {
	cases := []struct {
		status sim.TickTerminalStatus
		want   bool
	}{
		{sim.TickStatusSuccess, true},
		{sim.TickStatusDone, true},
		{sim.TickStatusBudgetForced, true},
		{sim.TickStatusFailedAfterRender, true},
		// Skipped addresses too — the noop-skip preflight read perception
		// and concluded the batch wasn't worth an LLM call. Consumed keys
		// MUST land in recently-consumed or the same warrants would
		// re-emit on the next scan and re-skip, busy-looping the gate.
		{sim.TickStatusSkipped, true},
		{sim.TickStatusFailedBeforeRender, false},
		{sim.TickStatusShutdown, false},
		{sim.TickStatusUnknown, false},
	}
	for _, tc := range cases {
		if got := sim.TerminalStatusAddresses(tc.status); got != tc.want {
			t.Errorf("TerminalStatusAddresses(%d) = %v, want %v", tc.status, got, tc.want)
		}
	}
}
