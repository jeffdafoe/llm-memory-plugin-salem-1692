package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// farm_upkeep_buy_test.go — LLM-299. Unit coverage for the situation-aware co-present shovel
// goad: buildFarmUpkeep's Block/SellerStock determination and renderFarmUpkeep's branch
// prose, the shovel twin of stall_repair_buy_test.go. The golden scenarios pin the assembled
// prompt; these pin the builder/render decision axes directly.

func renderUpkeep(v *FarmUpkeepView) string {
	var b strings.Builder
	renderFarmUpkeep(&b, v)
	return b.String()
}

func TestRenderFarmUpkeep_CoPresentFullStock_GoadsBuy(t *testing.T) {
	out := renderUpkeep(&FarmUpkeepView{
		ShovelsOwed: 3, ShovelsHeld: 0, ShovelsShort: 3,
		CoPresentSeller: "Ezekiel Crane", SellerStock: 12, Block: copresentBuyOK,
	})
	if !strings.Contains(out, "Buy it now —") {
		t.Errorf("full-stock co-present buy should goad 'Buy it now'; got:\n%s", out)
	}
	if !strings.Contains(out, "a qty up to 3") {
		t.Errorf("should ask the full shortfall (3); got:\n%s", out)
	}
}

func TestRenderFarmUpkeep_LowStock_CapsQtyToStock(t *testing.T) {
	out := renderUpkeep(&FarmUpkeepView{
		ShovelsOwed: 3, ShovelsHeld: 0, ShovelsShort: 3,
		CoPresentSeller: "Ezekiel Crane", SellerStock: 1, Block: copresentBuyOK,
	})
	if !strings.Contains(out, "can spare only 1") {
		t.Errorf("should name the seller's 1-shovel stock; got:\n%s", out)
	}
	if !strings.Contains(out, "a qty up to 1") {
		t.Errorf("should cap the ask at his stock (1); got:\n%s", out)
	}
	if strings.Contains(out, "up to 3") {
		t.Errorf("must not ask the full 3 when only 1 is held; got:\n%s", out)
	}
}

func TestRenderFarmUpkeep_CoinBlock_SoftensNoGoad(t *testing.T) {
	out := renderUpkeep(&FarmUpkeepView{
		ShovelsOwed: 3, ShovelsHeld: 0, ShovelsShort: 3,
		CoPresentSeller: "Ezekiel Crane", SellerStock: 10, Block: copresentBuyBlockedCoin,
	})
	if strings.Contains(out, "Buy it now —") {
		t.Errorf("a coin-blocked buy must not goad 'Buy it now'; got:\n%s", out)
	}
	if !strings.Contains(out, "coins recover") {
		t.Errorf("a coin block should point at the purse; got:\n%s", out)
	}
}

func TestRenderFarmUpkeep_TermsBlock_SoftensNoGoad(t *testing.T) {
	out := renderUpkeep(&FarmUpkeepView{
		ShovelsOwed: 3, ShovelsHeld: 0, ShovelsShort: 3,
		CoPresentSeller: "Ezekiel Crane", SellerStock: 10, Block: copresentBuyBlockedTerms,
	})
	if strings.Contains(out, "Buy it now —") {
		t.Errorf("a terms-blocked buy must not goad 'Buy it now'; got:\n%s", out)
	}
	if !strings.Contains(out, "aren't finding a deal") {
		t.Errorf("a terms block should point at the dead-ended negotiation; got:\n%s", out)
	}
}

func TestRenderFarmUpkeep_NoStock_SoftensNoGoad(t *testing.T) {
	out := renderUpkeep(&FarmUpkeepView{
		ShovelsOwed: 3, ShovelsHeld: 0, ShovelsShort: 3,
		CoPresentSeller: "Ezekiel Crane", SellerStock: 0, Block: copresentBuyBlockedNoStock,
	})
	if strings.Contains(out, "Buy it now —") {
		t.Errorf("a no-stock seller must not be goaded for a buy; got:\n%s", out)
	}
	if !strings.Contains(out, "no shovels to spare") {
		t.Errorf("a no-stock block should say he has none; got:\n%s", out)
	}
}

func TestRenderFarmUpkeep_PendingOffer_Bides(t *testing.T) {
	out := renderUpkeep(&FarmUpkeepView{
		ShovelsOwed: 3, ShovelsHeld: 0, ShovelsShort: 3,
		CoPresentSeller: "Ezekiel Crane", SellerStock: 10, PendingOffer: true,
	})
	if strings.Contains(out, "Buy it now —") {
		t.Errorf("a standing offer must not re-goad; got:\n%s", out)
	}
	if !strings.Contains(out, "wait here for their answer") {
		t.Errorf("a standing offer should bide; got:\n%s", out)
	}
}

func TestRenderFarmUpkeep_WalkTo_NoGoad(t *testing.T) {
	out := renderUpkeep(&FarmUpkeepView{
		ShovelsOwed: 3, ShovelsHeld: 0, ShovelsShort: 3,
		ShovelVendors: []RestockVendor{{StructureLabel: "Blacksmith", StructureID: "blacksmith"}},
	})
	if strings.Contains(out, "Buy it now —") {
		t.Errorf("the walk-to arm has no co-present seller to 'buy now' from; got:\n%s", out)
	}
	if !strings.Contains(out, "Use move_to to reach a supplier") {
		t.Errorf("the walk-to arm should name the move_to path; got:\n%s", out)
	}
}

func TestBuildFarmUpkeep_HealthyCoPresent_NoBlock(t *testing.T) {
	snap, actorID, _ := farmOwnerOwesUpkeepSellerPresent()
	v := buildFarmUpkeep(snap, actorID, snap.Actors[actorID])
	if v == nil {
		t.Fatal("expected a farm-upkeep errand")
	}
	if v.Block != copresentBuyOK {
		t.Errorf("Block = %v, want copresentBuyOK (healthy purse, no prior offers, well-stocked smith)", v.Block)
	}
	if v.SellerStock != 12 {
		t.Errorf("SellerStock = %d, want 12 (the smith's held shovels)", v.SellerStock)
	}
}

func TestBuildFarmUpkeep_LowStock_CapReflectsSellerStock(t *testing.T) {
	snap, actorID, _ := farmOwnerOwesUpkeepSellerLowStock()
	v := buildFarmUpkeep(snap, actorID, snap.Actors[actorID])
	if v == nil {
		t.Fatal("expected a farm-upkeep errand")
	}
	if v.Block != copresentBuyOK {
		t.Errorf("Block = %v, want copresentBuyOK (affordable, no standoff — the buy stands, just capped)", v.Block)
	}
	if v.SellerStock != 1 {
		t.Errorf("SellerStock = %d, want 1 (the smith's held stock)", v.SellerStock)
	}
	if v.SellerStock >= v.ShovelsShort {
		t.Errorf("test premise broken: seller stock %d should be below the shortfall %d", v.SellerStock, v.ShovelsShort)
	}
}

func TestBuildFarmUpkeep_TwoDeclines_TermsBlock(t *testing.T) {
	snap, actorID, _ := farmOwnerStandoffDeclinedShovels()
	v := buildFarmUpkeep(snap, actorID, snap.Actors[actorID])
	if v == nil {
		t.Fatal("expected a farm-upkeep errand")
	}
	if v.Block != copresentBuyBlockedTerms {
		t.Errorf("Block = %v, want copresentBuyBlockedTerms (two declines in this huddle)", v.Block)
	}
}

func TestBuildFarmUpkeep_OneDecline_NoBlock(t *testing.T) {
	snap, actorID, _ := farmOwnerStandoffDeclinedShovels()
	// A single decline is ordinary haggling, below the standoff threshold.
	delete(snap.PayLedger, 2)
	v := buildFarmUpkeep(snap, actorID, snap.Actors[actorID])
	if v == nil {
		t.Fatal("expected a farm-upkeep errand")
	}
	if v.Block != copresentBuyOK {
		t.Errorf("Block = %v, want copresentBuyOK (one decline is below the standoff threshold)", v.Block)
	}
}

func TestBuildFarmUpkeep_InsufficientFunds_FailFastCoinBlock(t *testing.T) {
	snap, actorID, _ := farmOwnerStandoffDeclinedShovels()
	// A single engine-hard insufficient-funds rejection is definitive on the first hit.
	snap.PayLedger = map[sim.LedgerID]*sim.PayLedgerEntry{
		1: {ID: 1, BuyerID: actorID, SellerID: "ezekiel", ItemKind: sim.ShovelItemKind, State: sim.PayLedgerStateFailedInsufficientFunds, HuddleID: "smith_huddle", ResolvedAt: snap.PublishedAt.Add(-1 * time.Minute)},
	}
	v := buildFarmUpkeep(snap, actorID, snap.Actors[actorID])
	if v == nil {
		t.Fatal("expected a farm-upkeep errand")
	}
	if v.Block != copresentBuyBlockedCoin {
		t.Errorf("Block = %v, want copresentBuyBlockedCoin (one insufficient-funds is fail-fast)", v.Block)
	}
}

func TestBuildFarmUpkeep_StaleDeclines_NoBlock(t *testing.T) {
	snap, actorID, _ := farmOwnerStandoffDeclinedShovels()
	// Push the two declines outside recentlyResolvedOfferWindow — a stale standoff must not
	// keep suppressing the buy.
	stale := snap.PublishedAt.Add(-1 * time.Hour)
	for _, e := range snap.PayLedger {
		e.ResolvedAt = stale
	}
	v := buildFarmUpkeep(snap, actorID, snap.Actors[actorID])
	if v == nil {
		t.Fatal("expected a farm-upkeep errand")
	}
	if v.Block != copresentBuyOK {
		t.Errorf("Block = %v, want copresentBuyOK (declines are older than the recency window)", v.Block)
	}
}

func TestBuildFarmUpkeep_DeclinesInOtherHuddle_Ignored(t *testing.T) {
	snap, actorID, _ := farmOwnerStandoffDeclinedShovels()
	// Re-stamp the declines under a different huddle — they don't scope this negotiation.
	for _, e := range snap.PayLedger {
		e.HuddleID = "some_other_huddle"
	}
	v := buildFarmUpkeep(snap, actorID, snap.Actors[actorID])
	if v == nil {
		t.Fatal("expected a farm-upkeep errand")
	}
	if v.Block != copresentBuyOK {
		t.Errorf("Block = %v, want copresentBuyOK (declines were in a different huddle)", v.Block)
	}
}

func TestBuildFarmUpkeep_Conserve_CoinBlock(t *testing.T) {
	snap, actorID, _ := farmOwnerConservingOwesUpkeep()
	v := buildFarmUpkeep(snap, actorID, snap.Actors[actorID])
	if v == nil {
		t.Fatal("expected a farm-upkeep errand")
	}
	if v.Block != copresentBuyBlockedCoin {
		t.Errorf("Block = %v, want copresentBuyBlockedCoin (keeper is in working-capital conserve mode)", v.Block)
	}
}
