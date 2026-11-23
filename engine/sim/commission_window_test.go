package sim_test

import (
	"math"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// commission_window_test.go — LLM-992. A commission's Ready window is sized from
// the units the keeper must still forge, not from one unit: a five-shovel order
// used to get one shovel's window and lapse overnight, refunding the deposit out
// of a smith who had already spent it on iron.

// nailWindow is the Ready window for a nail commission that needs `units` nails
// forged (nail: 1 per 1h, one per batch).
func nailWindow(units int) time.Duration {
	cycle := time.Duration(sim.CycleDurationSeconds(commissionRecipes()["nail"])) * time.Second
	return time.Duration(units)*cycle*time.Duration(sim.CommissionOrderSlack) + sim.CommissionOrderGrace
}

// TestAcceptPay_Commission_WindowScalesWithQuantity: a five-nail commission gets
// five nails' worth of forge time, and is still Ready past the window a single
// nail would have had.
func TestAcceptPay_Commission_WindowScalesWithQuantity(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	at := time.Now().UTC()
	commissionOffer(t, w, 1, "nail", 5, 30, at)
	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}

	o, ok := readOrders(t, w)[1]
	if !ok {
		t.Fatalf("no commission Order minted")
	}
	if want := at.Add(nailWindow(5)); !o.ExpiresAt.Equal(want) {
		t.Errorf("order.ExpiresAt = %v, want %v (five nails' forge time)", o.ExpiresAt, want)
	}

	// Past the old one-unit window the order must still be open.
	if _, err := w.Send(sim.EvaluateOrderSweep(at.Add(nailWindow(1) + time.Minute))); err != nil {
		t.Fatalf("EvaluateOrderSweep: %v", err)
	}
	if o := readOrders(t, w)[1]; o.State != sim.OrderStateReady {
		t.Errorf("order.State after a one-unit window = %q, want ready", o.State)
	}
	if alice := readHoldings(t, w, "alice"); alice.Coins != 20 {
		t.Errorf("alice.Coins = %d, want 20 (no refund yet)", alice.Coins)
	}

	// Past the five-unit window it lapses and refunds, as before.
	if _, err := w.Send(sim.EvaluateOrderSweep(at.Add(nailWindow(5) + time.Minute))); err != nil {
		t.Fatalf("EvaluateOrderSweep: %v", err)
	}
	if alice := readHoldings(t, w, "alice"); alice.Coins != 50 {
		t.Errorf("alice.Coins = %d, want 50 (refunded once the scaled window passed)", alice.Coins)
	}
}

// TestAcceptPay_Commission_WindowCountsQueuedOrders: a commission booked behind
// another open commission for the same good waits for those units too.
func TestAcceptPay_Commission_WindowCountsQueuedOrders(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	at := time.Now().UTC()
	commissionOffer(t, w, 1, "nail", 2, 12, at)
	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay 1: %v", err)
	}
	commissionOffer(t, w, 2, "nail", 3, 18, at)
	if _, err := w.Send(sim.AcceptPay("smith", 2, at)); err != nil {
		t.Fatalf("AcceptPay 2: %v", err)
	}

	orders := readOrders(t, w)
	if len(orders) != 2 {
		t.Fatalf("%d orders, want 2: %+v", len(orders), orders)
	}
	for _, o := range orders {
		var want time.Time
		switch o.Qty {
		case 2:
			want = at.Add(nailWindow(2))
		case 3:
			// 2 nails already reserved ahead of it, plus its own 3.
			want = at.Add(nailWindow(5))
		default:
			t.Fatalf("unexpected order %+v", o)
		}
		if !o.ExpiresAt.Equal(want) {
			t.Errorf("order for %d nails: ExpiresAt = %v, want %v", o.Qty, o.ExpiresAt, want)
		}
	}
}

// TestAcceptPay_Commission_WindowCountsOnlyTheShortfall: stock the keeper already
// holds is not forge time, so a commission he can half-fill now gets the window
// of the units still to make.
func TestAcceptPay_Commission_WindowCountsOnlyTheShortfall(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), map[sim.ItemKind]int{"nail": 2}, 50, nil)
	defer stop()
	at := time.Now().UTC()
	commissionOffer(t, w, 1, "nail", 5, 30, at)
	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	o, ok := readOrders(t, w)[1]
	if !ok {
		t.Fatalf("no commission Order minted")
	}
	if want := at.Add(nailWindow(3)); !o.ExpiresAt.Equal(want) {
		t.Errorf("order.ExpiresAt = %v, want %v (3 nails still to forge)", o.ExpiresAt, want)
	}
}

// TestCommissionOrderExpiresAt_BatchesAndCap: units round up to whole batches of
// the recipe's output, a unit count below 1 counts as 1, and an absurd count is
// held at the 7-day cap instead of overflowing.
func TestCommissionOrderExpiresAt_BatchesAndCap(t *testing.T) {
	// Ten per batch, one batch per 2h.
	w := &sim.World{Recipes: map[sim.ItemKind]*sim.ItemRecipe{
		"porridge": {OutputItem: "porridge", OutputQty: 10, RateQty: 5, RatePerHours: 1},
	}}
	cycle := time.Duration(sim.CycleDurationSeconds(w.Recipes["porridge"])) * time.Second
	if cycle != 2*time.Hour {
		t.Fatalf("porridge cycle = %v, want 2h", cycle)
	}
	at := time.Date(2026, 11, 23, 14, 0, 0, 0, time.UTC)
	window := func(batches int) time.Time {
		return at.Add(time.Duration(batches)*cycle*time.Duration(sim.CommissionOrderSlack) + sim.CommissionOrderGrace)
	}
	cases := []struct {
		units int
		want  time.Time
	}{
		{0, window(1)},
		{1, window(1)},
		{10, window(1)},
		{11, window(2)},
		{20, window(2)},
		{21, window(3)},
		{math.MaxInt, at.Add(sim.CommissionOrderWindowMax)},
	}
	for _, c := range cases {
		if got := sim.CommissionOrderExpiresAt(w, "porridge", c.units, at); !got.Equal(c.want) {
			t.Errorf("units %d: ExpiresAt = %v, want %v", c.units, got, c.want)
		}
	}
}
