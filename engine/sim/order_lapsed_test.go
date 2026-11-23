package sim_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// order_lapsed_test.go — LLM-993. A Ready order that expires settles its coins
// at the sweep with neither party's turn in flight; each party must get one
// order_lapsed beat on its next turn and one order_lapsed action-log row.

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

// lapsedRows returns the order_lapsed action-log texts written for an actor.
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

// requireOne asserts exactly one beat and one row for the actor, both equal to
// want, and returns nothing.
func requireOneLapse(t *testing.T, w *sim.World, id sim.ActorID, want string) {
	t.Helper()
	beats := lapsedBeats(t, w, id)
	if len(beats) != 1 || beats[0] != want {
		t.Errorf("%s order_lapsed beats = %q, want exactly [%q]", id, beats, want)
	}
	rows := lapsedRows(t, w, id)
	if len(rows) != 1 || rows[0] != want {
		t.Errorf("%s order_lapsed action rows = %q, want exactly [%q]", id, rows, want)
	}
}

// TestOrderLapsed_FullPrepayRefundTellsBothAndNamesTheDebt: alice prepays 6 for
// a nail smith never makes; smith spends the coins; the sweep refunds her out of
// his purse, taking him to -6. Both read why, and smith reads the debt.
func TestOrderLapsed_FullPrepayRefundTellsBothAndNamesTheDebt(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
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
	requireOneLapse(t, w, "smith", sim.OrderLapsedSellerNarration("Alice", "1 nail", 6, false, false, 6))
	requireOneLapse(t, w, "alice", sim.OrderLapsedBuyerNarration("Smith", "1 nail", 6, false, false))
	if beats := lapsedBeats(t, w, "smith"); len(beats) == 1 && !strings.Contains(beats[0], "6 coins in debt") {
		t.Errorf("smith's beat should name the debt, got %q", beats[0])
	}

	// A second sweep finds the order terminal and must not tell anyone again.
	if _, err := w.Send(sim.EvaluateOrderSweep(commissionExpiry(at).Add(time.Hour))); err != nil {
		t.Fatalf("second EvaluateOrderSweep: %v", err)
	}
	if n := len(lapsedRows(t, w, "smith")) + len(lapsedRows(t, w, "alice")); n != 2 {
		t.Errorf("order_lapsed rows after a second sweep = %d, want 2 (one per party, once)", n)
	}
}

// TestOrderLapsed_DepositRefundUsesDepositWording: a partial-payment commission
// refunds only the deposit, and both lines say "deposit". smith still holds the
// 5 coins, so there is no debt clause.
func TestOrderLapsed_DepositRefundUsesDepositWording(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	at := time.Now().UTC()
	commissionOfferWithDeposit(t, w, 1, "nail", 1, 15, 5, at)
	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	if _, err := w.Send(sim.EvaluateOrderSweep(commissionExpiry(at))); err != nil {
		t.Fatalf("EvaluateOrderSweep: %v", err)
	}
	requireOneLapse(t, w, "smith", sim.OrderLapsedSellerNarration("Alice", "1 nail", 5, true, false, 0))
	requireOneLapse(t, w, "alice", sim.OrderLapsedBuyerNarration("Smith", "1 nail", 5, true, false))
	if beats := lapsedBeats(t, w, "smith"); len(beats) == 1 && strings.Contains(beats[0], "debt") {
		t.Errorf("smith still holds the coins, so no debt clause; got %q", beats[0])
	}
}

// TestOrderLapsed_ForfeitTellsBothTheDepositStays: smith forged the nail but
// alice never came; the deposit stays with him and both read the forfeit.
func TestOrderLapsed_ForfeitTellsBothTheDepositStays(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	at := time.Now().UTC()
	commissionOfferWithDeposit(t, w, 1, "nail", 1, 15, 5, at)
	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	mustSend(t, w, func(world *sim.World) { world.Actors["smith"].Inventory = map[sim.ItemKind]int{"nail": 1} })
	if _, err := w.Send(sim.EvaluateOrderSweep(commissionExpiry(at))); err != nil {
		t.Fatalf("EvaluateOrderSweep: %v", err)
	}
	requireOneLapse(t, w, "smith", sim.OrderLapsedSellerNarration("Alice", "1 nail", 5, true, true, 0))
	requireOneLapse(t, w, "alice", sim.OrderLapsedBuyerNarration("Smith", "1 nail", 5, true, true))
}

// TestOrderLapsed_DeliveredOrderSaysNothing: an order handed over in time is not
// a lapse — no order_lapsed beat or row for either side.
func TestOrderLapsed_DeliveredOrderSaysNothing(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
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
	for _, id := range []sim.ActorID{"smith", "alice"} {
		if b, r := lapsedBeats(t, w, id), lapsedRows(t, w, id); len(b)+len(r) != 0 {
			t.Errorf("%s: delivered order produced order_lapsed beats %q / rows %q", id, b, r)
		}
	}
}
