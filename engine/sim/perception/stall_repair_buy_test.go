package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// stall_repair_buy_test.go — LLM-297. Unit coverage for the situation-aware co-present
// nail goad: buildStallRepairBuy's Block/SellerStock determination and
// renderStallRepairBuy's branch prose. The golden scenarios pin the assembled prompt;
// these pin the builder/render decision axes directly.

func renderStallBuy(v *StallRepairBuyView) string {
	var b strings.Builder
	renderStallRepairBuy(&b, v)
	return b.String()
}

func TestRenderStallRepairBuy_CoPresentFullStock_GoadsBuy(t *testing.T) {
	out := renderStallBuy(&StallRepairBuyView{
		Name: "Ellis Farm", NailsNeeded: 5, NailsHeld: 0, NailsShort: 5,
		CoPresentSeller: "Ezekiel Crane", SellerStock: 21, Block: copresentBuyOK,
	})
	if !strings.Contains(out, "Buy it now —") {
		t.Errorf("full-stock co-present buy should goad 'Buy it now'; got:\n%s", out)
	}
	if !strings.Contains(out, "a qty up to 5") {
		t.Errorf("should ask the full shortfall (5); got:\n%s", out)
	}
}

func TestRenderStallRepairBuy_LowStock_CapsQtyToStock(t *testing.T) {
	out := renderStallBuy(&StallRepairBuyView{
		Name: "Ellis Farm", NailsNeeded: 5, NailsHeld: 0, NailsShort: 5,
		CoPresentSeller: "Ezekiel Crane", SellerStock: 2, Block: copresentBuyOK,
	})
	if !strings.Contains(out, "can spare only 2") {
		t.Errorf("should name the seller's 2-nail stock; got:\n%s", out)
	}
	if !strings.Contains(out, "a qty up to 2") {
		t.Errorf("should cap the ask at his stock (2); got:\n%s", out)
	}
	if strings.Contains(out, "up to 5") {
		t.Errorf("must not ask the full 5 when only 2 are held; got:\n%s", out)
	}
}

func TestRenderStallRepairBuy_CoinBlock_SoftensNoGoad(t *testing.T) {
	out := renderStallBuy(&StallRepairBuyView{
		Name: "Ellis Farm", NailsNeeded: 5, NailsHeld: 0, NailsShort: 5,
		CoPresentSeller: "Ezekiel Crane", SellerStock: 10, Block: copresentBuyBlockedCoin,
	})
	if strings.Contains(out, "Buy it now —") {
		t.Errorf("a coin-blocked buy must not goad 'Buy it now'; got:\n%s", out)
	}
	if !strings.Contains(out, "coins recover") {
		t.Errorf("a coin block should point at the purse; got:\n%s", out)
	}
}

func TestRenderStallRepairBuy_TermsBlock_SoftensNoGoad(t *testing.T) {
	out := renderStallBuy(&StallRepairBuyView{
		Name: "Ellis Farm", NailsNeeded: 5, NailsHeld: 0, NailsShort: 5,
		CoPresentSeller: "Ezekiel Crane", SellerStock: 10, Block: copresentBuyBlockedTerms,
	})
	if strings.Contains(out, "Buy it now —") {
		t.Errorf("a terms-blocked buy must not goad 'Buy it now'; got:\n%s", out)
	}
	if !strings.Contains(out, "aren't finding a deal") {
		t.Errorf("a terms block should point at the dead-ended negotiation; got:\n%s", out)
	}
}

func TestRenderStallRepairBuy_NoStock_SoftensNoGoad(t *testing.T) {
	out := renderStallBuy(&StallRepairBuyView{
		Name: "Ellis Farm", NailsNeeded: 5, NailsHeld: 0, NailsShort: 5,
		CoPresentSeller: "Ezekiel Crane", SellerStock: 0, Block: copresentBuyBlockedNoStock,
	})
	if strings.Contains(out, "Buy it now —") {
		t.Errorf("a no-stock seller must not be goaded for a buy; got:\n%s", out)
	}
	if !strings.Contains(out, "no nails to spare") {
		t.Errorf("a no-stock block should say he has none; got:\n%s", out)
	}
}

func TestRenderStallRepairBuy_PendingOffer_Bides(t *testing.T) {
	out := renderStallBuy(&StallRepairBuyView{
		Name: "Ellis Farm", NailsNeeded: 5, NailsHeld: 0, NailsShort: 5,
		CoPresentSeller: "Ezekiel Crane", SellerStock: 10, PendingOffer: true,
	})
	if strings.Contains(out, "Buy it now —") {
		t.Errorf("a standing offer must not re-goad; got:\n%s", out)
	}
	if !strings.Contains(out, "wait here for their answer") {
		t.Errorf("a standing offer should bide; got:\n%s", out)
	}
}

func TestRenderStallRepairBuy_WalkTo_NoGoad(t *testing.T) {
	out := renderStallBuy(&StallRepairBuyView{
		Name: "Ellis Farm", NailsNeeded: 5, NailsHeld: 0, NailsShort: 5,
		Vendors: []RestockVendor{{StructureLabel: "Blacksmith", StructureID: "blacksmith"}},
	})
	if strings.Contains(out, "Buy it now —") {
		t.Errorf("the walk-to arm has no co-present seller to 'buy now' from; got:\n%s", out)
	}
	if !strings.Contains(out, "Use move_to to reach a supplier") {
		t.Errorf("the walk-to arm should name the move_to path; got:\n%s", out)
	}
}

func TestBuildStallRepairBuy_HealthyCoPresent_NoBlock(t *testing.T) {
	snap, actorID, _ := ownerOffPostAtSmithShortNails()
	v := buildStallRepairBuy(snap, actorID, snap.Actors[actorID])
	if v == nil {
		t.Fatal("expected a nail-buy errand")
	}
	if v.Block != copresentBuyOK {
		t.Errorf("Block = %v, want copresentBuyOK (healthy purse, no prior offers, well-stocked smith)", v.Block)
	}
	if v.SellerStock != 21 {
		t.Errorf("SellerStock = %d, want 21 (the smith's held nails)", v.SellerStock)
	}
}

func TestBuildStallRepairBuy_TwoDeclines_TermsBlock(t *testing.T) {
	snap, actorID, _ := ownerStandoffDeclinedNails()
	v := buildStallRepairBuy(snap, actorID, snap.Actors[actorID])
	if v == nil {
		t.Fatal("expected a nail-buy errand")
	}
	if v.Block != copresentBuyBlockedTerms {
		t.Errorf("Block = %v, want copresentBuyBlockedTerms (two declines in this huddle)", v.Block)
	}
}

func TestBuildStallRepairBuy_OneDecline_NoBlock(t *testing.T) {
	snap, actorID, _ := ownerStandoffDeclinedNails()
	// A single decline is ordinary haggling, below the standoff threshold.
	delete(snap.PayLedger, 2)
	v := buildStallRepairBuy(snap, actorID, snap.Actors[actorID])
	if v == nil {
		t.Fatal("expected a nail-buy errand")
	}
	if v.Block != copresentBuyOK {
		t.Errorf("Block = %v, want copresentBuyOK (one decline is below the standoff threshold)", v.Block)
	}
}

func TestBuildStallRepairBuy_InsufficientFunds_FailFastCoinBlock(t *testing.T) {
	snap, actorID, _ := ownerStandoffDeclinedNails()
	// A single engine-hard insufficient-funds rejection is definitive on the first hit.
	snap.PayLedger = map[sim.LedgerID]*sim.PayLedgerEntry{
		1: {ID: 1, BuyerID: actorID, SellerID: "ezekiel", ItemKind: sim.NailItemKind, State: sim.PayLedgerStateFailedInsufficientFunds, HuddleID: "smith_huddle", ResolvedAt: snap.PublishedAt.Add(-1 * time.Minute)},
	}
	v := buildStallRepairBuy(snap, actorID, snap.Actors[actorID])
	if v == nil {
		t.Fatal("expected a nail-buy errand")
	}
	if v.Block != copresentBuyBlockedCoin {
		t.Errorf("Block = %v, want copresentBuyBlockedCoin (one insufficient-funds is fail-fast)", v.Block)
	}
}

func TestBuildStallRepairBuy_AgedDeclines_StillTermsBlock(t *testing.T) {
	snap, actorID, _ := ownerStandoffDeclinedNails()
	// Age the two declines well past recentlyResolvedOfferWindow. The standoff latches for the
	// huddle's lifetime (LLM-510) — the old recency filter let consecutive declines age out
	// between wake-backoff-paced re-offers, so the goad returned and the live nail loop ran
	// 13 declines in 53 minutes. Only a fresh huddle resets the counter.
	aged := snap.PublishedAt.Add(-1 * time.Hour)
	for _, e := range snap.PayLedger {
		e.ResolvedAt = aged
	}
	v := buildStallRepairBuy(snap, actorID, snap.Actors[actorID])
	if v == nil {
		t.Fatal("expected a nail-buy errand")
	}
	if v.Block != copresentBuyBlockedTerms {
		t.Errorf("Block = %v, want copresentBuyBlockedTerms (the standoff latches for the huddle's lifetime)", v.Block)
	}
}

func TestBuildStallRepairBuy_StaleInsufficientFunds_NoCoinBlock(t *testing.T) {
	snap, actorID, _ := ownerStandoffDeclinedNails()
	// An insufficient-funds fail KEEPS the recency window (unlike declines): a purse can
	// genuinely recover within minutes, and merchantConserve covers ongoing poverty, so a
	// stale fail must not read as a coin block — nor count toward the terms standoff.
	snap.PayLedger = map[sim.LedgerID]*sim.PayLedgerEntry{
		1: {ID: 1, BuyerID: actorID, SellerID: "ezekiel", ItemKind: sim.NailItemKind, State: sim.PayLedgerStateFailedInsufficientFunds, HuddleID: "smith_huddle", ResolvedAt: snap.PublishedAt.Add(-20 * time.Minute)},
	}
	v := buildStallRepairBuy(snap, actorID, snap.Actors[actorID])
	if v == nil {
		t.Fatal("expected a nail-buy errand")
	}
	if v.Block != copresentBuyOK {
		t.Errorf("Block = %v, want copresentBuyOK (the insufficient-funds fail is outside the recency window)", v.Block)
	}
}

func TestBuildStallRepairBuy_StaleFundsPlusAgedDeclines_TermsBlock(t *testing.T) {
	snap, actorID, _ := ownerStandoffDeclinedNails()
	// A stale insufficient-funds entry falls through WITHOUT counting as a decline, while
	// the two aged declines still latch the terms standoff — the mixed-state interaction
	// between the window-gated coin arm and the lifetime decline arm.
	aged := snap.PublishedAt.Add(-20 * time.Minute)
	for _, e := range snap.PayLedger {
		e.ResolvedAt = aged
	}
	snap.PayLedger[3] = &sim.PayLedgerEntry{ID: 3, BuyerID: actorID, SellerID: "ezekiel", ItemKind: sim.NailItemKind, State: sim.PayLedgerStateFailedInsufficientFunds, HuddleID: "smith_huddle", ResolvedAt: aged}
	v := buildStallRepairBuy(snap, actorID, snap.Actors[actorID])
	if v == nil {
		t.Fatal("expected a nail-buy errand")
	}
	if v.Block != copresentBuyBlockedTerms {
		t.Errorf("Block = %v, want copresentBuyBlockedTerms (aged declines latch; the stale funds fail neither coin-blocks nor counts)", v.Block)
	}
}

func TestBuildStallRepairBuy_DeclinesInOtherHuddle_Ignored(t *testing.T) {
	snap, actorID, _ := ownerStandoffDeclinedNails()
	// Re-stamp the declines under a different huddle — they don't scope this negotiation.
	for _, e := range snap.PayLedger {
		e.HuddleID = "some_other_huddle"
	}
	v := buildStallRepairBuy(snap, actorID, snap.Actors[actorID])
	if v == nil {
		t.Fatal("expected a nail-buy errand")
	}
	if v.Block != copresentBuyOK {
		t.Errorf("Block = %v, want copresentBuyOK (declines were in a different huddle)", v.Block)
	}
}

func TestBuildStallRepairBuy_Conserve_CoinBlock(t *testing.T) {
	snap, actorID, _ := keeperConservingOwesNailRepair()
	v := buildStallRepairBuy(snap, actorID, snap.Actors[actorID])
	if v == nil {
		t.Fatal("expected a nail-buy errand")
	}
	if v.Block != copresentBuyBlockedCoin {
		t.Errorf("Block = %v, want copresentBuyBlockedCoin (keeper is in working-capital conserve mode)", v.Block)
	}
}

func TestBuildStallRepairBuy_LowStock_CapReflectsSellerStock(t *testing.T) {
	snap, actorID, _ := ownerShortNailsSellerLowStock()
	v := buildStallRepairBuy(snap, actorID, snap.Actors[actorID])
	if v == nil {
		t.Fatal("expected a nail-buy errand")
	}
	if v.Block != copresentBuyOK {
		t.Errorf("Block = %v, want copresentBuyOK (affordable, no standoff — the buy stands, just capped)", v.Block)
	}
	if v.SellerStock != 2 {
		t.Errorf("SellerStock = %d, want 2 (the smith's held stock)", v.SellerStock)
	}
	if v.SellerStock >= v.NailsShort {
		t.Errorf("test premise broken: seller stock %d should be below the shortfall %d", v.SellerStock, v.NailsShort)
	}
}
