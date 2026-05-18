package mem_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// TestRoundTrip_OrderClonesBreakAliasing — Seed → LoadAll → mutate →
// SaveSnapshot → LoadAll preserves values but produces fresh Orders.
// Same posture as the other mem roundtrip tests; the aliasing check
// is the load-bearing assertion (catches missing CloneOrder calls in
// the repo's hot paths).
func TestRoundTrip_OrderClonesBreakAliasing(t *testing.T) {
	ctx := context.Background()
	_, h := mem.NewRepository()

	now := time.Now().UTC()
	delivered := now.Add(30 * time.Second)
	expires := now.Add(15 * time.Minute)

	seed := map[sim.OrderID]*sim.Order{
		1: {
			ID:          1,
			State:       sim.OrderStateReady,
			BuyerID:     "alice",
			SellerID:    "bob",
			Item:        "stew",
			Qty:         2,
			Amount:      6,
			ConsumerIDs: []sim.ActorID{"alice", "carol"},
			LedgerID:    1,
			CreatedAt:   now,
			ExpiresAt:   expires,
		},
		2: {
			ID:          2,
			State:       sim.OrderStateDelivered,
			BuyerID:     "dave",
			SellerID:    "bob",
			Item:        "ale",
			Qty:         1,
			Amount:      3,
			ConsumerIDs: nil,
			LedgerID:    2,
			CreatedAt:   now,
			DeliveredAt: &delivered,
			ExpiresAt:   expires,
		},
	}
	h.Orders.Seed(seed)

	// Mutate the seed map post-Seed. The repo must not have aliased
	// our pointers — its internal copy should still hold the original
	// values.
	seed[1].State = sim.OrderStateExpired
	seed[1].Qty = 999
	seed[1].ConsumerIDs[0] = "tampered"

	loaded, err := h.Orders.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if got := loaded[1].State; got != sim.OrderStateReady {
		t.Errorf("loaded[1].State = %q, want ready (seed aliased!)", got)
	}
	if got := loaded[1].Qty; got != 2 {
		t.Errorf("loaded[1].Qty = %d, want 2 (seed aliased!)", got)
	}
	if got := loaded[1].ConsumerIDs[0]; got != "alice" {
		t.Errorf("loaded[1].ConsumerIDs[0] = %q, want alice (consumer slice aliased!)", got)
	}

	// LoadAll'd map must itself be a deep copy — caller mutating it
	// cannot affect the repo's internal state.
	loaded[1].State = sim.OrderStateExpired
	loaded[1].ConsumerIDs[0] = "tampered2"
	loaded2, _ := h.Orders.LoadAll(ctx)
	if got := loaded2[1].State; got != sim.OrderStateReady {
		t.Errorf("loaded2[1].State = %q, want ready (LoadAll output aliased!)", got)
	}
	if got := loaded2[1].ConsumerIDs[0]; got != "alice" {
		t.Errorf("loaded2[1].ConsumerIDs[0] = %q, want alice (LoadAll consumer slice aliased!)", got)
	}

	// SaveSnapshot replaces the map wholesale. Drop order 2, change
	// order 1's state, add order 3. After save, LoadAll should mirror
	// the snapshot exactly.
	snap := map[sim.OrderID]*sim.Order{
		1: {
			ID:        1,
			State:     sim.OrderStateDelivered,
			BuyerID:   "alice",
			SellerID:  "bob",
			Item:      "stew",
			Qty:       2,
			Amount:    6,
			LedgerID:  1,
			CreatedAt: now,
			ExpiresAt: expires,
		},
		3: {
			ID:        3,
			State:     sim.OrderStateReady,
			BuyerID:   "eve",
			SellerID:  "bob",
			Item:      "bread",
			Qty:       1,
			Amount:    2,
			LedgerID:  3,
			CreatedAt: now,
			ExpiresAt: expires,
		},
	}
	if err := h.Orders.SaveSnapshot(ctx, nil, snap); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	post, err := h.Orders.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll post-save: %v", err)
	}
	if len(post) != 2 {
		t.Fatalf("post-save len = %d, want 2 (snapshot replaces wholesale)", len(post))
	}
	if _, ok := post[2]; ok {
		t.Error("order 2 still present after snapshot replaced it")
	}
	if got := post[1].State; got != sim.OrderStateDelivered {
		t.Errorf("post-save loaded[1].State = %q, want delivered", got)
	}
	if got := post[3].Item; got != sim.ItemKind("bread") {
		t.Errorf("post-save loaded[3].Item = %q, want bread", got)
	}
}

// TestLoadRecentPrices_FiltersBySinceAndCaps verifies that mem's
// LoadRecentPrices implementation matches the pg contract: rows
// older than `since` are filtered out, per-(seller, item) bucket
// capped at perKeyCap most-recent entries, returned chronologically
// (oldest first) per key.
func TestLoadRecentPrices_FiltersBySinceAndCaps(t *testing.T) {
	_, h := mem.NewRepository()

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	k := sim.PriceBookKey{SellerID: "bob", Item: "ale"}

	h.Orders.SeedPrices([]sim.PriceBookSeedRecord{
		{Key: k, Observation: sim.PriceObservation{BuyerID: "ancient", Amount: 1, Qty: 1, Consumers: 1, At: base.Add(-100 * 24 * time.Hour)}},
		{Key: k, Observation: sim.PriceObservation{BuyerID: "alice", Amount: 2, Qty: 1, Consumers: 1, At: base.Add(-5 * 24 * time.Hour)}},
		{Key: k, Observation: sim.PriceObservation{BuyerID: "alice", Amount: 3, Qty: 1, Consumers: 1, At: base.Add(-3 * 24 * time.Hour)}},
		{Key: k, Observation: sim.PriceObservation{BuyerID: "carol", Amount: 2, Qty: 1, Consumers: 1, At: base.Add(-2 * 24 * time.Hour)}},
		{Key: k, Observation: sim.PriceObservation{BuyerID: "alice", Amount: 3, Qty: 1, Consumers: 1, At: base.Add(-1 * 24 * time.Hour)}}, // newest
		// Different seller-item key, separately capped.
		{Key: sim.PriceBookKey{SellerID: "joe", Item: "bread"}, Observation: sim.PriceObservation{BuyerID: "alice", Amount: 1, Qty: 1, Consumers: 1, At: base.Add(-2 * 24 * time.Hour)}},
	})

	since := base.Add(-30 * 24 * time.Hour) // filters out the "ancient" entry
	got, err := h.Orders.LoadRecentPrices(context.Background(), since, 3)
	if err != nil {
		t.Fatalf("LoadRecentPrices: %v", err)
	}

	// Expect (bob, ale) capped at 3 entries (the 4 in-window ones minus the oldest)
	// plus (joe, bread) at 1 entry.
	if len(got) != 4 {
		t.Fatalf("got %d records, want 4 (3 for bob/ale + 1 for joe/bread)", len(got))
	}

	// Bucket (bob, ale): per-key oldest-first ordering.
	aleBucket := []sim.PriceBookSeedRecord{}
	for _, r := range got {
		if r.Key == k {
			aleBucket = append(aleBucket, r)
		}
	}
	if len(aleBucket) != 3 {
		t.Fatalf("bob/ale bucket has %d entries, want 3", len(aleBucket))
	}
	// "ancient" filtered, oldest in-window "alice@-5d" capped out, leaving
	// alice@-3d, carol@-2d, alice@-1d.
	expectedBuyers := []sim.ActorID{"alice", "carol", "alice"}
	for i, b := range expectedBuyers {
		if aleBucket[i].Observation.BuyerID != b {
			t.Errorf("bob/ale[%d].BuyerID = %q, want %q", i, aleBucket[i].Observation.BuyerID, b)
		}
	}
	// And chronological order within bucket.
	if !aleBucket[0].Observation.At.Before(aleBucket[2].Observation.At) {
		t.Errorf("bob/ale bucket not in chronological order: %+v", aleBucket)
	}
}

func TestLoadRecentPrices_InvalidPerKeyCapReturnsEmpty(t *testing.T) {
	_, h := mem.NewRepository()
	h.Orders.SeedPrices([]sim.PriceBookSeedRecord{
		{Key: sim.PriceBookKey{SellerID: "bob", Item: "ale"}, Observation: sim.PriceObservation{BuyerID: "alice", Amount: 2, At: time.Now()}},
	})
	got, err := h.Orders.LoadRecentPrices(context.Background(), time.Time{}, 0)
	if err != nil {
		t.Errorf("expected nil error for perKeyCap=0, got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil records, got %v", got)
	}
}
