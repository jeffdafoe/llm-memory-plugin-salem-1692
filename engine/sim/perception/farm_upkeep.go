package perception

import (
	"fmt"
	"math"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// farm_upkeep.go — LLM-215 owner audience. The "## Farm upkeep" section: a standing
// reminder to a farm owner that the season has worn out their tools and they owe
// fresh shovels, bought from the smith (the coin conduit farm→smith, LLM-83).
// Stock-based, so it derives the obligation live from the owner's coins
// (sim.FarmUpkeepObligation) — no per-object accumulator — and shows only the
// shortfall over what they already carry. A nil view renders nothing (the common
// case: not a farm owner, or nothing owed). Unlike the at-the-stall repair cue this
// is NOT co-location-gated — the owner buys elsewhere (at the blacksmith), so it
// rides any tick. This standing cue is the primary driver; it re-surfaces on the
// owner's normal ticks after the once-a-day wake warrant (renderWarrantLine) is
// consumed.

// FarmUpkeepView is the farm owner's upkeep-buy cue. Non-nil only when the actor
// owns a farm AND owes more upkeep shovels than they currently carry.
type FarmUpkeepView struct {
	ShovelsOwed   int             // obligation derived from coins held above the floor
	ShovelsHeld   int             // shovels the owner currently carries
	ShovelsShort  int             // ShovelsOwed - ShovelsHeld (> 0 whenever the cue shows)
	ShovelVendors []RestockVendor // where to buy the shovels (LLM-274); the move_to destination(s)

	// OnOrder is the total shovels already owed to this owner across their open
	// (Ready) incoming orders — shovels ordered and awaiting hand-over. LLM-518:
	// when > 0 a shovel purchase is already in flight, so the cue states the scene
	// (owed / carried / on order) and renders no buy imperative or vendor/co-present
	// steer — even when the order only PARTLY covers the shortfall (the line shows the
	// remainder; deliberate, Jeff). buildFarmUpkeep returns such a view with
	// ShovelVendors and CoPresentSeller empty, so it carries no errand
	// (hasFarmUpkeepErrand / HasWalkToSupplier both stay false) and the return-to-post
	// steer brings her back to the farm to wait instead of re-pulling her to the smith
	// every tick (the Elizabeth Ellis farm↔blacksmith loop, 2026-07-24).
	OnOrder int

	// CoPresentSeller is a shovel seller sharing the owner's huddle right now, so a
	// pay_with_item resolves this very tick; "" when none. PendingOffer is true when
	// a still-pending shovel offer already stands with that seller. Both mirror the
	// nail repair-buy errand (LLM-277): the shovel errand walks the owner to the
	// smith the same way and so hit the same co-present dead-spot — at the seller,
	// with pay_with_item available, the weak model narrated and walked off. The
	// co-present imperative closes it; the pending-offer bide steer stops the re-offer
	// churn (LLM-64).
	CoPresentSeller string
	PendingOffer    bool

	// LLM-299: co-present-buy situation awareness, shared with the nail repair-buy
	// (copresent_buy.go). SellerStock is the shovels the CoPresentSeller holds right now
	// (0 when none present); Block selects whether renderFarmUpkeep issues the "Buy it now"
	// imperative or softens to a hold-off. Both are set only when a seller is co-present and
	// no offer is already standing (the PendingOffer bide steer wins first).
	SellerStock int
	Block       copresentBuyBlock
}

// buildFarmUpkeep returns the owner's upkeep-buy cue, or nil. Pure over the snapshot.
// Gated on: the actor owns a farm, and their coin balance implies more upkeep shovels
// than they hold (a non-positive FarmUpkeepCoinsPerShovel makes the obligation 0, so
// the feature's off-switch also silences the cue).
func buildFarmUpkeep(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot) *FarmUpkeepView {
	if snap == nil || actorSnap == nil {
		return nil
	}
	if sim.OwnedFarm(snap.VillageObjects, actorID) == nil {
		return nil
	}
	owed := sim.FarmUpkeepObligation(actorSnap.Coins, snap.FarmUpkeepFloor, snap.FarmUpkeepCoinsPerShovel)
	held := actorSnap.Inventory[sim.ShovelItemKind]
	// DYNAMIC target, not a fixed daily debt: `owed` is re-derived from CURRENT coins
	// every build, so as the owner buys shovels (coins fall, held rises) the shortfall
	// shrinks and clears at a fixed point (held == owed) — self-limiting, so a farm
	// whose balance drops mid-cycle is never over-taxed. This is the greenlit stock-
	// based, no-accumulator design; the coin actually collected is <= the boundary
	// assessment, by intent.
	if owed <= held {
		return nil // nothing owed beyond what they already carry
	}
	// LLM-518: ANY open shovel order (accepted, awaiting hand-over) means a purchase
	// is already in flight from the smith — the sole shovel producer, the farm→smith
	// upkeep conduit. Re-advertising a buy every tick while she waited is what fought
	// the return-to-post steer and ping-ponged her farm↔blacksmith. So on any order,
	// state the scene and stop — deliberately even when the order only PARTLY covers
	// the shortfall (Jeff's call): the rendered line shows owed / carried / on-order,
	// so the model can read any remainder and decide, rather than being handed an
	// imperative it can't complete. The gate is order-presence, not order-covers-
	// shortfall. Returning with ShovelVendors + CoPresentSeller empty is load-bearing:
	// the view then carries no errand (hasFarmUpkeepErrand / HasWalkToSupplier stay
	// false), so the return-to-post steer walks her back to the farm to wait. If the
	// smith never delivers, the order eventually leaves Ready (expiry sweep) and drops
	// out of this count — the buy cue re-arms on its own, the built-in fallback.
	if onOrder := openIncomingOrderQty(snap, actorID, sim.ShovelItemKind); onOrder > 0 {
		return &FarmUpkeepView{
			ShovelsOwed:  owed,
			ShovelsHeld:  held,
			ShovelsShort: owed - held,
			OnOrder:      onOrder,
		}
	}
	coName, coID := coPresentSellerForItem(snap, actorID, actorSnap, sim.ShovelItemKind)
	// LLM-274: resolve the shovel supplier(s) so the cue names a move_to destination
	// instead of the dead-end "the blacksmith". Same restock-directory path the
	// stall-repair nail cue uses — findItemVendors names only first-hand producers
	// (the smith produces shovels, LLM-200), separates out the remembered-shut and the
	// unpayable, and dedupes by workplace. Empty → render keeps the generic "from the
	// blacksmith". The blocked suppliers it also returns are the restock cue's to
	// narrate (LLM-406); this cue names the shortfall and its own steer, so it takes
	// only the destinations it can actually send the owner to.
	shovelVendors, _ := findItemVendors(snap, actorID, actorSnap, sim.ShovelItemKind)
	view := &FarmUpkeepView{
		ShovelsOwed:   owed,
		ShovelsHeld:   held,
		ShovelsShort:  owed - held,
		ShovelVendors: shovelVendors,
		// LLM-277: the co-present buy-here fast-path, mirroring the nail repair-buy.
		CoPresentSeller: coName,
		PendingOffer:    coID != "" && hasPendingOfferTo(snap, actorID, coID, sim.ShovelItemKind),
	}
	// LLM-299: make the co-present shovel goad situation-aware via the same shared classifier
	// the nail repair-buy uses (copresent_buy.go) — cap the ask at the seller's live stock and
	// hold off under no-stock / conserve or empty purse / a dead-ended negotiation, so the
	// shovel goad and the "## Restocking" hold-off never contradict. The PendingOffer bide
	// steer wins first, so skip the block scan under it.
	if coID != "" && !view.PendingOffer {
		view.SellerStock, view.Block = classifyCoPresentBuy(snap, actorID, actorSnap, coID, sim.ShovelItemKind)
	}
	return view
}

// openIncomingOrderQty totals the quantity of `item` the buyer is owed across all of
// their open (Ready) incoming orders — goods ordered and awaiting hand-over. Pure over
// the snapshot; mirrors the buyer-side scan in buildPendingOrderViews. LLM-518: the
// farm-upkeep cue calls this for shovel (a smith-only good) to know whether a purchase
// is already in flight. Scoped to BuyerID == buyer: the upkeep obligation is the farm
// owner's own, so an order someone else placed must not suppress her cue. Saturates to
// MaxInt on overflow rather than letting a malformed snapshot wrap the sum negative — a
// negative total would read as "nothing on order" and wrongly re-arm the buy cue (the
// one failure mode a plain += would introduce; code_review LLM-518).
func openIncomingOrderQty(snap *sim.Snapshot, buyer sim.ActorID, item sim.ItemKind) int {
	if snap == nil {
		return 0
	}
	total := 0
	for _, o := range snap.Orders {
		if o == nil || o.State != sim.OrderStateReady {
			continue
		}
		if o.BuyerID != buyer || o.Item != item || o.Qty <= 0 {
			continue
		}
		if total > math.MaxInt-o.Qty {
			return math.MaxInt
		}
		total += o.Qty
	}
	return total
}

// renderFarmUpkeep writes the "## Farm upkeep" section. Content-gated: a nil view
// writes nothing. Symmetrical awareness — states the worn-tools problem AND the way
// out (buy shovels from the blacksmith) in one place, and names the shortfall so the
// owner knows how many to buy.
// HasWalkToSupplier reports whether this cue will render a walk-to shovel supplier
// (LLM-491) — the off-scene destination that contradicts the at-post stabilizer.
// Mirrors renderFarmUpkeep: a co-present seller returns before the vendor list is
// reached (the buy happens right here), and the no-supplier fallback names the
// blacksmith in prose with no destination to walk to.
func (v *FarmUpkeepView) HasWalkToSupplier() bool {
	if v == nil || v.CoPresentSeller != "" {
		return false
	}
	return len(v.ShovelVendors) > 0
}

func renderFarmUpkeep(b *strings.Builder, v *FarmUpkeepView) {
	if v == nil {
		return
	}
	b.WriteString("## Farm upkeep\n")
	b.WriteString("The season's work has worn your farm tools. ")
	// LLM-518: a shovel purchase is already in flight. State the scene — owed /
	// carried / on order — and stop. No buy imperative and no vendor/co-present steer:
	// nagging a second purchase she can't complete is what drove the farm↔blacksmith
	// oscillation. The owed/carried/on-order counts are all shown, so a partly-covered
	// shortfall stays legible (the model can read the remainder) without an imperative.
	// "from the blacksmith" is accurate because shovel is a smith-produced good
	// (LLM-200), the farm→smith upkeep conduit.
	if v.OnOrder > 0 {
		if v.ShovelsHeld > 0 {
			fmt.Fprintf(b, "Upkeep calls for %d shovels and you carry %d, with %d more on order from the blacksmith.\n", v.ShovelsOwed, v.ShovelsHeld, v.OnOrder)
		} else {
			fmt.Fprintf(b, "Upkeep calls for %d shovels and you carry none, with %d on order from the blacksmith.\n", v.ShovelsOwed, v.OnOrder)
		}
		return
	}
	if v.ShovelsHeld > 0 {
		fmt.Fprintf(b, "Upkeep calls for %d shovels and you carry %d. ", v.ShovelsOwed, v.ShovelsHeld)
	}
	shovels := "a fresh shovel"
	if v.ShovelsShort != 1 {
		shovels = fmt.Sprintf("%d fresh shovels", v.ShovelsShort)
	}
	// LLM-277: a shovel seller shares the huddle right now — issue the concrete
	// buy-here imperative (or a bide steer when an offer already stands) instead of
	// the walk-to list, the same co-present progression the nail repair-buy and
	// "## Restocking" cues use. Placed before the walk-to/generic branches so those
	// keep their exact wording when no seller is co-present.
	if v.CoPresentSeller != "" {
		switch {
		case v.PendingOffer:
			// A shovel offer already stands with the co-present seller — bide, don't
			// re-offer (the LLM-64 co-present-offer guard).
			renderCoPresentBuyPending(b, v.CoPresentSeller, "shovel")
		case v.Block == copresentBuyBlockedNoStock, v.Block == copresentBuyBlockedCoin, v.Block == copresentBuyBlockedTerms:
			// LLM-299: seller has no stock / the purse can't take it on (conserve or an
			// insufficient-funds fail) / the negotiation has dead-ended — soften to a
			// hold-off instead of goading the buy, so it never contradicts the "## Restocking"
			// hold-off advice.
			renderCoPresentBuySoften(b, v.CoPresentSeller, "shovels", v.Block)
		case v.SellerStock > 0 && v.SellerStock < v.ShovelsShort:
			// LLM-299: he can't cover the whole shortfall — cap the ask at what he holds so
			// the "qty up to N" never exceeds his stock.
			renderCoPresentBuyCapped(b, v.CoPresentSeller, "shovels", sim.ShovelItemKind, v.SellerStock)
		default:
			fmt.Fprintf(b, "Buy %s to set the farm right for the season. ", shovels)
			renderCoPresentBuy(b, v.CoPresentSeller, "shovels", sim.ShovelItemKind, v.ShovelsShort)
		}
		return
	}
	if len(v.ShovelVendors) > 0 {
		// LLM-274: name the actual shovel supplier(s) — workplace + structure_id — in the
		// model-proven format so the weak model walks the errand. No "come back" hop: the
		// daily assessment consumes the held shovels, so buying them IS the whole action
		// (unlike stall repair, which mends on site).
		fmt.Fprintf(b, "Buy %s to set the farm right for the season. Use move_to to reach a supplier, then pay_with_item once you arrive:\n", shovels)
		renderWalkToVendors(b, v.ShovelVendors)
	} else {
		// No shovel supplier resolves (none produce them, or all shut/unaffordable) —
		// keep the generic sentence rather than a dead-end target (LLM-216 posture).
		fmt.Fprintf(b, "Buy %s from the blacksmith to set the farm right for the season.\n", shovels)
	}
}
