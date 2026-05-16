package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// order_test.go — Phase 3 PR S6 perception coverage. Verifies
// buildPendingOrderViews subject-relative filtering + render's
// section headings + qty/consumer-list formatting.

// orderSnap builds a minimal *sim.Snapshot with three actors
// (hannah/jefferey/mary) and the supplied Orders map.
func orderSnap(orders map[sim.OrderID]*sim.Order) *sim.Snapshot {
	return &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"hannah":   {DisplayName: "Hannah", Kind: sim.KindNPCShared},
			"jefferey": {DisplayName: "Jefferey", Kind: sim.KindNPCStateful},
			"mary":     {DisplayName: "Mary", Kind: sim.KindNPCStateful},
		},
		Orders:     orders,
		Scenes:     map[sim.SceneID]*sim.Scene{},
		Huddles:    map[sim.HuddleID]*sim.Huddle{},
		Structures: map[sim.StructureID]*sim.Structure{},
	}
}

// orderOf is a brief constructor for a Ready Order used across the
// build tests. expiresIn is relative to time.Now().
func orderOf(id sim.OrderID, seller, buyer sim.ActorID, item sim.ItemKind, qty int, consumers []sim.ActorID, expiresIn time.Duration) *sim.Order {
	return &sim.Order{
		ID:          id,
		State:       sim.OrderStateReady,
		BuyerID:     buyer,
		SellerID:    seller,
		Item:        item,
		Qty:         qty,
		ConsumerIDs: consumers,
		CreatedAt:   time.Now().UTC(),
		ExpiresAt:   time.Now().UTC().Add(expiresIn),
	}
}

// TestBuildPendingOrderViews_NilAndEmpty — both inputs return nil for
// an empty world.
func TestBuildPendingOrderViews_NilAndEmpty(t *testing.T) {
	fromMe, toMe := buildPendingOrderViews(nil, "hannah")
	if fromMe != nil || toMe != nil {
		t.Errorf("nil snap: fromMe=%v toMe=%v, want nil/nil", fromMe, toMe)
	}
	fromMe, toMe = buildPendingOrderViews(orderSnap(nil), "hannah")
	if fromMe != nil || toMe != nil {
		t.Errorf("empty Orders: fromMe=%v toMe=%v, want nil/nil", fromMe, toMe)
	}
}

// TestBuildPendingOrderViews_SellerSideOnly — subject is the seller of
// one Order; appears in fromMe, not toMe.
func TestBuildPendingOrderViews_SellerSideOnly(t *testing.T) {
	snap := orderSnap(map[sim.OrderID]*sim.Order{
		1: orderOf(1, "hannah", "jefferey", "stew", 1, []sim.ActorID{"jefferey"}, time.Hour),
	})
	fromMe, toMe := buildPendingOrderViews(snap, "hannah")
	if len(fromMe) != 1 || fromMe[0].ID != 1 {
		t.Errorf("fromMe = %v, want one Order ID=1", fromMe)
	}
	if toMe != nil {
		t.Errorf("toMe = %v, want nil (seller side only)", toMe)
	}
}

// TestBuildPendingOrderViews_BuyerSideOnly — subject is the buyer
// and implicit consumer; appears in toMe, not fromMe.
func TestBuildPendingOrderViews_BuyerSideOnly(t *testing.T) {
	snap := orderSnap(map[sim.OrderID]*sim.Order{
		1: orderOf(1, "hannah", "jefferey", "stew", 1, []sim.ActorID{"jefferey"}, time.Hour),
	})
	fromMe, toMe := buildPendingOrderViews(snap, "jefferey")
	if fromMe != nil {
		t.Errorf("fromMe = %v, want nil (buyer side only)", fromMe)
	}
	if len(toMe) != 1 || toMe[0].ID != 1 {
		t.Errorf("toMe = %v, want one Order ID=1", toMe)
	}
}

// TestBuildPendingOrderViews_ConsumerOnly — subject is a consumer
// (group order, not the buyer); appears in toMe.
func TestBuildPendingOrderViews_ConsumerOnly(t *testing.T) {
	snap := orderSnap(map[sim.OrderID]*sim.Order{
		1: orderOf(1, "hannah", "jefferey", "stew", 1, []sim.ActorID{"jefferey", "mary"}, time.Hour),
	})
	fromMe, toMe := buildPendingOrderViews(snap, "mary")
	if fromMe != nil {
		t.Errorf("fromMe = %v, want nil (consumer only)", fromMe)
	}
	if len(toMe) != 1 || toMe[0].ID != 1 {
		t.Errorf("toMe = %v, want one Order ID=1", toMe)
	}
	// ConsumerNames populated for group order.
	if len(toMe[0].ConsumerNames) != 2 {
		t.Errorf("ConsumerNames = %v, want 2 entries (group order)", toMe[0].ConsumerNames)
	}
}

// TestBuildPendingOrderViews_TerminalFiltered — Delivered and Expired
// orders never surface.
func TestBuildPendingOrderViews_TerminalFiltered(t *testing.T) {
	o1 := orderOf(1, "hannah", "jefferey", "stew", 1, []sim.ActorID{"jefferey"}, time.Hour)
	o1.State = sim.OrderStateDelivered
	o2 := orderOf(2, "hannah", "jefferey", "stew", 1, []sim.ActorID{"jefferey"}, time.Hour)
	o2.State = sim.OrderStateExpired
	o3 := orderOf(3, "hannah", "jefferey", "stew", 1, []sim.ActorID{"jefferey"}, time.Hour)
	snap := orderSnap(map[sim.OrderID]*sim.Order{1: o1, 2: o2, 3: o3})
	fromMe, _ := buildPendingOrderViews(snap, "hannah")
	if len(fromMe) != 1 || fromMe[0].ID != 3 {
		t.Errorf("fromMe = %v, want only ID=3 (terminals filtered)", fromMe)
	}
}

// TestBuildPendingOrderViews_DeterministicOrder — multiple Ready
// orders for the same seller sort by Order.ID ascending.
func TestBuildPendingOrderViews_DeterministicOrder(t *testing.T) {
	snap := orderSnap(map[sim.OrderID]*sim.Order{
		3: orderOf(3, "hannah", "jefferey", "stew", 1, []sim.ActorID{"jefferey"}, time.Hour),
		1: orderOf(1, "hannah", "jefferey", "stew", 1, []sim.ActorID{"jefferey"}, time.Hour),
		2: orderOf(2, "hannah", "jefferey", "stew", 1, []sim.ActorID{"jefferey"}, time.Hour),
	})
	fromMe, _ := buildPendingOrderViews(snap, "hannah")
	if len(fromMe) != 3 {
		t.Fatalf("fromMe count = %d, want 3", len(fromMe))
	}
	for i, want := range []sim.OrderID{1, 2, 3} {
		if fromMe[i].ID != want {
			t.Errorf("fromMe[%d].ID = %d, want %d", i, fromMe[i].ID, want)
		}
	}
}

// TestBuildPendingOrderViews_ImplicitBuyerConsumerSkipsConsumerNames —
// when ConsumerIDs == [BuyerID] (implicit), ConsumerNames is left
// empty (renderer skips the "and others" embellishment).
func TestBuildPendingOrderViews_ImplicitBuyerConsumerSkipsConsumerNames(t *testing.T) {
	snap := orderSnap(map[sim.OrderID]*sim.Order{
		1: orderOf(1, "hannah", "jefferey", "stew", 1, []sim.ActorID{"jefferey"}, time.Hour),
	})
	fromMe, _ := buildPendingOrderViews(snap, "hannah")
	if len(fromMe[0].ConsumerNames) != 0 {
		t.Errorf("ConsumerNames = %v, want empty (implicit buyer-as-consumer)", fromMe[0].ConsumerNames)
	}
}

// TestRenderPendingDeliveriesFromMe_HappyPath — section heading +
// item desc + buyer name + group-order embellishment + expiry.
func TestRenderPendingDeliveriesFromMe_HappyPath(t *testing.T) {
	var b strings.Builder
	now := time.Now()
	views := []OrderView{
		{
			ID: 7, Item: "stew", Qty: 2,
			BuyerName: "Jefferey", SellerName: "Hannah",
			ConsumerNames: []string{"Jefferey", "Mary"},
			CreatedAt:     now,
			ExpiresAt:     now.Add(5 * time.Minute),
		},
	}
	renderPendingDeliveriesFromMe(&b, views)
	out := b.String()
	for _, must := range []string{
		"## Orders to deliver",
		"#7:",
		"2 stew",
		"for Jefferey",
		"to deliver to: Jefferey, Mary",
		"expires in",
	} {
		if !strings.Contains(out, must) {
			t.Errorf("missing %q\n--- output ---\n%s", must, out)
		}
	}
}

// TestRenderPendingDeliveriesToMe_HappyPath — buyer-side rendering.
func TestRenderPendingDeliveriesToMe_HappyPath(t *testing.T) {
	var b strings.Builder
	now := time.Now()
	views := []OrderView{
		{
			ID: 7, Item: "stew", Qty: 1,
			BuyerName: "Jefferey", SellerName: "Hannah",
			CreatedAt: now,
			ExpiresAt: now.Add(5 * time.Minute),
		},
	}
	renderPendingDeliveriesToMe(&b, views)
	out := b.String()
	for _, must := range []string{
		"## Orders you're waiting on",
		"#7:",
		"stew",
		"from Hannah",
		"expires in",
	} {
		if !strings.Contains(out, must) {
			t.Errorf("missing %q\n--- output ---\n%s", must, out)
		}
	}
}

// TestRenderPendingOrders_EmptyListSkipsSection — content-gated.
func TestRenderPendingOrders_EmptyListSkipsSection(t *testing.T) {
	var b strings.Builder
	renderPendingDeliveriesFromMe(&b, nil)
	renderPendingDeliveriesToMe(&b, nil)
	if b.Len() != 0 {
		t.Errorf("empty list produced output: %q", b.String())
	}
}

// TestHumanizeDurationUntil — coarse minute formatting + past-due
// clamp + <1 minute case.
func TestHumanizeDurationUntil(t *testing.T) {
	now := time.Now()
	cases := []struct {
		dur  time.Duration
		want string
	}{
		{-time.Hour, "now"},
		{30 * time.Second, "<1 minute"},
		{time.Minute, "1 minute"},
		{5 * time.Minute, "5 minutes"},
	}
	for _, tc := range cases {
		got := humanizeDurationUntil(now.Add(tc.dur), now)
		if got != tc.want {
			t.Errorf("dur=%v: got %q, want %q", tc.dur, got, tc.want)
		}
	}
}
