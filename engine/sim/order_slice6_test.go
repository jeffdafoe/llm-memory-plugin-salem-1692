package sim_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// order_slice6_test.go — Slice 6 coverage. Three behavioral axes:
//
//  1. With a sink installed and WriteTerminal succeeding, the entry
//     is pruned from w.Orders after the state flip.
//  2. With a sink installed and WriteTerminal returning an error,
//     the entry stays in w.Orders at its terminal state (next
//     SaveSnapshot reconciles).
//  3. Without a sink installed (legacy behavior, default for tests
//     that don't wire one), the entry stays in w.Orders. This is
//     also covered by the pre-existing sweep tests; here we assert
//     it explicitly so the slice-6 contract has its own anchor.
//
// Plus: SetTerminalOrderSink can install and clear (nil clears).
// Plus: OrderDelivered carries Amount end-to-end through the emit.

// recordingTerminalOrderSink records every WriteTerminal call. Returns
// errOnNext (if set) on the next call. Pointer receiver — recordings
// persist across interface boxing.
type recordingTerminalOrderSink struct {
	calls     []*sim.Order
	errOnNext error
}

func (s *recordingTerminalOrderSink) WriteTerminal(_ context.Context, o *sim.Order) error {
	s.calls = append(s.calls, sim.CloneOrder(o))
	if s.errOnNext != nil {
		return s.errOnNext
	}
	return nil
}

// TestFinalizeOrderTerminal_PrunesWhenSinkSucceeds — the load-bearing
// happy path. Sink WriteTerminal returns nil; finalizeOrderTerminal
// deletes the entry from w.Orders.
func TestFinalizeOrderTerminal_PrunesWhenSinkSucceeds(t *testing.T) {
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)
	sink := &recordingTerminalOrderSink{}
	w.SetTerminalOrderSink(sink)

	at := time.Now().UTC()
	o := &sim.Order{
		ID:          1,
		State:       sim.OrderStateReady,
		BuyerID:     "alice",
		SellerID:    "bob",
		Item:        "stew",
		Qty:         1,
		Amount:      7,
		ConsumerIDs: []sim.ActorID{"alice"},
		LedgerID:    1,
		CreatedAt:   at.Add(-time.Hour),
		ExpiresAt:   at.Add(time.Hour),
	}
	w.Orders[o.ID] = o

	sim.FinalizeOrderTerminal(w, o, sim.OrderStateDelivered, at)

	if _, ok := w.Orders[1]; ok {
		t.Error("Order remains in w.Orders after successful write-through; want pruned")
	}
	if len(sink.calls) != 1 {
		t.Fatalf("sink WriteTerminal called %d times, want 1", len(sink.calls))
	}
	got := sink.calls[0]
	if got.State != sim.OrderStateDelivered {
		t.Errorf("sink received state %q, want %q", got.State, sim.OrderStateDelivered)
	}
	if got.DeliveredAt == nil || !got.DeliveredAt.Equal(at) {
		t.Errorf("sink received DeliveredAt = %v, want %v", got.DeliveredAt, at)
	}
}

// TestFinalizeOrderTerminal_LeavesEntryWhenSinkErrors — failure mode.
// Sink returns an error; entry stays in w.Orders at its terminal
// state. The OrderDelivered/OrderExpired event has already fired
// (asserted via the in-memory state flip having taken effect).
func TestFinalizeOrderTerminal_LeavesEntryWhenSinkErrors(t *testing.T) {
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)
	sink := &recordingTerminalOrderSink{errOnNext: errors.New("simulated pg failure")}
	w.SetTerminalOrderSink(sink)

	at := time.Now().UTC()
	o := &sim.Order{
		ID:          2,
		State:       sim.OrderStateReady,
		ExpiresAt:   at.Add(time.Hour),
		ConsumerIDs: []sim.ActorID{"alice"},
		Qty:         1,
	}
	w.Orders[o.ID] = o

	sim.FinalizeOrderTerminal(w, o, sim.OrderStateExpired, at)

	gotO, ok := w.Orders[2]
	if !ok {
		t.Fatal("Order pruned despite sink error; want retained for next SaveSnapshot")
	}
	if gotO.State != sim.OrderStateExpired {
		t.Errorf("retained Order state = %q, want %q", gotO.State, sim.OrderStateExpired)
	}
	if len(sink.calls) != 1 {
		t.Errorf("sink WriteTerminal called %d times, want 1", len(sink.calls))
	}
}

// TestFinalizeOrderTerminal_NoSinkLeavesEntry — explicit anchor for
// the legacy no-prune behavior. Mirrors the pre-Slice-6 contract that
// the sweep + DeliverOrder tests rely on.
func TestFinalizeOrderTerminal_NoSinkLeavesEntry(t *testing.T) {
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)
	// No SetTerminalOrderSink — w.terminalOrderSink stays nil.

	at := time.Now().UTC()
	o := &sim.Order{
		ID:          3,
		State:       sim.OrderStateReady,
		ConsumerIDs: []sim.ActorID{"alice"},
		Qty:         1,
	}
	w.Orders[o.ID] = o

	sim.FinalizeOrderTerminal(w, o, sim.OrderStateDelivered, at)

	gotO, ok := w.Orders[3]
	if !ok {
		t.Fatal("Order pruned with no sink installed; want legacy retain")
	}
	if gotO.State != sim.OrderStateDelivered {
		t.Errorf("retained Order state = %q, want %q", gotO.State, sim.OrderStateDelivered)
	}
}

// TestSetTerminalOrderSink_NilClears — passing nil clears the field
// back to the legacy no-prune behavior, mirroring "test installs
// sink, then a teardown nils it" patterns. Different from
// SetPayLedgerSink (which installs nullPayLedgerSink on nil) — the
// terminal sink has no null impl, and finalizeOrderTerminal nil-checks
// at the call site.
func TestSetTerminalOrderSink_NilClears(t *testing.T) {
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)
	sink := &recordingTerminalOrderSink{}
	w.SetTerminalOrderSink(sink)
	w.SetTerminalOrderSink(nil)

	at := time.Now().UTC()
	o := &sim.Order{ID: 4, State: sim.OrderStateReady, ConsumerIDs: []sim.ActorID{"alice"}, Qty: 1}
	w.Orders[o.ID] = o
	sim.FinalizeOrderTerminal(w, o, sim.OrderStateDelivered, at)

	if _, ok := w.Orders[4]; !ok {
		t.Error("Order pruned after sink cleared via SetTerminalOrderSink(nil)")
	}
	if len(sink.calls) != 0 {
		t.Errorf("sink received %d calls after being cleared, want 0", len(sink.calls))
	}
}

// TestFinalizeOrderTerminal_DeliveredCarriesAmount — Slice 6 adds
// Amount to OrderDelivered for the future price-book subscriber.
// Verify the emitted event carries Order.Amount.
func TestFinalizeOrderTerminal_DeliveredCarriesAmount(t *testing.T) {
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)

	var captured *sim.OrderDelivered
	w.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
		if d, ok := evt.(*sim.OrderDelivered); ok {
			captured = d
		}
	}))

	at := time.Now().UTC()
	o := &sim.Order{
		ID:          5,
		State:       sim.OrderStateReady,
		BuyerID:     "alice",
		SellerID:    "bob",
		Item:        "stew",
		Qty:         2,
		Amount:      13,
		ConsumerIDs: []sim.ActorID{"alice"},
		LedgerID:    5,
	}
	w.Orders[o.ID] = o
	sim.FinalizeOrderTerminal(w, o, sim.OrderStateDelivered, at)

	if captured == nil {
		t.Fatal("OrderDelivered not emitted")
	}
	if captured.Amount != 13 {
		t.Errorf("OrderDelivered.Amount = %d, want 13", captured.Amount)
	}
	if captured.Qty != 2 {
		t.Errorf("OrderDelivered.Qty = %d, want 2", captured.Qty)
	}
}

// TestRestartExpirePendingOrders_WriteThroughOnRestart — wiring order
// contract. SetTerminalOrderSink → seed stale Ready in w.Orders →
// restartExpirePendingOrders → sink receives the Expired and entry
// is pruned. Documents the main.go assembly requirement
// (SetTerminalOrderSink before LoadWorld).
func TestRestartExpirePendingOrders_WriteThroughOnRestart(t *testing.T) {
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)
	sink := &recordingTerminalOrderSink{}
	w.SetTerminalOrderSink(sink)

	at := time.Now().UTC()
	w.Orders[6] = &sim.Order{
		ID: 6, State: sim.OrderStateReady, ConsumerIDs: []sim.ActorID{"alice"}, Qty: 1,
		ExpiresAt: at.Add(-time.Hour), // stale — restart will expire
	}
	w.Orders[7] = &sim.Order{
		ID: 7, State: sim.OrderStateReady, ConsumerIDs: []sim.ActorID{"alice"}, Qty: 1,
		ExpiresAt: at.Add(time.Hour), // fresh — restart leaves alone
	}

	sim.RestartExpirePendingOrders(w, at)

	if _, ok := w.Orders[6]; ok {
		t.Error("Stale Ready not pruned after restart-flip + write-through")
	}
	if _, ok := w.Orders[7]; !ok {
		t.Error("Fresh Ready pruned at restart; should survive")
	}
	if len(sink.calls) != 1 {
		t.Errorf("sink received %d calls, want 1 (only stale)", len(sink.calls))
	}
	if len(sink.calls) > 0 && sink.calls[0].ID != 6 {
		t.Errorf("sink received order %d, want 6", sink.calls[0].ID)
	}
}
