package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// order_readyby_test.go — ZBBS-HOME-403 coverage for lodging advance booking
// (ready_in_days → ReadyBy), the lodging-aware Order ExpiresAt, and the
// refund-on-expiry that closes the deferred-path takeaway robbery.

// newReadyByWorld builds a minimal world with a lodging item (nights_stay) and
// an ordinary item (stew), plus the lodging settings the helpers read. No Run
// goroutine — the tests call the substrate helpers directly (single-threaded).
func newReadyByWorld(t *testing.T) *sim.World {
	t.Helper()
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)
	w.ItemKinds["nights_stay"] = &sim.ItemKindDef{
		Name:         "nights_stay",
		Capabilities: []string{"service", "lodging"},
	}
	w.ItemKinds["stew"] = &sim.ItemKindDef{Name: "stew"}
	w.Settings.LodgingCheckOutHour = 11
	w.Settings.Location = time.UTC
	return w
}

func midnightUTC(at time.Time) time.Time {
	u := at.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

func TestResolveOrderReadyBy(t *testing.T) {
	w := newReadyByWorld(t)
	at := time.Date(2026, 6, 6, 14, 30, 0, 0, time.UTC)
	today := midnightUTC(at)

	t.Run("today default for same-day order", func(t *testing.T) {
		got, err := sim.ResolveOrderReadyBy(w, "stew", 0, 0, at)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.Equal(today) {
			t.Errorf("ReadyBy = %v, want today %v", got, today)
		}
	})

	t.Run("lodging advance booking adds days", func(t *testing.T) {
		got, err := sim.ResolveOrderReadyBy(w, "nights_stay", 0, 3, at)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := today.AddDate(0, 0, 3)
		if !got.Equal(want) {
			t.Errorf("ReadyBy = %v, want %v", got, want)
		}
	})

	t.Run("advance booking rejected for non-lodging item", func(t *testing.T) {
		_, err := sim.ResolveOrderReadyBy(w, "stew", 0, 2, at)
		if err == nil {
			t.Fatal("expected error for ready_in_days on a non-lodging item")
		}
	})

	t.Run("days beyond cap rejected", func(t *testing.T) {
		_, err := sim.ResolveOrderReadyBy(w, "nights_stay", 0, sim.MaxOrderReadyInDays+1, at)
		if err == nil {
			t.Fatal("expected error for ready_in_days beyond the cap")
		}
	})

	t.Run("counter-response carries the parent's future booked date (lodging)", func(t *testing.T) {
		booked := today.AddDate(0, 0, 5)
		w.PayLedger[42] = &sim.PayLedgerEntry{ID: 42, ReadyBy: booked}
		// days=0 (haggle left ready_in_days out) → carry the parent's date.
		got, err := sim.ResolveOrderReadyBy(w, "nights_stay", 42, 0, at)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.Equal(booked) {
			t.Errorf("ReadyBy = %v, want parent's %v", got, booked)
		}
	})

	t.Run("carried future date rejected for non-lodging item", func(t *testing.T) {
		booked := today.AddDate(0, 0, 5)
		w.PayLedger[43] = &sim.PayLedgerEntry{ID: 43, ReadyBy: booked}
		// A counter that swaps the lodging booking for a non-lodging item must
		// not inherit the future date.
		if _, err := sim.ResolveOrderReadyBy(w, "stew", 43, 0, at); err == nil {
			t.Fatal("expected error carrying a future date onto a non-lodging item")
		}
	})

	t.Run("carried same-day date is fine on any item", func(t *testing.T) {
		w.PayLedger[44] = &sim.PayLedgerEntry{ID: 44, ReadyBy: today}
		got, err := sim.ResolveOrderReadyBy(w, "stew", 44, 0, at)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.Equal(today) {
			t.Errorf("ReadyBy = %v, want today %v", got, today)
		}
	})
}

func TestCreateOrderForPayWithItem_LodgingExpiryFromReadyBy(t *testing.T) {
	w := newReadyByWorld(t)
	at := time.Date(2026, 6, 6, 20, 0, 0, 0, time.UTC)
	readyBy := midnightUTC(at).AddDate(0, 0, 2) // booked two days ahead

	entry := &sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		ItemKind: "nights_stay", Qty: 2, Amount: 28,
		ConsumerIDs: []sim.ActorID{"alice"}, ReadyBy: readyBy,
	}
	id := sim.CreateOrderForPayWithItem(w, entry, at)
	o := w.Orders[id]
	if o == nil {
		t.Fatal("order not minted")
	}
	if !o.ReadyBy.Equal(readyBy) {
		t.Errorf("Order.ReadyBy = %v, want %v", o.ReadyBy, readyBy)
	}
	// A lodging order's deadline is the check-in deadline derived from ReadyBy
	// (the morning after the booked night), NOT the short takeaway TTL.
	wantExpiry := sim.ComputeLodgerUntil(readyBy, 1, 11, time.UTC)
	if !o.ExpiresAt.Equal(wantExpiry) {
		t.Errorf("Order.ExpiresAt = %v, want %v (ComputeLodgerUntil from ReadyBy)", o.ExpiresAt, wantExpiry)
	}
	ttlExpiry := at.Add(sim.EffectiveOrderTTL(w.Settings))
	if o.ExpiresAt.Equal(ttlExpiry) {
		t.Error("Order.ExpiresAt fell back to the flat TTL — a future booking would expire immediately")
	}
}

func TestCreateOrderForPayWithItem_NonLodgingDefaultsReadyByToToday(t *testing.T) {
	w := newReadyByWorld(t)
	at := time.Date(2026, 6, 6, 9, 15, 0, 0, time.UTC)

	entry := &sim.PayLedgerEntry{
		ID: 2, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 5,
		ConsumerIDs: []sim.ActorID{"alice"}, // ReadyBy left zero
	}
	id := sim.CreateOrderForPayWithItem(w, entry, at)
	o := w.Orders[id]
	if o == nil {
		t.Fatal("order not minted")
	}
	if !o.ReadyBy.Equal(midnightUTC(at)) {
		t.Errorf("Order.ReadyBy = %v, want today %v", o.ReadyBy, midnightUTC(at))
	}
	// An ordinary order keeps the flat TTL from creation (unchanged behavior).
	wantExpiry := at.Add(sim.EffectiveOrderTTL(w.Settings))
	if !o.ExpiresAt.Equal(wantExpiry) {
		t.Errorf("Order.ExpiresAt = %v, want %v (created + TTL)", o.ExpiresAt, wantExpiry)
	}
}

func TestFinalizeOrderTerminal_ExpiredRefundsBuyer(t *testing.T) {
	w := newReadyByWorld(t)
	at := time.Now().UTC()
	w.Actors["alice"] = &sim.Actor{ID: "alice", Coins: 5}
	w.Actors["bob"] = &sim.Actor{ID: "bob", Coins: 30}

	o := &sim.Order{
		ID: 1, State: sim.OrderStateReady,
		BuyerID: "alice", SellerID: "bob",
		Item: "nights_stay", Qty: 1, Amount: 20,
		ConsumerIDs: []sim.ActorID{"alice"},
		ExpiresAt:   at.Add(-time.Minute),
	}
	w.Orders[o.ID] = o

	sim.FinalizeOrderTerminal(w, o, sim.OrderStateExpired, at)

	if got := w.Actors["alice"].Coins; got != 25 {
		t.Errorf("buyer coins = %d, want 25 (5 + 20 refund)", got)
	}
	if got := w.Actors["bob"].Coins; got != 10 {
		t.Errorf("seller coins = %d, want 10 (30 - 20 reversed)", got)
	}
}

func TestFinalizeOrderTerminal_DeliveredDoesNotRefund(t *testing.T) {
	w := newReadyByWorld(t)
	at := time.Now().UTC()
	w.Actors["alice"] = &sim.Actor{ID: "alice", Coins: 5}
	w.Actors["bob"] = &sim.Actor{ID: "bob", Coins: 30}

	o := &sim.Order{
		ID: 1, State: sim.OrderStateReady,
		BuyerID: "alice", SellerID: "bob",
		Item: "nights_stay", Qty: 1, Amount: 20,
		ConsumerIDs: []sim.ActorID{"alice"},
	}
	w.Orders[o.ID] = o

	sim.FinalizeOrderTerminal(w, o, sim.OrderStateDelivered, at)

	if got := w.Actors["alice"].Coins; got != 5 {
		t.Errorf("buyer coins = %d, want 5 (delivered — no refund)", got)
	}
	if got := w.Actors["bob"].Coins; got != 30 {
		t.Errorf("seller coins = %d, want 30 (delivered — no refund)", got)
	}
}
