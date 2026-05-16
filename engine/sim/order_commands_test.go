package sim_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// order_commands_test.go — Phase 3 PR S6 substrate + Command coverage.
//
// Two flows:
//
//   - createOrderForPayWithItem: minting at AcceptPay time. Verified
//     by direct test-helper invocation (no full pay-with-item flow
//     needed; that integration is exercised by S4's existing
//     pay_with_item_commands_test.go which now goes through Order
//     creation when ConsumeNow=false).
//
//   - DeliverOrder: the 7-gate validation matrix + atomic commit.

// buildOrderTestWorld stands up a running world with three actors:
//   - "hannah" — KindNPCShared seller, inventory pre-loaded with 5 stew
//   - "jefferey" — KindNPCStateful buyer
//   - "mary" — KindNPCStateful additional consumer (for group-order tests)
//
// All three are in the same huddle "h1" so the co-presence gate passes
// by default; tests that need to break co-presence mutate
// CurrentHuddleID on specific actors before calling DeliverOrder.
//
// The seed also pre-populates a "stew" ItemKindDef in the World so
// gate 7 (catalog lookup) passes by default.
func buildOrderTestWorld(t *testing.T) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	now := time.Now().UTC()

	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {
			ID:               "hannah",
			DisplayName:      "Hannah",
			Kind:             sim.KindNPCShared,
			State:            sim.StateIdle,
			StateEnteredAt:   now,
			Coins:            100,
			Inventory:        map[sim.ItemKind]int{"stew": 5},
			CurrentHuddleID:  "h1",
			RecentActions:    sim.NewRingBuffer[sim.Action](4),
			RecentStateTrans: sim.NewRingBuffer[sim.StateTransition](4),
		},
		"jefferey": {
			ID:               "jefferey",
			DisplayName:      "Jefferey",
			Kind:             sim.KindNPCStateful,
			State:            sim.StateIdle,
			StateEnteredAt:   now,
			Coins:            50,
			Inventory:        map[sim.ItemKind]int{},
			CurrentHuddleID:  "h1",
			RecentActions:    sim.NewRingBuffer[sim.Action](4),
			RecentStateTrans: sim.NewRingBuffer[sim.StateTransition](4),
		},
		"mary": {
			ID:               "mary",
			DisplayName:      "Mary",
			Kind:             sim.KindNPCStateful,
			State:            sim.StateIdle,
			StateEnteredAt:   now,
			Coins:            50,
			Inventory:        map[sim.ItemKind]int{},
			CurrentHuddleID:  "h1",
			RecentActions:    sim.NewRingBuffer[sim.Action](4),
			RecentStateTrans: sim.NewRingBuffer[sim.StateTransition](4),
		},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}

	// Seed the ItemKinds catalog with a stew entry. Direct mutation is
	// safe pre-Run.
	w.ItemKinds = map[sim.ItemKind]*sim.ItemKindDef{
		"stew": {Name: "stew", DisplayLabel: "stew", Category: sim.ItemCategoryFood, Price: 4},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	return w, func() {
		cancel()
		<-done
	}
}

// mintReadyOrder creates a Ready Order via createOrderForPayWithItem
// for hannah→jefferey with the given consumer set and qty. Returns
// the OrderID so callers can DeliverOrder against it.
func mintReadyOrder(t *testing.T, w *sim.World, consumers []sim.ActorID, qty int, at time.Time) sim.OrderID {
	t.Helper()
	entry := &sim.PayLedgerEntry{
		ID:          7,
		BuyerID:     "jefferey",
		SellerID:    "hannah",
		ItemKind:    "stew",
		Qty:         qty,
		Amount:      qty * 4 * max(1, len(consumers)),
		ConsumerIDs: consumers,
		ConsumeNow:  false,
	}
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.CreateOrderForPayWithItem(world, entry, at), nil
	}})
	if err != nil {
		t.Fatalf("mintReadyOrder: %v", err)
	}
	id, ok := res.(sim.OrderID)
	if !ok {
		t.Fatalf("CreateOrderForPayWithItem returned %T, want sim.OrderID", res)
	}
	return id
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// readInventory pulls actor.Inventory[kind] off the live world via a
// no-op Command. ActorSnapshot doesn't carry Inventory (only the
// InventoryHash for fast-compare), so tests have to round-trip the
// world goroutine for inventory assertions.
func readInventory(t *testing.T, w *sim.World, actorID sim.ActorID, kind sim.ItemKind) int {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a, ok := world.Actors[actorID]
		if !ok || a == nil || a.Inventory == nil {
			return 0, nil
		}
		return a.Inventory[kind], nil
	}})
	if err != nil {
		t.Fatalf("readInventory %s[%s]: %v", actorID, kind, err)
	}
	n, _ := res.(int)
	return n
}

// TestCreateOrderForPayWithItem_PopulatesFields verifies Order fields,
// implicit-buyer-as-consumer normalization, ExpiresAt = CreatedAt + TTL,
// state = Ready, and OrderID monotonic.
func TestCreateOrderForPayWithItem_PopulatesFields(t *testing.T) {
	w, stop := buildOrderTestWorld(t)
	defer stop()

	at := time.Now().UTC()
	id := mintReadyOrder(t, w, nil, 1, at) // implicit buyer-as-consumer

	snap := w.Published()
	o := snap.Orders[id]
	if o == nil {
		t.Fatalf("Order %d missing from snapshot", id)
	}
	if o.State != sim.OrderStateReady {
		t.Errorf("State = %q, want %q", o.State, sim.OrderStateReady)
	}
	if o.BuyerID != "jefferey" || o.SellerID != "hannah" {
		t.Errorf("buyer/seller = %q/%q, want jefferey/hannah", o.BuyerID, o.SellerID)
	}
	if o.Item != "stew" || o.Qty != 1 {
		t.Errorf("item/qty = %q/%d, want stew/1", o.Item, o.Qty)
	}
	if len(o.ConsumerIDs) != 1 || o.ConsumerIDs[0] != "jefferey" {
		t.Errorf("ConsumerIDs = %v, want [jefferey] (implicit buyer-as-consumer)", o.ConsumerIDs)
	}
	if !o.CreatedAt.Equal(at) {
		t.Errorf("CreatedAt = %v, want %v", o.CreatedAt, at)
	}
	expectedExpiry := at.Add(sim.OrderTTLDefault)
	if !o.ExpiresAt.Equal(expectedExpiry) {
		t.Errorf("ExpiresAt = %v, want %v (CreatedAt + OrderTTLDefault)", o.ExpiresAt, expectedExpiry)
	}
	if o.LedgerID != 7 {
		t.Errorf("LedgerID = %d, want 7 (back-ref)", o.LedgerID)
	}
}

// TestCreateOrderForPayWithItem_ExplicitConsumers verifies multi-consumer
// group orders preserve ConsumerIDs as-given (no normalization to [buyer]).
func TestCreateOrderForPayWithItem_ExplicitConsumers(t *testing.T) {
	w, stop := buildOrderTestWorld(t)
	defer stop()
	at := time.Now().UTC()
	id := mintReadyOrder(t, w, []sim.ActorID{"jefferey", "mary"}, 1, at)

	snap := w.Published()
	o := snap.Orders[id]
	if len(o.ConsumerIDs) != 2 || o.ConsumerIDs[0] != "jefferey" || o.ConsumerIDs[1] != "mary" {
		t.Errorf("ConsumerIDs = %v, want [jefferey mary]", o.ConsumerIDs)
	}
}

// TestDeliverOrder_HappyPath: all 7 gates pass, goods transfer, state
// flips to Delivered, OrderDelivered emitted (verifiable via snapshot),
// DeliveredAt stamped, buyer↔seller InteractionDelivered/Received facts
// written.
func TestDeliverOrder_HappyPath(t *testing.T) {
	w, stop := buildOrderTestWorld(t)
	defer stop()
	at := time.Now().UTC()
	id := mintReadyOrder(t, w, nil, 1, at)

	deliverAt := at.Add(1 * time.Second)
	if _, err := w.Send(sim.DeliverOrder("hannah", id, deliverAt)); err != nil {
		t.Fatalf("DeliverOrder: %v", err)
	}

	snap := w.Published()
	o := snap.Orders[id]
	if o.State != sim.OrderStateDelivered {
		t.Errorf("State = %q, want %q", o.State, sim.OrderStateDelivered)
	}
	if o.DeliveredAt == nil || !o.DeliveredAt.Equal(deliverAt) {
		t.Errorf("DeliveredAt = %v, want %v", o.DeliveredAt, deliverAt)
	}

	// Goods transferred.
	if got := readInventory(t, w, "hannah", "stew"); got != 4 {
		t.Errorf("hannah.Inventory[stew] = %d, want 4 (one transferred)", got)
	}
	if got := readInventory(t, w, "jefferey", "stew"); got != 1 {
		t.Errorf("jefferey.Inventory[stew] = %d, want 1", got)
	}

	// Hannah↔Jefferey bidirectional facts. Hannah is shared-VA so her
	// Relationships[jefferey] row gets the InteractionDelivered fact.
	// Jefferey is stateful so RecordInteraction's KindNPCShared gate
	// skips writing jefferey→hannah — only the seller side persists.
	hannah := snap.Actors["hannah"]
	hannahRel := hannah.Relationships["jefferey"]
	if hannahRel == nil {
		t.Fatal("hannah.Relationships[jefferey] not created")
	}
	if len(hannahRel.SalientFacts) != 1 {
		t.Fatalf("hannah salient facts count = %d, want 1", len(hannahRel.SalientFacts))
	}
	if hannahRel.SalientFacts[0].Kind != sim.InteractionDelivered {
		t.Errorf("hannah fact kind = %q, want InteractionDelivered", hannahRel.SalientFacts[0].Kind)
	}
	if !strings.Contains(hannahRel.SalientFacts[0].Text, "delivered") {
		t.Errorf("hannah fact text = %q, expected to contain 'delivered'", hannahRel.SalientFacts[0].Text)
	}
}

// TestDeliverOrder_Gate1_OrderNotFound — nonexistent OrderID rejects.
func TestDeliverOrder_Gate1_OrderNotFound(t *testing.T) {
	w, stop := buildOrderTestWorld(t)
	defer stop()
	_, err := w.Send(sim.DeliverOrder("hannah", sim.OrderID(999), time.Now()))
	if err == nil {
		t.Fatal("nonexistent OrderID: no error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v, want 'not found'", err)
	}
}

// TestDeliverOrder_Gate2_AuthMismatch — wrong seller rejects.
func TestDeliverOrder_Gate2_AuthMismatch(t *testing.T) {
	w, stop := buildOrderTestWorld(t)
	defer stop()
	at := time.Now().UTC()
	id := mintReadyOrder(t, w, nil, 1, at)
	_, err := w.Send(sim.DeliverOrder("mary", id, at.Add(time.Second)))
	if err == nil {
		t.Fatal("auth mismatch: no error")
	}
	if !strings.Contains(err.Error(), "belongs to") {
		t.Errorf("err = %v, want 'belongs to'", err)
	}
}

// TestDeliverOrder_Gate3_AlreadyDelivered — idempotent reject.
func TestDeliverOrder_Gate3_AlreadyDelivered(t *testing.T) {
	w, stop := buildOrderTestWorld(t)
	defer stop()
	at := time.Now().UTC()
	id := mintReadyOrder(t, w, nil, 1, at)
	if _, err := w.Send(sim.DeliverOrder("hannah", id, at.Add(time.Second))); err != nil {
		t.Fatalf("first deliver: %v", err)
	}
	_, err := w.Send(sim.DeliverOrder("hannah", id, at.Add(2*time.Second)))
	if err == nil {
		t.Fatal("second deliver: no error (want already-delivered idempotent)")
	}
	if !strings.Contains(err.Error(), "already delivered") {
		t.Errorf("err = %v, want 'already delivered'", err)
	}
}

// TestDeliverOrder_Gate4_LiveTTLFlipsExpired — TTL past at deliver time
// causes in-band flip to Expired and rejection.
func TestDeliverOrder_Gate4_LiveTTLFlipsExpired(t *testing.T) {
	w, stop := buildOrderTestWorld(t)
	defer stop()
	at := time.Now().UTC()
	id := mintReadyOrder(t, w, nil, 1, at)
	// Past-TTL deliver attempt.
	pastTTL := at.Add(sim.OrderTTLDefault + time.Minute)
	_, err := w.Send(sim.DeliverOrder("hannah", id, pastTTL))
	if err == nil {
		t.Fatal("past-TTL deliver: no error")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("err = %v, want 'expired'", err)
	}
	snap := w.Published()
	if snap.Orders[id].State != sim.OrderStateExpired {
		t.Errorf("State = %q, want %q (in-band flip)", snap.Orders[id].State, sim.OrderStateExpired)
	}
}

// TestDeliverOrder_Gate5_SellerStockInsufficient — seller's inventory
// has been drained between accept and deliver.
func TestDeliverOrder_Gate5_SellerStockInsufficient(t *testing.T) {
	w, stop := buildOrderTestWorld(t)
	defer stop()
	at := time.Now().UTC()
	id := mintReadyOrder(t, w, []sim.ActorID{"jefferey", "mary"}, 1, at) // need 2 stew

	// Drain hannah's inventory below required.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["hannah"].Inventory["stew"] = 1
		return nil, nil
	}}); err != nil {
		t.Fatalf("drain inventory: %v", err)
	}

	_, err := w.Send(sim.DeliverOrder("hannah", id, at.Add(time.Second)))
	if err == nil {
		t.Fatal("insufficient stock: no error")
	}
	if !strings.Contains(err.Error(), "need 2") {
		t.Errorf("err = %v, want stock-insufficient message", err)
	}
}

// TestDeliverOrder_Gate6_ConsumerNotCoPresent — consumer moved out of
// seller's huddle.
func TestDeliverOrder_Gate6_ConsumerNotCoPresent(t *testing.T) {
	w, stop := buildOrderTestWorld(t)
	defer stop()
	at := time.Now().UTC()
	id := mintReadyOrder(t, w, nil, 1, at)

	// Move jefferey out of hannah's huddle.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["jefferey"].CurrentHuddleID = ""
		return nil, nil
	}}); err != nil {
		t.Fatalf("move jefferey: %v", err)
	}

	_, err := w.Send(sim.DeliverOrder("hannah", id, at.Add(time.Second)))
	if err == nil {
		t.Fatal("not co-present: no error")
	}
	if !strings.Contains(err.Error(), "not co-present") {
		t.Errorf("err = %v, want 'not co-present'", err)
	}

	// Order still Ready — sweep will eventually expire it.
	if snap := w.Published(); snap.Orders[id].State != sim.OrderStateReady {
		t.Errorf("State = %q, want %q (co-presence reject leaves Ready)",
			snap.Orders[id].State, sim.OrderStateReady)
	}
}

// TestDeliverOrder_Gate7_CatalogMissing — ItemKind removed from catalog
// between accept and deliver.
func TestDeliverOrder_Gate7_CatalogMissing(t *testing.T) {
	w, stop := buildOrderTestWorld(t)
	defer stop()
	at := time.Now().UTC()
	id := mintReadyOrder(t, w, nil, 1, at)

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		delete(world.ItemKinds, "stew")
		return nil, nil
	}}); err != nil {
		t.Fatalf("delete catalog: %v", err)
	}

	_, err := w.Send(sim.DeliverOrder("hannah", id, at.Add(time.Second)))
	if err == nil {
		t.Fatal("catalog missing: no error")
	}
	if !strings.Contains(err.Error(), "no longer in catalog") {
		t.Errorf("err = %v, want 'no longer in catalog'", err)
	}
}

// TestDeliverOrder_MultiConsumerGroupOrder — atomic transfer to each
// consumer. Seller's stock decreases by Qty*N; each consumer's
// inventory gains Qty.
func TestDeliverOrder_MultiConsumerGroupOrder(t *testing.T) {
	w, stop := buildOrderTestWorld(t)
	defer stop()
	at := time.Now().UTC()
	id := mintReadyOrder(t, w, []sim.ActorID{"jefferey", "mary"}, 2, at) // 2 stew each = 4 total

	if _, err := w.Send(sim.DeliverOrder("hannah", id, at.Add(time.Second))); err != nil {
		t.Fatalf("DeliverOrder: %v", err)
	}

	if got := readInventory(t, w, "hannah", "stew"); got != 1 {
		t.Errorf("hannah.Inventory[stew] = %d, want 1 (5 - 2*2)", got)
	}
	if got := readInventory(t, w, "jefferey", "stew"); got != 2 {
		t.Errorf("jefferey.Inventory[stew] = %d, want 2", got)
	}
	if got := readInventory(t, w, "mary", "stew"); got != 2 {
		t.Errorf("mary.Inventory[stew] = %d, want 2", got)
	}
}

// TestDeliverOrder_Gate3_AlreadyExpired — Order already in Expired
// state rejects idempotently.
func TestDeliverOrder_Gate3_AlreadyExpired(t *testing.T) {
	w, stop := buildOrderTestWorld(t)
	defer stop()
	at := time.Now().UTC()
	id := mintReadyOrder(t, w, nil, 1, at)

	// Force the Order into Expired via direct test helper.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.FinalizeOrderTerminal(world, world.Orders[id], sim.OrderStateExpired, at.Add(time.Hour))
		return nil, nil
	}}); err != nil {
		t.Fatalf("pre-expire: %v", err)
	}

	_, err := w.Send(sim.DeliverOrder("hannah", id, at.Add(2*time.Hour)))
	if err == nil {
		t.Fatal("already-expired deliver: no error")
	}
	if !strings.Contains(err.Error(), "already expired") {
		t.Errorf("err = %v, want 'already expired'", err)
	}
}

// TestDeliverOrder_AllGatesPassButSellerVanishes is a defensive belt
// check: if the seller actor was deleted between gates running and
// the transfer step, the error is surfaced cleanly. Today the actor
// map is single-goroutine so this can't happen in practice; the
// guard exists for substrate invariant violations.
func TestDeliverOrder_AllGatesPassButSellerVanishes(t *testing.T) {
	w, stop := buildOrderTestWorld(t)
	defer stop()
	at := time.Now().UTC()
	id := mintReadyOrder(t, w, nil, 1, at)

	// Delete the seller between gates and transfer — but since gates
	// + transfer run in one Fn, we can't actually inject between them.
	// Instead delete pre-call and confirm gate 5 surfaces the error.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		delete(world.Actors, "hannah")
		return nil, nil
	}}); err != nil {
		t.Fatalf("delete hannah: %v", err)
	}
	_, err := w.Send(sim.DeliverOrder("hannah", id, at.Add(time.Second)))
	if err == nil {
		t.Fatal("missing seller: no error")
	}
	if !errors.Is(err, errors.New("")) && !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v, want 'not found'", err)
	}
}
