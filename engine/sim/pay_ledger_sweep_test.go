package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pay_ledger_sweep_test.go — Phase 3 PR S4 step 8 coverage of
// EvaluatePayLedgerSweep. The AfterFunc self-rearm chain mirrors
// scene_quote_sweep.go and the substrate-level reactor evaluator; the
// substrate test exercises EvaluatePayLedgerSweep directly to keep
// timing deterministic. Test fixtures (buildPayWithItemWorld /
// seedLedgerEntry / installSinkRecorder / capturePayWithItemEvents /
// readPayLedger) come from pay_with_item_commands_test.go.

// TestEvaluatePayLedgerSweep_ExpiresPastTTL — a pending entry whose
// ExpiresAt has passed flips to Expired with ResolvedAt stamped and
// emits PayWithItemResolved{Expired}; the sink receives a Project
// call for the terminal state.
func TestEvaluatePayLedgerSweep_ExpiresPastTTL(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()
	events := capturePayWithItemEvents(t, w)
	sink := installSinkRecorder(t, w)

	now := time.Now().UTC()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 4,
		State:     sim.PayLedgerStatePending,
		CreatedAt: now.Add(-5 * time.Minute),
		ExpiresAt: now.Add(-time.Minute),
		SceneID:   "sc1", HuddleID: "h1",
	})
	if _, err := w.Send(sim.EvaluatePayLedgerSweep(now)); err != nil {
		t.Fatalf("EvaluatePayLedgerSweep: %v", err)
	}

	ledger := readPayLedger(t, w)
	entry, ok := ledger[1]
	if !ok {
		t.Fatalf("entry 1 missing post-sweep")
	}
	if entry.State != sim.PayLedgerStateExpired {
		t.Errorf("State = %q, want expired", entry.State)
	}
	if !entry.ResolvedAt.Equal(now) {
		t.Errorf("ResolvedAt = %v, want %v", entry.ResolvedAt, now)
	}

	if len(events.Resolved) != 1 || events.Resolved[0].TerminalState != sim.PayTerminalStateExpired {
		t.Errorf("PayWithItemResolved = %+v", events.Resolved)
	}
	if len(sink.calls) != 1 || sink.calls[0].State != sim.PayLedgerStateExpired {
		t.Errorf("sink calls = %+v", sink.calls)
	}
}

// TestEvaluatePayLedgerSweep_SkipsActive — a pending entry still
// within TTL stays pending and emits nothing.
func TestEvaluatePayLedgerSweep_SkipsActive(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()
	events := capturePayWithItemEvents(t, w)

	now := time.Now().UTC()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 4,
		State:     sim.PayLedgerStatePending,
		ExpiresAt: now.Add(2 * time.Minute),
		SceneID:   "sc1", HuddleID: "h1",
	})
	if _, err := w.Send(sim.EvaluatePayLedgerSweep(now)); err != nil {
		t.Fatalf("EvaluatePayLedgerSweep: %v", err)
	}
	ledger := readPayLedger(t, w)
	if got := ledger[1].State; got != sim.PayLedgerStatePending {
		t.Errorf("State = %q, want pending (within TTL)", got)
	}
	if len(events.Resolved) != 0 {
		t.Errorf("emitted Resolved on within-TTL entry: %v", events.Resolved)
	}
}

// TestEvaluatePayLedgerSweep_SkipsNonPending — terminal entries
// (accepted, declined, countered, withdrawn, already-expired,
// failed_*) are inert under the sweep. The sweep walks but doesn't
// touch them.
func TestEvaluatePayLedgerSweep_SkipsNonPending(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()
	events := capturePayWithItemEvents(t, w)

	now := time.Now().UTC()
	terminalStates := []sim.PayLedgerState{
		sim.PayLedgerStateAccepted,
		sim.PayLedgerStateDeclined,
		sim.PayLedgerStateCountered,
		sim.PayLedgerStateWithdrawnByBuyer,
		sim.PayLedgerStateExpired,
		sim.PayLedgerStateFailedInsufficientFunds,
		sim.PayLedgerStateFailedInsufficientStock,
		sim.PayLedgerStateFailedUnavailable,
	}
	for i, st := range terminalStates {
		seedLedgerEntry(t, w, sim.PayLedgerEntry{
			ID:      sim.LedgerID(i + 1),
			BuyerID: "alice", SellerID: "bob",
			ItemKind: "stew", Qty: 1, Amount: 4,
			State:      st,
			ExpiresAt:  now.Add(-time.Hour), // would expire if pending
			ResolvedAt: now.Add(-30 * time.Minute),
		})
	}
	if _, err := w.Send(sim.EvaluatePayLedgerSweep(now)); err != nil {
		t.Fatalf("EvaluatePayLedgerSweep: %v", err)
	}
	if len(events.Resolved) != 0 {
		t.Errorf("emitted Resolved on terminal entries: %+v", events.Resolved)
	}
	ledger := readPayLedger(t, w)
	for i, st := range terminalStates {
		id := sim.LedgerID(i + 1)
		if got := ledger[id].State; got != st {
			t.Errorf("ledger[%d].State = %q, want %q (sweep must not touch terminals)", id, got, st)
		}
	}
}

// TestEvaluatePayLedgerSweep_MultipleInOneSweep — when several pending
// entries are past TTL, all flip in one sweep call, and events emit in
// LedgerID-sorted order (replay determinism).
func TestEvaluatePayLedgerSweep_MultipleInOneSweep(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()
	events := capturePayWithItemEvents(t, w)

	now := time.Now().UTC()
	// Seed three pending entries with mixed-order IDs.
	for _, id := range []sim.LedgerID{5, 1, 3} {
		seedLedgerEntry(t, w, sim.PayLedgerEntry{
			ID: id, BuyerID: "alice", SellerID: "bob",
			ItemKind: "stew", Qty: 1, Amount: 4,
			State:     sim.PayLedgerStatePending,
			ExpiresAt: now.Add(-time.Minute),
			SceneID:   "sc1", HuddleID: "h1",
		})
	}
	if _, err := w.Send(sim.EvaluatePayLedgerSweep(now)); err != nil {
		t.Fatalf("EvaluatePayLedgerSweep: %v", err)
	}
	if len(events.Resolved) != 3 {
		t.Fatalf("PayWithItemResolved count = %d, want 3", len(events.Resolved))
	}
	// Events emit in sorted LedgerID order: 1, 3, 5.
	wantIDs := []sim.LedgerID{1, 3, 5}
	for i, want := range wantIDs {
		if got := events.Resolved[i].LedgerID; got != want {
			t.Errorf("Resolved[%d].LedgerID = %d, want %d (sorted)", i, got, want)
		}
		if events.Resolved[i].TerminalState != sim.PayTerminalStateExpired {
			t.Errorf("Resolved[%d].TerminalState = %q, want expired", i, events.Resolved[i].TerminalState)
		}
	}
}

// TestEvaluatePayLedgerSweep_EmptyLedger — sweeping with no PayLedger
// entries is a no-op.
func TestEvaluatePayLedgerSweep_EmptyLedger(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1"},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()
	events := capturePayWithItemEvents(t, w)
	if _, err := w.Send(sim.EvaluatePayLedgerSweep(time.Now().UTC())); err != nil {
		t.Fatalf("EvaluatePayLedgerSweep: %v", err)
	}
	if len(events.Resolved) != 0 {
		t.Errorf("emitted events on empty ledger: %+v", events.Resolved)
	}
}

// TestEvaluatePayLedgerSweep_ZeroExpiresAtSkipped — defensive: an
// entry whose ExpiresAt is zero (the unset sentinel) is left alone
// even when pending. The substrate contract is that pending entries
// always carry a non-zero ExpiresAt, but the sweep is conservative.
func TestEvaluatePayLedgerSweep_ZeroExpiresAtSkipped(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1"},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		State: sim.PayLedgerStatePending,
		// ExpiresAt zero
	})
	if _, err := w.Send(sim.EvaluatePayLedgerSweep(time.Now().UTC())); err != nil {
		t.Fatalf("EvaluatePayLedgerSweep: %v", err)
	}
	ledger := readPayLedger(t, w)
	if got := ledger[1].State; got != sim.PayLedgerStatePending {
		t.Errorf("State = %q, want pending (zero ExpiresAt should be left alone)", got)
	}
}
