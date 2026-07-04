package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// labor_persistence_test.go — LLM-259 restart-resume coverage for accepted
// labor contracts (en_route + working). Complements
// TestFinalizeLoad_RevertsStrandedLaboringActor (labor_commands_test.go), which
// pins the no-contract orphan path.

// laboringActor builds a worker as it reloads in production: State IS persisted
// (StateLaboring), but the LaborID/LaboringUntil mirror is transient (never
// checkpointed on the actor), so it comes back zeroed. Rehydrate must restore
// the mirror from the contract.
func laboringActor(id sim.ActorID) *sim.Actor {
	return &sim.Actor{
		ID: id, DisplayName: string(id), Kind: sim.KindNPCShared,
		State: sim.StateLaboring, LaborID: 0, LaboringUntil: nil,
		RecentActions: sim.NewRingBuffer[sim.Action](4),
	}
}

func plainActor(id sim.ActorID, state sim.ActorState) *sim.Actor {
	return &sim.Actor{
		ID: id, DisplayName: string(id), Kind: sim.KindNPCShared,
		State:         state,
		RecentActions: sim.NewRingBuffer[sim.Action](4),
	}
}

// TestFinalizeLoad_ResumesWorkingContract — a worker mid `working` contract
// reloads laboring: the ledger entry is rehydrated, the worker mirror
// (LaborID/LaboringUntil) is restored from the contract, the actor is NOT
// reverted to idle, and the LaborID counter is floored above the loaded id.
func TestFinalizeLoad_ResumesWorkingContract(t *testing.T) {
	repo, handles := mem.NewRepository()
	now := time.Now().UTC()
	workingUntil := now.Add(20 * time.Minute)
	acceptedAt := now.Add(-10 * time.Minute)
	startedAt := now.Add(-10 * time.Minute)

	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"lewis": laboringActor("lewis"),
		"boss":  plainActor("boss", sim.StateIdle),
	})
	handles.LaborContracts.Seed(map[sim.LaborID]*sim.LaborOffer{
		7: {
			ID: 7, WorkerID: "lewis", EmployerID: "boss",
			State: sim.LaborStateWorking, Reward: 5, DurationMin: 30,
			CreatedAt: now.Add(-12 * time.Minute), AcceptedAt: &acceptedAt,
			WorkStartedAt: &startedAt, WorkingUntil: &workingUntil,
		},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}

	offer, ok := w.LaborLedger[7]
	if !ok {
		t.Fatal("working contract 7 not rehydrated into LaborLedger")
	}
	if offer.State != sim.LaborStateWorking || offer.WorkerID != "lewis" || offer.Reward != 5 {
		t.Errorf("rehydrated offer = %+v, want working lewis reward 5", offer)
	}

	a := w.Actors["lewis"]
	if a.State != sim.StateLaboring {
		t.Errorf("worker State = %q, want laboring (resumed, not reverted)", a.State)
	}
	if a.LaborID != 7 {
		t.Errorf("worker LaborID = %d, want 7 (restored from contract)", a.LaborID)
	}
	if a.LaboringUntil == nil || !a.LaboringUntil.Equal(workingUntil) {
		t.Errorf("worker LaboringUntil = %v, want %v (restored from contract)", a.LaboringUntil, workingUntil)
	}

	if got := sim.LaborLedgerSeqForTest(w); got != 7 {
		t.Errorf("laborLedgerSeq = %d, want 7 (floored to max loaded id)", got)
	}
	if got := sim.NextLaborSeq(w); got != 8 {
		t.Errorf("nextLaborSeq = %d, want 8 (floored max + 1 — no id reuse after restart)", got)
	}
}

// TestFinalizeLoad_ResumesEnRouteContract — an en_route contract survives the
// load into the ledger. The worker is walking to the post (not StateLaboring),
// so the reconcile pass leaves it alone; the walk resumes via
// ResumeCheckpointedWalks and the arrival advances the rehydrated offer.
func TestFinalizeLoad_ResumesEnRouteContract(t *testing.T) {
	repo, handles := mem.NewRepository()
	now := time.Now().UTC()
	deadline := now.Add(5 * time.Minute)
	acceptedAt := now.Add(-1 * time.Minute)

	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"walker": plainActor("walker", sim.StateWalking),
		"boss":   plainActor("boss", sim.StateIdle),
	})
	handles.LaborContracts.Seed(map[sim.LaborID]*sim.LaborOffer{
		3: {
			ID: 3, WorkerID: "walker", EmployerID: "boss",
			State: sim.LaborStateEnRoute, Reward: 4, DurationMin: 20,
			CreatedAt: now.Add(-2 * time.Minute), AcceptedAt: &acceptedAt,
			EnRouteDeadline: deadline, EnRouteWaiting: false,
		},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}

	offer, ok := w.LaborLedger[3]
	if !ok {
		t.Fatal("en_route contract 3 not rehydrated into LaborLedger")
	}
	if offer.State != sim.LaborStateEnRoute || !offer.EnRouteDeadline.Equal(deadline) {
		t.Errorf("rehydrated offer = %+v, want en_route with deadline %v", offer, deadline)
	}

	a := w.Actors["walker"]
	if a.State != sim.StateWalking {
		t.Errorf("en_route worker State = %q, want walking (untouched — en_route sets no laboring mirror)", a.State)
	}
	if a.LaborID != 0 {
		t.Errorf("en_route worker LaborID = %d, want 0 (no working mirror for en_route)", a.LaborID)
	}
}

// TestFinalizeLoad_EnRouteResumeCompletesOnArrival — the resume actually FINISHES
// (Finding 4): after an en_route contract rehydrates, the arrival path flips it
// to `working` with a window and the worker mirror set. Proves the arrival
// handling reads only fields that survive the checkpoint — NOT the dropped
// huddle/scene/event ids or the transient LaborID. Both parties are placed
// "inside" the workplace so `actorAtWorkpost` is satisfied without geometry.
func TestFinalizeLoad_EnRouteResumeCompletesOnArrival(t *testing.T) {
	repo, handles := mem.NewRepository()
	now := time.Now().UTC()
	const ws = sim.StructureID("shop-1")

	walker := plainActor("walker", sim.StateWalking)
	walker.InsideStructureID = ws
	boss := plainActor("boss", sim.StateIdle)
	boss.WorkStructureID = ws
	boss.InsideStructureID = ws
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		ws: {ID: ws, DisplayName: "Shop", Tags: []string{}},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{"walker": walker, "boss": boss})
	handles.LaborContracts.Seed(map[sim.LaborID]*sim.LaborOffer{
		3: {
			ID: 3, WorkerID: "walker", EmployerID: "boss",
			State: sim.LaborStateEnRoute, Reward: 4, DurationMin: 20,
			CreatedAt: now.Add(-2 * time.Minute), EnRouteDeadline: now.Add(5 * time.Minute),
		},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	offer := w.LaborLedger[3]
	if offer == nil {
		t.Fatal("en_route contract 3 not rehydrated")
	}

	sim.AdvanceEnRouteOfferForTest(w, offer, now)

	if offer.State != sim.LaborStateWorking {
		t.Fatalf("offer State = %q after arrival, want working (resume did not complete)", offer.State)
	}
	if offer.WorkingUntil == nil {
		t.Error("offer.WorkingUntil nil after flip to working — no completion window")
	}
	a := w.Actors["walker"]
	if a.State != sim.StateLaboring || a.LaborID != 3 {
		t.Errorf("worker after arrival = {State:%q LaborID:%d}, want {laboring 3}", a.State, a.LaborID)
	}
}

// TestFinalizeLoad_DropsContractMissingWorker — a working contract referencing an
// actor absent from the loaded set (an out-of-band actor delete) is dropped, not
// added to the ledger; the load still SUCCEEDS (a live village must boot; a
// dropped contract is data-clean). warn-and-drop, per dropStructureBoundOrphanScenes.
func TestFinalizeLoad_DropsContractMissingWorker(t *testing.T) {
	repo, handles := mem.NewRepository()
	now := time.Now().UTC()
	workingUntil := now.Add(15 * time.Minute)
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{"boss": plainActor("boss", sim.StateIdle)})
	handles.LaborContracts.Seed(map[sim.LaborID]*sim.LaborOffer{
		5: {
			ID: 5, WorkerID: "ghost", EmployerID: "boss",
			State: sim.LaborStateWorking, Reward: 3, DurationMin: 30,
			CreatedAt: now, WorkingUntil: &workingUntil,
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld should boot (warn-and-drop), got: %v", err)
	}
	if _, ok := w.LaborLedger[5]; ok {
		t.Error("contract 5 (missing worker) should have been dropped, not added to the ledger")
	}
}

// TestFinalizeLoad_DropsWorkingContractWorkerNotLaboring — a working contract
// whose worker reloaded as something other than StateLaboring is an actor/contract
// disagreement (an out-of-band actor edit); the contract is dropped and the actor
// is left in its loaded state, not forced back to laboring.
func TestFinalizeLoad_DropsWorkingContractWorkerNotLaboring(t *testing.T) {
	repo, handles := mem.NewRepository()
	now := time.Now().UTC()
	workingUntil := now.Add(15 * time.Minute)
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"lewis": plainActor("lewis", sim.StateIdle), // NOT laboring — the disagreement
		"boss":  plainActor("boss", sim.StateIdle),
	})
	handles.LaborContracts.Seed(map[sim.LaborID]*sim.LaborOffer{
		6: {
			ID: 6, WorkerID: "lewis", EmployerID: "boss",
			State: sim.LaborStateWorking, Reward: 3, DurationMin: 30,
			CreatedAt: now, WorkingUntil: &workingUntil,
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld should boot (warn-and-drop), got: %v", err)
	}
	if _, ok := w.LaborLedger[6]; ok {
		t.Error("stale working contract 6 should have been dropped")
	}
	if a := w.Actors["lewis"]; a.State != sim.StateIdle || a.LaborID != 0 {
		t.Errorf("worker = {State:%q LaborID:%d}, want {idle 0} (not force-restored from stale contract)", a.State, a.LaborID)
	}
}

// TestFinalizeLoad_DropsAllContractsWhenWorkerHasTwo — two accepted contracts for
// one worker violates the one-live-job-per-worker invariant; BOTH are dropped
// (deterministic — never arbitrarily resume one), and the worker reverts to idle.
func TestFinalizeLoad_DropsAllContractsWhenWorkerHasTwo(t *testing.T) {
	repo, handles := mem.NewRepository()
	now := time.Now().UTC()
	until1 := now.Add(15 * time.Minute)
	until2 := now.Add(25 * time.Minute)
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"lewis": laboringActor("lewis"),
		"boss":  plainActor("boss", sim.StateIdle),
	})
	handles.LaborContracts.Seed(map[sim.LaborID]*sim.LaborOffer{
		10: {
			ID: 10, WorkerID: "lewis", EmployerID: "boss",
			State: sim.LaborStateWorking, Reward: 3, DurationMin: 30,
			CreatedAt: now, WorkingUntil: &until1,
		},
		11: {
			ID: 11, WorkerID: "lewis", EmployerID: "boss",
			State: sim.LaborStateWorking, Reward: 4, DurationMin: 30,
			CreatedAt: now, WorkingUntil: &until2,
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld should boot (warn-and-drop), got: %v", err)
	}
	if _, ok := w.LaborLedger[10]; ok {
		t.Error("contract 10 should have been dropped (worker has two accepted contracts)")
	}
	if _, ok := w.LaborLedger[11]; ok {
		t.Error("contract 11 should have been dropped (worker has two accepted contracts)")
	}
	if a := w.Actors["lewis"]; a.State != sim.StateIdle || a.LaborID != 0 {
		t.Errorf("worker = {State:%q LaborID:%d}, want {idle 0} (no job resumed; reverted)", a.State, a.LaborID)
	}
}

// TestFinalizeLoad_DropsNonResumableState — a persisted row with a non-accepted
// state (only reachable via out-of-band write, since SaveSnapshot filters) must
// not seed a phantom into the live ledger; it is dropped and the load succeeds.
func TestFinalizeLoad_DropsNonResumableState(t *testing.T) {
	repo, handles := mem.NewRepository()
	now := time.Now().UTC()
	resolved := now.Add(-1 * time.Minute)
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"lewis": plainActor("lewis", sim.StateIdle),
		"boss":  plainActor("boss", sim.StateIdle),
	})
	handles.LaborContracts.Seed(map[sim.LaborID]*sim.LaborOffer{
		8: {
			ID: 8, WorkerID: "lewis", EmployerID: "boss",
			State: sim.LaborStateCompleted, Reward: 2, DurationMin: 10,
			CreatedAt: now.Add(-30 * time.Minute), ResolvedAt: &resolved,
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld should boot (warn-and-drop), got: %v", err)
	}
	if _, ok := w.LaborLedger[8]; ok {
		t.Error("non-resumable (completed) contract 8 should have been dropped")
	}
}

// TestFinalizeLoad_MixedResumeAndStrand — one worker with a working contract
// resumes; a second laboring worker with NO contract is the genuine orphan and
// is reverted to idle. Pins that the conditional reconcile keys off the live
// ledger, not a blanket revert.
func TestFinalizeLoad_MixedResumeAndStrand(t *testing.T) {
	repo, handles := mem.NewRepository()
	now := time.Now().UTC()
	workingUntil := now.Add(15 * time.Minute)

	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"resumer":  laboringActor("resumer"),
		"stranded": laboringActor("stranded"),
		"boss":     plainActor("boss", sim.StateIdle),
	})
	handles.LaborContracts.Seed(map[sim.LaborID]*sim.LaborOffer{
		9: {
			ID: 9, WorkerID: "resumer", EmployerID: "boss",
			State: sim.LaborStateWorking, Reward: 6, DurationMin: 30,
			CreatedAt: now.Add(-5 * time.Minute), WorkingUntil: &workingUntil,
		},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}

	if a := w.Actors["resumer"]; a.State != sim.StateLaboring || a.LaborID != 9 {
		t.Errorf("resumer = {State:%q LaborID:%d}, want {laboring 9} (contract-backed, resumes)", a.State, a.LaborID)
	}
	stranded := w.Actors["stranded"]
	if stranded.State != sim.StateIdle {
		t.Errorf("stranded State = %q, want idle (no contract → reverted)", stranded.State)
	}
	if stranded.LaborID != 0 || stranded.LaboringUntil != nil {
		t.Errorf("stranded mirror not cleared: LaborID=%d LaboringUntil=%v", stranded.LaborID, stranded.LaboringUntil)
	}
}
