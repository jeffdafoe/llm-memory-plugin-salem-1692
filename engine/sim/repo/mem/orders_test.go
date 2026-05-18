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
