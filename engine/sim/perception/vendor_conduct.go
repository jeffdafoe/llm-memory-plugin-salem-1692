package perception

import (
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// vendor_conduct.go — LLM-413. The engine-side "is trade slow?" judgment behind
// the trade-conduct block's concession line (renderVendorOperating).
//
// The old block told every operating keeper, every turn, "when trade is slow,
// make a reasonable deal rather than hold the line on price" — but the model
// has no way to know whether trade is slow, so the concession read as a
// standing licence to discount (the one-way ratchet that helped bleed the
// distributor dry, LLM-413). The house pattern applies: the engine computes
// the judgment, render selects the phrase. The concession line now renders
// only when this predicate says the keeper's week actually was slow.

// keeperTradeSlow reports whether trade at the keeper's post has been slow over
// the trailing restockSalesWindow: no ware it sold reached a steady week's
// movement (movementTier >= MovementSteady). Movement is measured in each
// ware's natural weekly unit — a produced good against its own batch size (the
// same quantum the "## Your trade" scene narrates from, so the two cues cannot
// disagree on "what's moving"), any other ware (resold, foraged, lodging)
// against a single unit — so for a pure reseller "slow" means nothing moved
// all week. The conservative direction is deliberate: the concession line is a
// licence to discount, and the failure mode it replaces was that licence
// standing on every turn. A keeper with no sales on record (a fresh shop, or a
// dead week) is slow; one steadily-moving ware anywhere in the book is enough
// to withhold the licence.
func keeperTradeSlow(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot) bool {
	if snap == nil || actorSnap == nil {
		return false
	}
	// Batch quanta for the goods this keeper produces (recipe-backed produce
	// entries — the same set buildForgeChoice menus).
	var batchQuantum map[sim.ItemKind]int
	if actorSnap.RestockPolicy != nil && snap.Recipes != nil {
		for _, e := range actorSnap.RestockPolicy.ProduceEntries() {
			recipe := snap.Recipes[e.Item]
			if recipe == nil || recipe.OutputQty <= 1 {
				continue
			}
			if batchQuantum == nil {
				batchQuantum = make(map[sim.ItemKind]int)
			}
			batchQuantum[e.Item] = recipe.OutputQty
		}
	}
	for key := range snap.PriceBook {
		if key.SellerID != actorID {
			continue
		}
		units, _ := sellerRecentSales(snap, actorID, key.Item, restockSalesWindow)
		quantum := batchQuantum[key.Item]
		if quantum < 1 {
			quantum = 1
		}
		if movementTier(units, quantum) >= MovementSteady {
			return false
		}
	}
	return true
}
