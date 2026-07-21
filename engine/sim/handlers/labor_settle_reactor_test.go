package handlers_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/handlers"
)

// labor_settle_reactor_test.go — LLM-498 coverage of handleLaborSettledWarrants
// (wired by RegisterLaborHandlers). Driven through the REAL completion sweep
// (sim.EvaluateLaborLedgerSweep over a seeded Working offer past its window),
// so LaborResolved emits through the production settle path — the same posture
// as labor_reactor_test.go's solicit-driven offer coverage. Reuses
// buildReactorWorld / readWarrants / firstByKind / countByKind from the
// sibling reactor test files (same handlers_test package).

// seedAndSweepWorkingOffer registers the labor subscribers registerTimes times,
// seeds one Working labor offer already past its window (worker mirror set, the
// state AcceptWork would have left), then runs one completion sweep — settling
// the offer and emitting LaborResolved inline.
func seedAndSweepWorkingOffer(t *testing.T, w *sim.World, workerID, employerID sim.ActorID, reward int, registerTimes int) {
	t.Helper()
	now := time.Now().UTC()
	started := now.Add(-30 * time.Minute)
	until := now.Add(-time.Minute)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		for i := 0; i < registerTimes; i++ {
			handlers.RegisterLaborHandlers(world)
		}
		world.LaborLedger[1] = &sim.LaborOffer{
			ID:            1,
			WorkerID:      workerID,
			EmployerID:    employerID,
			Reward:        reward,
			DurationMin:   30,
			State:         sim.LaborStateWorking,
			HuddleID:      "h1",
			AcceptedAt:    &started,
			WorkStartedAt: &started,
			WorkingUntil:  &until,
		}
		worker := world.Actors[workerID]
		worker.State = sim.StateLaboring
		worker.LaborID = 1
		worker.LaboringUntil = &until
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed working offer: %v", err)
	}
	if _, err := w.Send(sim.EvaluateLaborLedgerSweep(now)); err != nil {
		t.Fatalf("EvaluateLaborLedgerSweep: %v", err)
	}
}

// TestSubscriber_LaborSettled_StampsBothParties is the core LLM-498 assertion:
// a mid-shift settle wakes BOTH parties with their own side's pre-rendered
// narration, so the worker doesn't role-play being owed and the employer
// doesn't pay a second time.
func TestSubscriber_LaborSettled_StampsBothParties(t *testing.T) {
	w, stop := buildReactorWorld(t, []reactorActor{
		{id: "anne", displayName: "Anne", kind: sim.KindNPCShared, huddleID: "h1", coins: 0},
		{id: "prudence", displayName: "Prudence", kind: sim.KindNPCStateful, huddleID: "h1", coins: 50},
	})
	defer stop()
	seedAndSweepWorkingOffer(t, w, "anne", "prudence", 5, 1)

	workerWarrants := readWarrants(t, w, "anne")
	m, ok := firstByKind(workerWarrants, sim.WarrantKindLaborSettled)
	if !ok {
		t.Fatalf("worker carries no labor-settled warrant; got %d warrants: %+v", len(workerWarrants), workerWarrants)
	}
	reason, ok := m.Reason.(sim.LaborSettledWarrantReason)
	if !ok {
		t.Fatalf("worker warrant reason is %T, want LaborSettledWarrantReason", m.Reason)
	}
	if reason.LaborID != 1 || reason.Counterparty != "prudence" {
		t.Errorf("worker reason = %+v, want labor 1 / counterparty prudence", reason)
	}
	if want := "Your work for Prudence is done — you've been paid 5 coins, as agreed."; reason.NarrationText != want {
		t.Errorf("worker narration = %q, want %q", reason.NarrationText, want)
	}
	if m.TriggerActorID != "prudence" {
		t.Errorf("worker warrant TriggerActorID = %q, want prudence", m.TriggerActorID)
	}

	employerWarrants := readWarrants(t, w, "prudence")
	m, ok = firstByKind(employerWarrants, sim.WarrantKindLaborSettled)
	if !ok {
		t.Fatalf("employer carries no labor-settled warrant; got %d warrants: %+v", len(employerWarrants), employerWarrants)
	}
	reason, ok = m.Reason.(sim.LaborSettledWarrantReason)
	if !ok {
		t.Fatalf("employer warrant reason is %T, want LaborSettledWarrantReason", m.Reason)
	}
	if reason.LaborID != 1 || reason.Counterparty != "anne" {
		t.Errorf("employer reason = %+v, want labor 1 / counterparty anne", reason)
	}
	if want := "Anne's work for you is done — you've paid 5 coins for it, as agreed."; reason.NarrationText != want {
		t.Errorf("employer narration = %q, want %q", reason.NarrationText, want)
	}
}

// TestSubscriber_LaborSettled_UnpaidCompletionStampsNothing — the stiffed path
// (the employer can no longer cover the reward at settle: FailedUnavailable
// with WorkPerformed=true) must NOT get the paid beat: "you've been paid, as
// agreed" over an unpaid settle would be a lie, and that path already narrates
// through the LLM-165 aggrieved relationship facts.
func TestSubscriber_LaborSettled_UnpaidCompletionStampsNothing(t *testing.T) {
	w, stop := buildReactorWorld(t, []reactorActor{
		{id: "anne", displayName: "Anne", kind: sim.KindNPCShared, huddleID: "h1", coins: 0},
		{id: "prudence", displayName: "Prudence", kind: sim.KindNPCStateful, huddleID: "h1", coins: 0},
	})
	defer stop()
	seedAndSweepWorkingOffer(t, w, "anne", "prudence", 5, 1)

	for _, id := range []sim.ActorID{"anne", "prudence"} {
		if got := countByKind(readWarrants(t, w, id), sim.WarrantKindLaborSettled); got != 0 {
			t.Errorf("%s: labor-settled warrants = %d, want 0 on an unpaid completion", id, got)
		}
	}
}

// TestSubscriber_LaborSettled_SkipsPCParty — a PC employer deliberates through
// the UI, not the reactor, so no warrant lands on it; the NPC worker's own
// settle beat still stamps.
func TestSubscriber_LaborSettled_SkipsPCParty(t *testing.T) {
	w, stop := buildReactorWorld(t, []reactorActor{
		{id: "anne", displayName: "Anne", kind: sim.KindNPCShared, huddleID: "h1", coins: 0},
		{id: "patron", displayName: "Patron", kind: sim.KindPC, huddleID: "h1", coins: 50},
	})
	defer stop()
	seedAndSweepWorkingOffer(t, w, "anne", "patron", 5, 1)

	if got := countByKind(readWarrants(t, w, "patron"), sim.WarrantKindLaborSettled); got != 0 {
		t.Errorf("PC employer labor-settled warrants = %d, want 0", got)
	}
	if _, ok := firstByKind(readWarrants(t, w, "anne"), sim.WarrantKindLaborSettled); !ok {
		t.Error("NPC worker should still carry its settle warrant when the employer is a PC")
	}
}

// TestSubscriber_LaborSettled_DedupesDoubleRegister — the settle beat fires
// once per party per job: a double registration invokes the subscriber twice,
// but the (WarrantKindLaborSettled, LaborID) dedup key collapses the stamps.
func TestSubscriber_LaborSettled_DedupesDoubleRegister(t *testing.T) {
	w, stop := buildReactorWorld(t, []reactorActor{
		{id: "anne", displayName: "Anne", kind: sim.KindNPCShared, huddleID: "h1", coins: 0},
		{id: "prudence", displayName: "Prudence", kind: sim.KindNPCStateful, huddleID: "h1", coins: 50},
	})
	defer stop()
	seedAndSweepWorkingOffer(t, w, "anne", "prudence", 5, 2)

	for _, id := range []sim.ActorID{"anne", "prudence"} {
		if got := countByKind(readWarrants(t, w, id), sim.WarrantKindLaborSettled); got != 1 {
			t.Errorf("%s: labor-settled warrants = %d, want 1 (double-register must dedupe)", id, got)
		}
	}
}

// exercise the narration fallbacks so a missing counterparty never renders a
// dangling possessive ("'s work for you is done").
func TestLaborSettledNarrationFallbacks(t *testing.T) {
	if got := sim.LaborSettledWorkerNarration("", 5, nil); !strings.Contains(got, "your employer") {
		t.Errorf("worker narration with empty name = %q, want the 'your employer' fallback", got)
	}
	if got := sim.LaborSettledEmployerNarration("", 5, nil); !strings.HasPrefix(got, "Your hired worker's work") {
		t.Errorf("employer narration with empty name = %q, want the 'Your hired worker' fallback", got)
	}
}
