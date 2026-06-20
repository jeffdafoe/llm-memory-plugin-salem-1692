package sim

import (
	"testing"
	"time"
)

// lodging_renewal_internal_test.go — LLM-47. advancePastHeldLodging is the
// shared helper behind the accept-time renewal advance (PayWithItem) and the
// deliver-time backstop (transferOrderGoods). It must move a booking off any
// night the buyer already holds from the same seller, so a "renew" extends the
// stay instead of minting a same-night duplicate that collides on
// pay_ledger_lodging_active_once and wedges checkpointing (the Ezekiel↔John
// incident, 2026-06-19).

func renewalEntry(id LedgerID, buyer, seller ActorID, readyBy time.Time, qty int, state PayLedgerState) *PayLedgerEntry {
	return &PayLedgerEntry{
		ID: id, BuyerID: buyer, SellerID: seller,
		ItemKind: "nights_stay", Qty: qty, State: state, ReadyBy: readyBy,
	}
}

func renewalWorld(entries ...*PayLedgerEntry) *World {
	w := &World{
		ItemKinds: map[ItemKind]*ItemKindDef{
			"nights_stay": {Name: "nights_stay", Capabilities: []string{"service", "lodging"}},
			"stew":        {Name: "stew"},
		},
		PayLedger: map[LedgerID]*PayLedgerEntry{},
	}
	for _, e := range entries {
		w.PayLedger[e.ID] = e
	}
	return w
}

func ymd(t time.Time) string { return t.Format("2006-01-02") }

// The renewal case: the buyer already holds tonight, so a same-night booking
// advances to the next night.
func TestAdvancePastHeldLodging_RenewSameNightBumpsToNext(t *testing.T) {
	jun19 := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	w := renewalWorld(renewalEntry(295, "ezekiel", "john", jun19, 1, PayLedgerStateAccepted))
	got := advancePastHeldLodging(w, "ezekiel", "john", jun19, 0)
	if ymd(got) != "2026-06-20" {
		t.Errorf("renew of a held night = %s, want 2026-06-20", ymd(got))
	}
}

// No existing coverage → the requested night is unchanged.
func TestAdvancePastHeldLodging_NoCoverageUnchanged(t *testing.T) {
	jun19 := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	w := renewalWorld()
	if got := advancePastHeldLodging(w, "ezekiel", "john", jun19, 0); !got.Equal(jun19) {
		t.Errorf("no coverage = %s, want unchanged 2026-06-19", ymd(got))
	}
}

// A past, non-overlapping booking does not push a fresh booking forward.
func TestAdvancePastHeldLodging_PastBookingIgnored(t *testing.T) {
	jun10 := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	jun19 := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	w := renewalWorld(renewalEntry(100, "ezekiel", "john", jun10, 1, PayLedgerStateAccepted))
	if got := advancePastHeldLodging(w, "ezekiel", "john", jun19, 0); !got.Equal(jun19) {
		t.Errorf("past booking shouldn't bump: got %s, want 2026-06-19", ymd(got))
	}
}

// Stacked renewals keep extending: holding 19 and 20, a renew of 19 lands on 21.
func TestAdvancePastHeldLodging_StackedExtends(t *testing.T) {
	jun19 := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	jun20 := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	w := renewalWorld(
		renewalEntry(295, "ezekiel", "john", jun19, 1, PayLedgerStateAccepted),
		renewalEntry(297, "ezekiel", "john", jun20, 1, PayLedgerStateAccepted),
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
		renewalEntry(1, "ezekiel", "john", jun19, 1, PayLedgerStateAccepted),
		renewalEntry(2, "ezekiel", "john", jun21, 1, PayLedgerStateAccepted),
	)
	if got := advancePastHeldLodging(w, "ezekiel", "john", jun19, 0); ymd(got) != "2026-06-20" {
		t.Errorf("gap fill = %s, want 2026-06-20", ymd(got))
	}
}

// A multi-night booking covers all its nights; a renew lands past the last one.
func TestAdvancePastHeldLodging_MultiNightQty(t *testing.T) {
	jun19 := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	w := renewalWorld(renewalEntry(295, "ezekiel", "john", jun19, 3, PayLedgerStateAccepted))
	if got := advancePastHeldLodging(w, "ezekiel", "john", jun19, 0); ymd(got) != "2026-06-22" {
		t.Errorf("3-night booking renew = %s, want 2026-06-22", ymd(got))
	}
}

// excludeID skips the entry being delivered so it doesn't count itself — the
// deliver-time backstop must not advance an order off its own night.
func TestAdvancePastHeldLodging_ExcludeSelf(t *testing.T) {
	jun19 := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	w := renewalWorld(renewalEntry(297, "ezekiel", "john", jun19, 1, PayLedgerStateAccepted))
	if got := advancePastHeldLodging(w, "ezekiel", "john", jun19, 297); !got.Equal(jun19) {
		t.Errorf("excluded self should not bump: got %s, want 2026-06-19", ymd(got))
	}
}

// Coverage is scoped to the same seller and to accepted lodging only.
func TestAdvancePastHeldLodging_ScopeSellerStateAndKind(t *testing.T) {
	jun19 := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	otherSeller := renewalEntry(1, "ezekiel", "moses", jun19, 1, PayLedgerStateAccepted)
	pending := renewalEntry(2, "ezekiel", "john", jun19, 1, PayLedgerStatePending)
	withdrawn := renewalEntry(3, "ezekiel", "john", jun19, 1, PayLedgerStateWithdrawnByBuyer)
	nonLodging := renewalEntry(4, "ezekiel", "john", jun19, 1, PayLedgerStateAccepted)
	nonLodging.ItemKind = "stew"
	w := renewalWorld(otherSeller, pending, withdrawn, nonLodging)
	if got := advancePastHeldLodging(w, "ezekiel", "john", jun19, 0); !got.Equal(jun19) {
		t.Errorf("none of these should count as held coverage for (ezekiel, john): got %s, want 2026-06-19", ymd(got))
	}
}
