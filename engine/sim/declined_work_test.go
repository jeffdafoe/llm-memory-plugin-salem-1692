package sim

import (
	"testing"
	"time"
)

// declined_work_test.go — LLM-198 capture subscriber. White-box (package sim) so
// it can drive handleDeclinedWorkOnResolved directly and reach the cbWorld/cbAgent
// fixtures from closed_business_test.go. The subscriber records the soliciting
// worker's "this employer declined me" memory, keyed by the employer's workplace,
// only on the Declined terminal.

func declined(worker, employer ActorID, terminal LaborTerminalState, at time.Time) *LaborResolved {
	return &LaborResolved{WorkerID: worker, EmployerID: employer, TerminalState: terminal, At: at}
}

func TestDeclinedWork_RecordsKeyedByEmployerWorkplace(t *testing.T) {
	w := cbWorld()
	now := time.Now()
	w.Structures["blacksmith"] = &Structure{ID: "blacksmith", DisplayName: "Blacksmith"}
	worker := cbAgent("silence", "", "")                       // workless NPC worker
	employer := cbAgent("ezekiel", "blacksmith", "blacksmith") // keeps the Blacksmith
	w.Actors["silence"] = worker
	w.Actors["ezekiel"] = employer

	handleDeclinedWorkOnResolved(w, declined("silence", "ezekiel", LaborTerminalStateDeclined, now))

	at, ok := worker.Observed.At(ObservedStateKey{StructureID: "blacksmith", Condition: ObservedDeclinedWork})
	if !ok {
		t.Fatalf("a decline must record the worker's avoidance of the employer's workplace, got %v", worker.Observed)
	}
	if !at.Equal(now) {
		t.Errorf("memory stamped at %v, want the event time %v", at, now)
	}
}

func TestDeclinedWork_NoRecordOnNonDeclineTerminal(t *testing.T) {
	w := cbWorld()
	now := time.Now()
	w.Structures["blacksmith"] = &Structure{ID: "blacksmith", DisplayName: "Blacksmith"}
	worker := cbAgent("silence", "", "")
	w.Actors["silence"] = worker
	w.Actors["ezekiel"] = cbAgent("ezekiel", "blacksmith", "blacksmith")

	// A completed job is not a refusal — no avoidance memory.
	handleDeclinedWorkOnResolved(w, declined("silence", "ezekiel", LaborTerminalStateCompleted, now))

	if worker.Observed.Len() != 0 {
		t.Fatalf("only a Declined terminal records avoidance, got %v", worker.Observed)
	}
}

func TestDeclinedWork_EmployerWithoutWorkplaceIgnored(t *testing.T) {
	w := cbWorld()
	now := time.Now()
	worker := cbAgent("silence", "", "")
	// Employer keeps no business (empty WorkStructureID) — e.g. a villager declining
	// a household-chore solicit. Nothing in the seek-work directory to suppress.
	w.Actors["silence"] = worker
	w.Actors["goody"] = cbAgent("goody", "", "")

	handleDeclinedWorkOnResolved(w, declined("silence", "goody", LaborTerminalStateDeclined, now))

	if worker.Observed.Len() != 0 {
		t.Fatalf("an employer with no workplace has no directory entry to drop, got %v", worker.Observed)
	}
}

func TestDeclinedWork_NonAgentWorkerIgnored(t *testing.T) {
	w := cbWorld()
	now := time.Now()
	w.Structures["blacksmith"] = &Structure{ID: "blacksmith", DisplayName: "Blacksmith"}
	pc := &Actor{ID: "pc", Kind: KindPC}
	w.Actors["pc"] = pc
	w.Actors["ezekiel"] = cbAgent("ezekiel", "blacksmith", "blacksmith")

	// A player doesn't perceive the seek-work directory, so it accrues no memory.
	handleDeclinedWorkOnResolved(w, declined("pc", "ezekiel", LaborTerminalStateDeclined, now))

	if pc.Observed.Len() != 0 {
		t.Fatalf("PCs don't accrue seek-work avoidance memory, got %v", pc.Observed)
	}
}

func TestDeclinedWork_MissingPartyIgnored(t *testing.T) {
	w := cbWorld()
	now := time.Now()
	w.Structures["blacksmith"] = &Structure{ID: "blacksmith", DisplayName: "Blacksmith"}
	worker := cbAgent("silence", "", "")
	w.Actors["silence"] = worker
	// Employer not in the world (already despawned) → no panic, no record.

	handleDeclinedWorkOnResolved(w, declined("silence", "ezekiel", LaborTerminalStateDeclined, now))

	if worker.Observed.Len() != 0 {
		t.Fatalf("a missing employer must be a no-op, got %v", worker.Observed)
	}
}
