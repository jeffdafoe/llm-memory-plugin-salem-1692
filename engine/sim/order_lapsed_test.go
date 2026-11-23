package sim_test

import (
	"context"
	"math"
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

// TestOrderLapsed_ShortRefundStopsAtZero (LLM-995): alice prepays 6 for a nail
// smith never makes; smith spends all but 2 coins; the sweep hands back only the
// 2 his purse holds. His wallet stops at 0 — never below — alice gets exactly
// those 2 (coins conserved), and both lines say the refund came up short. A
// second sweep finds the order terminal and adds nothing.
func TestOrderLapsed_ShortRefundStopsAtZero(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	sink := withLapseSink(t, w)
	at := time.Now().UTC()
	commissionOffer(t, w, 1, "nail", 1, 6, at)
	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	mustSend(t, w, func(world *sim.World) { world.Actors["smith"].Coins = 2 })
	if _, err := w.Send(sim.EvaluateOrderSweep(commissionExpiry(at))); err != nil {
		t.Fatalf("EvaluateOrderSweep: %v", err)
	}
	if smith := readHoldings(t, w, "smith"); smith.Coins != 0 {
		t.Fatalf("smith.Coins = %d, want 0 (handed back the 2 he held, no further)", smith.Coins)
	}
	if alice := readHoldings(t, w, "alice"); alice.Coins != 46 {
		t.Fatalf("alice.Coins = %d, want 46 (44 after paying, plus the 2 that came back)", alice.Coins)
	}
	sellerLine := sim.OrderLapsedSellerNarration("Alice", "1 nail", 6, 2, false, false)
	buyerLine := sim.OrderLapsedBuyerNarration("Smith", "1 nail", 6, 2, false, false)
	requireOneLapse(t, w, sink, "smith", sellerLine)
	requireOneLapse(t, w, sink, "alice", buyerLine)
	if !strings.Contains(sellerLine, "only the 2 coins") || strings.Contains(sellerLine, "debt") {
		t.Errorf("seller line should name the short refund and no debt, got %q", sellerLine)
	}
	if !strings.Contains(buyerLine, "only 2 coins came back") {
		t.Errorf("buyer line should name the short refund, got %q", buyerLine)
	}
	if rows := sink.lapsed("smith"); len(rows) == 1 {
		p := rows[0].Payload
		if p["amount"] != 6 || p["refunded"] != 2 || p["qty"] != 1 || p["item"] != "nail" || p["deposit"] != false || p["forfeited"] != false || p["counterparty"] != "Alice" {
			t.Errorf("smith durable payload = %+v", p)
		}
	}

	if _, err := w.Send(sim.EvaluateOrderSweep(commissionExpiry(at).Add(time.Hour))); err != nil {
		t.Fatalf("second EvaluateOrderSweep: %v", err)
	}
	requireOneLapse(t, w, sink, "smith", sellerLine)
	requireOneLapse(t, w, sink, "alice", buyerLine)
}

// TestOrderLapsed_EmptyPurseRefundsNothing (LLM-995): smith has spent every
// coin, so nothing goes back. His wallet stays at 0, alice's stays where her
// payment left it, and both are told none of it came back.
func TestOrderLapsed_EmptyPurseRefundsNothing(t *testing.T) {
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
	if smith := readHoldings(t, w, "smith"); smith.Coins != 0 {
		t.Fatalf("smith.Coins = %d, want 0 (never below zero)", smith.Coins)
	}
	if alice := readHoldings(t, w, "alice"); alice.Coins != 44 {
		t.Fatalf("alice.Coins = %d, want 44 (nothing came back)", alice.Coins)
	}
	requireOneLapse(t, w, sink, "smith", sim.OrderLapsedSellerNarration("Alice", "1 nail", 6, 0, false, false))
	requireOneLapse(t, w, sink, "alice", sim.OrderLapsedBuyerNarration("Smith", "1 nail", 6, 0, false, false))
}

// TestOrderLapsed_DepositRefundUsesDepositWording: a partial-payment commission
// refunds only the deposit, and both lines say "deposit". smith still holds the
// 5 coins, so the whole deposit goes back.
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
	sellerLine := sim.OrderLapsedSellerNarration("Alice", "1 nail", 5, 5, true, false)
	requireOneLapse(t, w, sink, "smith", sellerLine)
	requireOneLapse(t, w, sink, "alice", sim.OrderLapsedBuyerNarration("Smith", "1 nail", 5, 5, true, false))
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
	requireOneLapse(t, w, sink, "smith", sim.OrderLapsedSellerNarration("Alice", "1 nail", 5, 0, true, true))
	requireOneLapse(t, w, sink, "alice", sim.OrderLapsedBuyerNarration("Smith", "1 nail", 5, 0, true, true))
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
	requireOneLapse(t, w, sink, "alice", sim.OrderLapsedBuyerNarration("", "1 nail", 6, 6, false, false))
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
	requireOneLapse(t, w, sink, "smith", sim.OrderLapsedSellerNarration("", "1 nail", 5, 0, true, true))
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

// aliceFactsOnSmith returns the texts of alice's (shared-VA) relationship facts
// about smith that mention the deposit, read on the world goroutine. The
// accept's own "I paid" fact is left out.
func aliceFactsOnSmith(t *testing.T, w *sim.World) []string {
	t.Helper()
	var out []string
	mustSend(t, w, func(world *sim.World) {
		if rel := world.Actors["alice"].Relationships["smith"]; rel != nil {
			for _, f := range rel.SalientFacts {
				if strings.Contains(f.Text, "deposit") {
					out = append(out, f.Text)
				}
			}
		}
	})
	return out
}

// TestOrderLapsed_OverflowBlockedRefundSaysNothing (LLM-995 review): a refund
// the buyer's purse cannot take (the credit would overflow) moves no coin. No
// party is told the seller "had spent it", and no deposit fact is written.
func TestOrderLapsed_OverflowBlockedRefundSaysNothing(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	sink := withLapseSink(t, w)
	at := time.Now().UTC()
	commissionOfferWithDeposit(t, w, 1, "nail", 1, 15, 5, at)
	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	mustSend(t, w, func(world *sim.World) { world.Actors["alice"].Coins = math.MaxInt })
	smithBefore := readHoldings(t, w, "smith").Coins
	if _, err := w.Send(sim.EvaluateOrderSweep(commissionExpiry(at))); err != nil {
		t.Fatalf("EvaluateOrderSweep: %v", err)
	}
	if got := readHoldings(t, w, "smith").Coins; got != smithBefore {
		t.Errorf("smith.Coins = %d, want %d (nothing moved)", got, smithBefore)
	}
	if got := readHoldings(t, w, "alice").Coins; got != math.MaxInt {
		t.Errorf("alice.Coins = %d, want MaxInt (nothing moved)", got)
	}
	requireNoLapse(t, w, sink, "smith")
	requireNoLapse(t, w, sink, "alice")
	if facts := aliceFactsOnSmith(t, w); len(facts) != 0 {
		t.Errorf("alice facts on smith = %q, want none for a refund that never happened", facts)
	}
}

// TestOrderLapsed_NegativeSellerIsNotMadeWorse (LLM-995): a purse already below
// zero (state from before the fix) hands back nothing and is left as it is —
// never pushed further down.
func TestOrderLapsed_NegativeSellerIsNotMadeWorse(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	sink := withLapseSink(t, w)
	at := time.Now().UTC()
	commissionOffer(t, w, 1, "nail", 1, 6, at)
	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	mustSend(t, w, func(world *sim.World) { world.Actors["smith"].Coins = -4 })
	if _, err := w.Send(sim.EvaluateOrderSweep(commissionExpiry(at))); err != nil {
		t.Fatalf("EvaluateOrderSweep: %v", err)
	}
	if got := readHoldings(t, w, "smith").Coins; got != -4 {
		t.Errorf("smith.Coins = %d, want -4 (not made worse)", got)
	}
	if got := readHoldings(t, w, "alice").Coins; got != 44 {
		t.Errorf("alice.Coins = %d, want 44 (nothing came back)", got)
	}
	requireOneLapse(t, w, sink, "alice", sim.OrderLapsedBuyerNarration("Smith", "1 nail", 6, 0, false, false))
}

// TestOrderLapsed_ShortDepositRefundFact (LLM-995): smith has spent all but 2
// of alice's 5-coin deposit, so her relationship fact names the short refund.
func TestOrderLapsed_ShortDepositRefundFact(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	withLapseSink(t, w)
	at := time.Now().UTC()
	commissionOfferWithDeposit(t, w, 1, "nail", 1, 15, 5, at)
	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	mustSend(t, w, func(world *sim.World) { world.Actors["smith"].Coins = 2 })
	if _, err := w.Send(sim.EvaluateOrderSweep(commissionExpiry(at))); err != nil {
		t.Fatalf("EvaluateOrderSweep: %v", err)
	}
	facts := aliceFactsOnSmith(t, w)
	if len(facts) != 1 || !strings.Contains(facts[0], "only 2 of my 5-coin deposit came back") {
		t.Errorf("alice facts on smith = %q, want one naming the short refund", facts)
	}
}

// TestOrderLapsed_ShortRefundRestoresVisitorBudgetByWhatCameBack (LLM-995): a
// visitor buyer's spend budget comes back by the 2 coins that were refunded,
// not the 5 he was owed. (Visitors keep no relationship facts.)
func TestOrderLapsed_ShortRefundRestoresVisitorBudgetByWhatCameBack(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	withLapseSink(t, w)
	at := time.Now().UTC()
	commissionOfferWithDeposit(t, w, 1, "nail", 1, 15, 5, at)
	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	mustSend(t, w, func(world *sim.World) {
		world.Actors["smith"].Coins = 2
		world.Actors["alice"].VisitorState = &sim.VisitorState{SpendBudget: 10}
	})
	aliceBefore := readHoldings(t, w, "alice").Coins
	if _, err := w.Send(sim.EvaluateOrderSweep(commissionExpiry(at))); err != nil {
		t.Fatalf("EvaluateOrderSweep: %v", err)
	}
	if got := readHoldings(t, w, "smith").Coins; got != 0 {
		t.Errorf("smith.Coins = %d, want 0", got)
	}
	if got := readHoldings(t, w, "alice").Coins; got != aliceBefore+2 {
		t.Errorf("alice.Coins = %d, want %d (+2)", got, aliceBefore+2)
	}
	var budget int
	mustSend(t, w, func(world *sim.World) { budget = world.Actors["alice"].VisitorState.SpendBudget })
	if budget != 12 {
		t.Errorf("alice spend budget = %d, want 12 (restored by the 2 that came back, not the 5 owed)", budget)
	}
}

// TestOrderLapsed_ZeroDepositRefundFact (LLM-995): smith's purse is empty, so
// none of the deposit comes back and alice's fact says so.
func TestOrderLapsed_ZeroDepositRefundFact(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	withLapseSink(t, w)
	at := time.Now().UTC()
	commissionOfferWithDeposit(t, w, 1, "nail", 1, 15, 5, at)
	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	mustSend(t, w, func(world *sim.World) { world.Actors["smith"].Coins = 0 })
	if _, err := w.Send(sim.EvaluateOrderSweep(commissionExpiry(at))); err != nil {
		t.Fatalf("EvaluateOrderSweep: %v", err)
	}
	facts := aliceFactsOnSmith(t, w)
	if len(facts) != 1 || !strings.Contains(facts[0], "none of my 5-coin deposit came back") {
		t.Errorf("alice facts on smith = %q, want one saying none came back", facts)
	}
}

// TestOrderLapsed_OverflowCheckUsesTheShortRefund (LLM-995 review): the buyer
// could not take the 6 coins owed (MaxInt-2) but can take the 2 the seller
// holds, so the 2 move and both parties read a short refund. Guards against the
// overflow check drifting back to the owed amount.
func TestOrderLapsed_OverflowCheckUsesTheShortRefund(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	sink := withLapseSink(t, w)
	at := time.Now().UTC()
	commissionOffer(t, w, 1, "nail", 1, 6, at)
	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	mustSend(t, w, func(world *sim.World) {
		world.Actors["alice"].Coins = math.MaxInt - 2
		world.Actors["smith"].Coins = 2
	})
	if _, err := w.Send(sim.EvaluateOrderSweep(commissionExpiry(at))); err != nil {
		t.Fatalf("EvaluateOrderSweep: %v", err)
	}
	if got := readHoldings(t, w, "alice").Coins; got != math.MaxInt {
		t.Errorf("alice.Coins = %d, want MaxInt (the 2 moved)", got)
	}
	if got := readHoldings(t, w, "smith").Coins; got != 0 {
		t.Errorf("smith.Coins = %d, want 0", got)
	}
	requireOneLapse(t, w, sink, "smith", sim.OrderLapsedSellerNarration("Alice", "1 nail", 6, 2, false, false))
	requireOneLapse(t, w, sink, "alice", sim.OrderLapsedBuyerNarration("Smith", "1 nail", 6, 2, false, false))
}
