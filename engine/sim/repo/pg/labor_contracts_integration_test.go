package pg

// Real-pg integration tests for the labor_contract mirror (LLM-259). Run
// against embedded Postgres with the full prod-baseline schema + post-baseline
// migrations applied; skipped under `go test -short`.
//
// These prove the parts pgxmock can't: that the accepted-contract subset
// survives a genuine SaveWorld → LoadWorld roundtrip (en_route + working
// persist, pending + terminal are filtered out), that the reward goods leg
// round-trips through jsonb, that the worker mirror is restored on reload, and
// that a settled contract's row is swept by the gen-marker delete-stale.

import (
	"reflect"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

const (
	uuidWorkerWorking = sim.ActorID("aaaaaaaa-0000-0000-0000-000000000259")
	uuidWorkerEnRoute = sim.ActorID("bbbbbbbb-0000-0000-0000-000000000259")
	uuidEmployer      = sim.ActorID("cccccccc-0000-0000-0000-000000000259")
)

// TestIntegration_LaborContract_RoundTrip — a working contract (with a coin +
// goods reward) and an en_route contract survive SaveWorld → LoadWorld; a
// pending and a terminal offer in the same ledger are NOT persisted (filtered
// at build time). The working worker's transient mirror (LaborID/LaboringUntil,
// State laboring) is restored; the en_route worker is left walking.
func TestIntegration_LaborContract_RoundTrip(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := NewRepository(f.Pool)

	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	workingUntil := now.Add(20 * time.Minute)
	enRouteDeadline := now.Add(5 * time.Minute)
	acceptedAt := now.Add(-10 * time.Minute)

	w := checkpointableWorld(repo)
	w.Actors = map[sim.ActorID]*sim.Actor{
		uuidWorkerWorking: {ID: uuidWorkerWorking, DisplayName: "Worker A", State: sim.StateLaboring},
		uuidWorkerEnRoute: {ID: uuidWorkerEnRoute, DisplayName: "Worker B", State: sim.StateWalking},
		uuidEmployer:      {ID: uuidEmployer, DisplayName: "Boss", State: sim.StateIdle},
	}
	rewardItems := []sim.ItemKindQty{{Kind: "plank", Qty: 2}}
	w.LaborLedger = map[sim.LaborID]*sim.LaborOffer{
		7: {
			ID: 7, WorkerID: uuidWorkerWorking, EmployerID: uuidEmployer,
			State: sim.LaborStateWorking, Reward: 5, RewardItems: rewardItems,
			DurationMin: 30, CreatedAt: now.Add(-12 * time.Minute), AcceptedAt: &acceptedAt,
			WorkStartedAt: &acceptedAt, WorkingUntil: &workingUntil,
		},
		3: {
			ID: 3, WorkerID: uuidWorkerEnRoute, EmployerID: uuidEmployer,
			State: sim.LaborStateEnRoute, Reward: 4, DurationMin: 20,
			CreatedAt: now.Add(-2 * time.Minute), AcceptedAt: &acceptedAt,
			EnRouteDeadline: enRouteDeadline,
		},
		1: { // pending — must NOT persist
			ID: 1, WorkerID: uuidWorkerEnRoute, EmployerID: uuidEmployer,
			State: sim.LaborStatePending, Reward: 3, DurationMin: 15,
			CreatedAt: now, ExpiresAt: now.Add(2 * time.Minute),
		},
		9: { // completed terminal — must NOT persist
			ID: 9, WorkerID: uuidWorkerWorking, EmployerID: uuidEmployer,
			State: sim.LaborStateCompleted, Reward: 2, DurationMin: 10,
			CreatedAt: now.Add(-30 * time.Minute), ResolvedAt: &acceptedAt,
		},
	}

	if _, err := SaveWorld(ctx, repo, w.BuildCheckpointSnapshot()); err != nil {
		t.Fatalf("SaveWorld: %v", err)
	}

	loaded, err := LoadWorld(ctx, repo, true /*requireAllImpl*/)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}

	if len(loaded.LaborLedger) != 2 {
		t.Fatalf("LaborLedger = %d entries, want 2 (only en_route+working persist)", len(loaded.LaborLedger))
	}
	if _, ok := loaded.LaborLedger[1]; ok {
		t.Error("pending offer 1 should not have been persisted")
	}
	if _, ok := loaded.LaborLedger[9]; ok {
		t.Error("terminal offer 9 should not have been persisted")
	}

	working := loaded.LaborLedger[7]
	if working == nil {
		t.Fatal("working contract 7 did not round-trip")
	}
	if working.Reward != 5 || working.WorkerID != uuidWorkerWorking || working.EmployerID != uuidEmployer {
		t.Errorf("working contract = %+v, want reward 5 workerA/boss", working)
	}
	if working.WorkingUntil == nil || !working.WorkingUntil.Equal(workingUntil) {
		t.Errorf("working.WorkingUntil = %v, want %v", working.WorkingUntil, workingUntil)
	}
	if !reflect.DeepEqual(working.RewardItems, rewardItems) {
		t.Errorf("working.RewardItems = %+v, want %+v (jsonb goods leg round-trip)", working.RewardItems, rewardItems)
	}

	enRoute := loaded.LaborLedger[3]
	if enRoute == nil {
		t.Fatal("en_route contract 3 did not round-trip")
	}
	if enRoute.State != sim.LaborStateEnRoute || !enRoute.EnRouteDeadline.Equal(enRouteDeadline) {
		t.Errorf("en_route contract = %+v, want en_route deadline %v", enRoute, enRouteDeadline)
	}
	if len(enRoute.RewardItems) != 0 {
		t.Errorf("en_route.RewardItems = %+v, want empty (coin-only)", enRoute.RewardItems)
	}

	// Worker mirror restored for the working contract; en_route worker untouched.
	if a := loaded.Actors[uuidWorkerWorking]; a == nil || a.State != sim.StateLaboring || a.LaborID != 7 {
		t.Errorf("working worker = %+v, want laboring LaborID 7 (mirror restored)", a)
	}
	if a := loaded.Actors[uuidWorkerEnRoute]; a == nil || a.State != sim.StateWalking || a.LaborID != 0 {
		t.Errorf("en_route worker = %+v, want walking LaborID 0", a)
	}
	// (The LaborID allocator safety-floor is asserted in the sim-package unit
	// test TestFinalizeLoad_ResumesWorkingContract — the counter is in-memory
	// state, not a DB round-trip concern.)
}

// TestIntegration_LaborContract_DeleteStaleOnSettle — a second checkpoint after
// a working contract settled (dropped from the ledger) must prune its
// labor_contract row via the gen-marker delete-stale, end to end through
// SaveWorld. The still-active en_route contract survives.
func TestIntegration_LaborContract_DeleteStaleOnSettle(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := NewRepository(f.Pool)

	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	workingUntil := now.Add(20 * time.Minute)
	enRouteDeadline := now.Add(5 * time.Minute)

	w := checkpointableWorld(repo)
	w.Actors = map[sim.ActorID]*sim.Actor{
		uuidWorkerWorking: {ID: uuidWorkerWorking, DisplayName: "Worker A", State: sim.StateLaboring},
		uuidWorkerEnRoute: {ID: uuidWorkerEnRoute, DisplayName: "Worker B", State: sim.StateWalking},
		uuidEmployer:      {ID: uuidEmployer, DisplayName: "Boss", State: sim.StateIdle},
	}
	w.LaborLedger = map[sim.LaborID]*sim.LaborOffer{
		7: {
			ID: 7, WorkerID: uuidWorkerWorking, EmployerID: uuidEmployer,
			State: sim.LaborStateWorking, Reward: 5, DurationMin: 30,
			CreatedAt: now, WorkingUntil: &workingUntil,
		},
		3: {
			ID: 3, WorkerID: uuidWorkerEnRoute, EmployerID: uuidEmployer,
			State: sim.LaborStateEnRoute, Reward: 4, DurationMin: 20,
			CreatedAt: now, EnRouteDeadline: enRouteDeadline,
		},
	}
	if _, err := SaveWorld(ctx, repo, w.BuildCheckpointSnapshot()); err != nil {
		t.Fatalf("SaveWorld (both contracts): %v", err)
	}

	// The working contract settled between checkpoints — remove it from the
	// ledger. The next checkpoint must sweep its row.
	delete(w.LaborLedger, 7)
	if _, err := SaveWorld(ctx, repo, w.BuildCheckpointSnapshot()); err != nil {
		t.Fatalf("SaveWorld (after settle): %v", err)
	}

	loaded, err := LoadWorld(ctx, repo, true /*requireAllImpl*/)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	if len(loaded.LaborLedger) != 1 {
		t.Fatalf("LaborLedger = %d, want 1 (settled contract 7 swept)", len(loaded.LaborLedger))
	}
	if _, gone := loaded.LaborLedger[7]; gone {
		t.Error("settled contract 7 should have been pruned by delete-stale")
	}
	if loaded.LaborLedger[3] == nil {
		t.Error("active en_route contract 3 should survive the second checkpoint")
	}
}
