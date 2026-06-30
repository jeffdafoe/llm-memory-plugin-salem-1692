package sim_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pay_labor_steer_test.go — LLM-202 (Defect 2). A labor offer settles its reward
// at completion through the labor sweep, so a separate bare `pay` between the
// same pair double-compensates the one job — the live John Ellis / Silence Walker
// case (coins paid by hand up front AND a labor contract booked on top). sim.Pay
// now rejects a bare pay whenever a live (pending or working) labor offer stands
// between the two in EITHER direction, steering back to letting the contract
// settle. The pair-state is the signal, not a read of the free-text reason.
// Reuses buildLaborWorld + seedLaborOffer from labor_commands_test.go (same
// package).

// TestPay_WorkingLabor_EmployerPaysWorker_Blocked — the employer pays the worker
// by hand while the worker is mid-contract (Silence still serving ale). The
// Working branch steers and no coins move; the reward will settle on completion.
func TestPay_WorkingLabor_EmployerPaysWorker_Blocked(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 50},
	})
	defer stop()

	at := time.Now().UTC()
	accepted := at.Add(-30 * time.Minute)
	until := at.Add(90 * time.Minute)
	seedLaborOffer(t, w, sim.LaborOffer{
		ID: 1, WorkerID: "ezekiel", EmployerID: "josiah",
		Reward: 2, DurationMin: 120,
		State:    sim.LaborStateWorking,
		HuddleID: "h1", SceneID: "sc1",
		CreatedAt:    accepted,
		AcceptedAt:   &accepted,
		WorkingUntil: &until,
	})

	_, err := w.Send(sim.Pay("josiah", "Ezekiel", 4, "serving ale", at))
	if err == nil {
		t.Fatal("a bare pay to a worker mid-contract should be steered, not transferred")
	}
	if !strings.Contains(err.Error(), "working a job for you") {
		t.Errorf("expected the active-labor steer: %v", err)
	}
	snap := w.Published()
	if snap.Actors["josiah"].Coins != 50 || snap.Actors["ezekiel"].Coins != 0 {
		t.Errorf("coins moved on a steered pay: josiah=%d ezekiel=%d",
			snap.Actors["josiah"].Coins, snap.Actors["ezekiel"].Coins)
	}
}

// TestPay_PendingLabor_EmployerPaysWorker_SteersToAcceptWork — the worker has
// solicited and the employer reaches for pay instead of accept_work. The Pending
// branch names accept_work and no coins move.
func TestPay_PendingLabor_EmployerPaysWorker_SteersToAcceptWork(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 50},
	})
	defer stop()

	at := time.Now().UTC()
	seedLaborOffer(t, w, sim.LaborOffer{
		ID: 1, WorkerID: "ezekiel", EmployerID: "josiah",
		Reward: 2, DurationMin: 120,
		State:    sim.LaborStatePending,
		HuddleID: "h1", SceneID: "sc1",
		CreatedAt: at.Add(-time.Minute),
		ExpiresAt: at.Add(2 * time.Minute),
	})

	_, err := w.Send(sim.Pay("josiah", "Ezekiel", 4, "serving ale", at))
	if err == nil {
		t.Fatal("a bare pay against a pending labor offer should be steered to accept_work")
	}
	if !strings.Contains(err.Error(), "accept_work") {
		t.Errorf("expected the accept_work steer: %v", err)
	}
	snap := w.Published()
	if snap.Actors["josiah"].Coins != 50 || snap.Actors["ezekiel"].Coins != 0 {
		t.Errorf("coins moved on a steered pay: josiah=%d ezekiel=%d",
			snap.Actors["josiah"].Coins, snap.Actors["ezekiel"].Coins)
	}
}

// TestPay_WorkingLabor_WorkerPaysEmployer_Blocked — the either-direction guard:
// even the odd worker-pays-employer direction is steered (the default branch),
// proving the block doesn't depend on who pays whom. The worker has coins, so a
// plain pay WOULD otherwise succeed — the labor guard, not the balance check,
// is what stops it.
func TestPay_WorkingLabor_WorkerPaysEmployer_Blocked(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true, coins: 10},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 50},
	})
	defer stop()

	at := time.Now().UTC()
	accepted := at.Add(-30 * time.Minute)
	until := at.Add(90 * time.Minute)
	seedLaborOffer(t, w, sim.LaborOffer{
		ID: 1, WorkerID: "ezekiel", EmployerID: "josiah",
		Reward: 2, DurationMin: 120,
		State:    sim.LaborStateWorking,
		HuddleID: "h1", SceneID: "sc1",
		CreatedAt:    accepted,
		AcceptedAt:   &accepted,
		WorkingUntil: &until,
	})

	_, err := w.Send(sim.Pay("ezekiel", "Josiah", 1, "thanks", at))
	if err == nil {
		t.Fatal("a pay in the worker→employer direction should still be steered")
	}
	if !strings.Contains(err.Error(), "work arrangement") {
		t.Errorf("expected the either-direction work-arrangement steer: %v", err)
	}
	snap := w.Published()
	if snap.Actors["josiah"].Coins != 50 || snap.Actors["ezekiel"].Coins != 10 {
		t.Errorf("coins moved on a steered pay: josiah=%d ezekiel=%d",
			snap.Actors["josiah"].Coins, snap.Actors["ezekiel"].Coins)
	}
}

// TestPay_NoLaborOffer_PlainPayProceeds — the guard must not over-fire: with no
// labor offer between the two, a plain pay (a tip) transfers normally.
func TestPay_NoLaborOffer_PlainPayProceeds(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 50},
	})
	defer stop()

	at := time.Now().UTC()
	if _, err := w.Send(sim.Pay("josiah", "Ezekiel", 4, "thanks", at)); err != nil {
		t.Fatalf("a plain pay with no labor offer between the two should proceed: %v", err)
	}
	snap := w.Published()
	if snap.Actors["josiah"].Coins != 46 || snap.Actors["ezekiel"].Coins != 4 {
		t.Errorf("plain pay didn't transfer: josiah=%d ezekiel=%d",
			snap.Actors["josiah"].Coins, snap.Actors["ezekiel"].Coins)
	}
}

// TestPay_ExpiredPendingLabor_DoesNotBlock — a pending offer past its TTL is dead
// (awaiting the aging sweep) and must not block an unrelated pay between the two.
func TestPay_ExpiredPendingLabor_DoesNotBlock(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 50},
	})
	defer stop()

	at := time.Now().UTC()
	seedLaborOffer(t, w, sim.LaborOffer{
		ID: 1, WorkerID: "ezekiel", EmployerID: "josiah",
		Reward: 2, DurationMin: 120,
		State:    sim.LaborStatePending,
		HuddleID: "h1", SceneID: "sc1",
		CreatedAt: at.Add(-10 * time.Minute),
		ExpiresAt: at.Add(-time.Minute), // already expired
	})

	if _, err := w.Send(sim.Pay("josiah", "Ezekiel", 4, "thanks", at)); err != nil {
		t.Fatalf("an expired pending offer should not block a pay: %v", err)
	}
	snap := w.Published()
	if snap.Actors["josiah"].Coins != 46 || snap.Actors["ezekiel"].Coins != 4 {
		t.Errorf("pay didn't transfer past an expired offer: josiah=%d ezekiel=%d",
			snap.Actors["josiah"].Coins, snap.Actors["ezekiel"].Coins)
	}
}

// TestPay_MultipleLaborOffersBetweenPair_DeterministicSteer — when two live
// offers stand between the same pair (each is the other's worker in
// opposite-direction deals), activeLaborBetween must pick deterministically
// (Working before Pending, then lowest LaborID) so the steer message branch and
// named reward don't ride map-iteration order (code_review). The Working offer
// (Ezekiel working for Josiah) wins over the Pending one (Josiah's outgoing offer
// to Ezekiel) on every call, even though Go randomizes map range order per call.
func TestPay_MultipleLaborOffersBetweenPair_DeterministicSteer(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true, coins: 10},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", worker: true, coins: 50},
	})
	defer stop()

	at := time.Now().UTC()
	accepted := at.Add(-30 * time.Minute)
	until := at.Add(90 * time.Minute)
	// Offer 1 (Working): Ezekiel works for Josiah.
	seedLaborOffer(t, w, sim.LaborOffer{
		ID: 1, WorkerID: "ezekiel", EmployerID: "josiah",
		Reward: 2, DurationMin: 120, State: sim.LaborStateWorking,
		HuddleID: "h1", SceneID: "sc1",
		CreatedAt: accepted, AcceptedAt: &accepted, WorkingUntil: &until,
	})
	// Offer 2 (Pending): Josiah has offered to work for Ezekiel.
	seedLaborOffer(t, w, sim.LaborOffer{
		ID: 2, WorkerID: "josiah", EmployerID: "ezekiel",
		Reward: 3, DurationMin: 60, State: sim.LaborStatePending,
		HuddleID: "h1", SceneID: "sc1",
		CreatedAt: at.Add(-time.Minute), ExpiresAt: at.Add(2 * time.Minute),
	})

	// The Working offer must win on every call (a steered pay mutates nothing, so
	// the offers persist; only the iteration order varies).
	for i := 0; i < 5; i++ {
		_, err := w.Send(sim.Pay("josiah", "Ezekiel", 4, "serving ale", at))
		if err == nil {
			t.Fatal("a pay across a live labor offer should be steered")
		}
		if !strings.Contains(err.Error(), "working a job for you") {
			t.Fatalf("iteration %d: expected the Working-branch steer deterministically, got: %v", i, err)
		}
	}
}
