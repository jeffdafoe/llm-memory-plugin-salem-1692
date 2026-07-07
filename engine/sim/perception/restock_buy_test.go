package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// restock_buy_test.go — LLM-308. Unit coverage for the situation-aware co-present restock goad:
// renderRestocking's block/cap branch prose and buildRestocking's Block/SellerStock determination,
// the reseller twin of stall_repair_buy_test.go / farm_upkeep_buy_test.go. The golden scenarios pin
// the assembled prompt; these pin the render/builder decision axes directly.

func renderRestock(v *RestockingView) string {
	var b strings.Builder
	renderRestocking(&b, v)
	return b.String()
}

// coPresentSageView builds a one-item restock view for a co-present sage seller: Elizabeth is out
// of sage (cap 4 → headroom 4), the seller "Josiah Thorne" is co-present, and the caller sets the
// SellerStock/Block/PendingOffer axes under test. No anchor/sales/affordability facts (left silent)
// so the assertions bind to the co-present branch alone.
func coPresentSageView(stock int, block copresentBuyBlock, pending bool) *RestockingView {
	return &RestockingView{
		BuyerCoins: 61,
		Items: []RestockItemView{{
			ItemLabel:                     "sage",
			CurrentQty:                    0,
			Cap:                           4,
			CoPresentSeller:               "Josiah Thorne",
			PendingOfferToCoPresentSeller: pending,
			SellerStock:                   stock,
			Block:                         block,
			AffordableQty:                 -1,
			kind:                          "sage",
		}},
	}
}

func TestRenderRestocking_CoPresentFullStock_GoadsBuy(t *testing.T) {
	out := renderRestock(coPresentSageView(12, copresentBuyOK, false))
	if !strings.Contains(out, "Buy it now —") {
		t.Errorf("full-stock co-present buy should goad 'Buy it now'; got:\n%s", out)
	}
	if !strings.Contains(out, "a qty up to 4") {
		t.Errorf("should ask the full headroom (4); got:\n%s", out)
	}
}

func TestRenderRestocking_LowStock_CapsQtyToStock(t *testing.T) {
	out := renderRestock(coPresentSageView(1, copresentBuyOK, false))
	if !strings.Contains(out, "can spare only 1") {
		t.Errorf("should name the seller's 1-sage stock; got:\n%s", out)
	}
	if !strings.Contains(out, "a qty up to 1") {
		t.Errorf("should cap the ask at his stock (1); got:\n%s", out)
	}
	if strings.Contains(out, "up to 4") {
		t.Errorf("must not ask the full 4 when only 1 is held; got:\n%s", out)
	}
}

func TestRenderRestocking_CoinBlock_SoftensNoGoad(t *testing.T) {
	out := renderRestock(coPresentSageView(12, copresentBuyBlockedCoin, false))
	if strings.Contains(out, "Buy it now —") {
		t.Errorf("a coin-blocked buy must not goad 'Buy it now'; got:\n%s", out)
	}
	if !strings.Contains(out, "coins recover") {
		t.Errorf("a coin block should point at the purse; got:\n%s", out)
	}
}

func TestRenderRestocking_TermsBlock_SoftensNoGoad(t *testing.T) {
	out := renderRestock(coPresentSageView(12, copresentBuyBlockedTerms, false))
	if strings.Contains(out, "Buy it now —") {
		t.Errorf("a terms-blocked buy must not goad 'Buy it now'; got:\n%s", out)
	}
	if !strings.Contains(out, "aren't finding a deal") {
		t.Errorf("a terms block should point at the dead-ended negotiation; got:\n%s", out)
	}
}

func TestRenderRestocking_NoStock_SoftensNoGoad(t *testing.T) {
	out := renderRestock(coPresentSageView(0, copresentBuyBlockedNoStock, false))
	if strings.Contains(out, "Buy it now —") {
		t.Errorf("a no-stock seller must not be goaded for a buy; got:\n%s", out)
	}
	if !strings.Contains(out, "no sage to spare") {
		t.Errorf("a no-stock block should say he has none; got:\n%s", out)
	}
	if strings.Contains(out, "forging") {
		t.Errorf("the no-stock soften must be item-neutral for a restock good, not smith-forged; got:\n%s", out)
	}
}

func TestRenderRestocking_PendingOffer_Bides(t *testing.T) {
	out := renderRestock(coPresentSageView(12, copresentBuyOK, true))
	if strings.Contains(out, "Buy it now —") {
		t.Errorf("a standing offer must not re-goad; got:\n%s", out)
	}
	if !strings.Contains(out, "Wait here for their answer") {
		t.Errorf("a standing offer should bide; got:\n%s", out)
	}
}

// sageItem pulls the single sage line out of a built restock view.
func sageItem(t *testing.T, v *RestockingView) RestockItemView {
	t.Helper()
	if v == nil {
		t.Fatal("expected a restock view")
	}
	for _, it := range v.Items {
		if it.kind == "sage" {
			return it
		}
	}
	t.Fatal("no sage item in restock view")
	return RestockItemView{}
}

func TestBuildRestocking_CoPresentHealthy_NoBlock(t *testing.T) {
	snap, actorID, _ := resellerCoPresentSageSellerPresent()
	it := sageItem(t, buildRestocking(snap, actorID, snap.Actors[actorID]))
	if it.Block != copresentBuyOK {
		t.Errorf("Block = %v, want copresentBuyOK (healthy purse, no prior offers, well-stocked seller)", it.Block)
	}
	if it.SellerStock != 12 {
		t.Errorf("SellerStock = %d, want 12 (Josiah's held sage)", it.SellerStock)
	}
}

func TestBuildRestocking_LowStock_CapReflectsSellerStock(t *testing.T) {
	snap, actorID, _ := resellerCoPresentSageSellerLowStock()
	it := sageItem(t, buildRestocking(snap, actorID, snap.Actors[actorID]))
	if it.Block != copresentBuyOK {
		t.Errorf("Block = %v, want copresentBuyOK (affordable, no standoff — the buy stands, just capped)", it.Block)
	}
	if it.SellerStock != 1 {
		t.Errorf("SellerStock = %d, want 1 (Josiah's held stock)", it.SellerStock)
	}
	headroom := it.Cap - it.CurrentQty
	if it.SellerStock >= headroom {
		t.Errorf("test premise broken: seller stock %d should be below the headroom %d", it.SellerStock, headroom)
	}
}

func TestBuildRestocking_TwoDeclines_TermsBlock(t *testing.T) {
	snap, actorID, _ := resellerCoPresentSageStandoff()
	it := sageItem(t, buildRestocking(snap, actorID, snap.Actors[actorID]))
	if it.Block != copresentBuyBlockedTerms {
		t.Errorf("Block = %v, want copresentBuyBlockedTerms (two declines in this huddle)", it.Block)
	}
}

func TestBuildRestocking_OneDecline_NoBlock(t *testing.T) {
	snap, actorID, _ := resellerCoPresentSageStandoff()
	// A single decline is ordinary haggling, below the standoff threshold.
	delete(snap.PayLedger, 2)
	it := sageItem(t, buildRestocking(snap, actorID, snap.Actors[actorID]))
	if it.Block != copresentBuyOK {
		t.Errorf("Block = %v, want copresentBuyOK (one decline is below the standoff threshold)", it.Block)
	}
}

func TestBuildRestocking_StaleDeclines_NoBlock(t *testing.T) {
	snap, actorID, _ := resellerCoPresentSageStandoff()
	// Push the two declines outside recentlyResolvedOfferWindow — a stale standoff must not keep
	// suppressing the buy after the negotiation has aged out of what the cue reads.
	stale := snap.PublishedAt.Add(-1 * time.Hour)
	for _, e := range snap.PayLedger {
		e.ResolvedAt = stale
	}
	it := sageItem(t, buildRestocking(snap, actorID, snap.Actors[actorID]))
	if it.Block != copresentBuyOK {
		t.Errorf("Block = %v, want copresentBuyOK (declines are older than the recency window)", it.Block)
	}
}

func TestBuildRestocking_DeclinesInOtherHuddle_Ignored(t *testing.T) {
	snap, actorID, _ := resellerCoPresentSageStandoff()
	// Re-stamp the declines under a different huddle — they don't scope this negotiation.
	for _, e := range snap.PayLedger {
		e.HuddleID = "some_other_huddle"
	}
	it := sageItem(t, buildRestocking(snap, actorID, snap.Actors[actorID]))
	if it.Block != copresentBuyOK {
		t.Errorf("Block = %v, want copresentBuyOK (declines were in a different huddle)", it.Block)
	}
}

func TestBuildRestocking_InsufficientFunds_FailFastCoinBlock(t *testing.T) {
	snap, actorID, _ := resellerCoPresentSageStandoff()
	// A single engine-hard insufficient-funds rejection is definitive on the first hit — an empty
	// purse won't clear by re-offering the same coins, so it reads as a coin block, not a terms one.
	snap.PayLedger = map[sim.LedgerID]*sim.PayLedgerEntry{
		1: {ID: 1, BuyerID: actorID, SellerID: "josiah", ItemKind: "sage", State: sim.PayLedgerStateFailedInsufficientFunds, HuddleID: "store_huddle", ResolvedAt: snap.PublishedAt.Add(-1 * time.Minute)},
	}
	it := sageItem(t, buildRestocking(snap, actorID, snap.Actors[actorID]))
	if it.Block != copresentBuyBlockedCoin {
		t.Errorf("Block = %v, want copresentBuyBlockedCoin (one insufficient-funds is fail-fast)", it.Block)
	}
}
