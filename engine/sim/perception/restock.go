package perception

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

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

	// Conserve marks the coin-poor-but-stock-rich keeper (LLM-294): purse below the
	// operator working-capital floor AND sitting on unsold sellable stock
	// (merchantConserve). When set, renderRestocking flips the section from a buy
	// directory to a hold-off-buying steer — the low items are still named (so the
	// keeper knows what it will need once coin returns) but WITHOUT a buy imperative,
	// so the cue can't tell the keeper to both buy now and conserve at once.
	Conserve bool
}

// RestockItemView is one low `buy` item the reseller could replenish: its label,
// current on-hand quantity, the cap it restocks toward, and the suppliers
// selling it. buildRestocking only emits an item that has at least one actionable
// buy path — a co-present seller, or a reachable, open, affordable walk-to
// supplier; an item with neither is omitted rather than surfaced as a dead-end
// cue the weak model would tour on (LLM-216).
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
	// the "## Offers you have standing" cue). When set, render drops the sell-through/
	// affordability lines and the "buy it now" imperative for a stay-and-wait steer, so the reseller bides
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

	// BuyAnchorUnit is the per-unit rate this item is worth buying in at — the
	// corrective to the buyer's OWN self-poisoning last-paid history (one overpay
	// re-anchors every later offer: the live Josiah 2.2/unit milk leg). 0 when
	// neither an observed rate nor a catalog price is on record (clause omitted).
	//
	// Resolved observed-first (LLM-295): the rate the item's in-scope suppliers have
	// actually been selling it for (observedSupplierBuyRate), falling back to the
	// catalog band's low end (catalogBulkRate) ONLY until real transactions exist —
	// the catalog prices are hand-authored SEED, not lived rates. BuyAnchorObserved
	// records which source won, so render can phrase a lived rate ("has lately been
	// going for about N") differently from a seed estimate ("is generally worth
	// about N"). Anchoring on the buyer's actual suppliers also auto-scopes the tier:
	// buying from a producer anchors on the observed wholesale rate, buying from the
	// distributor on what the distributor actually charges (the mid-tier price the
	// two-price catalog can't express).
	BuyAnchorUnit     int
	BuyAnchorObserved bool

	// RecentSalesUnits / RecentSalesCoins / RecentBuyCost are this item's trailing-
	// restockSalesWindow economics, read off the price book (LLM-63):
	//   - RecentSalesUnits: units SOLD (Qty×Consumers per accepted sale, seller view).
	//   - RecentSalesCoins: coins taken IN on those sales (seller view, sum of amount).
	//   - RecentBuyCost: coins the reseller PAID restocking this item this window
	//     (buyer view, sum of amount across every seller it bought from).
	// Render surfaces them as the demand + weekly-P&L signal the reseller sizes its
	// restock against ("you've sold about N over the past week, at a cost of X coins
	// and sales of Y coins"), in place of the fill-to-cap PRICE anchor that biased the
	// weak model into a copy-paste max offer and drained its working capital. The
	// whole sentence is silent when RecentSalesUnits is 0, so a new or dormant good
	// asserts no rate rather than a misleading zero.
	RecentSalesUnits int
	RecentSalesCoins int
	RecentBuyCost    int

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
	// The effective buy demand (LLM-260): explicit `buy` entries plus the ones
	// derived from the actor's produce recipes' unsourced inputs — the same set
	// the warrant producer (restock_tick.go firstActionableLowEntry) scans, so
	// the warrant and this section agree on what the actor needs to buy.
	buyEntries := sim.EffectiveBuyEntries(snap.Recipes, actorSnap.RestockPolicy)
	// Produce-input batch floors (LLM-279) — same catalog the warrant reads, so a
	// recipe input surfaces here on the same batch-coverage line the warrant wakes
	// on. 0 for pure-resale goods, which stay on the cap fraction.
	floors := sim.ReorderFloors(snap.Recipes, actorSnap.RestockPolicy)
	var items []RestockItemView
	for _, e := range buyEntries {
		cap := e.Cap()
		current := actorSnap.Inventory[e.Item]
		if !sim.RestockReorderThresholdMet(current, cap, pct, floors[e.Item]) {
			continue
		}
		// Coins-bound the buy plan (ZBBS-HOME-459). -1 (no price on record)
		// leaves the fact silent; render further gates it to the case where coins
		// bind before the cap.
		affordable := -1
		if qty, ok := buyerLastPaidAffordableQty(snap, actorID, e.Item, actorSnap.Coins); ok {
			affordable = qty
		}
		// Recent demand + weekly P&L the reseller sizes its restock against (LLM-63):
		// units sold + coins taken in (seller view), coins paid restocking (buyer
		// view). 0 units sold leaves the whole sentence silent at render.
		salesUnits, salesCoins := sellerRecentSales(snap, actorID, e.Item, restockSalesWindow)
		buyCost := buyerRecentSpend(snap, actorID, e.Item, restockSalesWindow)
		coName, coID := coPresentSellerForItem(snap, actorID, actorSnap, e.Item)
		vendors := findItemVendors(snap, actorID, actorSnap, e.Item)
		// LLM-216: omit an item with no actionable buy path — no co-present seller to
		// transact with here-and-now, and no reachable, open, affordable walk-to
		// supplier (findItemVendors drops the shut and the unaffordable). A broke or
		// dead-ended keeper is no longer handed an unactionable restock cue it would
		// tour on the wasted-move loop (the live Josiah Thorne shut-farm case); the
		// item returns the moment a supplier opens or the purse can cover one.
		if len(vendors) == 0 && coName == "" {
			continue
		}
		// LLM-295: observed-first buy anchor, scoped to the reachable suppliers just
		// resolved (so it agrees with their walk-to prices); catalog seed only until
		// real transactions exist.
		buyAnchor, buyAnchorObserved := observedSupplierBuyRate(snap, actorID, e.Item, vendors, coID, restockSalesWindow)
		if !buyAnchorObserved {
			buyAnchor = catalogBulkRate(snap, e.Item)
		}
		items = append(items, RestockItemView{
			ItemLabel:                     itemDisplayLabel(snap, e.Item),
			CurrentQty:                    current,
			Cap:                           cap,
			Vendors:                       vendors,
			CoPresentSeller:               coName,
			PendingOfferToCoPresentSeller: coID != "" && hasPendingOfferTo(snap, actorID, coID, e.Item),
			AffordableQty:                 affordable,
			BuyAnchorUnit:                 buyAnchor,
			BuyAnchorObserved:             buyAnchorObserved,
			RecentSalesUnits:              salesUnits,
			RecentSalesCoins:              salesCoins,
			RecentBuyCost:                 buyCost,
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
		Items:      items,
		BuyerCoins: actorSnap.Coins,
		// LLM-294: coin-poor + overstocked flips the section to a conserve steer. Shared
		// with the Tier-2 sell nudge (buildTradeValue) via the same determination.
		Conserve: merchantConserve(snap, actorID, actorSnap).Active,
	}
}

// catalogBulkRate is the catalog's per-unit buying-in anchor for kind: the low
// end of the item_recipe wholesale–retail band, since a merchant buying stock in
// buys at the producer→merchant end (recipe.go defines WholesalePrice exactly
// so). Normalizes a swapped band and collapses a single-priced row to its one
// price, the same way buildTradeValue derives its Low; 0 when the catalog
// carries no price for the kind (the anchor clause is then omitted). LLM-292.
func catalogBulkRate(snap *sim.Snapshot, kind sim.ItemKind) int {
	if snap == nil || snap.Recipes == nil {
		return 0
	}
	recipe := snap.Recipes[kind]
	if recipe == nil {
		return 0
	}
	lo, hi := recipe.WholesalePrice, recipe.RetailPrice
	if lo > hi {
		lo, hi = hi, lo
	}
	if lo <= 0 {
		lo = hi // only one price configured — collapse to it
	}
	if lo < 0 {
		return 0
	}
	return lo
}

// observedSupplierBuyRate returns the per-unit rate the item's REACHABLE suppliers
// have actually been selling `item` for, nearest-rounded to a coin, and whether
// any such sale is on record (LLM-295). Scoped to exactly the suppliers the cue
// surfaces as buy destinations — the sellers at the surviving walk-to structures
// (`vendors`, already through the LLM-216 shut/affordability drops) plus the
// co-present seller (`coPresentID`) — so the anchor agrees with the walk-to prices
// beside it instead of averaging in a shut farm the buyer can't reach (which would
// hand the weak model two conflicting numbers). Their seller-view PriceBook sales
// are summed. This is the observed anchor that supersedes the hand-authored catalog
// seed: grounded in the destinations the buyer restocks from, so buying from a
// producer yields the observed wholesale rate and buying from the distributor
// yields what the distributor actually charges (the mid-tier price the two-price
// catalog can't express). Per-supplier by construction, so it barely dents the
// PriceBook "knowledge earned by patronage" asymmetry. 0/false when no reachable
// supplier has a sale in the window (caller falls back to seed).
func observedSupplierBuyRate(snap *sim.Snapshot, buyerID sim.ActorID, item sim.ItemKind, vendors []RestockVendor, coPresentID sim.ActorID, window time.Duration) (int, bool) {
	structures := make(map[sim.StructureID]bool, len(vendors))
	for _, v := range vendors {
		structures[v.StructureID] = true
	}
	suppliers := map[sim.ActorID]bool{}
	if coPresentID != "" {
		suppliers[coPresentID] = true
	}
	eachVendorOffer(snap, buyerID, func(o vendorOffer) {
		if o.Kind != item || !isRestockSupplierOf(snap, o.VendorID, item) {
			return
		}
		if structures[o.StructureID] {
			suppliers[o.VendorID] = true
		}
	})
	if len(suppliers) == 0 {
		return 0, false
	}
	var units, coins int64
	for sellerID := range suppliers {
		u, c := sellerRecentSales(snap, sellerID, item, window)
		units += int64(u)
		coins += int64(c)
	}
	if units <= 0 {
		return 0, false
	}
	return int((coins + units/2) / units), true // nearest-rounded per-unit
}

// observedShopSales totals what the item's SHOPS have actually been selling `item`
// for over the window — every seller of the item that RESELLS it (does not
// produce/forage it) rather than makes it, from the seller-view PriceBook
// (LLM-295). The retail-side observed rate behind the wholesale-producer cue's
// "folk pay about N in the shops" figure; the producer's own wholesale sales are
// deliberately excluded (they feed the bulk figure, sellerRecentSales on the
// producer itself). Both 0 when no shop has a sale of the item on record. int64
// guards the sums; results clamp into int range.
func observedShopSales(snap *sim.Snapshot, item sim.ItemKind, window time.Duration) (units int, coins int) {
	if snap == nil || snap.PriceBook == nil {
		return 0, 0
	}
	cutoff := snap.PublishedAt.Add(-window)
	var u, c int64
	for key, buf := range snap.PriceBook {
		if key.Item != item || buf == nil || buf.Len() == 0 {
			continue
		}
		seller := snap.Actors[key.SellerID]
		if seller == nil || seller.RestockPolicy.ProducesOrForages(item) {
			continue // no such seller, or a first-hand producer (the wholesale side)
		}
		for _, obs := range buf.Snapshot() {
			if obs.At.Before(cutoff) {
				continue
			}
			u += observationUnits(obs)
			c += int64(obs.Amount)
		}
	}
	if u > int64(math.MaxInt32) {
		u = int64(math.MaxInt32)
	}
	if c > int64(math.MaxInt32) {
		c = int64(math.MaxInt32)
	}
	return int(u), int(c)
}

// itemHasActionableBuyPath reports whether buildRestocking would render an item
// line for kind: a co-present seller to transact with here-and-now, or at least
// one surviving walk-to supplier (findItemVendors, after the LLM-216 shut /
// affordability drops). Shared with the "## Keeping up production" gate
// (LLM-260) so the motivate-half and the where-half of the LLM-64 split can't
// disagree: an input with no actionable buy path renders in NEITHER section —
// a runway line with nowhere to buy is exactly the dead-end that had the live
// Hannah Boggs narrating phantom fetch-water hires.
func itemHasActionableBuyPath(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot, kind sim.ItemKind) bool {
	if coName, _ := coPresentSellerForItem(snap, actorID, actorSnap, kind); coName != "" {
		return true
	}
	return len(findItemVendors(snap, actorID, actorSnap, kind)) > 0
}

// buyerLatestPriceObs returns the buyer's newest accepted purchase observation
// for `item` across every seller's PriceBook ring, and whether one exists. The
// lookup behind the affordability fact (ZBBS-HOME-459).
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

// sellerRecentSales totals what the actor has SOLD of `item` within the trailing
// `window`, read from the seller view of the price book (snap.PriceBook, keyed by
// (seller, item)): units = Qty×Consumers per accepted sale (the bundle's true unit
// count, as observationUnits gives the buyer side), coins = the amounts taken in.
// Summed over observations no older than snap.PublishedAt − window; both 0 when no
// sale is on record in the window. The empirical demand + revenue half of the
// restock cue's weekly P&L (LLM-63). Per-seller by construction — the ring is keyed
// by seller, so this reads only this actor's own sales. int64 guards the sums; the
// results clamp into int range.
func sellerRecentSales(snap *sim.Snapshot, sellerID sim.ActorID, item sim.ItemKind, window time.Duration) (units int, coins int) {
	if snap == nil || snap.PriceBook == nil {
		return 0, 0
	}
	buf, ok := snap.PriceBook[sim.PriceBookKey{SellerID: sellerID, Item: item}]
	if !ok || buf == nil || buf.Len() == 0 {
		return 0, 0
	}
	cutoff := snap.PublishedAt.Add(-window)
	var u, c int64
	for _, obs := range buf.Snapshot() {
		if obs.At.Before(cutoff) {
			continue
		}
		u += observationUnits(obs)
		c += int64(obs.Amount)
	}
	if u > int64(math.MaxInt32) {
		u = int64(math.MaxInt32)
	}
	if c > int64(math.MaxInt32) {
		c = int64(math.MaxInt32)
	}
	return int(u), int(c)
}

// buyerRecentPurchases totals what the actor has BOUGHT of `item` within the trailing
// `window`, read from the buyer view of the price book: units = Qty×Consumers per
// accepted purchase (observationUnits, as the seller side gives), coins = the amounts
// it paid. Price knowledge is per-buyer, so this scans every (seller, item) ring for
// observations the actor BOUGHT (obs.BuyerID == buyerID) no older than
// snap.PublishedAt − window — its purchases across all the sellers it restocked from.
// Both 0 when it has bought none in the window. The buyer-side sibling of
// sellerRecentSales, and the cost-basis source for the reseller leg of the wares-worth
// cue (LLM-191). int64 guards the sums; results clamp into int range.
func buyerRecentPurchases(snap *sim.Snapshot, buyerID sim.ActorID, item sim.ItemKind, window time.Duration) (units int, coins int) {
	if snap == nil || snap.PriceBook == nil {
		return 0, 0
	}
	cutoff := snap.PublishedAt.Add(-window)
	var u, c int64
	for key, buf := range snap.PriceBook {
		if key.Item != item || buf == nil || buf.Len() == 0 {
			continue
		}
		for _, obs := range buf.Snapshot() {
			if obs.BuyerID != buyerID || obs.At.Before(cutoff) {
				continue
			}
			u += observationUnits(obs)
			c += int64(obs.Amount)
		}
	}
	if u > int64(math.MaxInt32) {
		u = int64(math.MaxInt32)
	}
	if c > int64(math.MaxInt32) {
		c = int64(math.MaxInt32)
	}
	return int(u), int(c)
}

// buyerRecentSpend totals just the coins the actor has PAID restocking `item` within
// the trailing `window` — the cost half of the restock cue's weekly P&L (LLM-63). Thin
// wrapper over buyerRecentPurchases for the call site that wants only the spend.
func buyerRecentSpend(snap *sim.Snapshot, buyerID sim.ActorID, item sim.ItemKind, window time.Duration) int {
	_, coins := buyerRecentPurchases(snap, buyerID, item, window)
	return coins
}

// isRestockSupplierOf reports whether vendor qualifies as a restock supplier of
// itemKind (LLM-252): it supplies itemKind at first hand — produces or forages it
// — or it is the village distributor. A vendor merely holding itemKind from a past
// `buy` (a fellow reseller) does NOT qualify: treating a reseller's retail stock
// as a supply source is what let the Josiah↔John carrot buy-back loop form. Gating
// on this keeps the supply chain a one-way DAG (producers → distributor →
// resellers). Scoped to the restock cue surface only — a hungry buyer purchasing
// itemKind to CONSUME rides the same eachVendorOffer scan but not this gate, so it
// is unaffected. Shared by findItemVendors and coPresentSellerForItem so the
// directory and the co-present buy-here imperative gate identically.
func isRestockSupplierOf(snap *sim.Snapshot, vendorID sim.ActorID, itemKind sim.ItemKind) bool {
	if snap == nil {
		return false
	}
	vendor := snap.Actors[vendorID]
	if vendor == nil {
		return false
	}
	if vendor.RestockPolicy.ProducesOrForages(itemKind) {
		return true
	}
	return sim.ActorIsDistributor(snap.VillageObjects, vendor.WorkStructureID)
}

// findItemVendors resolves the suppliers selling itemKind, ONE cue per workplace
// structure, sorted deterministically by (StructureLabel, StructureID). Runs over
// the shared structural-vendorship scan (eachVendorOffer, consumable_vendors.go),
// the same supplier-resolution path the satiation/recovery consumable cues use.
//
// It drops two kinds of non-destination before returning (LLM-216, mirroring the
// seek-work directory and the need-redirect affordability skip): a supplier the
// buyer remembers finding shut, and one whose remembered price the buyer's purse
// can't cover — both are unactionable walk-to targets the weak model would
// otherwise tour (the live Josiah Thorne shut-farm move_to loop).
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
	eachVendorOffer(snap, buyerID, func(o vendorOffer) {
		if o.Kind != itemKind {
			return
		}
		if !isRestockSupplierOf(snap, o.VendorID, itemKind) {
			return // LLM-252: only first-hand suppliers (or the distributor), never a reseller's retail stock
		}
		if cur, ok := best[o.StructureID]; ok && cur.vendorID <= o.VendorID {
			return // keep the lowest VendorID at this structure
		}
		best[o.StructureID] = pick{vendorID: o.VendorID, structure: o.Structure}
	})
	if len(best) == 0 {
		return nil
	}
	coins := buyerSnap.Coins
	out := make([]RestockVendor, 0, len(best))
	for structureID, p := range best {
		// LLM-216: drop a supplier the buyer remembers finding shut, mirroring the
		// seek-work directory (buildSeekWorkPlaces). Annotating it — the old
		// ZBBS-HOME-353 / LLM-126 "found it shut up" posture — left the weak model
		// touring the dead ends (Josiah's every-tick move_to loop among shut farms).
		// The shut memory is experiential and TTL-decayed, so the supplier reappears
		// once it lapses (he'd go there and find a keeper), preserving the retry the
		// annotation aimed for without the wasted trips in between.
		if businessRememberedShut(snap, buyerSnap, structureID) {
			continue
		}
		// LLM-216: drop a supplier the buyer can't afford, mirroring the need-redirect
		// affordability skip (needRedirectFor, build.go). A REMEMBERED price above the
		// purse names an unactionable destination; an unknown price (0, never bought
		// there) is kept — patronage earns the number, so the buyer walks over and
		// learns it (and can still barter goods on arrival). A broke keeper with a
		// KNOWN-price supplier (the live Josiah case: 0 coins, ~4/~6 farms) thus drops
		// it, and the cue self-heals the moment he earns.
		if price := buyerLastPaidCoins(snap, buyerID, p.vendorID, itemKind); price > 0 && coins < price {
			continue
		}
		out = append(out, RestockVendor{
			StructureLabel: vendorStructureLabel(p.structure),
			StructureID:    structureID,
			// Empty fallback when no price is on record (was "ask the supplier",
			// which invited the reseller to SPEAK a price question instead of
			// calling pay_with_item — ZBBS-HOME-386). With "", renderRestocking
			// omits the cost clause entirely; the header carries the action.
			CostText: buyerLastPaidText(snap, buyerID, p.vendorID, itemKind, ""),
		})
	}
	// Alphabetical for deterministic output over the surviving suppliers (the shut
	// and the unaffordable were dropped above).
	sort.Slice(out, func(i, j int) bool {
		if out[i].StructureLabel != out[j].StructureLabel {
			return out[i].StructureLabel < out[j].StructureLabel
		}
		return out[i].StructureID < out[j].StructureID
	})
	return out
}

// conversationalScopeStructure resolves the structure an actor is conversationally
// scoped to, off the perception snapshot: the structure it stands inside, else the
// named object whose loiter pin it stands within AudienceScopeTiles of — the
// owner-only-shop commerce position, where the customer converses across the
// threshold without ever entering (ZBBS-HOME-378). Empty when neither holds (open
// ground, no stall within the loiter ring). The snapshot mirror of
// sim.conversationalScopeStructure (the live-World scope EnsureColocatedHuddle
// forms the co-located huddle on) and of httpapi.pcAudienceStructure — same
// inside-else-loiter-pin rule, resolved over the same ResolveLoiteringObject, so
// the buy-here cue and the pay_with_item huddle bootstrap agree on who is co-present.
func conversationalScopeStructure(snap *sim.Snapshot, a *sim.ActorSnapshot) sim.StructureID {
	if a.InsideStructureID != "" {
		return a.InsideStructureID
	}
	if id, ok := sim.ResolveLoiteringObject(snap.VillageObjects, snap.Assets, a.Pos, sim.AudienceScopeTiles); ok {
		return sim.StructureID(string(id))
	}
	return ""
}

// coPresentSellerForItem returns the display name of a seller holding itemKind
// who is co-present with the reseller RIGHT NOW — i.e. a pay_with_item(seller: …)
// for this item resolves this very tick. Co-presence is the exact precondition
// pay_with_item resolves through withHuddleBootstrap (ZBBS-HOME-400): the pay call
// runs EnsureColocatedHuddle before its "you're not in a conversation" gate, so a
// seller is reachable when they EITHER already share the reseller's huddle OR stand
// in the reseller's conversational structure scope (where the pay call bootstraps
// the co-located huddle first). The huddle branch covers a seller met away from
// their shop (the Josiah↔John General Store haggle); the structure-scope branch
// covers the arrival tick at a quiet shop, where no huddle exists yet — the moment
// the buy-here imperative was built for, but the old huddle-only gate fired exactly
// one tick too late for (LLM-286). Runs over the shared structural-vendorship scan
// (eachVendorOffer), like findItemVendors, and only first-hand suppliers pass
// (isRestockSupplierOf, LLM-252).
//
// The BUYER resolves scope loiter-aware (a customer at an owner-only shop stands at
// the loiter pin OUTSIDE, InsideStructureID == ""), but the SELLER is tested by its
// literal InsideStructureID: EnsureColocatedHuddle pulls co-located actors into the
// huddle by InsideStructureID == scope (colocatedConversationalActors), so a seller
// merely loitering at the same stall — whom the pay bootstrap would NOT huddle —
// must not read as co-present and lure a "buy it now" the tool would then reject.
//
// Returns ("", "") when the reseller is neither in a huddle nor in a structure
// scope, or no co-present seller of itemKind has a usable display name.
// Deterministic: lowest VendorID among the co-present sellers, so the named seller
// is stable across snapshots. The id is returned alongside the name so the caller
// can check for a standing offer to this exact seller (hasPendingOfferTo, LLM-64).
// ZBBS-HOME-388.
func coPresentSellerForItem(snap *sim.Snapshot, buyerID sim.ActorID, buyerSnap *sim.ActorSnapshot, itemKind sim.ItemKind) (string, sim.ActorID) {
	huddle := buyerSnap.CurrentHuddleID
	buyerScope := conversationalScopeStructure(snap, buyerSnap)
	if huddle == "" && buyerScope == "" {
		return "", "" // neither conversing nor standing in a shop scope — no one to pay here
	}
	var bestID sim.ActorID
	var bestName string
	eachVendorOffer(snap, buyerID, func(o vendorOffer) {
		if o.Kind != itemKind {
			return
		}
		if !isRestockSupplierOf(snap, o.VendorID, itemKind) {
			return // LLM-252: only first-hand suppliers (or the distributor), never a reseller's retail stock
		}
		seller := snap.Actors[o.VendorID]
		if seller == nil || seller.DisplayName == "" {
			return
		}
		// LLM-286: co-present via a shared huddle OR the buyer's structure scope —
		// the two ways withHuddleBootstrap lets the pay call resolve a seller this
		// tick. Seller scope is its literal InsideStructureID (see the doc comment),
		// the buyer's is loiter-aware.
		sharesHuddle := huddle != "" && seller.CurrentHuddleID == huddle
		sharesScope := buyerScope != "" && seller.InsideStructureID == buyerScope
		if !sharesHuddle && !sharesScope {
			return
		}
		if bestID == "" || o.VendorID < bestID {
			bestID = o.VendorID
			bestName = seller.DisplayName
		}
	})
	return bestName, bestID
}

// restockSalesWindow is the trailing window over which the restock cue measures a
// reseller's recent sell-through (LLM-63 / sellerRecentSalesUnits). Game time is
// wall-clock, so this is a literal week — long enough to smooth a low-volume good's
// sparse sales into a stable demand read. The "past week" wording in
// renderRestocking is tied to it; change both together. A tuning knob, kept a
// constant for now; promote to RestockReorderPct's setting pattern if it needs
// live tuning.
const restockSalesWindow = 7 * 24 * time.Hour

// renderRestocking writes the "## Restocking" section. Content-gated: a
// nil/empty view writes nothing. Each low item leads with on-hand + the headroom as
// a capacity ceiling, then — when a sale is on record — the week's demand and coin
// in/out (LLM-63: the empirical signal the reseller sizes the buy against). What was
// removed is the fill-to-cap PRICE — the concrete fill total that anchored the weak
// model into a copy-paste max offer and drained its working capital; the cap is
// shown as a ceiling, not a target, and is advisory (not enforced — coins are the
// only hard limit). Then EITHER a "buy it here now" imperative when a seller of that
// item shares the reseller's huddle (CoPresentSeller set), OR the generic list of
// where to walk to buy (structure_id for move_to) — pay now if a seller is here,
// else walk then pay, without ordering movement first (ZBBS-HOME-388). The cue
// deliberately carries neither the word "ask" nor "price" — ZBBS-HOME-386: the old
// prose ("walk to a supplier and pay") plus an "ask the supplier" price hint drew
// the stateful model into SPEAKING price questions on a loop instead of calling
// pay_with_item, and even a negated "do not ask the price" still primes that on a
// weak model (code_review), so the wording avoids both tokens. Same actionable-cue
// treatment WORK-372 gave deliver_order. ZBBS-HOME-388 added the co-present
// imperative because the generic cue alone failed live: at the seller, with
// pay_with_item available and named, the model still narrated and walked off — so
// at the moment a seller is co-present the cue gives a complete, copyable
// pay_with_item call and drops the now-redundant walk-to list for that item.
func renderRestocking(b *strings.Builder, v *RestockingView) {
	if v == nil || len(v.Items) == 0 {
		return
	}
	b.WriteString("## Restocking\n")
	if v.Conserve {
		// LLM-294: coin-poor + overstocked. Lead with the hold-off-buying steer; the
		// items below are named without a buy imperative so the cue never says "buy now"
		// and "conserve" in the same breath. Plain modern English + the stake (the
		// weak-model prose rule): say what to do and why.
		fmt.Fprintf(b, "Your purse is nearly empty (%s) and your shelves are full of goods still waiting to sell. Hold off buying more for now — sell down what you have and let your coins recover first. You'll want to restock these once you can afford them:\n", coinsPhrase(v.BuyerCoins))
	} else {
		b.WriteString("Your shop stock of these bought-in goods is running low. You choose how much to buy.\n")
	}
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
			// LLM-294: in conserve mode reinforce "no new buying" while keeping the
			// LLM-64 anti-churn wait-steer — the offer is already out (coin committed),
			// so the keeper only waits; it must not stake fresh offers on a thin purse.
			if v.Conserve {
				fmt.Fprintf(b, "- %s is here with you, and your offer for %s is still with them (see Offers you have standing). Wait for their answer — and put out no new offers while your purse is thin.\n",
					seller, sanitizeInline(string(it.kind)))
			} else {
				fmt.Fprintf(b, "- %s is here with you, and your offer for %s is still with them (see Offers you have standing). Wait here for their answer — do not re-offer or leave.\n",
					seller, sanitizeInline(string(it.kind)))
			}
			continue
		}
		// LLM-294 conserve mode: name the low item but issue NO buy imperative — the
		// section lead already told the keeper to hold off buying and sell down first.
		// Drops the headroom/P&L/affordability lines and the co-present/walk-to buy
		// steer for this item, so the cue is internally consistent.
		if v.Conserve {
			fmt.Fprintf(b, "- You are low on %s.\n", sanitizeInline(it.ItemLabel))
			continue
		}
		headroom := it.Cap - it.CurrentQty
		if headroom < 0 {
			headroom = 0
		}
		// Lead with on-hand + the headroom as a capacity ceiling ("at the most"),
		// then the week's demand + coin in/out (LLM-63). What was removed is the
		// fill-to-cap PRICE — the concrete "fill is N coins" total that anchored the
		// weak model into a copy-paste max offer and drained its working capital. The
		// cap itself is advisory: it is NOT enforced on a buy (MaxPayWithItemQty is
		// unbounded), so coins are the only hard limit — the ceiling just tells the
		// reseller how much room it has, and the co-present imperative's "qty up to N"
		// restates it at the buy moment.
		fmt.Fprintf(b, "- You have %d %s on hand and room for %d more at the most.",
			it.CurrentQty, sanitizeInline(it.ItemLabel), headroom)
		// The buying-in anchor, with its stake — the corrective to the buyer's own
		// self-poisoning last-paid (one overpay re-anchors every later offer: the
		// live Josiah 2.2/unit milk leg). Deliberately a per-unit rate and never a
		// fill total — LLM-63 removed the concrete fill-to-cap figure because the
		// weak model copy-pasted it as a max offer and drained its purse; a rate to
		// weigh an offer against does not re-open that. No "ask"/"price" token (the
		// HOME-386 speaking-loop guard). LLM-295: phrase a lived observed rate as
		// such, and a catalog SEED as a soft estimate — the model must not be told a
		// hand-authored guess is a rate the market has set.
		if it.BuyAnchorUnit > 0 {
			if it.BuyAnchorObserved {
				fmt.Fprintf(b, " Of late it has been going for about %s each — pay much above that and you're overpaying.",
					coinsPhrase(it.BuyAnchorUnit))
			} else {
				fmt.Fprintf(b, " It is generally worth about %s each — pay much above that and you're overpaying.",
					coinsPhrase(it.BuyAnchorUnit))
			}
		}
		// The week's demand + P&L the reseller sizes its restock against: units sold,
		// coins paid restocking, coins taken in selling. Silent at 0 units sold (no
		// sale on record in the window) so a new or dormant good asserts no rate.
		if it.RecentSalesUnits > 0 {
			fmt.Fprintf(b, " You've sold about %d over the past week, at a cost of %d coins and sales of %d coins.",
				it.RecentSalesUnits, it.RecentBuyCost, it.RecentSalesCoins)
		}
		// ZBBS-HOME-459: the purse covers fewer units than the cap leaves room for,
		// so coins are the binding limit — state it as a fact so the model sizes the
		// buy to the purse instead of over-offering (the John Ellis 25-meat-on-248-
		// coins case). Gated to a known unit price AND coins binding before the cap;
		// a purse that comfortably covers the headroom adds no line. No "ask"/"price"/
		// "cost" token — stays clear of the HOME-386 speaking-loop trap. "Can't afford
		// even one" (AffordableQty 0) stays silent; the pay_with_item rejection steer
		// catches an attempt.
		if it.AffordableQty >= 1 && it.AffordableQty < headroom {
			fmt.Fprintf(b, " Your %d coins cover about %d at what you last paid.", v.BuyerCoins, it.AffordableQty)
		}
		// A seller of this item is in the conversation right now: give the exact
		// pay_with_item call and skip the walk-to list — he is already there.
		if it.CoPresentSeller != "" {
			seller := sanitizeInline(it.CoPresentSeller)
			fmt.Fprintf(b, "\n  - %s is here with you and sells %s. Buy it now — first call pay_with_item with seller \"%s\", item \"%s\", a qty up to %d, and a payment: coins (amount), goods you carry (pay_items), or both, with consume_now false. Then also use speak for a brief handoff line as you make the offer. They will accept or counter your offer.\n",
				seller, sanitizeInline(it.ItemLabel), seller, sanitizeInline(string(it.kind)), headroom)
			continue
		}
		// Defensive: buildRestocking omits an item that has no co-present seller and
		// no surviving walk-to supplier (LLM-216), so this branch is unreachable in
		// the assembled prompt — it keeps renderRestocking total for a directly
		// constructed view (unit tests) rather than emitting a bare capacity line.
		if len(it.Vendors) == 0 {
			b.WriteString(" No supplier nearby is currently holding stock.\n")
			continue
		}
		// No co-present seller — name the actual situation and the two-step buy
		// (move_to then pay_with_item). This instruction used to live in the section
		// header; LLM-10 moved it here so the header no longer hedges "if a seller is
		// here / otherwise" and each item line reflects whether a seller is present.
		b.WriteString(" No seller is here now — use move_to to reach a supplier below, then pay_with_item once you arrive.\n")
		renderWalkToVendors(b, it.Vendors)
	}
	b.WriteString("\n")
}

// renderWalkToVendors writes the shared walk-to buy bullets — one
// "  - buy from <workplace> (structure_id: <id>)[, <last-paid>]" line per supplier
// — used by both the Restocking cue and the stall-repair buy-nails steer (LLM-274).
// Keeping the two cues on a single renderer guarantees they present the move_to
// destination in the identical, model-proven format; the structure_id is the token
// the weak model echoes into move_to (HOME-349).
func renderWalkToVendors(b *strings.Builder, vendors []RestockVendor) {
	for _, vd := range vendors {
		b.WriteString("  - buy from ")
		b.WriteString(sanitizeInline(vd.StructureLabel))
		if vd.StructureID != "" {
			fmt.Fprintf(b, " (structure_id: %s)", vd.StructureID)
		}
		if vd.CostText != "" {
			fmt.Fprintf(b, ", %s", vd.CostText)
		}
		b.WriteString("\n")
	}
}

// renderCoPresentBuy writes the shared "a seller is here with you — buy it now"
// imperative used by the owner supply errands (the LLM-277 nail repair-buy and the
// shovel farm-upkeep buy) when a qualifying seller of the item shares the buyer's
// huddle. It mirrors the model-proven pay_with_item wording renderRestocking issues
// at the co-present moment (ZBBS-HOME-388): a complete, copyable pay_with_item call
// plus a brief speak handoff, so the weak stateful model transacts here instead of
// narrating its need and walking off (the live Elizabeth-at-the-smith failure that
// motivated LLM-277). qty is the "up to" ceiling — the shortfall the errand needs.
// Inputs are sanitized here, so callers pass raw snapshot strings.
func renderCoPresentBuy(b *strings.Builder, seller, itemLabel string, itemKind sim.ItemKind, qty int) {
	s := sanitizeInline(seller)
	fmt.Fprintf(b, "%s is here with you and sells %s. Buy it now — call pay_with_item with seller \"%s\", item \"%s\", a qty up to %d, and a payment: coins (amount), goods you carry (pay_items), or both, with consume_now false. Then also use speak for a brief handoff line as you make the offer. They will accept or counter your offer.\n",
		s, sanitizeInline(itemLabel), s, sanitizeInline(string(itemKind)), qty)
}
