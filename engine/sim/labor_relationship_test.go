package sim_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// labor_relationship_test.go — LLM-165 coverage of the bidirectional
// RecordInteraction relationship facts written at the labor terminals (the
// labor analogue of pay's Paid/Declined writes). Asserts the four-way matrix:
// Completed writes Worked/Hired, the completion-time unpaid FailedUnavailable
// writes WorkedUnpaid/LeftWorkerUnpaid, Declined writes WorkDeclinedBy/
// DeclinedWork, and the no-work terminals (Expired, accept-time
// FailedUnavailable) write nothing — proving the LaborResolved.WorkPerformed
// disambiguation of the overloaded FailedUnavailable terminal. Reuses the
// buildLaborWorld / seedLaborOffer / captureLaborEvents fixtures from
// labor_commands_test.go (same sim_test package).

// readLaborFacts snapshots actorID's full relationship facts toward otherID on
// the world goroutine, copying out so the caller never races the live slice.
// (give_commands_test.go's readSalientFacts returns only the texts; the labor
// matrix asserts on Kind too.)
func readLaborFacts(t *testing.T, w *sim.World, actorID, otherID sim.ActorID) []sim.SalientFact {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a, ok := world.Actors[actorID]
		if !ok || a == nil || a.Relationships == nil {
			return []sim.SalientFact(nil), nil
		}
		rel, ok := a.Relationships[otherID]
		if !ok || rel == nil {
			return []sim.SalientFact(nil), nil
		}
		out := make([]sim.SalientFact, len(rel.SalientFacts))
		copy(out, rel.SalientFacts)
		return out, nil
	}})
	if err != nil {
		t.Fatalf("readSalientFacts %q→%q: %v", actorID, otherID, err)
	}
	return res.([]sim.SalientFact)
}

// requireFact asserts exactly one relationship fact exists actorID→otherID, that
// it carries wantKind, and that its text contains every substring in wantText.
func requireFact(t *testing.T, w *sim.World, actorID, otherID sim.ActorID, wantKind sim.InteractionKind, wantText ...string) {
	t.Helper()
	facts := readLaborFacts(t, w, actorID, otherID)
	if len(facts) != 1 {
		t.Fatalf("%q→%q facts = %d (%+v), want exactly 1", actorID, otherID, len(facts), facts)
	}
	f := facts[0]
	if f.Kind != wantKind {
		t.Errorf("%q→%q fact kind = %q, want %q", actorID, otherID, f.Kind, wantKind)
	}
	for _, sub := range wantText {
		if !strings.Contains(f.Text, sub) {
			t.Errorf("%q→%q fact text = %q, want substring %q", actorID, otherID, f.Text, sub)
		}
	}
}

// requireNoFacts asserts neither direction accumulated any relationship fact.
func requireNoFacts(t *testing.T, w *sim.World, a, b sim.ActorID) {
	t.Helper()
	if facts := readLaborFacts(t, w, a, b); len(facts) != 0 {
		t.Errorf("%q→%q facts = %+v, want none", a, b, facts)
	}
	if facts := readLaborFacts(t, w, b, a); len(facts) != 0 {
		t.Errorf("%q→%q facts = %+v, want none", b, a, facts)
	}
}

// seedWorkingOffer seeds an accepted-and-working offer whose window has already
// elapsed, plus the worker's StateLaboring mirror — the shape AcceptWork would
// have produced, ready for the completion sweep to settle.
func seedWorkingOffer(t *testing.T, w *sim.World, worker, employer sim.ActorID, reward, durationMin int, now time.Time) {
	t.Helper()
	accepted := now.Add(-time.Duration(durationMin+1) * time.Minute)
	until := now.Add(-time.Minute) // window elapsed
	seedLaborOffer(t, w, sim.LaborOffer{
		ID: 1, WorkerID: worker, EmployerID: employer,
		Reward: reward, DurationMin: durationMin, State: sim.LaborStateWorking,
		HuddleID: "h1", SceneID: "sc1",
		AcceptedAt: &accepted, WorkingUntil: &until,
	})
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors[worker]
		u := until
		a.LaborID = 1
		a.LaboringUntil = &u
		a.State = sim.StateLaboring
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed worker mirror: %v", err)
	}
}

// A completed, paid job writes Worked (worker side) + Hired (employer side),
// both carrying the reward + humanized duration, and the LaborResolved event
// carries WorkPerformed=true.
func TestLaborCompleted_WritesRelationshipFacts(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 50},
	})
	defer stop()
	events := captureLaborEvents(t, w)
	now := time.Now().UTC()
	seedWorkingOffer(t, w, "ezekiel", "josiah", 10, 30, now)

	if _, err := w.Send(sim.EvaluateLaborLedgerSweep(now)); err != nil {
		t.Fatalf("EvaluateLaborLedgerSweep: %v", err)
	}
	if got := readLaborLedger(t, w)[1].State; got != sim.LaborStateCompleted {
		t.Fatalf("offer State = %q, want completed", got)
	}
	if len(events.Resolved) != 1 || !events.Resolved[0].WorkPerformed {
		t.Errorf("LaborResolved WorkPerformed = %+v, want one event with WorkPerformed=true", events.Resolved)
	}

	// Worker remembers earning; employer remembers paying. Asymmetric POV.
	requireFact(t, w, "ezekiel", "josiah", sim.InteractionWorked, "I worked for Josiah", "10 coins", "30 minutes")
	requireFact(t, w, "josiah", "ezekiel", sim.InteractionHired, "Ezekiel worked for me", "paid them 10 coins", "30 minutes")
}

// A finished job the employer can no longer pay for (broke at completion) is the
// completion-time FailedUnavailable: no coins move, but the work WAS performed,
// so it writes the aggrieved WorkedUnpaid / LeftWorkerUnpaid facts and the event
// carries WorkPerformed=true.
func TestLaborCompletionUnpaid_WritesStiffedFacts(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 5}, // can't cover the 10 reward
	})
	defer stop()
	events := captureLaborEvents(t, w)
	now := time.Now().UTC()
	seedWorkingOffer(t, w, "ezekiel", "josiah", 10, 120, now)

	if _, err := w.Send(sim.EvaluateLaborLedgerSweep(now)); err != nil {
		t.Fatalf("EvaluateLaborLedgerSweep: %v", err)
	}
	if got := readLaborLedger(t, w)[1].State; got != sim.LaborStateFailedUnavailable {
		t.Fatalf("offer State = %q, want failed_unavailable", got)
	}
	// No coins move on an unpaid completion.
	if got := readActor(t, w, "josiah").Coins; got != 5 {
		t.Errorf("employer coins = %d, want 5 (unpaid — no transfer)", got)
	}
	if got := readActor(t, w, "ezekiel").Coins; got != 0 {
		t.Errorf("worker coins = %d, want 0 (unpaid — no transfer)", got)
	}
	if len(events.Resolved) != 1 || !events.Resolved[0].WorkPerformed {
		t.Errorf("LaborResolved WorkPerformed = %+v, want one event with WorkPerformed=true", events.Resolved)
	}

	requireFact(t, w, "ezekiel", "josiah", sim.InteractionWorkedUnpaid, "I worked for Josiah", "2 hours", "never paid", "10 coins")
	requireFact(t, w, "josiah", "ezekiel", sim.InteractionLeftWorkerUnpaid, "Ezekiel worked for me", "2 hours", "could not pay", "10 coins")
}

// A declined offer writes WorkDeclinedBy / DeclinedWork (no duration — the work
// never started) and the event carries WorkPerformed=false.
func TestLaborDeclined_WritesRelationshipFacts(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 50},
	})
	defer stop()
	events := captureLaborEvents(t, w)
	now := time.Now().UTC()
	seedLaborOffer(t, w, sim.LaborOffer{
		ID: 1, WorkerID: "ezekiel", EmployerID: "josiah",
		Reward: 10, DurationMin: 30, State: sim.LaborStatePending,
		HuddleID: "h1", SceneID: "sc1", ExpiresAt: now.Add(2 * time.Minute),
	})

	if _, err := w.Send(sim.DeclineWork("josiah", 1, now)); err != nil {
		t.Fatalf("DeclineWork: %v", err)
	}
	if len(events.Resolved) != 1 || events.Resolved[0].WorkPerformed {
		t.Errorf("LaborResolved = %+v, want one event with WorkPerformed=false", events.Resolved)
	}

	requireFact(t, w, "ezekiel", "josiah", sim.InteractionWorkDeclinedBy, "Josiah declined my offer to work", "10 coins")
	requireFact(t, w, "josiah", "ezekiel", sim.InteractionDeclinedWork, "I declined Ezekiel's offer to work", "10 coins")
}

// An expired pending offer (the employer never answered) is a non-event — no
// work, no social move — so it writes no relationship facts on either side.
func TestLaborExpired_WritesNoRelationshipFacts(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 50},
	})
	defer stop()
	events := captureLaborEvents(t, w)
	now := time.Now().UTC()
	seedLaborOffer(t, w, sim.LaborOffer{
		ID: 1, WorkerID: "ezekiel", EmployerID: "josiah",
		Reward: 10, DurationMin: 30, State: sim.LaborStatePending,
		HuddleID: "h1", SceneID: "sc1", ExpiresAt: now.Add(-time.Minute),
	})

	if _, err := w.Send(sim.EvaluateLaborLedgerSweep(now)); err != nil {
		t.Fatalf("EvaluateLaborLedgerSweep: %v", err)
	}
	if got := readLaborLedger(t, w)[1].State; got != sim.LaborStateExpired {
		t.Fatalf("offer State = %q, want expired", got)
	}
	if len(events.Resolved) != 1 || events.Resolved[0].WorkPerformed {
		t.Errorf("LaborResolved = %+v, want one event with WorkPerformed=false", events.Resolved)
	}
	requireNoFacts(t, w, "ezekiel", "josiah")
}

// An accept-time FailedUnavailable (here gate 8 — the employer is visibly broke
// at accept) ends the deal BEFORE any work happens. It shares the
// failed_unavailable terminal with the unpaid-completion case but carries
// WorkPerformed=false, so it writes NO facts — the WorkPerformed disambiguation
// in action.
func TestLaborAcceptTimeFailed_WritesNoRelationshipFacts(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 0}, // broke — gate 8 flips failed_unavailable at accept
	})
	defer stop()
	events := captureLaborEvents(t, w)
	now := time.Now().UTC()
	seedLaborOffer(t, w, sim.LaborOffer{
		ID: 1, WorkerID: "ezekiel", EmployerID: "josiah",
		Reward: 10, DurationMin: 30, State: sim.LaborStatePending,
		HuddleID: "h1", SceneID: "sc1", ExpiresAt: now.Add(2 * time.Minute),
	})

	if _, err := w.Send(sim.AcceptWork("josiah", 1, now)); err != nil {
		t.Fatalf("AcceptWork: %v", err)
	}
	if got := readLaborLedger(t, w)[1].State; got != sim.LaborStateFailedUnavailable {
		t.Fatalf("offer State = %q, want failed_unavailable", got)
	}
	if len(events.Resolved) != 1 || events.Resolved[0].WorkPerformed {
		t.Errorf("LaborResolved = %+v, want one event with WorkPerformed=false", events.Resolved)
	}
	requireNoFacts(t, w, "ezekiel", "josiah")
}
