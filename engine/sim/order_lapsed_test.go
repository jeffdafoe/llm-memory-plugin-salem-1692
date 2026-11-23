package sim_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// order_lapsed_test.go — LLM-993. A Ready order that expires settles its coins
// at the sweep with neither party's turn in flight; each party still in the
// world must get one order_lapsed beat on its next turn, one action-ring entry
// and one durable order_lapsed row — and only for what the settlement did.

// lapsedBeats returns the OrderLapsed warrant narrations stamped on an actor.
func lapsedBeats(t *testing.T, w *sim.World, id sim.ActorID) []string {
	t.Helper()
	var out []string
	for _, m := range readWarrants(t, w, id) {
		if r, ok := m.Reason.(sim.OrderLapsedWarrantReason); ok {
			out = append(out, r.NarrationText)
		}
	}
	return out
}

// lapsedRows returns the order_lapsed action-ring texts written for an actor.
func lapsedRows(t *testing.T, w *sim.World, id sim.ActorID) []string {
	t.Helper()
	var out []string
	for _, e := range readActionLog(t, w) {
		if e.ActorID == id && e.ActionType == sim.ActionTypeOrderLapsed {
			out = append(out, e.Text)
		}
	}
	return out
}

// lapseSink captures the durable action-log rows the world submits. The world
// calls Append synchronously on its own goroutine; the mutex covers the test
// goroutine's reads.
type lapseSink struct {
	mu   sync.Mutex
	rows []sim.DurableActionLogRow
}

func (s *lapseSink) Append(_ context.Context, row sim.DurableActionLogRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, row)
	return nil
}

// lapsed returns the order_lapsed rows submitted for one actor.
func (s *lapseSink) lapsed(id sim.ActorID) []sim.DurableActionLogRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []sim.DurableActionLogRow
	for _, r := range s.rows {
		if r.ActorID == id && r.ActionType == sim.ActionTypeOrderLapsed {
			out = append(out, r)
		}
	}
	return out
}

// withLapseSink installs a capturing durable sink on the running world.
func withLapseSink(t *testing.T, w *sim.World) *lapseSink {
	t.Helper()
	sink := &lapseSink{}
	mustSend(t, w, func(world *sim.World) { world.SetActionLogSink(sink) })
	return sink
}

// requireOneLapse asserts exactly one beat, one ring entry and one durable row
// for the actor, all carrying want.
func requireOneLapse(t *testing.T, w *sim.World, sink *lapseSink, id sim.ActorID, want string) {
	t.Helper()
	beats := lapsedBeats(t, w, id)
	if len(beats) != 1 || beats[0] != want {
		t.Errorf("%s order_lapsed beats = %q, want exactly [%q]", id, beats, want)
	}
	rows := lapsedRows(t, w, id)
	if len(rows) != 1 || rows[0] != want {
		t.Errorf("%s order_lapsed ring entries = %q, want exactly [%q]", id, rows, want)
	}
	durable := sink.lapsed(id)
	if len(durable) != 1 || durable[0].Payload["text"] != want {
		t.Errorf("%s durable order_lapsed rows = %+v, want exactly one with text %q", id, durable, want)
	}
}

// requireNoLapse asserts the actor got no beat, ring entry or durable row.
func requireNoLapse(t *testing.T, w *sim.World, sink *lapseSink, id sim.ActorID) {
	t.Helper()
	if b, r, d := lapsedBeats(t, w, id), lapsedRows(t, w, id), sink.lapsed(id); len(b)+len(r)+len(d) != 0 {
		t.Errorf("%s: want no order_lapsed beat/entry/row, got beats %q, entries %q, durable %d", id, b, r, len(d))
	}
}

// TestOrderLapsed_FullPrepayRefundTellsBothAndNamesTheDebt: alice prepays 6 for
// a nail smith never makes; smith spends the coins; the sweep refunds her out of
// his purse, taking him to -6. Both read why, and smith reads the debt. A
// second sweep finds the order terminal and adds nothing.
func TestOrderLapsed_FullPrepayRefundTellsBothAndNamesTheDebt(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	sink := withLapseSink(t, w)
	at := time.Now().UTC()
	commissionOffer(t, w, 1, "nail", 1, 6, at)
	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	mustSend(t, w, func(world *sim.World) { world.Actors["smith"].Coins = 0 })
	if _, err := w.Send(sim.EvaluateOrderSweep(commissionExpiry(at))); err != nil {
		t.Fatalf("EvaluateOrderSweep: %v", err)
	}
	if smith := readHoldings(t, w, "smith"); smith.Coins != -6 {
		t.Fatalf("smith.Coins = %d, want -6 (refund out of spent coins)", smith.Coins)
	}
	sellerLine := sim.OrderLapsedSellerNarration("Alice", "1 nail", 6, false, false, 6)
	requireOneLapse(t, w, sink, "smith", sellerLine)
	requireOneLapse(t, w, sink, "alice", sim.OrderLapsedBuyerNarration("Smith", "1 nail", 6, false, false))
	if !strings.Contains(sellerLine, "6 coins in debt") {
		t.Errorf("seller line should name the debt, got %q", sellerLine)
	}
	if rows := sink.lapsed("smith"); len(rows) == 1 {
		p := rows[0].Payload
		if p["amount"] != 6 || p["qty"] != 1 || p["item"] != "nail" || p["deposit"] != false || p["forfeited"] != false || p["counterparty"] != "Alice" {
			t.Errorf("smith durable payload = %+v", p)
		}
	}

	if _, err := w.Send(sim.EvaluateOrderSweep(commissionExpiry(at).Add(time.Hour))); err != nil {
		t.Fatalf("second EvaluateOrderSweep: %v", err)
	}
	requireOneLapse(t, w, sink, "smith", sellerLine)
	requireOneLapse(t, w, sink, "alice", sim.OrderLapsedBuyerNarration("Smith", "1 nail", 6, false, false))
}

// TestOrderLapsed_DepositRefundUsesDepositWording: a partial-payment commission
// refunds only the deposit, and both lines say "deposit". smith still holds the
// 5 coins, so there is no debt clause.
func TestOrderLapsed_DepositRefundUsesDepositWording(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	sink := withLapseSink(t, w)
	at := time.Now().UTC()
	commissionOfferWithDeposit(t, w, 1, "nail", 1, 15, 5, at)
	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	if _, err := w.Send(sim.EvaluateOrderSweep(commissionExpiry(at))); err != nil {
		t.Fatalf("EvaluateOrderSweep: %v", err)
	}
	sellerLine := sim.OrderLapsedSellerNarration("Alice", "1 nail", 5, true, false, 0)
	requireOneLapse(t, w, sink, "smith", sellerLine)
	requireOneLapse(t, w, sink, "alice", sim.OrderLapsedBuyerNarration("Smith", "1 nail", 5, true, false))
	if strings.Contains(sellerLine, "debt") {
		t.Errorf("smith still holds the coins, so no debt clause; got %q", sellerLine)
	}
}

// TestOrderLapsed_ForfeitTellsBothTheDepositStays: smith forged the nail but
// alice never came; the deposit stays with him and both read the forfeit.
func TestOrderLapsed_ForfeitTellsBothTheDepositStays(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	sink := withLapseSink(t, w)
	at := time.Now().UTC()
	commissionOfferWithDeposit(t, w, 1, "nail", 1, 15, 5, at)
	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	mustSend(t, w, func(world *sim.World) { world.Actors["smith"].Inventory = map[sim.ItemKind]int{"nail": 1} })
	if _, err := w.Send(sim.EvaluateOrderSweep(commissionExpiry(at))); err != nil {
		t.Fatalf("EvaluateOrderSweep: %v", err)
	}
	requireOneLapse(t, w, sink, "smith", sim.OrderLapsedSellerNarration("Alice", "1 nail", 5, true, true, 0))
	requireOneLapse(t, w, sink, "alice", sim.OrderLapsedBuyerNarration("Smith", "1 nail", 5, true, true))
}

// TestOrderLapsed_DeliveredOrderSaysNothing: an order handed over in time is not
// a lapse — no beat, entry or durable row for either side.
func TestOrderLapsed_DeliveredOrderSaysNothing(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	sink := withLapseSink(t, w)
	at := time.Now().UTC()
	commissionOffer(t, w, 1, "nail", 1, 6, at)
	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	mustSend(t, w, func(world *sim.World) { world.Actors["smith"].Inventory = map[sim.ItemKind]int{"nail": 1} })
	if _, err := w.Send(sim.DeliverOrder("smith", 1, at.Add(time.Minute))); err != nil {
		t.Fatalf("DeliverOrder: %v", err)
	}
	if _, err := w.Send(sim.EvaluateOrderSweep(commissionExpiry(at))); err != nil {
		t.Fatalf("EvaluateOrderSweep: %v", err)
	}
	requireNoLapse(t, w, sink, "smith")
	requireNoLapse(t, w, sink, "alice")
}

// TestOrderLapsed_SellerGoneStillTellsTheBuyer: the seller's actor is gone at
// the sweep, so the refund credits alice without a debit (refundOrderPayment's
// absent-seller leg). She is still told her coins came back, with the fallback
// seller name.
func TestOrderLapsed_SellerGoneStillTellsTheBuyer(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	sink := withLapseSink(t, w)
	at := time.Now().UTC()
	commissionOffer(t, w, 1, "nail", 1, 6, at)
	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	mustSend(t, w, func(world *sim.World) { delete(world.Actors, "smith") })
	if _, err := w.Send(sim.EvaluateOrderSweep(commissionExpiry(at))); err != nil {
		t.Fatalf("EvaluateOrderSweep: %v", err)
	}
	if alice := readHoldings(t, w, "alice"); alice.Coins != 50 {
		t.Fatalf("alice.Coins = %d, want 50 (refunded)", alice.Coins)
	}
	requireOneLapse(t, w, sink, "alice", sim.OrderLapsedBuyerNarration("", "1 nail", 6, false, false))
	if n := len(sink.lapsed("smith")); n != 0 {
		t.Errorf("durable rows for the absent seller = %d, want 0", n)
	}
}

// TestOrderLapsed_BuyerGoneRefundSaysNothing: with the buyer gone the refund
// credits no one and debits no one, so no coin moved and nobody is told.
func TestOrderLapsed_BuyerGoneRefundSaysNothing(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	sink := withLapseSink(t, w)
	at := time.Now().UTC()
	commissionOffer(t, w, 1, "nail", 1, 6, at)
	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	mustSend(t, w, func(world *sim.World) { delete(world.Actors, "alice") })
	if _, err := w.Send(sim.EvaluateOrderSweep(commissionExpiry(at))); err != nil {
		t.Fatalf("EvaluateOrderSweep: %v", err)
	}
	if smith := readHoldings(t, w, "smith"); smith.Coins != 6 {
		t.Fatalf("smith.Coins = %d, want 6 (no refund without a buyer)", smith.Coins)
	}
	requireNoLapse(t, w, sink, "smith")
}

// TestOrderLapsed_BuyerGoneForfeitTellsTheSeller: a forfeit stands without the
// buyer (the seller keeps the deposit), so the seller is told, with the fallback
// buyer name.
func TestOrderLapsed_BuyerGoneForfeitTellsTheSeller(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	sink := withLapseSink(t, w)
	at := time.Now().UTC()
	commissionOfferWithDeposit(t, w, 1, "nail", 1, 15, 5, at)
	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	mustSend(t, w, func(world *sim.World) {
		world.Actors["smith"].Inventory = map[sim.ItemKind]int{"nail": 1}
		delete(world.Actors, "alice")
	})
	if _, err := w.Send(sim.EvaluateOrderSweep(commissionExpiry(at))); err != nil {
		t.Fatalf("EvaluateOrderSweep: %v", err)
	}
	requireOneLapse(t, w, sink, "smith", sim.OrderLapsedSellerNarration("", "1 nail", 5, true, true, 0))
}

// TestOrderLapsed_RestartPassStampsTheBeat: the boot-time expiry pass
// (restartExpirePendingOrders, run inside LoadWorld before the durable sink is
// wired) still stamps each party's beat, so the turn line survives an order that
// lapsed while the engine was down. No sink is installed, matching boot, so no
// durable row can be written.
func TestOrderLapsed_RestartPassStampsTheBeat(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	at := time.Now().UTC()
	commissionOffer(t, w, 1, "nail", 1, 6, at)
	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	mustSend(t, w, func(world *sim.World) { sim.RestartExpirePendingOrders(world, commissionExpiry(at)) })
	if b := lapsedBeats(t, w, "smith"); len(b) != 1 {
		t.Errorf("smith beats after the restart pass = %q, want one", b)
	}
	if b := lapsedBeats(t, w, "alice"); len(b) != 1 {
		t.Errorf("alice beats after the restart pass = %q, want one", b)
	}
}
