package handlers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// repair_labor_integration_test.go — end-to-end coverage for a HIRED worker
// mending an employer's worn business (LLM-271/278/280), driven through the real
// pipeline: EvaluateReactors -> reactor -> worker pool -> Harness.RunTick ->
// perception/gateTools/dispatch -> RunTickToolCommand -> the source-activity
// completion sweep.
//
// Motivation (live failure, 2026-07-04, Lewis Walker at PW Apothecary): the hired
// repair had never once mended a stall in production, yet every prior test was
// green. Those tests froze the actor in the one instant the mechanism works and
// asserted a PIECE — the golden cue renders, StartRepair on a hand-placed actor
// consumes/resets, the backstop sweep stamps. None drove the mend end to end for
// a LABORING worker across the labor lifecycle, which is the only place it runs
// in the wild. These tests do, and assert the invariant that actually matters:
// a hired worker cued to repair leaves the business with Wear == 0.

const (
	rlWorker   = sim.ActorID("alice") // the hired hand (reuses the fixture actor)
	rlEmployer = sim.ActorID("employer")
	rlBusiness = sim.VillageObjectID("apothecary")
	rlNails    = 5
)

// newRepairLaborFixture builds the fixture with a repair/speak/done registry and
// a model scripted to call repair then end the tick.
func newRepairLaborFixture(t *testing.T) *integrationFixture {
	t.Helper()
	r := NewRegistry()
	if err := RegisterSpeak(r); err != nil {
		t.Fatalf("register speak: %v", err)
	}
	if err := RegisterRepair(r); err != nil {
		t.Fatalf("register repair: %v", err)
	}
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("register done: %v", err)
	}
	client := llm.NewFakeClient(
		callTurn("c1", "repair", `{}`),
		callTurn("c2", "done", `{}`),
	)
	return newIntegrationFixture(t, r, client)
}

// seedHiredRepairWorld seeds a worn business the employer owns plus a worker
// Working a live hired job for them, standing INSIDE the business with exactly
// enough nails to mend, woken by the hired-repair warrant (the wake that pierces
// the laboring shelve-gate, LLM-271/280).
func seedHiredRepairWorld(t *testing.T, f *integrationFixture, now time.Time) {
	t.Helper()
	if _, err := f.world.Send(sim.Command{Fn: func(w *sim.World) (any, error) {
		w.Settings.StallWearRepairThreshold = 400
		w.Settings.StallWearDegradeThreshold = 600
		w.Settings.StallNailsPerRepair = rlNails
		w.Settings.StallRepairDurationSeconds = 1 // short window so the completion sweep lands fast

		// A structure-backed business the employer owns, worn past the repair
		// threshold (500 >= 400, < 600 degrade) — needs mending, still trades.
		w.VillageObjects[rlBusiness] = &sim.VillageObject{
			ID:           rlBusiness,
			OwnerActorID: rlEmployer,
			Tags:         []string{sim.TagBusiness},
			Wear:         500,
		}

		a := w.Actors[rlWorker]
		a.Kind = sim.KindNPCShared
		a.State = sim.StateLaboring
		until := now.Add(time.Hour)
		a.LaboringUntil = &until
		a.LaborID = 1
		// Structure-backed businesses share their id with the structure, so
		// standing inside == co-located with the business (AtBusiness inside-branch).
		a.InsideStructureID = sim.StructureID(rlBusiness)
		a.Inventory = map[sim.ItemKind]int{sim.NailItemKind: rlNails}
		a.Needs = map[sim.NeedKey]int{"hunger": 5, "thirst": 5, "tiredness": 5}

		workingUntil := now.Add(time.Hour)
		w.LaborLedger[1] = &sim.LaborOffer{
			ID: 1, WorkerID: rlWorker, EmployerID: rlEmployer,
			State: sim.LaborStateWorking, WorkingUntil: &workingUntil,
		}

		since := now.Add(-50 * time.Millisecond)
		due := now.Add(-time.Millisecond)
		a.WarrantedSince = &since
		a.WarrantDueAt = &due
		a.Warrants = []sim.WarrantMeta{{
			TriggerActorID: rlWorker,
			Reason:         sim.StallRepairHiredWarrantReason{StallID: rlBusiness},
		}}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed world: %v", err)
	}
}

// runRepairTick drives one reactor tick and asserts it completed, then asserts
// StartRepair took effect (nails consumed up front + a repair window opened) —
// the seam the live trace showed silently broken.
func runRepairTick(t *testing.T, f *integrationFixture, now time.Time) {
	t.Helper()
	if _, err := f.world.Send(sim.EvaluateReactors(now)); err != nil {
		t.Fatalf("EvaluateReactors: %v", err)
	}
	rec := f.waitForTerminalTelemetry(t)
	if rec.Kind != "completed" {
		t.Fatalf("tick did not complete cleanly: kind=%q", rec.Kind)
	}
	nails, hasWindow := readRepairState(t, f.world, rlWorker)
	if nails != 0 {
		t.Errorf("nails after repair: got %d, want 0 — StartRepair must consume %d nails up front", nails, rlNails)
	}
	if !hasWindow {
		t.Errorf("no repair SourceActivity window opened — the repair tool was a no-op through the dispatch")
	}
}

// TestHiredWorkerRepair_EndToEnd — the happy path: the hire stays live through
// the whole mend. Must stay green after any fix (the normal case must not break).
func TestHiredWorkerRepair_EndToEnd(t *testing.T) {
	f := newRepairLaborFixture(t)
	defer f.stop()

	now := time.Now()
	seedHiredRepairWorld(t, f, now)
	runRepairTick(t, f, now)

	tctx, tcancel := context.WithCancel(context.Background())
	defer tcancel()
	go sim.RunSourceActivityTicker(tctx, f.world)

	eventually(t, "business wear cleared to 0 after the repair completes", func() bool {
		return readWear(t, f.world, rlBusiness) == 0
	})
}

// TestHiredWorkerRepair_HireSettlesDuringWindow reproduces the LIVE failure: the
// worker's hire settles (the ledger offer is cleared, as labor_settle.go does)
// DURING the short repair window — the common case, because a hired hand is only
// woken to mend at the tail of the stint. The mend the worker STARTED and paid
// nails for must still land: the business must end at Wear == 0.
//
// Today it does not — applyCompletedSourceActivity re-resolves the mendable stall
// via WearableStallToMend at completion, which requires the actor to STILL be
// Working the hire; once the offer is gone it returns nil and the wear reset is
// skipped (source_activity.go). Nails spent, no mend. This test goes red until
// completion is bound to the object the window began at instead.
func TestHiredWorkerRepair_HireSettlesDuringWindow(t *testing.T) {
	f := newRepairLaborFixture(t)
	defer f.stop()

	now := time.Now()
	seedHiredRepairWorld(t, f, now)
	runRepairTick(t, f, now)

	// The hire settles WHILE the repair window is still open — the bug condition.
	// Mirror the load-bearing part of labor_settle.go: the completed hire deletes
	// the ledger offer (the old completion re-read it via WearableStallToMend) and
	// drops the worker out of StateLaboring. Then force the still-open window due so
	// the sweep lands deterministically rather than racing the wall clock — the
	// settle happened first, so completion sees no Working offer, exactly as live.
	if _, err := f.world.Send(sim.Command{Fn: func(w *sim.World) (any, error) {
		delete(w.LaborLedger, 1)
		a := w.Actors[rlWorker]
		if a == nil {
			return nil, fmt.Errorf("actor %s not found", rlWorker)
		}
		a.State = sim.StateIdle
		a.LaboringUntil = nil
		a.LaborID = 0
		if a.SourceActivity == nil {
			return nil, fmt.Errorf("expected an open repair window before settling the hire")
		}
		due := now.Add(-time.Second)
		a.SourceActivity.Until = due
		return nil, nil
	}}); err != nil {
		t.Fatalf("settle hire: %v", err)
	}

	tctx, tcancel := context.WithCancel(context.Background())
	defer tcancel()
	go sim.RunSourceActivityTicker(tctx, f.world)

	eventually(t, "business wear cleared to 0 even though the hire settled mid-repair", func() bool {
		return readWear(t, f.world, rlBusiness) == 0
	})
}

// readRepairState reads the worker's nail count and whether a repair
// SourceActivity window is open, off the world goroutine.
func readRepairState(t *testing.T, w *sim.World, id sim.ActorID) (nails int, hasRepairWindow bool) {
	t.Helper()
	v, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors[id]
		if a == nil {
			return nil, fmt.Errorf("actor %s not found", id)
		}
		open := a.SourceActivity != nil && a.SourceActivity.Kind == sim.SourceActivityRepair
		return [2]int{a.Inventory[sim.NailItemKind], boolToInt(open)}, nil
	}})
	if err != nil {
		t.Fatalf("readRepairState: %v", err)
	}
	got := v.([2]int)
	return got[0], got[1] == 1
}

// readWear reads a village object's current wear off the world goroutine.
func readWear(t *testing.T, w *sim.World, id sim.VillageObjectID) int {
	t.Helper()
	v, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		obj := world.VillageObjects[id]
		if obj == nil {
			return nil, fmt.Errorf("village object %s not found", id)
		}
		return obj.Wear, nil
	}})
	if err != nil {
		t.Fatalf("readWear: %v", err)
	}
	return v.(int)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
