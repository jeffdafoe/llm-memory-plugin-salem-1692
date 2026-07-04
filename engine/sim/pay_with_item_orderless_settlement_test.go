package sim_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pay_with_item_orderless_settlement_test.go — LLM-246 coverage of the
// order-less settlement write-through: accepted settlements that mint no
// Order (consume_now eat-here singles, bundle quote-takes) must reach
// the OrderlessSettlementSink at accept, and Order-minting settlements
// must NOT (their pay_ledger row is owned by the checkpoint upsert).

// captureSettlementSink records every WriteOrderlessSettlement call.
// Mutex-guarded so -race runs stay clean even though the world goroutine
// writes and the test goroutine reads strictly after Send returns.
type captureSettlementSink struct {
	mu    sync.Mutex
	calls []capturedSettlement
}

type capturedSettlement struct {
	entry sim.PayLedgerEntry
	at    time.Time
}

func (s *captureSettlementSink) WriteOrderlessSettlement(_ context.Context, e *sim.PayLedgerEntry, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, capturedSettlement{entry: *sim.ClonePayLedgerEntry(e), at: at})
	return nil
}

func (s *captureSettlementSink) snapshot() []capturedSettlement {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]capturedSettlement(nil), s.calls...)
}

// installSettlementSink wires the capture sink into a running world via
// a Command (SetOrderlessSettlementSink is world-goroutine-only once Run
// has started).
func installSettlementSink(t *testing.T, w *sim.World) *captureSettlementSink {
	t.Helper()
	sink := &captureSettlementSink{}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.SetOrderlessSettlementSink(sink)
		return nil, nil
	}}); err != nil {
		t.Fatalf("install settlement sink: %v", err)
	}
	return sink
}

func TestOrderlessSettlement_ConsumeNowAccept_WritesThrough(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	sink := installSettlementSink(t, w)

	created := time.Now().UTC().Add(-30 * time.Second)
	at := time.Now().UTC()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, ConsumeNow: true, Amount: 4,
		State:     sim.PayLedgerStatePending,
		CreatedAt: created, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})

	if _, err := w.Send(sim.AcceptPay("bob", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}

	calls := sink.snapshot()
	if len(calls) != 1 {
		t.Fatalf("sink calls = %d, want 1 (consume_now accept writes through)", len(calls))
	}
	got := calls[0]
	if got.entry.ID != 1 || got.entry.BuyerID != "alice" || got.entry.SellerID != "bob" {
		t.Errorf("entry identity = id %d buyer %q seller %q, want 1/alice/bob",
			got.entry.ID, got.entry.BuyerID, got.entry.SellerID)
	}
	if got.entry.ItemKind != "stew" || got.entry.Qty != 1 || got.entry.Amount != 4 || !got.entry.ConsumeNow {
		t.Errorf("entry terms = %q x%d for %d consume_now=%v, want stew x1 for 4 consume_now=true",
			got.entry.ItemKind, got.entry.Qty, got.entry.Amount, got.entry.ConsumeNow)
	}
	if !got.entry.CreatedAt.Equal(created) {
		t.Errorf("entry.CreatedAt = %v, want the offer-mint time %v", got.entry.CreatedAt, created)
	}
	if !got.at.Equal(at) {
		t.Errorf("at = %v, want the accept time %v (slow path stamps ResolvedAt after commit)", got.at, at)
	}
}

func TestOrderlessSettlement_TakeHomeAccept_SkipsSink(t *testing.T) {
	// A take-home single mints an Order — its pay_ledger row is owned by
	// the checkpoint upsert; a sink write here would race the same id.
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	sink := installSettlementSink(t, w)

	at := time.Now().UTC()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 4,
		State:     sim.PayLedgerStatePending,
		CreatedAt: at, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})

	if _, err := w.Send(sim.AcceptPay("bob", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	ledger := readPayLedger(t, w)
	if got := ledger[1].State; got != sim.PayLedgerStateAccepted {
		t.Fatalf("ledger.State = %q, want accepted", got)
	}
	if calls := sink.snapshot(); len(calls) != 0 {
		t.Errorf("sink calls = %d, want 0 (take-home persistence is the Order checkpoint's)", len(calls))
	}
}

func TestOrderlessSettlement_BundleTake_WritesThrough(t *testing.T) {
	// A bundle quote-take mints no Order regardless of consume_now — the
	// settlement row is the only durable pay_ledger trace. ItemKind stays
	// empty (goods ride Lines); the pg impl writes NULL item_kind.
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "jeff", displayName: "Jefferey", kind: sim.KindPC, huddleID: "h1", coins: 50},
		{id: "pru", displayName: "Prudence", kind: sim.KindNPCShared, huddleID: "h1",
			inventory: map[sim.ItemKind]int{"ale": 3, "bread": 3}},
	})
	defer stop()
	sink := installSettlementSink(t, w)

	at := time.Now().UTC()
	seedQuote(t, w, sim.SceneQuote{
		ID:         9,
		SceneID:    "sc1",
		SellerID:   "pru",
		Lines:      []sim.QuoteLine{{ItemKind: "ale", Qty: 1}, {ItemKind: "bread", Qty: 1}},
		Amount:     6,
		ConsumeNow: true,
		State:      sim.SceneQuoteStateActive,
		ExpiresAt:  at.Add(10 * time.Minute),
	})

	if _, err := w.Send(sim.PayWithItem("jeff", "Prudence", "ale", 1, 6, true, nil, nil, 9, 0, "", at)); err != nil {
		t.Fatalf("PayWithItem (eat-here bundle): %v", err)
	}

	calls := sink.snapshot()
	if len(calls) != 1 {
		t.Fatalf("sink calls = %d, want 1 (bundle take writes through)", len(calls))
	}
	got := calls[0]
	if got.entry.ItemKind != "" {
		t.Errorf("entry.ItemKind = %q, want empty (bundle goods ride Lines)", got.entry.ItemKind)
	}
	if len(got.entry.Lines) != 2 {
		t.Errorf("entry.Lines = %d, want 2", len(got.entry.Lines))
	}
	if got.entry.Amount != 6 || !got.entry.ConsumeNow {
		t.Errorf("entry terms = %d coins consume_now=%v, want 6/true", got.entry.Amount, got.entry.ConsumeNow)
	}
}

func TestOrderlessSettlement_NoSink_AcceptStillCommits(t *testing.T) {
	// Nil sink (the default) must stay a no-op — harness worlds settle
	// in-memory only, and the accept itself must not care.
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()

	at := time.Now().UTC()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, ConsumeNow: true, Amount: 4,
		State:     sim.PayLedgerStatePending,
		CreatedAt: at, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	if _, err := w.Send(sim.AcceptPay("bob", 1, at)); err != nil {
		t.Fatalf("AcceptPay with no sink installed: %v", err)
	}
	ledger := readPayLedger(t, w)
	if got := ledger[1].State; got != sim.PayLedgerStateAccepted {
		t.Errorf("ledger.State = %q, want accepted", got)
	}
}
