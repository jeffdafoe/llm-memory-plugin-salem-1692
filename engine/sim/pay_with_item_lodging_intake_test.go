package sim_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pay_with_item_lodging_intake_test.go — ZBBS-WORK-343 + ZBBS-WORK-344.
// Two lodging-shape intake gates in PayWithItem that pre-reject Orders
// the deliver_order path would refuse forever (coin commits but Order
// stays Ready, keeper LLM burns ticks):
//
//   - WORK-343: keeper's work_structure has zero private bedrooms
//     (operator-data gap). Distinct from "all rooms occupied" — that
//     transient case stays at delivery time on purpose.
//   - WORK-344: lodging take-home naming a non-buyer consumer. v2 supports
//     non-lodging take-home with named consumers (feature); the LODGING
//     subset can't deliver (deliver_order's single-self-consumer guard).
//
// Both fire from PayWithItem, after kind + consumer resolution and before
// the fast-path/slow-path split. Tests reuse buildPayWithItemWorld /
// readPayLedger / pwiActor from pay_with_item_commands_test.go (same
// package).
//
// LLM-84 extends this file with same-day-vs-advance lodging ACCEPT tests: a
// same-day walk-in grants the room at accept (coin or barter), a future
// reservation stays a deferred deliver_order check-in (coin-only), and the
// same-day availability gate fails the accept (no charge) when the inn is full.

// seedLodgingFixture installs the nights_stay item kind (service +
// lodging capabilities), an inn Structure with the supplied rooms, and
// pins the seller's WorkStructureID to the inn. buildPayWithItemWorld
// doesn't take WorkStructureID or seed Structures, so the lodging tests
// layer those in via a Send command.
func seedLodgingFixture(t *testing.T, w *sim.World, sellerID sim.ActorID, innRooms []*sim.Room) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.ItemKinds["nights_stay"] = &sim.ItemKindDef{
			Name: "nights_stay", DisplayLabel: "a night's stay",
			Capabilities: []string{"service", "lodging"},
		}
		world.Structures["inn"] = &sim.Structure{
			ID: "inn", DisplayName: "The Inn", Rooms: innRooms,
		}
		if seller := world.Actors[sellerID]; seller != nil {
			seller.WorkStructureID = "inn"
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seedLodgingFixture: %v", err)
	}
}

// ============================================================
// WORK-343 — zero-private-rooms intake gate
// ============================================================

// TestPayWithItem_Lodging_NoPrivateRooms_Rejected — keeper's work_structure
// has only a common room. PayWithItem(nights_stay) returns a clear error
// and mints no ledger entry; the buyer's coins stay put.
func TestPayWithItem_Lodging_NoPrivateRooms_Rejected(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCStateful, huddleID: "h1", coins: 100},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()

	// Common room only — no private bedrooms. Operator-data gap.
	seedLodgingFixture(t, w, "bob", []*sim.Room{
		{ID: 1, StructureID: "inn", Kind: sim.RoomKindCommon, Name: "common"},
	})

	_, err := w.Send(sim.PayWithItem("alice", "Bob", "nights_stay", 1, 4, false, nil, nil, 0, 0, "", time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "no bedrooms") {
		t.Fatalf("want no-bedrooms reject, got %v", err)
	}
	if ledger := readPayLedger(t, w); len(ledger) != 0 {
		t.Errorf("ledger has %d entries after rejected intake, want 0", len(ledger))
	}
	if snap := w.Published(); snap.Actors["alice"].Coins != 100 {
		t.Errorf("alice.Coins moved on rejected intake: %d", snap.Actors["alice"].Coins)
	}
}

// TestPayWithItem_Lodging_NoWorkStructure_Rejected — keeper without any
// WorkStructureID set (data shape distinct from "structure exists but
// has no rooms"). Surfaces with the same intake-shape error so the buyer
// LLM gets a clear "ask an operator" signal.
func TestPayWithItem_Lodging_NoWorkStructure_Rejected(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCStateful, huddleID: "h1", coins: 100},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()

	// Inject nights_stay but do NOT call seedLodgingFixture — bob has no
	// WorkStructureID at all.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.ItemKinds["nights_stay"] = &sim.ItemKindDef{
			Name: "nights_stay", DisplayLabel: "a night's stay",
			Capabilities: []string{"service", "lodging"},
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed nights_stay: %v", err)
	}

	_, err := w.Send(sim.PayWithItem("alice", "Bob", "nights_stay", 1, 4, false, nil, nil, 0, 0, "", time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "no work structure") {
		t.Fatalf("want no-work-structure reject, got %v", err)
	}
	if ledger := readPayLedger(t, w); len(ledger) != 0 {
		t.Errorf("ledger has %d entries after rejected intake, want 0", len(ledger))
	}
}

// TestPayWithItem_Lodging_OnePrivateRoom_Passes — one private bedroom in
// the keeper's work_structure is enough to clear the WORK-343 gate; the
// offer mints normally.
func TestPayWithItem_Lodging_OnePrivateRoom_Passes(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCStateful, huddleID: "h1", coins: 100},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()

	seedLodgingFixture(t, w, "bob", []*sim.Room{
		{ID: 1, StructureID: "inn", Kind: sim.RoomKindCommon, Name: "common"},
		{ID: 2, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
	})

	res, err := w.Send(sim.PayWithItem("alice", "Bob", "nights_stay", 1, 4, false, nil, nil, 0, 0, "", time.Now().UTC()))
	if err != nil {
		t.Fatalf("PayWithItem: %v", err)
	}
	result := res.(sim.PayWithItemResult)
	if result.State != sim.PayLedgerStatePending {
		t.Errorf("State = %q, want pending", result.State)
	}
	if ledger := readPayLedger(t, w); len(ledger) != 1 {
		t.Errorf("ledger entries = %d, want 1", len(ledger))
	}
}

// TestAcceptPay_Lodging_SameDay_GrantsEagerly — LLM-84: accepting a SAME-DAY
// nights_stay grants the room on the spot (like a physical-goods handover), NOT a
// deferred deliver_order check-in. After accept the Order is Delivered and the
// buyer holds the RoomAccess; the guest beds into it at night via the sleep
// machine, with no separate keeper check-in.
func TestAcceptPay_Lodging_SameDay_GrantsEagerly(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCStateful, huddleID: "h1", coins: 100},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()
	seedLodgingFixture(t, w, "bob", []*sim.Room{
		{ID: 1, StructureID: "inn", Kind: sim.RoomKindCommon, Name: "common"},
		{ID: 2, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
	})
	at := time.Now().UTC()

	res, err := w.Send(sim.PayWithItem("alice", "Bob", "nights_stay", 1, 4, false, nil, nil, 0, 0, "", at))
	if err != nil {
		t.Fatalf("PayWithItem: %v", err)
	}
	ledgerID := res.(sim.PayWithItemResult).LedgerID
	if _, err := w.Send(sim.AcceptPay("bob", ledgerID, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}

	var orderState sim.OrderState
	var orderCount, aliceRooms, aliceCoins int
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		aliceRooms = len(world.Actors["alice"].RoomAccess)
		aliceCoins = world.Actors["alice"].Coins
		for _, o := range world.Orders {
			if o != nil && o.SellerID == "bob" && o.BuyerID == "alice" {
				orderState = o.State
				orderCount++
			}
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("read world: %v", err)
	}
	if orderCount != 1 {
		t.Fatalf("lodging order count = %d, want 1", orderCount)
	}
	if orderState != sim.OrderStateDelivered {
		t.Errorf("lodging order State = %q, want delivered (same-day eager grant)", orderState)
	}
	if aliceRooms != 1 {
		t.Errorf("alice holds %d RoomAccess after accept, want 1 (room granted on the spot)", aliceRooms)
	}
	if aliceCoins != 96 {
		t.Errorf("alice.Coins = %d, want 96 (paid at accept)", aliceCoins)
	}
}

// TestAcceptPay_Lodging_AdvanceBooking_StaysDeferred — LLM-84: a FUTURE booking
// (ready_in_days > 0) keeps the deferred two-phase flow — accept mints a Ready
// Order and the keeper checks the guest in via deliver_order on the booked day.
// The room is NOT granted at accept; the buyer holds no RoomAccess yet.
func TestAcceptPay_Lodging_AdvanceBooking_StaysDeferred(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCStateful, huddleID: "h1", coins: 100},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()
	seedLodgingFixture(t, w, "bob", []*sim.Room{
		{ID: 1, StructureID: "inn", Kind: sim.RoomKindCommon, Name: "common"},
		{ID: 2, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
	})
	at := time.Now().UTC()

	// ready_in_days = 3 → a reservation for a future night.
	res, err := w.Send(sim.PayWithItem("alice", "Bob", "nights_stay", 1, 4, false, nil, nil, 0, 0, "", at, sim.PayWithItemOpts{ReadyInDays: 3}))
	if err != nil {
		t.Fatalf("PayWithItem: %v", err)
	}
	ledgerID := res.(sim.PayWithItemResult).LedgerID
	if _, err := w.Send(sim.AcceptPay("bob", ledgerID, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}

	var orderState sim.OrderState
	var orderCount, aliceRooms, aliceCoins int
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		aliceRooms = len(world.Actors["alice"].RoomAccess)
		aliceCoins = world.Actors["alice"].Coins
		for _, o := range world.Orders {
			if o != nil && o.SellerID == "bob" && o.BuyerID == "alice" {
				orderState = o.State
				orderCount++
			}
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("read world: %v", err)
	}
	if orderCount != 1 {
		t.Fatalf("lodging order count = %d, want 1", orderCount)
	}
	if orderState != sim.OrderStateReady {
		t.Errorf("advance-booking order State = %q, want ready (deferred check-in)", orderState)
	}
	if aliceRooms != 0 {
		t.Errorf("alice holds %d RoomAccess at accept, want 0 (granted at deliver_order)", aliceRooms)
	}
	if aliceCoins != 96 {
		t.Errorf("alice.Coins = %d, want 96 (booking paid at accept)", aliceCoins)
	}
}

// TestPayWithItem_Lodging_SameDayBarter_Allowed — LLM-84: a SAME-DAY walk-in room
// may be paid with goods (barter). The HOME-403 coin-only rule now scopes to
// FUTURE bookings only — a same-day room is granted at accept with no un-occupied
// window to refund. This is Ezekiel's 2-skillets-for-a-night case made to settle.
func TestPayWithItem_Lodging_SameDayBarter_Allowed(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCStateful, huddleID: "h1"},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()
	seedLodgingFixture(t, w, "bob", []*sim.Room{
		{ID: 2, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
	})
	// alice carries 2 skillets to barter (no coins).
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.ItemKinds["skillet"] = &sim.ItemKindDef{Name: "skillet", DisplayLabel: "skillet"}
		world.Actors["alice"].Inventory = map[sim.ItemKind]int{"skillet": 2}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed skillets: %v", err)
	}
	at := time.Now().UTC()

	res, err := w.Send(sim.PayWithItem("alice", "Bob", "nights_stay", 1, 0, false, nil, []sim.PayItemInput{{Item: "skillet", Qty: 2}}, 0, 0, "", at))
	if err != nil {
		t.Fatalf("same-day barter lodging should be allowed: %v", err)
	}
	ledgerID := res.(sim.PayWithItemResult).LedgerID
	if _, err := w.Send(sim.AcceptPay("bob", ledgerID, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}

	var aliceRooms, aliceSkillets, bobSkillets int
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		aliceRooms = len(world.Actors["alice"].RoomAccess)
		aliceSkillets = world.Actors["alice"].Inventory["skillet"]
		bobSkillets = world.Actors["bob"].Inventory["skillet"]
		return nil, nil
	}}); err != nil {
		t.Fatalf("read world: %v", err)
	}
	if aliceRooms != 1 {
		t.Errorf("alice holds %d RoomAccess after barter accept, want 1", aliceRooms)
	}
	if aliceSkillets != 0 || bobSkillets != 2 {
		t.Errorf("skillets after barter: alice=%d bob=%d, want alice=0 bob=2", aliceSkillets, bobSkillets)
	}
}

// TestPayWithItem_Lodging_AdvanceBarter_Rejected — LLM-84: a FUTURE room booking
// (ready_in_days > 0) must still be paid in coins. A deferred booking can expire
// un-occupied and only coins can be refunded — the Order carries no goods leg.
func TestPayWithItem_Lodging_AdvanceBarter_Rejected(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCStateful, huddleID: "h1"},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()
	seedLodgingFixture(t, w, "bob", []*sim.Room{
		{ID: 2, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
	})
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.ItemKinds["skillet"] = &sim.ItemKindDef{Name: "skillet", DisplayLabel: "skillet"}
		world.Actors["alice"].Inventory = map[sim.ItemKind]int{"skillet": 2}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed skillets: %v", err)
	}

	_, err := w.Send(sim.PayWithItem("alice", "Bob", "nights_stay", 1, 0, false, nil, []sim.PayItemInput{{Item: "skillet", Qty: 2}}, 0, 0, "", time.Now().UTC(), sim.PayWithItemOpts{ReadyInDays: 3}))
	if err == nil || !strings.Contains(err.Error(), "future night must be paid in coins") {
		t.Fatalf("want future-barter reject, got %v", err)
	}
	if ledger := readPayLedger(t, w); len(ledger) != 0 {
		t.Errorf("ledger has %d entries after rejected intake, want 0", len(ledger))
	}
}

// TestAcceptPay_Lodging_SameDay_InnFull_FailsUnavailable — LLM-84: the same-day
// availability gate (gate 10b) fails the accept when no private room is grantable
// (the only bedroom is held by another lodger), so the buyer is never charged for
// a room that can't be granted. The offer flips to failed_unavailable.
func TestAcceptPay_Lodging_SameDay_InnFull_FailsUnavailable(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCStateful, huddleID: "h1", coins: 100},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
		{id: "carol", displayName: "Carol", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()
	seedLodgingFixture(t, w, "bob", []*sim.Room{
		{ID: 2, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
	})
	// carol holds the only bedroom, so no room is grantable to alice.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		key := sim.RoomAccessKey{RoomID: 2, Source: sim.AccessSourceLedger}
		world.Actors["carol"].RoomAccess = map[sim.RoomAccessKey]*sim.RoomAccess{
			key: {RoomID: 2, Source: sim.AccessSourceLedger, Active: true},
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed occupancy: %v", err)
	}
	at := time.Now().UTC()

	res, err := w.Send(sim.PayWithItem("alice", "Bob", "nights_stay", 1, 4, false, nil, nil, 0, 0, "", at))
	if err != nil {
		t.Fatalf("PayWithItem (intake should pass — availability is an accept-time gate): %v", err)
	}
	ledgerID := res.(sim.PayWithItemResult).LedgerID
	if _, err := w.Send(sim.AcceptPay("bob", ledgerID, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}

	var state sim.PayLedgerState
	var aliceCoins, aliceRooms int
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		aliceCoins = world.Actors["alice"].Coins
		aliceRooms = len(world.Actors["alice"].RoomAccess)
		if e := world.PayLedger[ledgerID]; e != nil {
			state = e.State
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("read world: %v", err)
	}
	if state != sim.PayLedgerStateFailedUnavailable {
		t.Errorf("ledger state = %q, want failed_unavailable (inn full)", state)
	}
	if aliceCoins != 100 {
		t.Errorf("alice.Coins = %d, want 100 (not charged for an ungrantable room)", aliceCoins)
	}
	if aliceRooms != 0 {
		t.Errorf("alice holds %d RoomAccess, want 0 (grant failed)", aliceRooms)
	}
}

// TestPayWithItem_Lodging_OnePrivateRoomOccupied_StillPasses pins the
// v1-deliberate scope: intake doesn't tighten to "available rooms".
// Occupancy is transient (existing lodgers check out in the morning), so
// it stays at delivery time where AssignBedroomForLodger surfaces
// RoomID=0 → "try again shortly". A future refactor that tightens this
// gate to availability would break this test.
//
// The gate code only counts `r.Kind == RoomKindPrivate` and never reads
// RoomAccess; this test seeds an active RoomAccess to document the
// invariant.
func TestPayWithItem_Lodging_OnePrivateRoomOccupied_StillPasses(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCStateful, huddleID: "h1", coins: 100},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
		{id: "carol", displayName: "Carol", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	seedLodgingFixture(t, w, "bob", []*sim.Room{
		{ID: 1, StructureID: "inn", Kind: sim.RoomKindCommon, Name: "common"},
		{ID: 2, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
	})

	// Mark the bedroom as occupied by carol so a future tighten-to-
	// availability refactor would fail this test.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		carol := world.Actors["carol"]
		key := sim.RoomAccessKey{RoomID: 2, Source: sim.AccessSourceLedger}
		carol.RoomAccess = map[sim.RoomAccessKey]*sim.RoomAccess{
			key: {RoomID: 2, Source: sim.AccessSourceLedger, Active: true},
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed occupancy: %v", err)
	}

	res, err := w.Send(sim.PayWithItem("alice", "Bob", "nights_stay", 1, 4, false, nil, nil, 0, 0, "", time.Now().UTC()))
	if err != nil {
		t.Fatalf("PayWithItem: %v (intake should pass — occupancy stays at delivery)", err)
	}
	if result := res.(sim.PayWithItemResult); result.State != sim.PayLedgerStatePending {
		t.Errorf("State = %q, want pending", result.State)
	}
}

// ============================================================
// LLM-391 — consume_now lodging grants a room (never eaten for nothing)
// ============================================================

// TestPayWithItem_LodgingConsumeNow_SelfConsumer_GrantsRoom is the LLM-391
// regression: a buyer renting a room with consume_now=TRUE must still get a
// RoomAccess grant. Before the fix, a consume_now nights_stay fell into
// commitPayTransfer's eat-on-the-spot branch — coins settled, "delivered" row
// written, but NO room granted and NO Order minted. PayWithItem now normalizes
// consume_now→false for lodging at intake, so the accept mints an Order and
// grants the room exactly like a consume_now=false walk-in.
func TestPayWithItem_LodgingConsumeNow_SelfConsumer_GrantsRoom(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCStateful, huddleID: "h1", coins: 100},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()
	seedLodgingFixture(t, w, "bob", []*sim.Room{
		{ID: 1, StructureID: "inn", Kind: sim.RoomKindCommon, Name: "common"},
		{ID: 2, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
	})
	at := time.Now().UTC()

	// consume_now = TRUE — the disposition that produced the paid-for-nothing bug.
	res, err := w.Send(sim.PayWithItem("alice", "Bob", "nights_stay", 1, 4, true, nil, nil, 0, 0, "", at))
	if err != nil {
		t.Fatalf("PayWithItem: %v", err)
	}
	ledgerID := res.(sim.PayWithItemResult).LedgerID
	if _, err := w.Send(sim.AcceptPay("bob", ledgerID, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}

	ra, ok := activeLedgerRoomAccess(t, w, "alice")
	if !ok {
		t.Fatal("alice holds no active RoomAccess after a consume_now room booking (the LLM-391 bug: paid, no room)")
	}
	if ra.RoomID != 2 {
		t.Errorf("RoomAccess.RoomID = %d, want 2 (bedroom_1)", ra.RoomID)
	}

	var orderState sim.OrderState
	var orderCount, aliceCoins int
	var entryConsumeNow bool
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		aliceCoins = world.Actors["alice"].Coins
		for _, o := range world.Orders {
			if o != nil && o.SellerID == "bob" && o.BuyerID == "alice" {
				orderState = o.State
				orderCount++
			}
		}
		if e := world.PayLedger[ledgerID]; e != nil {
			entryConsumeNow = e.ConsumeNow
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("read world: %v", err)
	}
	if orderCount != 1 {
		t.Fatalf("lodging order count = %d, want 1 (an Order must be minted, not the order-less consume path)", orderCount)
	}
	if orderState != sim.OrderStateDelivered {
		t.Errorf("order State = %q, want delivered (same-day walk-in grant)", orderState)
	}
	if aliceCoins != 96 {
		t.Errorf("alice.Coins = %d, want 96 (paid 4 at accept)", aliceCoins)
	}
	if entryConsumeNow {
		t.Error("persisted ledger entry still ConsumeNow=true — lodging must be normalized to the booking shape")
	}
}

// TestPayWithItem_LodgingConsumeNow_SameNightRebook_Coalesces is the durable
// invariant regression: a second nights_stay booked for a night the buyer
// already holds at this keeper must coalesce to the NEXT night (extending the
// stay), never mint a duplicate (buyer, seller, ready_by) — the collision that
// violates pay_ledger_lodging_active_once and wedges every checkpoint. Both
// bookings go through the consume_now path to prove the grant (and thus
// advancePastHeldLodging's coverage read) is reliable end-to-end.
func TestPayWithItem_LodgingConsumeNow_SameNightRebook_Coalesces(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCStateful, huddleID: "h1", coins: 100},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()
	seedLodgingFixture(t, w, "bob", []*sim.Room{
		{ID: 2, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
	})
	at := time.Now().UTC()

	book := func(tag string) {
		res, err := w.Send(sim.PayWithItem("alice", "Bob", "nights_stay", 1, 4, true, nil, nil, 0, 0, "", at))
		if err != nil {
			t.Fatalf("PayWithItem (%s): %v", tag, err)
		}
		id := res.(sim.PayWithItemResult).LedgerID
		if _, err := w.Send(sim.AcceptPay("bob", id, at)); err != nil {
			t.Fatalf("AcceptPay (%s): %v", tag, err)
		}
	}
	book("first")
	book("second") // same night, same keeper — the double-book attempt

	var readyBys []string
	var aliceRooms int
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		aliceRooms = len(world.Actors["alice"].RoomAccess)
		for _, o := range world.Orders {
			if o != nil && o.SellerID == "bob" && o.BuyerID == "alice" && o.Item == "nights_stay" {
				readyBys = append(readyBys, o.ReadyBy.Format("2006-01-02"))
			}
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("read world: %v", err)
	}
	if len(readyBys) != 2 {
		t.Fatalf("nights_stay order count = %d, want 2", len(readyBys))
	}
	if readyBys[0] == readyBys[1] {
		t.Errorf("both bookings landed on ready_by %s — a duplicate (buyer,seller,night) that wedges the checkpoint; the second must coalesce to the next night", readyBys[0])
	}
	// The renewal extends the ONE grant in place (branch 1), not a second room.
	if aliceRooms != 1 {
		t.Errorf("alice holds %d RoomAccess grants, want 1 (renewal extends in place, no room-hop)", aliceRooms)
	}
}

// TestAcceptPay_LodgingConsumeNow_Backstop_GrantsRoom isolates the fix (2)
// commit chokepoint: a nights_stay ledger entry that reaches accept with
// ConsumeNow=TRUE WITHOUT passing through PayWithItem's intake normalization —
// the "pre-fix pending offer reloaded across a deploy, then accepted" shape the
// incident calls out. seedLedgerEntry writes the entry straight into
// w.PayLedger (no intake), so this proves commitPayTransfer's lodging exclusion
// (not the intake normalize) is what routes it to a real room grant instead of
// the order-less consume branch.
func TestAcceptPay_LodgingConsumeNow_Backstop_GrantsRoom(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCStateful, huddleID: "h1", coins: 100},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()
	seedLodgingFixture(t, w, "bob", []*sim.Room{
		{ID: 2, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
	})
	at := time.Now().UTC()

	// A ConsumeNow=true nights_stay pending entry that never saw intake
	// normalization — exactly what a pre-fix offer reloaded post-deploy looks like.
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob", ItemKind: "nights_stay",
		Qty: 1, Amount: 4, ConsumeNow: true,
		State: sim.PayLedgerStatePending, HuddleID: "h1", SceneID: "sc1",
		CreatedAt: at, ExpiresAt: at.Add(10 * time.Minute),
	})

	if _, err := w.Send(sim.AcceptPay("bob", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}

	if _, ok := activeLedgerRoomAccess(t, w, "alice"); !ok {
		t.Fatal("alice holds no room after accepting a ConsumeNow=true nights_stay — the commit backstop must route lodging to a grant, not the consume branch")
	}
}

// ============================================================
// WORK-344 — lodging take-home non-buyer consumer gate
// ============================================================

// TestPayWithItem_LodgingTakeHome_NonBuyerConsumer_Rejected — Alice tries
// to book a room "for Carol" via consumers=[Carol], consume_now=false.
// deliver_order would refuse this Order forever — pre-reject at intake.
func TestPayWithItem_LodgingTakeHome_NonBuyerConsumer_Rejected(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCStateful, huddleID: "h1", coins: 100},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
		{id: "carol", displayName: "Carol", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	seedLodgingFixture(t, w, "bob", []*sim.Room{
		{ID: 2, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
	})

	_, err := w.Send(sim.PayWithItem("alice", "Bob", "nights_stay", 1, 4, false, []string{"Carol"}, nil, 0, 0, "", time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "for someone else") {
		t.Fatalf("want non-buyer-consumer reject, got %v", err)
	}
	if ledger := readPayLedger(t, w); len(ledger) != 0 {
		t.Errorf("ledger has %d entries after rejected intake, want 0", len(ledger))
	}
}

// TestPayWithItem_LodgingTakeHome_BuyerAsSoleConsumer_Passes — buyer
// listing themselves explicitly is redundant but coherent (deliver_order
// would accept). Gate is narrow — only NON-buyer consumers reject.
func TestPayWithItem_LodgingTakeHome_BuyerAsSoleConsumer_Passes(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCStateful, huddleID: "h1", coins: 100},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()

	seedLodgingFixture(t, w, "bob", []*sim.Room{
		{ID: 2, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
	})

	res, err := w.Send(sim.PayWithItem("alice", "Bob", "nights_stay", 1, 4, false, []string{"Alice"}, nil, 0, 0, "", time.Now().UTC()))
	if err != nil {
		t.Fatalf("PayWithItem: %v (redundant self-consumer should pass)", err)
	}
	if result := res.(sim.PayWithItemResult); result.State != sim.PayLedgerStatePending {
		t.Errorf("State = %q, want pending", result.State)
	}
}

// TestPayWithItem_LodgingConsumeNow_NonBuyerConsumer_Rejected — LLM-391
// normalizes consume_now→false for every lodging intake (a room is delivered
// via an Order + grant, never eaten on the spot), so the WORK-344 non-buyer
// gate now covers the former consume_now hole too: booking a room "for Carol"
// is rejected whatever the disposition. Before LLM-391 this passed intake and
// the consume branch silently granted no room (the paid-for-nothing bug).
func TestPayWithItem_LodgingConsumeNow_NonBuyerConsumer_Rejected(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCStateful, huddleID: "h1", coins: 100},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
		{id: "carol", displayName: "Carol", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	seedLodgingFixture(t, w, "bob", []*sim.Room{
		{ID: 2, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
	})

	_, err := w.Send(sim.PayWithItem("alice", "Bob", "nights_stay", 1, 4, true, []string{"Carol"}, nil, 0, 0, "", time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "for someone else") {
		t.Fatalf("want non-buyer-consumer reject, got %v", err)
	}
	if ledger := readPayLedger(t, w); len(ledger) != 0 {
		t.Errorf("ledger has %d entries after rejected intake, want 0", len(ledger))
	}
}
