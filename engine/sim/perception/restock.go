package perception

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// restock.go — ZBBS-WORK-322. The "## Restocking" perception section: surfaces,
// to a reseller whose bought-in stock is running low, how to replenish it — each
// `buy` RestockEntry below the reorder threshold (on-hand vs cap) and the
// suppliers selling that item (their workplace, its structure_id for move_to,
// and a per-buyer price hint). The reseller's own LLM decides whether, what, and
// how much to restock, then acts via the existing move_to + pay_with_item tools.
//
// This is the buyer-facing affordance half of the buy-side restock feature; the
// restock producer (engine/sim/restock_tick.go) is the half that brings the
// reseller to a reactor tick by warranting it. Both gate on the same reorder
// threshold (restockReorderThresholdMet, surfaced into the snapshot as
// RestockReorderPct) so the section and the warrant never disagree.
//
// Supplier resolution reuses the structural-vendorship model from
// consumable_vendors.go (a vendor is a non-PC actor stationed at a resolvable
// WorkStructureID holding qty>0 of the item) and its shared helpers
// (itemDisplayLabel, vendorStructureLabel, buyerLastPaidText) — same surface the
// satiation/recovery cues use. The difference: this finder keys on a specific
// ItemKind the reseller wants to buy, not on a need a consumable eases.

// RestockingView is the content-gated "## Restocking" section. A nil view (or
// empty Items) means render omits the section.
type RestockingView struct {
	Items []RestockItemView

	// BuyerCoins is the reseller's coin balance at build time. Rendered into the
	// per-item affordability fact (ZBBS-HOME-459) when coins are the binding
	// limit — see RestockItemView.AffordableQty.
	BuyerCoins int

	// HasOtherBuyGoods is true when the reseller restocks more than one bought-in
	// good (its RestockPolicy holds >1 `buy` entry). The LLM-10 reserve nudge uses
	// it to phrase "keep some back for your other goods" only when there really are
	// other goods to keep coins for; otherwise it falls back to "keep some coins
	// back."
	HasOtherBuyGoods bool
}

// RestockItemView is one low `buy` item the reseller could replenish: its label,
// current on-hand quantity, the cap it restocks toward, and the suppliers
// selling it. Vendors may be empty — the item still surfaces (the reseller knows
// it's low) but with no actionable "buy at X" destination this tick.
type RestockItemView struct {
	ItemLabel  string
	CurrentQty int
	Cap        int
	Vendors    []RestockVendor

	// CoPresentSeller is the display name of a seller of this item who shares the
	// reseller's CURRENT huddle right now — so a pay_with_item(seller: …) for this
	// item resolves this very tick (ZBBS-HOME-388). When set, render swaps the
	// generic "walk to a supplier" vendor list for a concrete "buy it here now"
	// imperative naming this seller: live evidence showed that, even standing at
	// the seller with pay_with_item available and named in the cue, the weak
	// stateful model narrated its need ("I am in need of milk…") and walked off
	// without ever calling the tool. The arrived-at-the-seller moment is exactly
	// when the imperative must become concrete. Empty when no seller of the item
	// is co-present (then the walk-to list renders as before).
	CoPresentSeller string

	// PendingOfferToCoPresentSeller is true when CoPresentSeller is set AND the
	// reseller already has a still-pending pay_with_item offer to that seller for
	// this item (via hasPendingOfferTo — the same still-pending check the satiation
	// co-present cue uses, narrowed to this seller+item — so it can't disagree with
	// the "## Offers you have standing" cue). When set, render drops the headroom/cost lines and
	// the "buy it now" imperative for a stay-and-wait steer, so the reseller bides
	// for the answer instead of re-staking the offer and churning (the
	// Josiah↔Elizabeth milk loop). Auto-clears once the offer leaves Pending
	// (expired/accepted → RecentlyResolvedOffersFromMe), restoring the buy cue. LLM-64.
	PendingOfferToCoPresentSeller bool

	// AffordableQty is how many units the reseller's purse covers at the rate
	// they last paid for this item (newest price-book observation, across all
	// sellers). -1 when no price is on record, in which case the
	// affordability fact stays silent (ZBBS-HOME-459 — the buyer-purse mirror of
	// the WORK-392 sufficiency fact). Render shows it only when coins bind before
	// the cap (AffordableQty < headroom), so a purse that comfortably covers the
	// headroom adds no line.
	AffordableQty int

	// FillCost is the approximate coins to buy the full headroom (cap − current) at
	// the rate the reseller last paid for this item — the same newest price-book
	// observation AffordableQty reads. -1 when no price is on record. LLM-10: render
	// surfaces it as the offer anchor ("Filling to cap is about N coins…") so the
	// weak model sizes the payment to fair cost instead of its whole purse, and the
	// reserve nudge compares it against BuyerCoins to decide whether one fill would
	// take a big share of the purse. Surfaced only when coins comfortably cover the
	// headroom (the AffordableQty-binding case renders the HOME-459 fact instead).
	FillCost int

	// kind is the final sort tie-break so two item kinds sharing a display label
	// order deterministically (BuyEntries order is stable, but the sort makes the
	// section robust to policy reordering too). Unexported — never rendered.
	// Same posture as OwnStockItem.kind (consumable_vendors.go).
	kind sim.ItemKind
}

// RestockVendor is one (workplace, supplier) buy opportunity for a low item.
// StructureID is the supplier's workplace key — the reseller passes it straight
// to move_to(structure_id), then pay_with_item once co-present.
type RestockVendor struct {
	StructureLabel string // "Thorne's General Store" — where the reseller walks to
	StructureID    sim.StructureID
	CostText       string // per-buyer last-paid "~3 coins", or "" when no price is on record

	// Shut is true when the reseller has a live experiential memory of finding
	// this supplier shut (no keeper) within the decay window — render annotates
	// the line so the model deprioritizes the trip. ZBBS-HOME-353.
	Shut bool

	// ClosedNow is true when this structure stocks the item but every vendor of
	// it here is asleep right now — a LIVE read off the snapshot, not the
	// decaying Shut memory. Render prefers it over Shut (live state beats stale
	// recollection). The buyer-side mirror of satiation's keeper-asleep gate
	// (ZBBS-HOME-387): without it the restock cue named an asleep keeper's shop
	// as an open supplier, so a merchant petitioned an unreachable seller in a
	// loop (the Josiah↔Tavern spiral). ZBBS-HOME-406.
	ClosedNow bool
}

// buildRestocking builds the restock view for actorSnap, or nil when the actor
// holds no `buy` entry below the reorder threshold, restock is disabled
// (RestockReorderPct == 0), or it carries no RestockPolicy. Pure over the
// snapshot.
func buildRestocking(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot) *RestockingView {
	if snap == nil || actorSnap == nil || actorSnap.RestockPolicy == nil {
		return nil
	}
	pct := snap.RestockReorderPct
	if pct <= 0 {
		return nil // producer/feature disabled
	}
	buyEntries := actorSnap.RestockPolicy.BuyEntries()
	var items []RestockItemView
	for _, e := range buyEntries {
		cap := e.Cap()
		current := actorSnap.Inventory[e.Item]
		if !sim.RestockReorderThresholdMet(current, cap, pct) {
			continue
		}
		// Coins-bound the buy plan (ZBBS-HOME-459). -1 (no price on record)
		// leaves the fact silent; render further gates it to the case where coins
		// bind before the cap.
		affordable := -1
		if qty, ok := buyerLastPaidAffordableQty(snap, actorID, e.Item, actorSnap.Coins); ok {
			affordable = qty
		}
		// Approximate coins to fill the headroom at the last-paid rate (LLM-10) —
		// the offer anchor + the input to the reserve nudge. Same newest observation
		// AffordableQty reads; -1 (no price) leaves both silent.
		headroom := cap - current
		if headroom < 0 {
			headroom = 0
		}
		fillCost := -1
		if c, ok := buyerLastPaidFillCost(snap, actorID, e.Item, headroom); ok {
			fillCost = c
		}
		coName, coID := coPresentSellerForItem(snap, actorID, actorSnap, e.Item)
		items = append(items, RestockItemView{
			ItemLabel:                     itemDisplayLabel(snap, e.Item),
			CurrentQty:                    current,
			Cap:                           cap,
			Vendors:                       findItemVendors(snap, actorID, actorSnap, e.Item),
			CoPresentSeller:               coName,
			PendingOfferToCoPresentSeller: coID != "" && hasPendingOfferTo(snap, actorID, coID, e.Item),
			AffordableQty:                 affordable,
			FillCost:                      fillCost,
			kind:                          e.Item,
		})
	}
	if len(items) == 0 {
		return nil
	}
	// Deterministic section order — by item label, then the underlying kind as
	// a tie-break for two kinds sharing a display label (BuyEntries order is
	// stable, but the sort makes the section robust to policy reordering too).
	sort.Slice(items, func(i, j int) bool {
		if items[i].ItemLabel != items[j].ItemLabel {
			return items[i].ItemLabel < items[j].ItemLabel
		}
		return items[i].kind < items[j].kind
	})
	return &RestockingView{
		Items:            items,
		BuyerCoins:       actorSnap.Coins,
		HasOtherBuyGoods: len(buyEntries) > 1,
	}
}

// buyerLatestPriceObs returns the buyer's newest accepted purchase observation
// for `item` across every seller's PriceBook ring, and whether one exists. The
// shared lookup behind both the affordability fact and the fill-cost anchor.
// Price knowledge is per-buyer — a buyer who has never bought the item gets
// ok=false. Snapshot() is oldest-first; the globally newest match across all
// sellers wins, with the lowest seller id breaking an exact-timestamp tie so the
// pick is deterministic regardless of map-iteration order (code_review). A
// returned observation always has Amount >= 1 (a zero-amount record carries no
// usable rate). Perception runs off the world goroutine, so it reads
// Snapshot.PriceBook, not the live World accessor.
func buyerLatestPriceObs(snap *sim.Snapshot, buyerID sim.ActorID, item sim.ItemKind) (sim.PriceObservation, bool) {
	if snap == nil || snap.PriceBook == nil {
		return sim.PriceObservation{}, false
	}
	var newest sim.PriceObservation
	var newestSeller sim.ActorID
	found := false
	for key, buf := range snap.PriceBook {
		if key.Item != item || buf == nil || buf.Len() == 0 {
			continue
		}
		for _, obs := range buf.Snapshot() {
			if obs.BuyerID != buyerID {
				continue
			}
			if !found || obs.At.After(newest.At) ||
				(obs.At.Equal(newest.At) && key.SellerID < newestSeller) {
				newest = obs
				newestSeller = key.SellerID
				found = true
			}
		}
	}
	if !found || newest.Amount < 1 {
		return sim.PriceObservation{}, false
	}
	return newest, true
}

// observationUnits is the total units in an observation's bundle (qty × consumers,
// with the consumer count floored at 1). 0 means the bundle carried no units, so
// any rate derived from it is meaningless — callers treat 0 as "no usable rate".
func observationUnits(obs sim.PriceObservation) int64 {
	consumers := obs.Consumers
	if consumers < 1 {
		consumers = 1
	}
	units := int64(obs.Qty) * int64(consumers)
	if units < 1 {
		return 0
	}
	return units
}

// buyerLastPaidAffordableQty returns how many units `coins` covers at the buyer's
// most recent accepted purchase RATE for `item` (across all sellers), and whether
// any such observation exists. The numeric sibling of buyerLastPaidText
// (recovery_options.go): that renders a per-seller bundle "~N coins" hint; this
// answers "how many can my purse cover" for the restock affordability fact.
//
// The affordable count is computed straight from the observed bundle ratio
// (coins * units / bundlePrice), NOT via an intermediate floored unit price —
// flooring the unit price first OVERSTATES the count (last paid 5 coins for 2
// units floors to 2/unit, so 9 coins reads as 4 affordable when the true ratio
// covers only 3), which is exactly the over-promise this cue exists to prevent
// (code_review). int64 throughout guards the multiply-before-divide on a 32-bit
// int build; the result is clamped into int range.
func buyerLastPaidAffordableQty(snap *sim.Snapshot, buyerID sim.ActorID, item sim.ItemKind, coins int) (int, bool) {
	obs, ok := buyerLatestPriceObs(snap, buyerID, item)
	if !ok {
		return 0, false
	}
	units := observationUnits(obs)
	if units < 1 {
		return 0, false
	}
	affordable := int64(coins) * units / int64(obs.Amount)
	if affordable > int64(math.MaxInt32) {
		affordable = int64(math.MaxInt32)
	}
	return int(affordable), true
}

// buyerLastPaidFillCost returns the approximate coins to buy `qty` units at the
// buyer's most recent rate for `item`, and whether a price is on record. The cost
// sibling of buyerLastPaidAffordableQty: that answers "how many can my purse
// cover", this answers "what would buying this many cost" — the LLM-10 offer
// anchor. Computed from the same observed bundle ratio (qty * bundlePrice / units)
// and rounded to the nearest coin so the anchor reads as a fair round total. A
// non-positive qty has nothing to cost. int64 throughout guards the
// multiply-before-divide; the result is clamped into int range.
func buyerLastPaidFillCost(snap *sim.Snapshot, buyerID sim.ActorID, item sim.ItemKind, qty int) (int, bool) {
	if qty <= 0 {
		return 0, false
	}
	obs, ok := buyerLatestPriceObs(snap, buyerID, item)
	if !ok {
		return 0, false
	}
	units := observationUnits(obs)
	if units < 1 {
		return 0, false
	}
	// Round to the nearest coin: (qty*bundlePrice + units/2) / units.
	cost := (int64(qty)*int64(obs.Amount) + units/2) / units
	if cost > int64(math.MaxInt32) {
		cost = int64(math.MaxInt32)
	}
	return int(cost), true
}

// findItemVendors resolves the suppliers selling itemKind, ONE cue per workplace
// structure, sorted deterministically by (StructureLabel, StructureID). Runs over
// the shared structural-vendorship scan (eachVendorOffer, consumable_vendors.go),
// the same supplier-resolution path the satiation/recovery consumable cues use.
//
// Dedupe-by-structure: the LLM only needs a destination — move_to(structure_id)
// then pay_with_item resolves which co-present seller actually transacts — so two
// NPCs working the same structure and both holding the item collapse to one cue
// (which also kills the duplicate-line + map-order nondeterminism, code_review).
// The representative seller is the lowest VendorID at that structure, picked
// deterministically so the per-buyer CostText (last-paid from that seller) is
// stable across snapshots regardless of map iteration order.
func findItemVendors(snap *sim.Snapshot, buyerID sim.ActorID, buyerSnap *sim.ActorSnapshot, itemKind sim.ItemKind) []RestockVendor {
	type pick struct {
		vendorID  sim.ActorID
		structure *sim.Structure
	}
	best := map[sim.StructureID]pick{}
	// anyAwake[structureID] is true once a NON-asleep vendor of itemKind is seen
	// at that structure. ClosedNow is its negation: the shop stocks the item but
	// no one awake is tending it. Checked structure-wide (not just on the
	// representative pick) because the representative is the lowest VendorID,
	// which is arbitrary w.r.t. wakefulness — keying ClosedNow off it could
	// false-close a structure whose lowest-id keeper sleeps while another tends,
	// re-creating the very "avoid a valid supplier" bug this fixes. ZBBS-HOME-406.
	anyAwake := map[sim.StructureID]bool{}
	eachVendorOffer(snap, buyerID, func(o vendorOffer) {
		if o.Kind != itemKind {
			return
		}
		if !vendorKeeperAsleep(snap, o.VendorID) {
			anyAwake[o.StructureID] = true
		}
		if cur, ok := best[o.StructureID]; ok && cur.vendorID <= o.VendorID {
			return // keep the lowest VendorID at this structure
		}
		best[o.StructureID] = pick{vendorID: o.VendorID, structure: o.Structure}
	})
	if len(best) == 0 {
		return nil
	}
	out := make([]RestockVendor, 0, len(best))
	for structureID, p := range best {
		out = append(out, RestockVendor{
			StructureLabel: vendorStructureLabel(p.structure),
			StructureID:    structureID,
			// Empty fallback when no price is on record (was "ask the supplier",
			// which invited the reseller to SPEAK a price question instead of
			// calling pay_with_item — ZBBS-HOME-386). With "", renderRestocking
			// omits the cost clause entirely; the header carries the action.
			CostText:  buyerLastPaidText(snap, buyerID, p.vendorID, itemKind, ""),
			Shut:      businessRememberedShut(snap, buyerSnap, structureID),
			ClosedNow: !anyAwake[structureID],
		})
	}
	// Open suppliers lead closed ones (a structure whose every vendor of the item
	// is asleep can't sell now), then alphabetical for deterministic output —
	// mirrors the satiation buy menu so a closed supplier doesn't lead the cue.
	sort.Slice(out, func(i, j int) bool {
		if out[i].ClosedNow != out[j].ClosedNow {
			return !out[i].ClosedNow
		}
		if out[i].StructureLabel != out[j].StructureLabel {
			return out[i].StructureLabel < out[j].StructureLabel
		}
		return out[i].StructureID < out[j].StructureID
	})
	return out
}

// coPresentSellerForItem returns the display name of a seller holding itemKind
// who shares the reseller's CURRENT huddle — i.e. a pay_with_item(seller: …) for
// this item resolves this very tick. pay_with_item requires the seller to be "in
// your conversation", and a shared huddle IS that conversation, so this is the
// exact precondition for the buy-here imperative (and the same co-presence test
// deliver_order's gate uses — see absentRecipientNames in build.go). Runs over
// the shared structural-vendorship scan (eachVendorOffer), like findItemVendors.
// Returns ("", "") when the reseller is in no huddle, or no co-present seller of
// itemKind has a usable display name. Deterministic: lowest VendorID among the
// co-present sellers, so the named seller is stable across snapshots. The id is
// returned alongside the name so the caller can check for a standing offer to
// this exact seller (hasPendingOfferTo, LLM-64). ZBBS-HOME-388.
func coPresentSellerForItem(snap *sim.Snapshot, buyerID sim.ActorID, buyerSnap *sim.ActorSnapshot, itemKind sim.ItemKind) (string, sim.ActorID) {
	huddle := buyerSnap.CurrentHuddleID
	if huddle == "" {
		return "", ""
	}
	var bestID sim.ActorID
	var bestName string
	eachVendorOffer(snap, buyerID, func(o vendorOffer) {
		if o.Kind != itemKind {
			return
		}
		seller := snap.Actors[o.VendorID]
		if seller == nil || seller.DisplayName == "" || seller.CurrentHuddleID != huddle {
			return
		}
		if bestID == "" || o.VendorID < bestID {
			bestID = o.VendorID
			bestName = seller.DisplayName
		}
	})
	return bestName, bestID
}

// restockReserveTriggerPct is the share of the reseller's purse a single
// fill-to-cap must reach before the LLM-10 reserve nudge fires ("buy fewer to keep
// some back…"). A cheap top-up that costs less than this fraction of the purse
// adds no nag. A tuning knob — start conservative; raise to nag sooner, lower to
// nag only on near-total drains. Kept a constant (not a world setting) for now;
// promote to RestockReorderPct's setting pattern if it needs live tuning.
const restockReserveTriggerPct = 50

// renderRestocking writes the "## Restocking" section. Content-gated: a
// nil/empty view writes nothing. Each low item leads with its on-hand/cap so the
// reseller can size the buy (it picks its own quantity — the line hints the
// headroom), then EITHER a "buy it here now" imperative when a seller of that
// item shares the reseller's huddle (CoPresentSeller set), OR the generic list
// of where to walk to buy (structure_id for move_to). The header covers both
// cases — pay now if a seller is here, else walk then pay — without ordering
// movement first (ZBBS-HOME-388), and deliberately carries neither the word
// "ask" nor "price" — ZBBS-HOME-386: the old prose
// ("walk to a supplier and pay") plus an "ask the supplier" price hint drew the
// stateful model into SPEAKING price questions on a loop instead of calling
// pay_with_item, and even a negated "do not ask the price" still primes that on a
// weak model (code_review), so the wording avoids both tokens. Same actionable-
// cue treatment WORK-372 gave deliver_order. ZBBS-HOME-388 added the co-present
// imperative because the generic cue alone failed live: at the seller, with
// pay_with_item available and named, the model still narrated and walked off — so
// at the moment a seller is co-present the cue gives a complete, copyable
// pay_with_item call and drops the now-redundant walk-to list for that item.
func renderRestocking(b *strings.Builder, v *RestockingView) {
	if v == nil || len(v.Items) == 0 {
		return
	}
	b.WriteString("## Restocking\n")
	b.WriteString("Your shop stock of these bought-in goods is running low. You choose how much to buy.\n")
	for _, it := range v.Items {
		// LLM-64: the reseller already has a standing pay_with_item offer to a
		// co-present seller for this item. Drop the headroom/cost lines and the
		// "buy it now" imperative — they fight the "## Offers you have standing"
		// bide-and-wait cue — and render a positive stay-steer instead, so the model
		// waits for the answer rather than re-staking the offer or walking off
		// (delivery needs co-presence, so leaving would strand the standing offer).
		if it.PendingOfferToCoPresentSeller {
			seller := sanitizeInline(it.CoPresentSeller)
			if seller == "" {
				seller = "the seller"
			}
			fmt.Fprintf(b, "- %s is here with you, and your offer for %s is still with them (see Offers you have standing). Wait here for their answer — do not re-offer or leave.\n",
				seller, sanitizeInline(string(it.kind)))
			continue
		}
		headroom := it.Cap - it.CurrentQty
		if headroom < 0 {
			headroom = 0
		}
		fmt.Fprintf(b, "- %s: %d on hand of %d cap (room for %d more).",
			sanitizeInline(it.ItemLabel), it.CurrentQty, it.Cap, headroom)
		switch {
		case it.AffordableQty >= 1 && it.AffordableQty < headroom:
			// ZBBS-HOME-459: the purse covers fewer units than the cap leaves room
			// for, so coins are the binding limit — state it as a fact so the model
			// sizes the buy to the purse instead of the headroom and then over-offers
			// (the John Ellis 25-meat-on-248-coins case). Gated to a known unit price
			// AND coins binding before the cap; a purse that covers the headroom is
			// the LLM-10 anchor case below, not this one. No "ask"/"price"/"cost"
			// token — stays clear of the HOME-386 speaking-loop trap. "Can't afford
			// even one" (AffordableQty 0) stays silent; the pay_with_item rejection
			// steer catches an attempt.
			fmt.Fprintf(b, " Your %d coins cover about %d at what you last paid.", v.BuyerCoins, it.AffordableQty)
		case it.FillCost >= 1:
			// LLM-10 (A): the purse comfortably covers the headroom, so the weak
			// model otherwise anchors its pay_with_item amount to the bare purse
			// balance ("Coins in your purse: N") and offers the lot. Surface the fair
			// fill cost as the anchor instead, and invite a lower OFFER (the
			// pay_with_item amount, not a spoken price question — HOME-386-safe).
			fmt.Fprintf(b, " Filling to cap is about %d coins at what you last paid, though you might offer less and see if they take it.", it.FillCost)
			// LLM-10 (B): when filling to cap would take a big share of the purse,
			// nudge a partial buy so the reseller keeps coins for its other bought-in
			// goods. Soft — the headroom stays visible and the model picks the qty.
			// Gated on the fill being >= restockReserveTriggerPct of the purse.
			if v.BuyerCoins > 0 && int64(it.FillCost)*100 >= int64(v.BuyerCoins)*restockReserveTriggerPct {
				if v.HasOtherBuyGoods {
					fmt.Fprintf(b, " That is a big share of your %d coins — buy fewer to keep some back for your other goods.", v.BuyerCoins)
				} else {
					fmt.Fprintf(b, " That is a big share of your %d coins — buy fewer to keep some coins back.", v.BuyerCoins)
				}
			}
		}
		// A seller of this item is in the conversation right now: give the exact
		// pay_with_item call and skip the walk-to list — he is already there.
		if it.CoPresentSeller != "" {
			seller := sanitizeInline(it.CoPresentSeller)
			fmt.Fprintf(b, "\n  - %s is here with you and sells %s. Buy it now — first call pay_with_item with seller \"%s\", item \"%s\", a qty up to %d, and a payment: coins (amount), goods you carry (pay_items), or both, with consume_now false. Then also use speak for a brief handoff line as you make the offer. They will accept or counter your offer.\n",
				seller, sanitizeInline(it.ItemLabel), seller, sanitizeInline(string(it.kind)), headroom)
			continue
		}
		if len(it.Vendors) == 0 {
			b.WriteString(" No supplier nearby is currently holding stock.\n")
			continue
		}
		// No co-present seller — name the actual situation and the two-step buy
		// (move_to then pay_with_item). This instruction used to live in the section
		// header; LLM-10 moved it here so the header no longer hedges "if a seller is
		// here / otherwise" and each item line reflects whether a seller is present.
		b.WriteString(" No seller is here now — use move_to to reach a supplier below, then pay_with_item once you arrive.\n")
		for _, vd := range it.Vendors {
			b.WriteString("  - buy from ")
			b.WriteString(sanitizeInline(vd.StructureLabel))
			// A supplier whose every vendor of the item is asleep can't sell now
			// — mark it "(currently closed)" right after the name, not a soft
			// trailing clause the weak model skims (mirrors satiation,
			// ZBBS-HOME-387/406).
			if vd.ClosedNow {
				b.WriteString(closedNowMarker)
			}
			if vd.StructureID != "" {
				fmt.Fprintf(b, " (structure_id: %s)", vd.StructureID)
			}
			if vd.CostText != "" {
				fmt.Fprintf(b, ", %s", vd.CostText)
			}
			// The stale experiential Shut memory only annotates when the supplier
			// isn't live-closed — a present-tense read beats a decaying recollection.
			if !vd.ClosedNow && vd.Shut {
				b.WriteString(closedBusinessAnnotation)
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
}
