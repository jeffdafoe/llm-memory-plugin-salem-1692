package sim_test

import (
	"context"
	"errors"
	"math"
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

	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {
			ID:              "hannah",
			DisplayName:     "Hannah",
			Kind:            sim.KindNPCShared,
			State:           sim.StateIdle,
			Coins:           100,
			Inventory:       map[sim.ItemKind]int{"stew": 5},
			CurrentHuddleID: "h1",
			RecentActions:   sim.NewRingBuffer[sim.Action](4),
		},
		"jefferey": {
			ID:              "jefferey",
			DisplayName:     "Jefferey",
			Kind:            sim.KindNPCStateful,
			State:           sim.StateIdle,
			Coins:           50,
			Inventory:       map[sim.ItemKind]int{},
			CurrentHuddleID: "h1",
			RecentActions:   sim.NewRingBuffer[sim.Action](4),
		},
		"mary": {
			ID:              "mary",
			DisplayName:     "Mary",
			Kind:            sim.KindNPCStateful,
			State:           sim.StateIdle,
			Coins:           50,
			Inventory:       map[sim.ItemKind]int{},
			CurrentHuddleID: "h1",
			RecentActions:   sim.NewRingBuffer[sim.Action](4),
		},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}

	// Seed the ItemKinds catalog with a stew entry. Direct mutation is
	// safe pre-Run.
	w.ItemKinds = map[sim.ItemKind]*sim.ItemKindDef{
		"stew": {Name: "stew", DisplayLabel: "stew", Category: sim.ItemCategoryFood},
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

// mintReadyOrder creates a Ready Order via createOrderForPayWithItem for
// hannah→jefferey with the given consumer set and qty. Returns the OrderID
// so callers can DeliverOrder against it. The pay_ledger id is chosen from
// this world's current orders (Order.ID == LedgerID, ZBBS-HOME-394, so ids
// must be distinct) — scoped per-world, so no shared/global counter leaks
// state across tests.
func mintReadyOrder(t *testing.T, w *sim.World, consumers []sim.ActorID, qty int, at time.Time) sim.OrderID {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		var nextID sim.LedgerID = 1
		for id := range world.Orders {
			if sim.LedgerID(id) >= nextID {
				nextID = sim.LedgerID(id) + 1
			}
		}
		entry := &sim.PayLedgerEntry{
			ID:          nextID,
			BuyerID:     "jefferey",
			SellerID:    "hannah",
			ItemKind:    "stew",
			Qty:         qty,
			Amount:      qty * 4 * max(1, len(consumers)),
			ConsumerIDs: consumers,
			ConsumeNow:  false,
		}
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
	// ZBBS-HOME-394: an Order IS its pay_ledger row — Order.ID == LedgerID
	// == the originating entry's id (also the snapshot key `id`). This is the
	// invariant the checkpoint enforces (pg orders SaveSnapshot).
	if sim.OrderID(o.LedgerID) != o.ID {
		t.Errorf("Order.ID/LedgerID = %d/%d, want equal (pay_ledger row identity)", o.ID, o.LedgerID)
	}
	if o.LedgerID != sim.LedgerID(id) {
		t.Errorf("LedgerID = %d, want %d (the minted id)", o.LedgerID, id)
	}
}

// TestFinalizeLoad_SeedsLedgerSeqFromMaxLedgerID — ZBBS-HOME-394. The
// LedgerID allocator must start above every id ever persisted, or a
// post-restart mint would reuse an id and the checkpoint upsert would clobber
// an unrelated historical row. FinalizeLoad seeds payLedgerSeq from
// repo.Orders.MaxLedgerID — NOT from w.Orders (which holds only the in-flight
// subset and is empty on the mem load path), so a high terminal-row id is
// still covered. Seed a Delivered (terminal) order at id 50 and assert the
// counter floors to it.
func TestFinalizeLoad_SeedsLedgerSeqFromMaxLedgerID(t *testing.T) {
	repo, handles := mem.NewRepository()
	at := time.Now().UTC()
	handles.Orders.Seed(map[sim.OrderID]*sim.Order{
		50: {
			ID: 50, LedgerID: 50, State: sim.OrderStateDelivered,
			BuyerID: "b", SellerID: "s", Item: "stew", Qty: 1,
			ConsumerIDs: []sim.ActorID{"b"}, CreatedAt: at, ExpiresAt: at.Add(time.Hour),
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	if got := sim.PayLedgerSeqForTest(w); got != 50 {
		t.Errorf("payLedgerSeq = %d, want 50 (floored from repo MaxLedgerID so the next mint can't reuse id 50)", got)
	}
}

// TestFinalizeLoad_SeedsLedgerSeqFromActionLogLedgerID — LLM-245. v2
// consume_now settlements mint a LedgerID but write NO pay_ledger row; their id
// survives only in the `paid` action-log payload. So the pay_ledger max can run
// BELOW the true high-water mark, and without the action-log floor a restart
// re-mints an already-referenced id and corrupts LLM-105's audit join. Seed a
// terminal order at id 50 but an action-log max of 200 (a consume_now id with
// no pay_ledger row) and assert the counter floors to the GREATER — 200.
func TestFinalizeLoad_SeedsLedgerSeqFromActionLogLedgerID(t *testing.T) {
	repo, handles := mem.NewRepository()
	at := time.Now().UTC()
	handles.Orders.Seed(map[sim.OrderID]*sim.Order{
		50: {
			ID: 50, LedgerID: 50, State: sim.OrderStateDelivered,
			BuyerID: "b", SellerID: "s", Item: "stew", Qty: 1,
			ConsumerIDs: []sim.ActorID{"b"}, CreatedAt: at, ExpiresAt: at.Add(time.Hour),
		},
	})
	handles.Orders.SeedPaidActionLogMax(200)
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	if got := sim.PayLedgerSeqForTest(w); got != 200 {
		t.Errorf("payLedgerSeq = %d, want 200 (floored from the consume_now action-log id so the next mint can't reuse it)", got)
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

// TestOutstandingReadyOrderQty_SumsObligations verifies the reservation
// accounting helper that prevents over-selling.
func TestOutstandingReadyOrderQty_SumsObligations(t *testing.T) {
	w, stop := buildOrderTestWorld(t)
	defer stop()
	at := time.Now().UTC()

	// No orders → 0.
	if got, _ := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.OutstandingReadyOrderQty(world, "hannah", "stew"), nil
	}}); got.(int) != 0 {
		t.Errorf("empty world: outstandingReadyOrderQty = %d, want 0", got)
	}

	// Mint two Ready orders + one Delivered (should not count).
	mintReadyOrder(t, w, nil, 1, at)                               // qty 1, 1 consumer = 1
	mintReadyOrder(t, w, []sim.ActorID{"jefferey", "mary"}, 2, at) // qty 2, 2 consumers = 4
	deliveredID := mintReadyOrder(t, w, nil, 1, at)                // will flip to Delivered
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.FinalizeOrderTerminal(world, world.Orders[deliveredID], sim.OrderStateDelivered, at.Add(time.Second))
		return nil, nil
	}}); err != nil {
		t.Fatalf("flip to Delivered: %v", err)
	}

	got, _ := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.OutstandingReadyOrderQty(world, "hannah", "stew"), nil
	}})
	if got.(int) != 5 {
		t.Errorf("outstandingReadyOrderQty = %d, want 5 (1 + 4, Delivered excluded)", got)
	}

	// Different item kind → 0.
	got, _ = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.OutstandingReadyOrderQty(world, "hannah", "bread"), nil
	}})
	if got.(int) != 0 {
		t.Errorf("different item: outstandingReadyOrderQty = %d, want 0", got)
	}

	// Different seller → 0.
	got, _ = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.OutstandingReadyOrderQty(world, "jefferey", "stew"), nil
	}})
	if got.(int) != 0 {
		t.Errorf("different seller: outstandingReadyOrderQty = %d, want 0", got)
	}
}

// TestOutstandingReadyOrderQty_OverflowSaturates is the PR S6 R2
// code_review regression test. A corrupt Ready Order with a huge Qty
// (e.g. a future repo path loading malformed data) cannot be allowed
// to wrap the multiplication arithmetic — that would yield a negative
// or wrap-small `reserved` and reopen the over-selling path R1 patched.
// The helper saturates to math.MaxInt on overflow, which makes the
// downstream accept stock gate fail closed (treats the seller as
// having infinite outstanding reservations).
func TestOutstandingReadyOrderQty_OverflowSaturates(t *testing.T) {
	w, stop := buildOrderTestWorld(t)
	defer stop()
	at := time.Now().UTC()

	// Mint a normal Ready Order, then directly corrupt its Qty to a
	// value that would overflow when multiplied by 2 consumers.
	id := mintReadyOrder(t, w, []sim.ActorID{"jefferey", "mary"}, 1, at)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Orders[id].Qty = math.MaxInt
		return nil, nil
	}}); err != nil {
		t.Fatalf("corrupt qty: %v", err)
	}

	got, _ := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.OutstandingReadyOrderQty(world, "hannah", "stew"), nil
	}})
	if got.(int) != math.MaxInt {
		t.Errorf("overflow case: outstandingReadyOrderQty = %d, want math.MaxInt (saturated)", got)
	}
}

// TestDeliverOrder_DefensiveGate_ZeroQty — Order.Qty = 0 must reject
// before stock math runs.
func TestDeliverOrder_DefensiveGate_ZeroQty(t *testing.T) {
	w, stop := buildOrderTestWorld(t)
	defer stop()
	at := time.Now().UTC()
	id := mintReadyOrder(t, w, nil, 1, at)
	// Corrupt the Order's Qty to 0.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Orders[id].Qty = 0
		return nil, nil
	}}); err != nil {
		t.Fatalf("corrupt qty: %v", err)
	}
	_, err := w.Send(sim.DeliverOrder("hannah", id, at.Add(time.Second)))
	if err == nil || !strings.Contains(err.Error(), "invalid quantity") {
		t.Errorf("expected 'invalid quantity' error, got %v", err)
	}
}

// TestDeliverOrder_DefensiveGate_EmptyConsumers — Order.ConsumerIDs
// empty must reject before stock math.
func TestDeliverOrder_DefensiveGate_EmptyConsumers(t *testing.T) {
	w, stop := buildOrderTestWorld(t)
	defer stop()
	at := time.Now().UTC()
	id := mintReadyOrder(t, w, nil, 1, at)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Orders[id].ConsumerIDs = nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("clear consumers: %v", err)
	}
	_, err := w.Send(sim.DeliverOrder("hannah", id, at.Add(time.Second)))
	if err == nil || !strings.Contains(err.Error(), "no consumers") {
		t.Errorf("expected 'no consumers' error, got %v", err)
	}
}

// TestDeliverOrder_SellerNotHuddled — seller's CurrentHuddleID is
// empty; gate-6 rejects with a seller-specific message.
func TestDeliverOrder_SellerNotHuddled(t *testing.T) {
	w, stop := buildOrderTestWorld(t)
	defer stop()
	at := time.Now().UTC()
	id := mintReadyOrder(t, w, nil, 1, at)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["hannah"].CurrentHuddleID = ""
		return nil, nil
	}}); err != nil {
		t.Fatalf("clear seller huddle: %v", err)
	}
	_, err := w.Send(sim.DeliverOrder("hannah", id, at.Add(time.Second)))
	if err == nil || !strings.Contains(err.Error(), "seller") {
		t.Errorf("expected seller-not-huddled error, got %v", err)
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
