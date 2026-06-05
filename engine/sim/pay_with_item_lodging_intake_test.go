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

// TestAcceptPay_Lodging_StaysDeferred — accepting a nights_stay (lodging)
// offer mints a Ready Order for the keeper to check the guest in via
// deliver_order; it is NOT handed over at accept like physical takeaway
// (ZBBS-HOME-398). The room grant (AssignBedroomForLodger) happens at the
// deliver_order check-in, so the buyer holds no RoomAccess yet — this is the
// designed two-phase booking flow, preserved.
func TestAcceptPay_Lodging_StaysDeferred(t *testing.T) {
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
	if orderState != sim.OrderStateReady {
		t.Errorf("lodging order State = %q, want ready (deferred check-in, not immediate handover)", orderState)
	}
	if aliceRooms != 0 {
		t.Errorf("alice holds %d RoomAccess at accept, want 0 (room granted at deliver_order check-in)", aliceRooms)
	}
	if aliceCoins != 96 {
		t.Errorf("alice.Coins = %d, want 96 (booking paid at accept)", aliceCoins)
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

// TestPayWithItem_LodgingConsumeNow_NonBuyerConsumer_Passes — the WORK-344
// gate scopes to take-home (!consumeNow). consume_now=true for lodging
// is incoherent but not a fulfillment-impossibility (commitPayTransfer's
// consume branch silently skips inventory depletion for service-cap
// items, and applyConsumeSatisfactions no-ops for items without
// satisfaction effects). Existing v2 behavior; this ticket doesn't
// tighten it.
func TestPayWithItem_LodgingConsumeNow_NonBuyerConsumer_Passes(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCStateful, huddleID: "h1", coins: 100},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
		{id: "carol", displayName: "Carol", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	seedLodgingFixture(t, w, "bob", []*sim.Room{
		{ID: 2, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
	})

	res, err := w.Send(sim.PayWithItem("alice", "Bob", "nights_stay", 1, 4, true, []string{"Carol"}, nil, 0, 0, "", time.Now().UTC()))
	if err != nil {
		t.Fatalf("PayWithItem: %v (WORK-344 gate scopes to take-home only)", err)
	}
	if result := res.(sim.PayWithItemResult); result.State != sim.PayLedgerStatePending {
		t.Errorf("State = %q, want pending", result.State)
	}
}
