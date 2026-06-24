package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// dup_room_steer_test.go — LLM-89. The keeper-side steer that pairs with the
// AcceptPay duplicate-room gate: when a pending lodging offer comes from a buyer
// who already holds an undelivered room from this keeper, "## Offers awaiting
// your decision" tells the keeper to deliver the room already sold rather than
// accept (and have the engine reject) a second.

func TestBuildRoomAlreadySold(t *testing.T) {
	snap := &sim.Snapshot{
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"nights_stay": {Name: "nights_stay", Capabilities: []string{"service", "lodging"}},
			"stew":        {Name: "stew"},
		},
		Orders: map[sim.OrderID]*sim.Order{
			77: {ID: 77, State: sim.OrderStateReady, SellerID: "hannah", BuyerID: "ezekiel",
				Item: "nights_stay", ConsumerIDs: []sim.ActorID{"ezekiel"}},
		},
	}
	offers := []sim.PayOfferWarrantReason{
		{LedgerID: 323, Buyer: "ezekiel", Item: "nights_stay"},  // overlaps the undelivered room
		{LedgerID: 400, Buyer: "ezekiel", Item: "stew"},         // not lodging → skipped
		{LedgerID: 401, Buyer: "stranger", Item: "nights_stay"}, // no room owed to stranger → skipped
	}
	got := buildRoomAlreadySold(snap, "hannah", offers)
	if len(got) != 1 {
		t.Fatalf("want exactly one mapped offer, got %v", got)
	}
	if got[323] != 77 {
		t.Errorf("offer 323 should map to outstanding order 77, got %d", got[323])
	}
}

func TestRenderPayOffers_DuplicateRoomSteer(t *testing.T) {
	var b strings.Builder
	offers := []sim.PayOfferWarrantReason{
		{LedgerID: 323, Buyer: "ezekiel", Item: "nights_stay", Qty: 1, Amount: 4},
	}
	nameOf := func(id sim.ActorID) string {
		if id == "ezekiel" {
			return "Ezekiel Crane"
		}
		return string(id)
	}
	roomAlreadySold := map[sim.LedgerID]sim.OrderID{323: 77}
	renderPayOffers(&b, offers, nameOf, nil, roomAlreadySold)
	out := b.String()
	for _, want := range []string{"Ezekiel Crane", "#77", "deliver_order", "before accepting another"} {
		if !strings.Contains(out, want) {
			t.Errorf("duplicate-room steer missing %q, got %q", want, out)
		}
	}
}

func TestRenderPayOffers_NoDuplicateNoSteer(t *testing.T) {
	var b strings.Builder
	offers := []sim.PayOfferWarrantReason{
		{LedgerID: 100, Buyer: "ezekiel", Item: "nights_stay", Qty: 1, Amount: 4},
	}
	nameOf := func(id sim.ActorID) string { return string(id) }
	renderPayOffers(&b, offers, nameOf, nil, nil) // nil map → no overlap
	if strings.Contains(b.String(), "deliver_order") {
		t.Errorf("no outstanding room → no deliver steer, got %q", b.String())
	}
}
