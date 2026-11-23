package pg

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// order_lapsed_boot_integration_test.go — LLM-994. An order that lapses while
// the engine is down is expired by restartExpirePendingOrders inside
// FinalizeLoad. Its durable order_lapsed rows must reach agent_action_log, which
// needs the action-log sink on the World before LoadWorld finalizes. Wired here
// as cmd/engine wires it: repo.ActionLog set before pg.LoadWorld.

// drainActionLog flushes everything the sink has buffered. Run returns once ctx
// is cancelled, after a final drain — a cancelled ctx makes it a synchronous
// flush.
func drainActionLog(ctx context.Context, sink *ActionLogRepo) {
	drained, cancel := context.WithCancel(ctx)
	cancel()
	sink.Run(drained)
}

// lapsedRowCount counts the order_lapsed rows for one actor and order.
func lapsedRowCount(t *testing.T, f *integrationFixture, actorID sim.ActorID, orderID sim.OrderID) int {
	t.Helper()
	var n int
	if err := f.Pool.QueryRow(t.Context(), `
		SELECT count(*) FROM agent_action_log
		 WHERE actor_id = $1 AND action_type = 'order_lapsed' AND payload->>'order_id' = $2`,
		string(actorID), strconv.FormatUint(uint64(orderID), 10),
	).Scan(&n); err != nil {
		t.Fatalf("count order_lapsed rows for %s: %v", actorID, err)
	}
	return n
}

func TestIntegration_OrderLapsedWhileDown_WritesDurableRowsAtBoot(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := NewRepository(f.Pool)

	const (
		buyerID  = "11111111-aaaa-4aaa-8aaa-111111111111"
		sellerID = "22222222-bbbb-4bbb-8bbb-222222222222"
		orderID  = sim.OrderID(1)
		price    = 6
	)
	// pay_ledger.item_kind FKs to item_kind(name).
	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO item_kind (name, display_label, category) VALUES ('nail', 'Nail', 'material')`,
	); err != nil {
		t.Fatalf("seed item_kind: %v", err)
	}

	// The world as the last checkpoint before the downtime left it: the buyer
	// has paid for a nail at accept (the coins already sit in the seller's
	// purse), and the order's window closed an hour ago, while nothing ran.
	w := checkpointableWorld(repo)
	acceptedAt := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Microsecond)
	w.Actors = map[sim.ActorID]*sim.Actor{
		buyerID:  {ID: buyerID, DisplayName: "Alice Porter", Kind: sim.KindNPCShared, LLMAgent: sim.VendorAgentName, State: sim.StateIdle, Coins: 10, Inventory: map[sim.ItemKind]int{}},
		sellerID: {ID: sellerID, DisplayName: "John Smith", Kind: sim.KindNPCShared, LLMAgent: sim.VendorAgentName, State: sim.StateIdle, Coins: 20, Inventory: map[sim.ItemKind]int{}},
	}
	w.Orders = map[sim.OrderID]*sim.Order{
		orderID: {
			ID:          orderID,
			LedgerID:    sim.LedgerID(orderID),
			State:       sim.OrderStateReady,
			BuyerID:     buyerID,
			SellerID:    sellerID,
			Item:        "nail",
			Qty:         1,
			Amount:      price,
			ConsumerIDs: []sim.ActorID{buyerID},
			CreatedAt:   acceptedAt,
			ReadyBy:     time.Date(acceptedAt.Year(), acceptedAt.Month(), acceptedAt.Day(), 0, 0, 0, 0, time.UTC),
			ExpiresAt:   time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond),
		},
	}
	if err := SaveWorld(ctx, repo, w.BuildCheckpointSnapshot()); err != nil {
		t.Fatalf("SaveWorld (before downtime): %v", err)
	}

	// Boot, wired as cmd/engine does it: the sink goes on the repo before load,
	// and its writer goroutine is not running yet.
	sink := NewActionLogRepo(f.Pool)
	repo.ActionLog = sink
	loaded, err := LoadWorld(ctx, repo, true /*requireAllImpl*/)
	if err != nil {
		t.Fatalf("LoadWorld (boot): %v", err)
	}
	drainActionLog(ctx, sink)

	if o := loaded.Orders[orderID]; o == nil || o.State != sim.OrderStateExpired {
		t.Fatalf("order after boot = %+v, want it expired by the restart pass", o)
	}
	if got := loaded.Actors[buyerID].Coins; got != 10+price {
		t.Errorf("buyer coins after boot = %d, want %d (refunded)", got, 10+price)
	}
	if got := loaded.Actors[sellerID].Coins; got != 20-price {
		t.Errorf("seller coins after boot = %d, want %d", got, 20-price)
	}
	for _, id := range []sim.ActorID{buyerID, sellerID} {
		if n := lapsedRowCount(t, f, id, orderID); n != 1 {
			t.Errorf("order_lapsed rows for %s after boot = %d, want 1", id, n)
		}
	}
	var refunded, counterparty string
	if err := f.Pool.QueryRow(ctx, `
		SELECT payload->>'refunded', payload->>'counterparty' FROM agent_action_log
		 WHERE actor_id = $1 AND action_type = 'order_lapsed'`, buyerID,
	).Scan(&refunded, &counterparty); err != nil {
		t.Fatalf("read the buyer's order_lapsed row: %v", err)
	}
	if refunded != "6" || counterparty != "John Smith" {
		t.Errorf("buyer row refunded %q counterparty %q, want 6 / John Smith", refunded, counterparty)
	}

	// The first checkpoint after boot saves the Expired order with the refunded
	// purses. A second boot finds nothing to expire and writes no second row.
	if err := SaveWorld(ctx, repo, loaded.BuildCheckpointSnapshot()); err != nil {
		t.Fatalf("SaveWorld (first checkpoint after boot): %v", err)
	}
	sink2 := NewActionLogRepo(f.Pool)
	repo.ActionLog = sink2
	reloaded, err := LoadWorld(ctx, repo, true /*requireAllImpl*/)
	if err != nil {
		t.Fatalf("LoadWorld (second boot): %v", err)
	}
	drainActionLog(ctx, sink2)

	if _, ok := reloaded.Orders[orderID]; ok {
		t.Errorf("order %d reloaded after its Expired checkpoint, want it gone from the in-flight set", orderID)
	}
	if got := reloaded.Actors[buyerID].Coins; got != 10+price {
		t.Errorf("buyer coins after second boot = %d, want %d (refunded once)", got, 10+price)
	}
	for _, id := range []sim.ActorID{buyerID, sellerID} {
		if n := lapsedRowCount(t, f, id, orderID); n != 1 {
			t.Errorf("order_lapsed rows for %s after second boot = %d, want 1 (no duplicate)", id, n)
		}
	}
}
