package perception

import (
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// working_capital.go — LLM-294. The "conserve mode" determination shared by the two
// render targets: the "## Restocking" buy cue, softened to a hold-off-buying steer
// (restock.go, Tier 1), and the "## What your wares fetch" cue, extended with a
// sell-first nudge (trade_value.go, Tier 2). A keeper falls into conserve mode when
// its purse is below the operator working-capital floor (snap.MerchantCoinFloor) AND
// it is sitting on unsold sellable stock — the coin-poor-but-stock-rich case that
// otherwise keeps the restock producer draining its purse on inputs it can't yet
// afford. A coin-poor keeper with EMPTY shelves is NOT in conserve mode: it still
// needs to buy inputs to have anything to sell (the empty-shelf exception), so the
// ordinary buy cue stands.
//
// Perception facts, not clamps (the LLM-223 philosophy): the gate only changes what
// the keeper is TOLD; its own deliberation still chooses. Nothing here touches the
// engine's buy/sell mechanics.

const (
	// merchantOverstockWeeksCover is how many weeks of a ware's own recent sell-through
	// a keeper must hold on hand for that ware to read as overstocked — the
	// velocity-relative half of the test (honoring "high relative to sales velocity"):
	// a fast mover must pile up proportionally more before it counts.
	merchantOverstockWeeksCover = 2

	// merchantOverstockAbsFloor is the absolute on-hand units at which a ware reads as
	// overstocked regardless of sell-through — the DEAD-STOCK half of the test. A ware
	// the keeper is not selling at all (weekly sell-through 0) has no velocity to
	// multiply, so the velocity term collapses to 0 and only this floor catches it;
	// without it the worst hoarder (full shelves, zero recent sales — the live
	// miller-flour case) would never flag. The two halves combine with max(), so a
	// ware is overstocked when its on-hand exceeds EITHER.
	merchantOverstockAbsFloor = 8
)

// merchantConserveState carries the conserve-mode determination for a keeper: whether
// it is coin-poor-and-overstocked, its coin balance (for the render prose), and the
// display label of its most-overstocked ware (named in the Tier-2 sell nudge). The
// zero value (Active false) means ordinary buying stands.
type merchantConserveState struct {
	Active          bool
	Coins           int
	OverstockedWare string
}

// merchantConserve computes the conserve state for actorSnap. Active requires: the
// floor is enabled (snap.MerchantCoinFloor > 0 — 0 is the operator off-switch), the
// purse is below it, and at least one of the keeper's own sellable wares is
// overstocked. Pure over the snapshot. Shared by buildRestocking (Tier 1) and
// buildTradeValue (Tier 2) so the buy-cue softening and the sell nudge can never
// disagree on who is conserving.
//
// The coin-poor test short-circuits BEFORE the overstock scan, so the price-book
// walk only runs for a keeper actually below the floor — a healthy keeper (the common
// case) pays nothing.
func merchantConserve(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot) merchantConserveState {
	if snap == nil || actorSnap == nil || actorSnap.RestockPolicy == nil {
		return merchantConserveState{}
	}
	floor := snap.MerchantCoinFloor
	if floor <= 0 || actorSnap.Coins >= floor {
		return merchantConserveState{} // feature off, or purse healthy
	}
	// The keeper's own sellable wares — goods it produces AND goods it resells, the
	// same set the wares-fetch cue values. Pick the most-overstocked (largest on-hand
	// over its OWN threshold) to name in the sell nudge, deterministic by
	// (excess desc, label asc, kind asc).
	bestWare := ""
	bestExcess := 0
	var bestKind sim.ItemKind
	seen := make(map[sim.ItemKind]bool)
	better := func(excess int, label string, kind sim.ItemKind) bool {
		if bestWare == "" {
			return true
		}
		if excess != bestExcess {
			return excess > bestExcess
		}
		if label != bestWare {
			return label < bestWare
		}
		return kind < bestKind
	}
	consider := func(item sim.ItemKind) {
		if seen[item] {
			return
		}
		seen[item] = true
		onHand := actorSnap.Inventory[item]
		if onHand <= 0 {
			return
		}
		// Overstocked when on-hand clears max(dead-stock floor, weeks-cover × weekly
		// sell-through). weeklyUnits is this actor's own realized sales of the good over
		// the weekly window (0 for a good it never sells — an input it buys-and-consumes
		// self-excludes here unless its raw pile alone clears the absolute floor).
		weeklyUnits, _ := sellerRecentSales(snap, actorID, item, restockSalesWindow)
		threshold := merchantOverstockAbsFloor
		if velo := merchantOverstockWeeksCover * weeklyUnits; velo > threshold {
			threshold = velo
		}
		if onHand < threshold {
			return
		}
		excess := onHand - threshold
		label := itemDisplayLabel(snap, item)
		if better(excess, label, item) {
			bestWare, bestExcess, bestKind = label, excess, item
		}
	}
	for _, e := range actorSnap.RestockPolicy.ProduceEntries() {
		consider(e.Item)
	}
	for _, e := range actorSnap.RestockPolicy.BuyEntries() {
		consider(e.Item)
	}
	if bestWare == "" {
		return merchantConserveState{} // coin-poor but shelves not overstocked — empty-shelf exception
	}
	return merchantConserveState{Active: true, Coins: actorSnap.Coins, OverstockedWare: bestWare}
}
