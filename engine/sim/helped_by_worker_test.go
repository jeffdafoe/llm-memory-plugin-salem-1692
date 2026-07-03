package sim

import (
	"testing"
	"time"
)

// helped_by_worker_test.go — LLM-228 capture subscriber. White-box (package sim)
// so it can drive handleHelpedByWorkerOnResolved directly and reuse the
// cbWorld/cbAgent fixtures + the declined() LaborResolved builder from the
// closed_business / declined_work tests. The subscriber records the EMPLOYER's
// memory of a worker who completed a paid job, keyed by the worker's PeerID, only
// on the Completed terminal.

func helpedKey(worker ActorID) ObservedStateKey {
	return ObservedStateKey{PeerID: worker, Condition: ObservedHelpedByWorker}
}

func TestHelpedByWorker_RecordsOnCompletedKeyedByWorker(t *testing.T) {
	w := cbWorld()
	now := time.Now()
	worker := cbAgent("anne", "", "")           // the worker who did the job
	employer := cbAgent("hannah", "inn", "inn") // the keeper who hired her
	w.Actors["anne"] = worker
	w.Actors["hannah"] = employer

	handleHelpedByWorkerOnResolved(w, declined("anne", "hannah", LaborTerminalStateCompleted, now))

	at, ok := employer.Observed.At(helpedKey("anne"))
	if !ok {
		t.Fatalf("a completed paid job must record the employer's memory of the worker, got %v", employer.Observed)
	}
	if !at.Equal(now) {
		t.Errorf("memory stamped at %v, want the event time %v", at, now)
	}
	// The memory lives on the EMPLOYER, not the worker (the mirror of ObservedDeclinedWork).
	if worker.Observed.Len() != 0 {
		t.Errorf("the worker accrues no helped-by memory, got %v", worker.Observed)
	}
}

func TestHelpedByWorker_NoRecordOnDeclinedOrExpired(t *testing.T) {
	for _, term := range []LaborTerminalState{LaborTerminalStateDeclined, LaborTerminalStateExpired} {
		w := cbWorld()
		now := time.Now()
		w.Actors["anne"] = cbAgent("anne", "", "")
		employer := cbAgent("hannah", "inn", "inn")
		w.Actors["hannah"] = employer

		handleHelpedByWorkerOnResolved(w, declined("anne", "hannah", term, now))

		if employer.Observed.Len() != 0 {
			t.Errorf("terminal %q performed no paid work — no helped-by memory, got %v", term, employer.Observed)
		}
	}
}

func TestHelpedByWorker_NoRecordOnUnpaidCompletion(t *testing.T) {
	w := cbWorld()
	now := time.Now()
	w.Actors["anne"] = cbAgent("anne", "", "")
	employer := cbAgent("hannah", "inn", "inn")
	w.Actors["hannah"] = employer

	// The worker finished the job but the employer went broke during the window,
	// so the completion sweep settled it FailedUnavailable — WORK WAS PERFORMED but
	// nobody was paid. "You got more done" must not read over a stiffed settle; the
	// InteractionLeftWorkerUnpaid relationship fact carries that aggrieved beat.
	stiffed := &LaborResolved{
		WorkerID:      "anne",
		EmployerID:    "hannah",
		TerminalState: LaborTerminalStateFailedUnavailable,
		WorkPerformed: true,
		At:            now,
	}
	handleHelpedByWorkerOnResolved(w, stiffed)

	if employer.Observed.Len() != 0 {
		t.Fatalf("an unpaid completion must not read as help that got more done, got %v", employer.Observed)
	}
}

func TestHelpedByWorker_NonAgentEmployerIgnored(t *testing.T) {
	w := cbWorld()
	now := time.Now()
	w.Actors["anne"] = cbAgent("anne", "", "")
	pc := &Actor{ID: "pc", Kind: KindPC}
	w.Actors["pc"] = pc

	// A PC employer carries continuity through the player, not the Observed store.
	handleHelpedByWorkerOnResolved(w, declined("anne", "pc", LaborTerminalStateCompleted, now))

	if pc.Observed.Len() != 0 {
		t.Fatalf("a PC employer accrues no helped-by memory, got %v", pc.Observed)
	}
}

func TestHelpedByWorker_MissingEmployerIgnored(t *testing.T) {
	w := cbWorld()
	now := time.Now()
	worker := cbAgent("anne", "", "")
	w.Actors["anne"] = worker
	// Employer not in the world (already despawned) → no panic, no record.

	handleHelpedByWorkerOnResolved(w, declined("anne", "hannah", LaborTerminalStateCompleted, now))

	if worker.Observed.Len() != 0 {
		t.Fatalf("a missing employer must be a no-op, got %v", worker.Observed)
	}
}
