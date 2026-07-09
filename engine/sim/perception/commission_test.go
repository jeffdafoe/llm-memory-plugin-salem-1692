package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// commission_test.go — LLM-338. Perception guard for a made-to-order commission:
// a Ready order the seller has taken payment for but not yet forged renders
// passively ("you've yet to make it") and does NOT cue deliver_order (gate 5
// would bounce it), so the keeper is steered to make the good first rather than
// into a bounce loop.

// commissionSnap builds a snapshot with a producing seller (smith) and a
// co-present buyer (alice) holding one Ready nail order. sellerInv is smith's
// nail holding; produces toggles whether smith carries a nail produce entry.
func commissionSnap(sellerInv map[sim.ItemKind]int, produces bool) *sim.Snapshot {
	var restock []sim.RestockEntry
	if produces {
		restock = []sim.RestockEntry{{Item: "nail", Source: sim.RestockSourceProduce, Max: 20}}
	}
	return &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"smith": {DisplayName: "Smith", Kind: sim.KindNPCStateful, CurrentHuddleID: "h1", Inventory: sellerInv, RestockPolicy: &sim.RestockPolicy{Restock: restock}},
			"alice": {DisplayName: "Alice", Kind: sim.KindNPCShared, CurrentHuddleID: "h1"},
		},
		Orders: map[sim.OrderID]*sim.Order{
			1: {
				ID: 1, State: sim.OrderStateReady,
				BuyerID: "alice", SellerID: "smith",
				Item: "nail", Qty: 1, ConsumerIDs: []sim.ActorID{"alice"},
				CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(5 * time.Hour),
			},
		},
		Scenes:     map[sim.SceneID]*sim.Scene{},
		Huddles:    map[sim.HuddleID]*sim.Huddle{},
		Structures: map[sim.StructureID]*sim.Structure{},
	}
}

// TestBuildPendingOrderViews_AwaitingMake_ProducerShort — a producer who owes a
// nail it holds 0 of, with the buyer co-present, is AwaitingMake (not merely an
// absent-recipient case): the guard keys off stock, not co-presence.
func TestBuildPendingOrderViews_AwaitingMake_ProducerShort(t *testing.T) {
	fromMe, _ := buildPendingOrderViews(commissionSnap(nil, true), "smith")
	if len(fromMe) != 1 {
		t.Fatalf("fromMe = %v, want one order", fromMe)
	}
	if !fromMe[0].AwaitingMake {
		t.Errorf("AwaitingMake = false, want true (producer holds 0 of a nail it owes)")
	}
	if len(fromMe[0].AbsentRecipientNames) != 0 {
		t.Errorf("AbsentRecipientNames = %v, want empty (buyer co-present)", fromMe[0].AbsentRecipientNames)
	}
}

// TestBuildPendingOrderViews_AwaitingMake_FalseWhenStocked — once the good is
// forged (held), the order is deliverable, not awaiting a make.
func TestBuildPendingOrderViews_AwaitingMake_FalseWhenStocked(t *testing.T) {
	fromMe, _ := buildPendingOrderViews(commissionSnap(map[sim.ItemKind]int{"nail": 1}, true), "smith")
	if len(fromMe) != 1 {
		t.Fatalf("fromMe = %v, want one order", fromMe)
	}
	if fromMe[0].AwaitingMake {
		t.Errorf("AwaitingMake = true, want false (seller holds the nail — deliverable now)")
	}
}

// TestBuildPendingOrderViews_AwaitingMake_FalseWhenNotProducer — the guard is
// commission-specific: a shortfall on a good the seller doesn't make is not an
// awaiting-make (and never mints a Ready order in practice).
func TestBuildPendingOrderViews_AwaitingMake_FalseWhenNotProducer(t *testing.T) {
	fromMe, _ := buildPendingOrderViews(commissionSnap(nil, false), "smith")
	if len(fromMe) != 1 {
		t.Fatalf("fromMe = %v, want one order", fromMe)
	}
	if fromMe[0].AwaitingMake {
		t.Errorf("AwaitingMake = true, want false (seller doesn't produce the good)")
	}
}

// TestRenderOrdersReady_AwaitingMake_Passive — an unforged commission renders as
// a passive "you've yet to make it" with NO deliver_order instruction, and does
// not read as an absent-recipient wait.
func TestRenderOrdersReady_AwaitingMake_Passive(t *testing.T) {
	var b strings.Builder
	renderPendingDeliveriesFromMe(&b, []OrderView{
		{ID: 1, Item: "nail", Qty: 1, BuyerName: "Alice", AwaitingMake: true, ExpiresAt: time.Now().Add(5 * time.Hour)},
	}, startOfUTCDay(time.Now()), time.Time{})
	out := b.String()
	if !strings.Contains(out, "you've yet to make it") {
		t.Errorf("unforged commission should render passively; got:\n%s", out)
	}
	if strings.Contains(out, "call deliver_order") {
		t.Errorf("nothing is forged; deliver_order instruction must be suppressed; got:\n%s", out)
	}
	if strings.Contains(out, "waiting for") {
		t.Errorf("commission-not-made should not read as an absent-recipient wait; got:\n%s", out)
	}
}

// TestRenderOrdersReady_MixedMakeAndDeliverable — with one unforged commission
// and one in-stock order, the commission renders passively AND the instruction
// still surfaces for the deliverable one.
func TestRenderOrdersReady_MixedMakeAndDeliverable(t *testing.T) {
	var b strings.Builder
	renderPendingDeliveriesFromMe(&b, []OrderView{
		{ID: 1, Item: "nail", Qty: 1, BuyerName: "Alice", AwaitingMake: true, ExpiresAt: time.Now().Add(5 * time.Hour)},
		{ID: 2, Item: "bread", Qty: 1, BuyerName: "Mary", ExpiresAt: time.Now().Add(time.Hour)},
	}, startOfUTCDay(time.Now()), time.Time{})
	out := b.String()
	if !strings.Contains(out, "you've yet to make it") {
		t.Errorf("the unforged commission should still render passively; got:\n%s", out)
	}
	if !strings.Contains(out, "call deliver_order") {
		t.Errorf("the in-stock order is deliverable; instruction must surface; got:\n%s", out)
	}
}

// TestOrderView_DeliverableNow — the shared predicate the "## Orders to deliver"
// instruction and the deliver_order tool-advertising gate both read (LLM-338):
// deliverable iff the good is on hand (not AwaitingMake) AND the recipient is
// co-present (no AbsentRecipientNames).
func TestOrderView_DeliverableNow(t *testing.T) {
	cases := []struct {
		name string
		o    OrderView
		want bool
	}{
		{"good on hand, recipient present", OrderView{ID: 1, Item: "nails"}, true},
		{"unforged commission", OrderView{ID: 1, Item: "shovel", AwaitingMake: true}, false},
		{"absent recipient", OrderView{ID: 1, Item: "stew", AbsentRecipientNames: []string{"Jefferey"}}, false},
		{"both unforged and absent", OrderView{ID: 1, Item: "shovel", AwaitingMake: true, AbsentRecipientNames: []string{"Jefferey"}}, false},
	}
	for _, tc := range cases {
		if got := tc.o.DeliverableNow(); got != tc.want {
			t.Errorf("%s: DeliverableNow() = %v, want %v", tc.name, got, tc.want)
		}
	}
}
