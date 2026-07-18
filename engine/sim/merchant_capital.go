package sim

import (
	"math"
	"time"
)

// merchant_capital.go — LLM-294. The working-capital coin floor: the purse
// balance below which a keeper sitting on unsold sellable stock is steered to
// conserve coin (hold off buying, sell down its shelves) instead of restocking.
// The determination + overstock qualifier live in the perception package (that is
// where the render targets are); this file carries only the sim-side default the
// pg loader seeds when the setting key is absent.
//
// MerchantCoinFloor semantics mirror LaborProduceBoostPct, NOT SeekWorkCoinCeiling:
// the pg loader seeds MerchantCoinFloorDefault when merchant_coin_floor is unset, and
// an explicit 0 STICKS and disables the gate (the off-switch). So there is no
// effective-value indirection — the snapshot mirrors the raw WorldSettings value and
// the perception gate reads it as "floor > 0 && coins < floor" (0 ⇒ feature off).

// MerchantCoinFloorDefault is the coin balance below which a stock-rich keeper is
// steered to conserve (LLM-294). Seeded by the pg loader when merchant_coin_floor is
// absent; an operator can raise/lower it or set 0 to disable via the umbilical. A
// guesstimate, tuned live.
const MerchantCoinFloorDefault = 10

const (
	// MerchantOverstockWeeksCover / MerchantOverstockAbsFloor are the overstock half of
	// the conserve test (LLM-294): a ware is overstocked when its on-hand clears
	// max(abs floor, weeks-cover × weekly sell-through) — the dead-stock floor catches a
	// zero-velocity hoarder, the velocity term scales the bar for a fast mover. Exported
	// and shared with perception.merchantConserve (MerchantOverstockThreshold) so the
	// restock warrant (this package) and the "## Restocking" section (perception) can
	// never disagree on who is conserving.
	MerchantOverstockWeeksCover = 2
	MerchantOverstockAbsFloor   = 8
)

// merchantConserveSalesWindow is the sell-through window for the overstock test — the
// same 7-day window the perception restock demand figures use (restockSalesWindow).
const merchantConserveSalesWindow = 7 * 24 * time.Hour

// MerchantOverstockThreshold is the on-hand count at or above which a ware with the
// given weekly sell-through reads as overstocked: max(abs floor, weeks-cover × weekly
// units). The single source of the overstock bar, shared by perception's conserve
// naming loop and the sim-side actorConserving so the section and the warrant agree.
func MerchantOverstockThreshold(weeklyUnits int) int {
	threshold := MerchantOverstockAbsFloor
	// weeklyUnits is clamped to MaxInt32 upstream, but the velocity term multiplies it
	// by MerchantOverstockWeeksCover — guard the multiply so it can't overflow int on a
	// 32-bit build (a saturating count already reads as "overstocked past any shelf").
	if weeklyUnits > math.MaxInt/MerchantOverstockWeeksCover {
		return math.MaxInt
	}
	if velo := MerchantOverstockWeeksCover * weeklyUnits; velo > threshold {
		threshold = velo
	}
	return threshold
}

// sellerRecentSellUnits sums a seller's own sell-through of item over the conserve
// window ending at now, from the world price book — the sim-side mirror of the
// perception sellerRecentSales units so actorConserving sizes the overstock bar off the
// same demand signal the section shows. Units per book row = qty × consumers (a group
// buy books one row for N consumers), floored at 0 — mirrors perception.observationUnits
// and the price-book doc.
func sellerRecentSellUnits(w *World, sellerID ActorID, item ItemKind, now time.Time) int {
	if w == nil || w.PriceBook == nil {
		return 0
	}
	buf, ok := w.PriceBook[PriceBookKey{SellerID: sellerID, Item: item}]
	if !ok || buf == nil || buf.Len() == 0 {
		return 0
	}
	cutoff := now.Add(-merchantConserveSalesWindow)
	var u int64
	for _, obs := range buf.Snapshot() {
		if obs.At.Before(cutoff) {
			continue
		}
		consumers := obs.Consumers
		if consumers < 1 {
			consumers = 1
		}
		if units := int64(obs.Qty) * int64(consumers); units > 0 {
			u += units
		}
	}
	if u > int64(math.MaxInt32) {
		u = int64(math.MaxInt32)
	}
	return int(u)
}

// actorConserving reports whether the actor is in LLM-294 conserve mode: the working-
// capital floor is enabled (w.Settings.MerchantCoinFloor > 0), the purse is below it,
// AND at least one of the actor's own sellable wares (a produce or buy ware it holds) is
// overstocked. A ware here excludes the actor's own production inputs (LLM-462) — see
// the floors comment below. The coin gate short-circuits before the price-book walk, so a healthy
// keeper (the common case) pays nothing. Mirrors perception.merchantConserve.Active — it
// shares MerchantOverstockThreshold + the sell-through read with the section, so the
// restock warrant producer and the "## Restocking" cue can never disagree on who is
// conserving.
func actorConserving(w *World, a *Actor, now time.Time) bool {
	if w == nil || a == nil || a.RestockPolicy == nil {
		return false
	}
	floor := w.Settings.MerchantCoinFloor
	if floor <= 0 || a.Coins >= floor {
		return false // feature off, or purse healthy
	}
	// LLM-462: a required input to one of the actor's OWN produce recipes is raw
	// material, not merchandise waiting to sell, so it never counts toward the
	// overstock verdict. Hannah Boggs held 19 water — five of which every batch of
	// porridge drinks — and the dead-stock floor read that pile as a full storeroom,
	// which suppressed EVERY buy warrant she had, the stalled porridge line's flour
	// included. She was never woken again. Scoping the test to actual wares puts her
	// back under the empty-shelf exception (her porridge/journeycake/fried_meat
	// shelves are bare), which is what she always was. A finished ware that feeds no
	// recipe of the actor's still counts, so the LLM-298 case (John Ellis conserving
	// on 20 unsold ale, his carrots buy correctly suppressed) is unchanged.
	//
	// ReorderFloors keys exactly the REQUIRED inputs — elective BoostInputs never
	// stall a line, so they stay merchandise — and is the same catalog the reorder
	// threshold reads, so "raw material" means the same thing here and there.
	//
	// INVARIANT — required-input-always-wins. The test is ITEM-level, not quantity-
	// level: an item that is a required input is never merchandise, however much of it
	// is on hand, and even if the actor also produces and sells it (a dual-role good
	// like John Ellis's water, which he both sells and cooks stew with). The obvious
	// alternative — reserve the production floor and weigh the EXCESS as ware — was
	// measured against the live case and does not work: Hannah's floor is 10 (2 batches
	// x 5 water) against 19 on hand, so the excess is 9, and her water sell-through of 4
	// a week puts the bar at max(8, 2x4) = 8. 9 >= 8, so she would still read as
	// overstocked and still never be woken. A reserve big enough to clear that bar would
	// have to be tuned per item, which is a knob with no principled setting.
	//
	// The cost of the simpler rule is bounded and one-directional: a keeper sitting on a
	// genuinely large pile of a dual-role good is not steered to sell it down. It never
	// suppresses a wake, and the actual sell nudge for such a good still comes from the
	// ordinary wares-fetch cue. Under-conserving is the safe side — over-conserving is
	// what silences a keeper completely.
	//
	// Exclusion is scoped to the OVERSTOCK VERDICT only. A required input keeps its own
	// reorder threshold and its own buy warrant (that is the whole point — Hannah's flour
	// warrant is what this restores), so nothing here makes an item unbuyable.
	floors := ReorderFloors(w.Recipes, a.RestockPolicy)
	seen := map[ItemKind]bool{}
	overstocked := func(item ItemKind) bool {
		if seen[item] {
			return false
		}
		seen[item] = true
		if floors[item] > 0 {
			return false // raw material for its own production, not unsold stock
		}
		onHand := a.Inventory[item]
		if onHand <= 0 {
			return false
		}
		return onHand >= MerchantOverstockThreshold(sellerRecentSellUnits(w, a.ID, item, now))
	}
	for _, e := range a.RestockPolicy.ProduceEntries() {
		if overstocked(e.Item) {
			return true
		}
	}
	for _, e := range a.RestockPolicy.BuyEntries() {
		if overstocked(e.Item) {
			return true
		}
	}
	return false
}
