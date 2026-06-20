package sim

import (
	"testing"
	"time"
)

// lodging_renewal_internal_test.go — LLM-47. advancePastHeldLodging is the
// shared helper behind the accept-time renewal advance (PayWithItem) and the
// deliver-time backstop (transferOrderGoods). "Held" is a DELIVERED lodging
// grant (read from w.Orders, matching the pay_ledger_lodging_active_once unique
// index predicate state='accepted' AND fulfillment_status='delivered'); the
// helper must move a booking off any such night so a "renew" extends the stay
// instead of minting a same-night duplicate that wedges checkpointing (the
// Ezekiel↔John incident, 2026-06-19).

func renewalOrder(id LedgerID, buyer, seller ActorID, readyBy time.Time, qty int, state OrderState) *Order {
	return &Order{
		ID: OrderID(id), LedgerID: id, BuyerID: buyer, SellerID: seller,
		Item: "nights_stay", Qty: qty, State: state, ReadyBy: readyBy,
	}
}

func renewalWorld(orders ...*Order) *World {
	w := &World{
		ItemKinds: map[ItemKind]*ItemKindDef{
			"nights_stay": {Name: "nights_stay", Capabilities: []string{"service", "lodging"}},
			"stew":        {Name: "stew"},
		},
		Orders: map[OrderID]*Order{},
	}
	for _, o := range orders {
		w.Orders[o.ID] = o
	}
	return w
}

func ymd(t time.Time) string { return t.Format("2006-01-02") }

// The renewal case: the buyer already holds (delivered) tonight, so a same-night
// booking advances to the next night.
func TestAdvancePastHeldLodging_RenewSameNightBumpsToNext(t *testing.T) {
	jun19 := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	w := renewalWorld(renewalOrder(295, "ezekiel", "john", jun19, 1, OrderStateDelivered))
	got := advancePastHeldLodging(w, "ezekiel", "john", jun19, 0)
	if ymd(got) != "2026-06-20" {
		t.Errorf("renew of a held night = %s, want 2026-06-20", ymd(got))
	}
}

// A not-yet-delivered (Ready) booking is NOT a held grant — it must not block a
// same-night booking (the deliver-time backstop resolves any overlap). This is
// the unique-index-semantics fix: only delivered lodging counts.
func TestAdvancePastHeldLodging_ReadyOrderDoesNotCount(t *testing.T) {
	jun19 := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	w := renewalWorld(renewalOrder(295, "ezekiel", "john", jun19, 1, OrderStateReady))
	if got := advancePastHeldLodging(w, "ezekiel", "john", jun19, 0); !got.Equal(jun19) {
		t.Errorf("a Ready (undelivered) order should not count as held: got %s, want 2026-06-19", ymd(got))
	}
}

// No delivered coverage → the requested night is unchanged.
func TestAdvancePastHeldLodging_NoCoverageUnchanged(t *testing.T) {
	jun19 := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	if got := advancePastHeldLodging(renewalWorld(), "ezekiel", "john", jun19, 0); !got.Equal(jun19) {
		t.Errorf("no coverage = %s, want unchanged 2026-06-19", ymd(got))
	}
}

// A past, non-overlapping booking does not push a fresh booking forward.
func TestAdvancePastHeldLodging_PastBookingIgnored(t *testing.T) {
	jun10 := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	jun19 := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	w := renewalWorld(renewalOrder(100, "ezekiel", "john", jun10, 1, OrderStateDelivered))
	if got := advancePastHeldLodging(w, "ezekiel", "john", jun19, 0); !got.Equal(jun19) {
		t.Errorf("past booking shouldn't bump: got %s, want 2026-06-19", ymd(got))
	}
}

// Stacked renewals keep extending: holding 19 and 20, a renew of 19 lands on 21.
func TestAdvancePastHeldLodging_StackedExtends(t *testing.T) {
	jun19 := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	jun20 := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	w := renewalWorld(
		renewalOrder(295, "ezekiel", "john", jun19, 1, OrderStateDelivered),
		renewalOrder(297, "ezekiel", "john", jun20, 1, OrderStateDelivered),
	)
	if got := advancePastHeldLodging(w, "ezekiel", "john", jun19, 0); ymd(got) != "2026-06-21" {
		t.Errorf("stacked renew = %s, want 2026-06-21", ymd(got))
	}
}

// A gap in coverage is filled: holding 19 and 21 (not 20), a renew of 19 lands
// on the free 20, not past everything.
func TestAdvancePastHeldLodging_FillsGap(t *testing.T) {
	jun19 := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	jun21 := time.Date(2026, 6, 21, 0, 0, 0, 0, time.UTC)
	w := renewalWorld(
		renewalOrder(1, "ezekiel", "john", jun19, 1, OrderStateDelivered),
		renewalOrder(2, "ezekiel", "john", jun21, 1, OrderStateDelivered),
	)
	if got := advancePastHeldLodging(w, "ezekiel", "john", jun19, 0); ymd(got) != "2026-06-20" {
		t.Errorf("gap fill = %s, want 2026-06-20", ymd(got))
	}
}

// A multi-night booking covers all its nights; a renew lands past the last one.
func TestAdvancePastHeldLodging_MultiNightQty(t *testing.T) {
	jun19 := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	w := renewalWorld(renewalOrder(295, "ezekiel", "john", jun19, 3, OrderStateDelivered))
	if got := advancePastHeldLodging(w, "ezekiel", "john", jun19, 0); ymd(got) != "2026-06-22" {
		t.Errorf("3-night booking renew = %s, want 2026-06-22", ymd(got))
	}
}

// excludeOrderID skips the order being delivered so it doesn't count itself —
// the deliver-time backstop must not advance an order off its own night.
// Order.ID and LedgerID deliberately differ here to prove exclusion is by
// OrderID, not LedgerID (in production they coincide, but the helper iterates
// orders, so it must key on the order's own id).
func TestAdvancePastHeldLodging_ExcludeSelf(t *testing.T) {
	jun19 := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	w := renewalWorld(&Order{
		ID: 999, LedgerID: 297, BuyerID: "ezekiel", SellerID: "john",
		Item: "nights_stay", Qty: 1, State: OrderStateDelivered, ReadyBy: jun19,
	})
	if got := advancePastHeldLodging(w, "ezekiel", "john", jun19, 999); !got.Equal(jun19) {
		t.Errorf("excluded self (by OrderID) should not bump: got %s, want 2026-06-19", ymd(got))
	}
}

// Coverage is scoped to the same seller, to delivered, and to lodging items.
func TestAdvancePastHeldLodging_ScopeSellerStateAndKind(t *testing.T) {
	jun19 := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	otherSeller := renewalOrder(1, "ezekiel", "moses", jun19, 1, OrderStateDelivered)
	notDelivered := renewalOrder(2, "ezekiel", "john", jun19, 1, OrderStateReady)
	expiredOrder := renewalOrder(3, "ezekiel", "john", jun19, 1, OrderStateExpired)
	nonLodging := renewalOrder(4, "ezekiel", "john", jun19, 1, OrderStateDelivered)
	nonLodging.Item = "stew"
	w := renewalWorld(otherSeller, notDelivered, expiredOrder, nonLodging)
	if got := advancePastHeldLodging(w, "ezekiel", "john", jun19, 0); !got.Equal(jun19) {
		t.Errorf("none of these should count as held coverage for (ezekiel, john): got %s, want 2026-06-19", ymd(got))
	}
}
