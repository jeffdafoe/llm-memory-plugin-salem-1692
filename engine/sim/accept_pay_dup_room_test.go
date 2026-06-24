package sim

import (
	"strings"
	"testing"
	"time"
)

// accept_pay_dup_room_test.go — LLM-89. A nights_stay grant is created only at
// deliver_order, so between accept and deliver the buyer holds no grant and
// nothing stopped the keeper accepting a SECOND room offer from the same buyer
// (gate 10's stock reservation is skipped for service items). AcceptPay gate 4b
// rejects that while an undelivered room from this keeper to this buyer is still
// outstanding; undeliveredLodgingOrderFor is the predicate behind it.

func dupRoomWorld(orderState OrderState, orderItem ItemKind) *World {
	return &World{
		Actors: map[ActorID]*Actor{
			"hannah":  {ID: "hannah", DisplayName: "Hannah Boggs"},
			"ezekiel": {ID: "ezekiel", DisplayName: "Ezekiel Crane", Coins: 100},
		},
		ItemKinds: map[ItemKind]*ItemKindDef{
			"nights_stay": {Name: "nights_stay", Capabilities: []string{"service", "lodging"}},
			"stew":        {Name: "stew"},
		},
		Orders: map[OrderID]*Order{
			77: {ID: 77, State: orderState, SellerID: "hannah", BuyerID: "ezekiel",
				Item: orderItem, Qty: 1, ConsumerIDs: []ActorID{"ezekiel"}},
		},
		PayLedger: map[LedgerID]*PayLedgerEntry{},
	}
}

func TestUndeliveredLodgingOrderFor(t *testing.T) {
	// An outstanding Ready room from hannah to ezekiel matches.
	w := dupRoomWorld(OrderStateReady, "nights_stay")
	if id, ok := undeliveredLodgingOrderFor(w, "hannah", "ezekiel"); !ok || id != 77 {
		t.Fatalf("outstanding ready room: want (77, true), got (%d, %v)", id, ok)
	}
	// A delivered room no longer counts (the grant exists; it is not owed).
	w = dupRoomWorld(OrderStateDelivered, "nights_stay")
	if id, ok := undeliveredLodgingOrderFor(w, "hannah", "ezekiel"); ok {
		t.Errorf("delivered room must not count, got (%d, %v)", id, ok)
	}
	// A non-lodging Ready order does not count.
	w = dupRoomWorld(OrderStateReady, "stew")
	if _, ok := undeliveredLodgingOrderFor(w, "hannah", "ezekiel"); ok {
		t.Error("non-lodging order must not count")
	}
	// A different buyer does not match.
	w = dupRoomWorld(OrderStateReady, "nights_stay")
	if _, ok := undeliveredLodgingOrderFor(w, "hannah", "stranger"); ok {
		t.Error("a different buyer must not match")
	}
}

func TestAcceptPay_RejectsDuplicateRoom(t *testing.T) {
	at := time.Date(2026, 6, 24, 13, 0, 0, 0, time.UTC)
	w := dupRoomWorld(OrderStateReady, "nights_stay")
	// A second pending nights_stay offer from ezekiel awaiting hannah's accept.
	w.PayLedger[323] = &PayLedgerEntry{
		ID: 323, BuyerID: "ezekiel", SellerID: "hannah", ItemKind: "nights_stay",
		Qty: 1, Amount: 4, State: PayLedgerStatePending,
		ConsumerIDs: []ActorID{"ezekiel"},
	}
	_, err := AcceptPay("hannah", 323, at).Fn(w)
	if err == nil || !strings.Contains(err.Error(), "deliver_order") {
		t.Fatalf("want a duplicate-room rejection naming deliver_order, got %v", err)
	}
	if !strings.Contains(err.Error(), "#77") {
		t.Errorf("rejection should name the outstanding order #77, got %v", err)
	}
	// Idempotent reject — NO transition — so the offer stays pending and can be
	// accepted once the first room is delivered (a genuine next night).
	if w.PayLedger[323].State != PayLedgerStatePending {
		t.Errorf("rejected offer must stay pending, got %s", w.PayLedger[323].State)
	}
}
