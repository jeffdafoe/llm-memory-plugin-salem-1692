package sim_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// commission_taken_test.go — LLM-1026. CommissionTakenFor tells the accept
// result whether the settle it just made left the seller owing goods. It reads
// the Order the accept minted, so each case runs the accept and the lookup on the
// world goroutine in turn.

// commissionTakenAfterAccept runs the accept and the lookup in one Command, as
// the accept_pay handler does, and fails unless the accept settled.
func commissionTakenAfterAccept(t *testing.T, w *sim.World, seller sim.ActorID, ledgerID sim.LedgerID, at time.Time) (sim.CommissionTaken, bool) {
	t.Helper()
	type out struct {
		taken sim.CommissionTaken
		ok    bool
	}
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		st, err := sim.AcceptPay(seller, ledgerID, at).Fn(world)
		if err != nil {
			return nil, err
		}
		if st != sim.PayLedgerStateAccepted {
			return nil, fmt.Errorf("accept resolved to %v, want accepted", st)
		}
		taken, ok := sim.CommissionTakenFor(world, seller, ledgerID)
		return out{taken, ok}, nil
	}})
	if err != nil {
		t.Fatalf("accept + CommissionTakenFor: %v", err)
	}
	r := res.(out)
	return r.taken, r.ok
}

// Stock on hand that earlier orders already owe does not cover a new order: the
// accept is a commission with nothing free on hand.
func TestCommissionTakenFor_ReservedStockIsCommission(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), map[sim.ItemKind]int{"nail": 3}, 50, nil)
	defer stop()
	at := time.Now().UTC()
	mustSend(t, w, func(world *sim.World) {
		world.Orders[2] = &sim.Order{
			ID: 2, State: sim.OrderStateReady, SellerID: "smith", BuyerID: "carol",
			Item: "nail", Qty: 3, ConsumerIDs: []sim.ActorID{"carol"},
			CreatedAt: at, ExpiresAt: at.Add(time.Hour),
		}
	})
	commissionOffer(t, w, 1, "nail", 3, 18, at)

	taken, ok := commissionTakenAfterAccept(t, w, "smith", 1, at)
	if !ok || taken.PartlyOnHand {
		t.Errorf("reserved stock: CommissionTakenFor = %+v (ok=%v), want a commission with nothing free on hand", taken, ok)
	}
}

// Some free stock, not enough: still a commission, flagged partly on hand.
func TestCommissionTakenFor_PartlyOnHand(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), map[sim.ItemKind]int{"nail": 1}, 50, nil)
	defer stop()
	at := time.Now().UTC()
	commissionOffer(t, w, 1, "nail", 3, 18, at)

	taken, ok := commissionTakenAfterAccept(t, w, "smith", 1, at)
	if !ok || !taken.PartlyOnHand || taken.Qty != 3 {
		t.Errorf("1 of 3 held: CommissionTakenFor = %+v (ok=%v), want a 3-nail commission partly on hand", taken, ok)
	}
}

// A stockless accept of a good the seller makes is a commission, and the lookup
// reports what is owed: the whole quantity, the count-aware noun, the coin taken.
func TestCommissionTakenFor_StocklessAcceptIsCommission(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	at := time.Now().UTC()
	commissionOffer(t, w, 1, "nail", 3, 18, at)

	taken, ok := commissionTakenAfterAccept(t, w, "smith", 1, at)
	if !ok {
		t.Fatal("stockless accept of a produced good: CommissionTakenFor = false, want a commission")
	}
	want := sim.CommissionTaken{OrderID: 1, BuyerName: "Alice", Qty: 3, Noun: "nails", Charged: 18, BalanceDue: 0}
	if taken != want {
		t.Errorf("CommissionTakenFor = %+v, want %+v", taken, want)
	}
}

// A single good takes the singular noun.
func TestCommissionTakenFor_SingleGoodSingularNoun(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	at := time.Now().UTC()
	commissionOffer(t, w, 1, "nail", 1, 6, at)

	taken, ok := commissionTakenAfterAccept(t, w, "smith", 1, at)
	if !ok || taken.Qty != 1 || taken.Noun != "nail" {
		t.Errorf("CommissionTakenFor = %+v (ok=%v), want qty 1 noun \"nail\"", taken, ok)
	}
}

// A partial-payment commission reports the deposit taken and the balance due.
func TestCommissionTakenFor_DepositReportsChargedAndBalance(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	at := time.Now().UTC()
	commissionOfferWithDeposit(t, w, 1, "nail", 1, 15, 5, at)

	taken, ok := commissionTakenAfterAccept(t, w, "smith", 1, at)
	if !ok {
		t.Fatal("deposit commission: CommissionTakenFor = false")
	}
	if taken.Charged != 5 || taken.BalanceDue != 10 {
		t.Errorf("Charged/BalanceDue = %d/%d, want 5/10", taken.Charged, taken.BalanceDue)
	}
}

// A sale handed over from stock is not a commission, even for a good the seller
// makes: the order is Delivered before the accept returns.
func TestCommissionTakenFor_InStockSaleIsNotCommission(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), map[sim.ItemKind]int{"nail": 3}, 50, nil)
	defer stop()
	at := time.Now().UTC()
	commissionOffer(t, w, 1, "nail", 3, 18, at)

	if taken, ok := commissionTakenAfterAccept(t, w, "smith", 1, at); ok {
		t.Errorf("in-stock sale reported as a commission: %+v", taken)
	}
}

// The lookup answers only for the seller of that order.
func TestCommissionTakenFor_OtherActorIsNotSeller(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	at := time.Now().UTC()
	commissionOffer(t, w, 1, "nail", 3, 18, at)
	commissionTakenAfterAccept(t, w, "smith", 1, at)

	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		_, ok := sim.CommissionTakenFor(world, "alice", 1)
		return ok, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.(bool) {
		t.Error("CommissionTakenFor answered true for the buyer, not the seller")
	}
}
