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

// AllBlocked reports whether EVERY low item in the section is blocked — nothing here
// can be bought, from anyone, by any means (LLM-406). This is the state that used to
// render as no section at all, leaving an illiquid keeper with an unmet obligation and
// not one word about it. Render keys the section lead off it, so such a keeper is told
// the situation rather than "you choose how much to buy".
//
// An empty view is NOT "all blocked" — there is no item, blocked or otherwise. Render
// happens to gate the section on a non-empty Items before ever consulting this, so a
// vacuous true would be harmless there today; it is still the wrong answer to the
// question the method asks, and a footgun for the next caller (code_review).
func (v *RestockingView) AllBlocked() bool {
	if v == nil || len(v.Items) == 0 {
		return false
	}
	for _, it := range v.Items {
		if !it.blocked() {
			return false
		}
	}
	return true
}

// HasWalkToSupplier reports whether this section will render at least one
// "(destination: <id>)" bullet — an off-scene supplier the reseller is meant to
// walk to (LLM-491). It is the signal that reconciles the at-post duty stabilizer
// with this cue: a keeper told to stay at his post while this section names a
// place to go is handed two contradictory instructions and left to pick.
//
// The branch order below MIRRORS renderRestocking's, and must keep mirroring it —
// that correspondence is the whole guarantee. Conserve short-circuits everything
// (its lead replaces the buy directory and every item renders as a hold-off);
// all-blocked renders reasons with deliberately NO destination ids; and per item, a
// standing offer, a blocked item, or a co-present seller each render something
// other than the walk-to list.
func (v *RestockingView) HasWalkToSupplier() bool {
	if v == nil || len(v.Items) == 0 || v.Conserve || v.AllBlocked() {
		return false
	}
	for _, it := range v.Items {
		if it.PendingOfferToCoPresentSeller || it.blocked() || it.CoPresentSeller != "" {
			continue
		}
		if len(it.Vendors) > 0 {
			return true
		}
	}
	return false
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

	// Blocked names the suppliers of this item the buyer cannot transact with right
	// now, with the reason for each (LLM-406). Populated ONLY for an item with no
	// actionable buy path at all — no co-present seller and no surviving walk-to
	// supplier — where the pre-LLM-406 code omitted the item outright and, if every
	// item was omitted, rendered no section at all. That silence turned an illiquid
	// keeper into an absorbing state: he was told neither what he needed, nor who had
	// it, nor why he could not get it (the live Josiah Thorne deadlock — see
	// sim/means_to_pay.go). Suppression may remove the imperative to go buy; it must
	// never remove the NEED, the supplier's IDENTITY, or the REASON.
	//
	// An item nobody sells anywhere stays omitted, with no Blocked entry — there is no
	// supplier to name and nothing to say, so a line for it would be the dead-end cue
	// LLM-216 removed (the live Hannah Boggs phantom fetch-water hires).
	Blocked []RestockBlockedSupplier

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

	// SellerStock / Block are the LLM-308 co-present-buy situation awareness, shared with
	// the nail repair-buy and shovel farm-upkeep buys via the classifier in copresent_buy.go.
	// SellerStock is how many of this item CoPresentSeller holds right now (0 when none is
	// co-present); Block selects whether renderRestocking issues the "Buy it now" imperative,
	// caps its "qty up to N" at the seller's stock, or softens to a hold-off. Both are set only
	// when a seller is co-present and no offer is already standing (PendingOfferToCoPresentSeller
	// wins first). The Terms block is the fix for the live sage loop: an unsoftened, uncapped
	// imperative refired every turn while Josiah declined 11 times, and the weak model obeyed
	// the freshest cue over its own history. The conserve/coin path is already handled section-
	// wide by RestockingView.Conserve (LLM-294); Block's coin arm here additionally catches a
	// hard insufficient-funds failure that conserve mode would not.
	SellerStock int
	Block       copresentBuyBlock

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

	// ResaleUnit is the reseller's OWN realized per-unit resale rate for this item
	// over the window (its sellerRecentSales, nearest-rounded), 0 with no sale on
	// record (LLM-385). The buying-in anchor above is the market/supplier going-rate;
	// this is the number that actually BINDS for a distributor — pay above what you
	// resell for and every unit loses coin. Render judges it against the buy anchor
	// into a margin tier (restockMarginTierOf, LLM-427), falling back to the lone
	// resale-ceiling sentence when no anchor is on record. Its absence (no sale
	// history) leaves the margin unknown; the
	// live Josiah bleed was buying milk/carrots at ~ the going rate while reselling for
	// less, a loss the market-only anchor could never flag.
	ResaleUnit int

	// RecentSalesUnits is the units of this item the reseller SOLD over the trailing
	// restockSalesWindow (Qty×Consumers per accepted sale, seller view of the price
	// book). Render grades it into a demand tier (restockDemandTierOf) and speaks the
	// tier with the count as its evidence ("It sells slowly — about 4 this past
	// week"); 0 renders nothing, so a new or dormant good asserts no demand rather
	// than a misleading "none sold". What is deliberately NOT carried any more is the
	// LLM-63 aggregate coin pair (RecentBuyCost / RecentSalesCoins): coins paid for
	// EVERYTHING bought this window against revenue from ONLY the units that sold was
	// never apples-to-apples, and the model subtracted the two into a phantom loss on
	// any good bought faster than it sells — the live Josiah Thorne cheese refusal
	// ("a fool's arithmetic I don't mean to repeat") on a per-unit-profitable line.
	// Per-unit economics carry the honest signal instead (restockMarginTierOf over
	// BuyAnchorUnit vs ResaleUnit). LLM-427.
	RecentSalesUnits int

	// RecentBuyUnits is the units the reseller BOUGHT of this item this window (buyer
	// view, the unit sibling of RecentBuyCost) and UsingOwnStock flags that it bought
	// markedly more than it SOLD. buildRestocking sets it when RecentBuyUnits >= 4 AND
	// >= 2×RecentSalesUnits+1, which also catches a dead good it keeps restocking though
	// it sells none. Because the restock cue only fires when on-hand is below the reorder
	// point (section membership), a large buy>>sell gap has NOT piled up on the shelf: it
	// left through non-sale channels — the keeper consumed it, used it to make its own wares
	// (a bought recipe input surfaces here too, via EffectiveBuyEntries), or bartered/gave
	// it away (a goods trade the coin price book never books as a sale, so it lives entirely
	// in this residual). Render names that honestly with a verb broad enough to cover all
	// three ("bought N, sold M, used or traded the rest") — "used", not "consumed", so it
	// stays true for a production input that went into the pot, not the keeper's mouth.
	//
	// This supersedes the LLM-385 "buy sparingly, if at all" over-buying hold-off, which
	// read exactly backwards here: an item in the restock section is by definition NOT
	// overstocked, so telling a sold-out, self-consumed line to buy sparingly talked the
	// model out of restocking — the live Josiah cheese confabulation, where it invented
	// "22 still unsold" to reconcile a 0-stock shelf against the overstock steer (LLM-424).
	// True coin-poor overstock is handled section-wide by Conserve, not here.
	RecentBuyUnits int
	UsingOwnStock  bool

	// kind is the final sort tie-break so two item kinds sharing a display label
	// order deterministically (BuyEntries order is stable, but the sort makes the
	// section robust to policy reordering too). Unexported — never rendered.
	// Same posture as OwnStockItem.kind (consumable_vendors.go).
	kind sim.ItemKind
}

// blocked reports whether this item has no actionable buy path — no seller co-present
// to transact with here-and-now, and no open, payable walk-to supplier — but at least
// one supplier that is merely blocked, and so can be named with its reason (LLM-406).
// An item with no supplier anywhere never reaches the view at all (buildRestocking
// omits it), so a blocked item always has something to say.
func (it RestockItemView) blocked() bool {
	return len(it.Vendors) == 0 && it.CoPresentSeller == "" && len(it.Blocked) > 0
}

// RestockVendor is one (workplace, supplier) buy opportunity for a low item.
// StructureID is the supplier's workplace key — the reseller passes it straight
// to move_to(structure_id), then pay_with_item once co-present.
type RestockVendor struct {
	StructureLabel string // "Thorne's General Store" — where the reseller walks to
	StructureID    sim.StructureID
	// VendorID is the representative seller at StructureID (lowest-VendorID, the one
	// whose CostText is shown). Carried so observedSupplierBuyRate (LLM-295) keys the
	// observed anchor on the EXACT sellers this list points at — not every seller at
	// the structure — so the anchor can't average in a non-rendered co-worker.
	VendorID sim.ActorID
	CostText string // per-buyer last-paid "~3 coins", or "" when no price is on record

	// Barter marks a supplier the buyer's COINS cannot cover but its GOODS can reach
	// — the LLM-406 means-to-pay state, and the exact mirror of SatiationVendor.Barter
	// (LLM-222). Render then swaps the coin cost hint for a goods-in-trade steer:
	// showing a "~4 coins" price to a keeper holding 3 would only invite a
	// pay_with_item the purse can't fund, when the bundle it CAN fund is sitting in
	// its pack. Set only on the walk-to list; the co-present imperative already names
	// all three payment forms (coins, pay_items, or both).
	Barter bool

	// CoPresentKeeper is VendorID's display name when that seller is co-present
	// with the buyer right now — sharing the buyer's huddle, or standing (by
	// literal InsideStructureID) in the buyer's conversational structure scope:
	// the same two-way test coPresentSellerForItem applies. "" when absent.
	// LLM-504: the walk-to bullet then reads "buy from Blacksmith (Ezekiel
	// Crane) (destination: …)" — linking shop to person, so a model that already
	// sees the keeper in "## Around you" can ask the man in front of it instead
	// of walking to his shut premises (the live John Ellis case: the repair
	// cue's walk-to steer sent him toward Ezekiel's empty smithy while Ezekiel
	// drank at his bar). The name is the whole mechanism — no imperative, no
	// phrasing branch. Cues with a co-present-buy arm (restock, repair-buy)
	// suppress the walk-to list when a seller is co-present, so this mostly
	// surfaces on the at-business repair cue, which deliberately has no such arm.
	CoPresentKeeper string
}

// restockBlockReason is why a supplier that sells a low item is not a destination
// the buyer can act on this tick. Unexported — render maps it to prose.
type restockBlockReason int

const (
	// restockBlockShut — the buyer remembers finding this supplier's workplace shut
	// (businessRememberedShut, the TTL-decayed experiential memory). It will come back
	// as a destination once the memory lapses.
	restockBlockShut restockBlockReason = iota
	// restockBlockNoMeans — the buyer has no coin that covers the remembered price AND
	// no goods at all to put up in trade. The genuine payment dead-end: nothing to pay
	// with, in any form the seller could accept.
	restockBlockNoMeans
)

// RestockBlockedSupplier is a supplier of a low item that the buyer cannot transact
// with right now, and why (LLM-406). It exists so a blocked keeper is told the truth
// — the need, WHO holds what it needs, and WHY it can't be had — instead of being
// told nothing at all, which is what the LLM-216 drops did once the last supplier
// fell away: the section rendered nil and the keeper stood in an empty shop with no
// cue, no warrant, and no way to reason about his own deadlock.
//
// It deliberately carries NO StructureID. "(destination: <id>)" is the token the
// weak model echoes into move_to (HOME-349), and a blocked supplier is precisely a
// destination that must not be walked to — handing over the id is what produced the
// original LLM-216 touring loop. The supplier is named in PROSE, which gives the
// model the fact without the tour target.
type RestockBlockedSupplier struct {
	StructureLabel string
	Reason         restockBlockReason
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
	// LLM-304: a degraded business is shut for restock — it can't turn a buy into
	// shelf stock until it's mended. Suppress the buy directory so it can't steer a
	// restock the shop can't act on (which would fight the "## Your business" cue's
	// "can't restock until mended"). The keeper sells down what's on hand and mends.
	if ownerBusinessDegraded(snap, actorID) {
		return nil
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
		// Recent demand the reseller sizes its restock against: units sold + coins
		// taken in (seller view — the coins feed only the resale rate below), units
		// bought (buyer view — the self-use accounting). The buy-cost sum is
		// deliberately unread: pairing it with sold-only revenue was the LLM-63
		// aggregate P&L the model subtracted into a phantom loss (LLM-427).
		salesUnits, salesCoins := sellerRecentSales(snap, actorID, e.Item, restockSalesWindow)
		boughtUnits, _ := buyerRecentPurchases(snap, actorID, e.Item, restockSalesWindow)
		// LLM-385: the reseller's OWN realized resale rate — the ceiling the buying-in
		// anchor should be judged against, because a distributor's binding number is
		// what it can resell the good for, not the market going-rate. 0 with no sale on
		// record.
		resaleUnit := 0
		if salesUnits > 0 {
			resaleUnit = (salesCoins + salesUnits/2) / salesUnits
		}
		// LLM-424: buy-outpaces-sell flag — bought markedly more than it SOLD this window.
		// Since the item is below its reorder point (or it would not be in this section),
		// that gap did not pile up on the shelf; it left through non-sale channels —
		// consumed, used to make its own wares, or bartered/given away (a goods trade the
		// coin price book never books as a sale). Render reframes it as an honest self-use
		// accounting, superseding the
		// LLM-385 "buy sparingly" hold-off that read backwards on a sold-out, self-consumed
		// line. Fires at >= twice-sold plus a small floor to avoid noise on tiny numbers,
		// and covers the sold-nothing case (a dead good it keeps restocking).
		usingOwnStock := boughtUnits >= 4 && boughtUnits >= 2*salesUnits+1
		coName, coID := coPresentSellerForItem(snap, actorID, actorSnap, e.Item)
		vendors, blocked := findItemVendors(snap, actorID, actorSnap, e.Item)
		// LLM-308: make the co-present buy imperative situation-aware via the same shared
		// classifier the nail/shovel co-present buys use (copresent_buy.go) — cap the ask at
		// the seller's live stock and soften to a hold-off when the negotiation has dead-ended
		// (>=2 recent declines this huddle), the purse can't take it on, or he holds nothing.
		// This is what stops the live sage offer→decline loop, where the uncapped, unsoftened
		// imperative refired verbatim through 11 declines. The pending-offer bide steer wins
		// first, so skip the block scan under it (mirrors buildFarmUpkeep / buildStallRepairBuy).
		pendingCoPresent := coID != "" && hasPendingOfferTo(snap, actorID, coID, e.Item)
		sellerStock := 0
		block := copresentBuyOK
		if coID != "" && !pendingCoPresent {
			sellerStock, block = classifyCoPresentBuy(snap, actorID, actorSnap, coID, e.Item)
		}
		// No actionable buy path — no co-present seller to transact with here-and-now,
		// and no open, payable walk-to supplier (findItemVendors separates out the shut
		// and the unpayable). LLM-216 omitted the item outright here, so a keeper with
		// no path was never handed a cue it would tour on the wasted-move loop.
		//
		// LLM-406 keeps that restraint — no destination, no buy imperative — but not the
		// SILENCE. If suppliers exist and are merely blocked, name them and why: the
		// keeper still has an unmet obligation, and telling him nothing about it is what
		// turned the live Josiah Thorne into an absorbing state (no cue, no warrant, no
		// way to reason about his own deadlock). If NOBODY sells the item anywhere,
		// there is no supplier to name and nothing to say — omit it exactly as before,
		// rather than dangle a want with no possible outlet (the Hannah Boggs phantom
		// fetch-water hires). Either way the item returns to a normal buy line the
		// moment a supplier opens or the buyer can pay.
		if len(vendors) == 0 && coName == "" && len(blocked) == 0 {
			continue
		}
		// LLM-295: observed-first buy anchor, scoped to the reachable suppliers just
		// resolved (so it agrees with their walk-to prices); catalog seed only until
		// real transactions exist.
		buyAnchor, buyAnchorObserved := observedSupplierBuyRate(vendors, coID, snap, e.Item, restockSalesWindow)
		if !buyAnchorObserved {
			buyAnchor = catalogBulkRate(snap, e.Item)
		}
		items = append(items, RestockItemView{
			ItemLabel:                     itemDisplayLabel(snap, e.Item),
			CurrentQty:                    current,
			Cap:                           cap,
			Vendors:                       vendors,
			Blocked:                       blocked,
			CoPresentSeller:               coName,
			PendingOfferToCoPresentSeller: pendingCoPresent,
			SellerStock:                   sellerStock,
			Block:                         block,
			AffordableQty:                 affordable,
			BuyAnchorUnit:                 buyAnchor,
			BuyAnchorObserved:             buyAnchorObserved,
			ResaleUnit:                    resaleUnit,
			RecentSalesUnits:              salesUnits,
			RecentBuyUnits:                boughtUnits,
			UsingOwnStock:                 usingOwnStock,
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
// any such sale is on record (LLM-295). Scoped to EXACTLY the sellers the cue
// surfaces as buy destinations — the representative vendor of each surviving
// walk-to structure (`vendors[].VendorID`, already through the LLM-216
// shut/affordability drops and the one-per-structure dedupe) plus the co-present
// seller (`coPresentID`) — so the anchor is drawn from the same sellers whose
// walk-to prices sit beside it, never a shut farm the buyer can't reach nor a
// non-rendered co-worker at a listed structure (which would hand the weak model a
// number that disagrees with the line it's on — code_review). Their seller-view
// PriceBook sales are summed. This is the observed anchor that supersedes the
// hand-authored catalog seed: grounded in the destinations the buyer restocks
// from, so buying from a producer yields the observed wholesale rate and buying
// from the distributor yields what the distributor actually charges (the mid-tier
// price the two-price catalog can't express). Per-supplier by construction, so it
// barely dents the PriceBook "knowledge earned by patronage" asymmetry. 0/false
// when no reachable supplier has a sale in the window (caller falls back to seed).
func observedSupplierBuyRate(vendors []RestockVendor, coPresentID sim.ActorID, snap *sim.Snapshot, item sim.ItemKind, window time.Duration) (int, bool) {
	suppliers := map[sim.ActorID]bool{}
	for _, v := range vendors {
		if v.VendorID != "" {
			suppliers[v.VendorID] = true
		}
	}
	if coPresentID != "" {
		suppliers[coPresentID] = true
	}
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
	// Only the payable, open suppliers count as a path — a blocked supplier is
	// named to the buyer (LLM-406) but is not somewhere it can act.
	vendors, _ := findItemVendors(snap, actorID, actorSnap, kind)
	return len(vendors) > 0
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
// The definition lives in sim (sim.ObservationUnits) so the cue, the umbilical
// sell-through row, and the wear cost basis all count units the same way.
func observationUnits(obs sim.PriceObservation) int64 {
	return sim.ObservationUnits(obs)
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
	u, c := sim.BuyerPurchaseTotals(snap.PriceBook, buyerID, item, snap.PublishedAt.Add(-window))
	if u > int64(math.MaxInt32) {
		u = int64(math.MaxInt32)
	}
	if c > int64(math.MaxInt32) {
		c = int64(math.MaxInt32)
	}
	return int(u), int(c)
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
// It separates two kinds of non-destination out of the returned list (LLM-216,
// mirroring the seek-work directory and the need-redirect affordability skip): a
// supplier the buyer remembers finding shut, and one the buyer has no MEANS to pay.
// Neither is a walk-to target — both are dead ends the weak model would otherwise
// tour (the live Josiah Thorne shut-farm move_to loop) — so neither appears in
// `vendors`. They are returned separately in `blocked` (LLM-406) so the caller can
// still NAME them, in prose and without a move_to id, rather than let a fully-blocked
// item vanish into a silent nil section.
//
// MEANS TO PAY, not coins (LLM-406). The old test was coins-only: any supplier whose
// remembered price exceeded the purse was dropped. But pay_with_item settles in goods
// as readily as coin, so that gate asked the wrong question and silently erased a
// goods-rich, coin-poor keeper from his own supply chain. A supplier is payable when
// the purse covers the remembered price, when no price is on record and the buyer has
// coin at all (patronage earns the number — walk over, learn it, pay), or, failing
// coin, when the buyer holds any OTHER good to put up in trade — in which case the
// vendor survives with Barter set and render steers it to a goods offer.
//
// So a supplier is blocked exactly when the buyer holds no goods to offer AND either
// cannot cover its remembered price in coin, or has no coin at all for an unknown one.
// Being merely short of a known price does not block it on its own; the pack has to be
// empty too. Same coin-OR-goods shape gatherSatiationVendors has had since LLM-222; the
// sim-side warrant mirror is sim.buyerCanTransact.
//
// Dedupe-by-structure: the LLM only needs a destination — move_to(structure_id)
// then pay_with_item resolves which co-present seller actually transacts — so two
// NPCs working the same structure and both holding the item collapse to one cue
// (which also kills the duplicate-line + map-order nondeterminism, code_review).
// The representative seller is the lowest VendorID at that structure, picked
// deterministically so the per-buyer CostText (last-paid from that seller) is
// stable across snapshots regardless of map iteration order.
func findItemVendors(snap *sim.Snapshot, buyerID sim.ActorID, buyerSnap *sim.ActorSnapshot, itemKind sim.ItemKind) ([]RestockVendor, []RestockBlockedSupplier) {
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
		return nil, nil
	}
	coins := buyerSnap.Coins
	// Goods it could put up in trade FOR this item — the item itself excluded, since a
	// keeper down to his last few carrots cannot buy carrots by offering carrots
	// (LLM-406). Same predicate the warrant reads (sim.buyerCanTransact).
	hasGoods := sim.HoldsBarterableGoodsExcept(snap.ItemKinds, buyerSnap.Inventory, itemKind)
	// LLM-504: buyer-side co-presence anchors, for naming a keeper who is standing
	// with the buyer on that keeper's walk-to bullet (CoPresentKeeper). Same
	// huddle-or-scope pair coPresentSellerForItem resolves through.
	huddle := buyerSnap.CurrentHuddleID
	buyerScope := conversationalScopeStructure(snap, buyerSnap)
	out := make([]RestockVendor, 0, len(best))
	var blocked []RestockBlockedSupplier
	for structureID, p := range best {
		label := vendorStructureLabel(p.structure)
		// LLM-216: a supplier the buyer remembers finding shut is not a destination,
		// mirroring the seek-work directory (buildSeekWorkPlaces). Annotating it — the
		// old ZBBS-HOME-353 / LLM-126 "found it shut up" posture — left the weak model
		// touring the dead ends (Josiah's every-tick move_to loop among shut farms).
		// The shut memory is experiential and TTL-decayed, so the supplier returns to
		// the walk-to list once it lapses (he'd go there and find a keeper), preserving
		// the retry the annotation aimed for without the wasted trips in between.
		if businessRememberedShut(snap, buyerSnap, structureID) {
			blocked = append(blocked, RestockBlockedSupplier{StructureLabel: label, Reason: restockBlockShut})
			continue
		}
		// The LLM-406 means-to-pay gate (see the doc comment): coin that covers the
		// remembered price, coin with no price yet on record, or — failing coin — goods
		// to barter. Only the buyer with neither is blocked.
		barter := false
		price := buyerLastPaidCoins(snap, buyerID, p.vendorID, itemKind)
		switch {
		case price > 0 && coins >= price:
			// The purse covers what he last paid here — an ordinary coin buy.
		case price == 0 && coins > 0:
			// No price on record but he has coin: patronage earns the number, so he
			// walks over, learns it, and pays (and can still barter on arrival).
		case hasGoods:
			// Coin can't cover it, but his pack can. pay_with_item takes goods and the
			// SELLER adjudicates the bundle — so this is a real destination, steered to
			// a goods offer rather than a coin price he cannot meet.
			barter = true
		default:
			// No coin that covers it and nothing whatever to trade — the genuine payment
			// dead-end. Not a destination, but still NAMED to the caller so a keeper with
			// no path left is told who holds what he needs and why he can't have it.
			blocked = append(blocked, RestockBlockedSupplier{StructureLabel: label, Reason: restockBlockNoMeans})
			continue
		}
		// LLM-504: is the representative seller standing with the buyer right now?
		// Seller tested by literal InsideStructureID against the buyer's loiter-aware
		// scope (or a shared huddle) — the coPresentSellerForItem rule, so the name
		// only appears when a pay_with_item to this seller would resolve here.
		coPresent := ""
		if seller := snap.Actors[p.vendorID]; seller != nil && seller.DisplayName != "" {
			sharesHuddle := huddle != "" && seller.CurrentHuddleID == huddle
			sharesScope := buyerScope != "" && seller.InsideStructureID == buyerScope
			if sharesHuddle || sharesScope {
				coPresent = seller.DisplayName
			}
		}
		out = append(out, RestockVendor{
			StructureLabel:  label,
			StructureID:     structureID,
			CoPresentKeeper: coPresent,
			VendorID:        p.vendorID, // the representative whose CostText below is shown (LLM-295)
			// Empty fallback when no price is on record (was "ask the supplier",
			// which invited the reseller to SPEAK a price question instead of
			// calling pay_with_item — ZBBS-HOME-386). With "", renderRestocking
			// omits the cost clause entirely; the header carries the action.
			CostText: buyerLastPaidText(snap, buyerID, p.vendorID, itemKind, ""),
			Barter:   barter,
		})
	}
	// Alphabetical for deterministic output over the surviving suppliers (the shut
	// and the unpayable were separated out above); the blocked list is sorted on the
	// same key so its prose ordering is stable too.
	sort.Slice(out, func(i, j int) bool {
		if out[i].StructureLabel != out[j].StructureLabel {
			return out[i].StructureLabel < out[j].StructureLabel
		}
		return out[i].StructureID < out[j].StructureID
	})
	sort.Slice(blocked, func(i, j int) bool {
		if blocked[i].StructureLabel != blocked[j].StructureLabel {
			return blocked[i].StructureLabel < blocked[j].StructureLabel
		}
		return blocked[i].Reason < blocked[j].Reason
	})
	return out, blocked
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

// restockDemandTier grades an item's weekly sell-through for the restock cue's
// demand clause (LLM-427) — the engine-computes-judgments half of the felt-needs
// pattern: the cue speaks a tier ("It sells slowly") with the unit count as its
// evidence, never a raw figure the model must interpret alone. demandSilent (no
// sale in the window) renders nothing at all, preserving the LLM-63 posture that
// a new or dormant good asserts no demand rather than a misleading "none sold";
// a dead good the keeper keeps buying is still named by the self-use accounting.
// Unexported — render maps tiers to prose.
type restockDemandTier int

const (
	demandSilent restockDemandTier = iota
	demandSlow
	demandSteady
	demandBrisk
)

// restockDemandTierOf grades units sold over the trailing restockSalesWindow. The
// cutoffs read against that literal week: slow is a unit a day at most, brisk is
// roughly three a day or better, steady the band between. Tuning knobs, kept
// constants like the window itself.
func restockDemandTierOf(salesUnits int) restockDemandTier {
	switch {
	case salesUnits <= 0:
		return demandSilent
	case salesUnits <= 7:
		return demandSlow
	case salesUnits <= 20:
		return demandSteady
	default:
		return demandBrisk
	}
}

// restockMarginTier is the engine's judgment of an item's per-unit resale margin —
// buy at the anchor, resell at the realized rate, and say which way the coin flows
// (LLM-427). It replaces the pair of warning-tailed anchor/ceiling sentences whose
// numbers the model was left to reconcile itself — and, fatally, the LLM-63
// aggregate cost-vs-sales pair it reconciled them AGAINST, subtracting all-bought
// cost from sold-only revenue into a phantom loss (the live Josiah cheese refusal
// on a line that earned a coin per unit). Unexported — render maps tiers to prose.
type restockMarginTier int

const (
	marginUnknown restockMarginTier = iota
	marginHighlyProfitable
	marginNicelyProfitable
	marginSlightlyProfitable
	marginBreakEven
	marginLosing
)

// Profit-band cutoffs, as a multiple of the buy rate. Graded by RATIO, not by the
// coin difference: +1 coin on a 4-coin good is a thin 25% and +1 on a 1-coin good is
// a doubling, and a merchant deciding where to put his purse cares about the second
// far more than the first. Cross-multiplied against the displayed integers (see
// restockMarginTierOf) so no division or float enters the comparison.
//
// The bands are coarse on purpose. With integer coin rates a 1-coin good can only
// resell at 1 (breakeven), 2 (nicely) or 3+ (highly) — there is no finer judgment to
// be had, and pretending otherwise would grade noise.
const (
	marginHighlyProfitableMultiple = 3
	marginNicelyProfitableMultiple = 2
)

// restockMarginTierOf judges the two DISPLAYED per-unit rates against each other.
// The comparison deliberately runs on the nearest-rounded coin integers the cue
// prints, not the un-rounded internals: the judgment shares a sentence with those
// numbers, and "you buy at about 2 and resell at about 2 — each one earns you
// coin" would read as a contradiction even when a sub-coin margin makes it
// technically true. Sub-coin margins therefore grade break-even — the tier is a
// judgment, not an audit. Unknown when either rate is missing, in which case
// render falls back to the single known rate's own sentence.
func restockMarginTierOf(buyAnchorUnit, resaleUnit int) restockMarginTier {
	if buyAnchorUnit <= 0 || resaleUnit <= 0 {
		return marginUnknown
	}
	// Compared by DIVIDING the resale rate, never by multiplying the buy rate. Both
	// rates are independently accumulated ledger aggregates, so a cross-multiply
	// (buy*3) can overflow on a large buy rate and REVERSE a verdict rather than
	// merely garble it — widening to int64 does not fix that, it only moves the
	// ceiling (code_review). Division cannot overflow for positive operands, and both
	// are guaranteed positive by the guard above.
	//
	// Truncation is harmless here because the comparison is `buy <= resale/N`: integer
	// division rounds the threshold DOWN, which can only make the band harder to
	// enter, never easier, so a good is never promoted into a band it has not earned.
	// Exact multiples land on the boundary and are included (resale 6 / buy 2 → 6/3=2,
	// 2<=2 → highly), which is the intended inclusive semantics.
	switch {
	case buyAnchorUnit <= resaleUnit/marginHighlyProfitableMultiple:
		return marginHighlyProfitable
	case buyAnchorUnit <= resaleUnit/marginNicelyProfitableMultiple:
		return marginNicelyProfitable
	case resaleUnit > buyAnchorUnit:
		return marginSlightlyProfitable
	case resaleUnit < buyAnchorUnit:
		return marginLosing
	default:
		return marginBreakEven
	}
}

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
	// LLM-406: every low item is blocked — no seller here, nowhere payable to walk to.
	// "You choose how much to buy" would be a lie, and a lie the model would act on, so
	// the section leads with the situation instead. Conserve (a keeper who COULD buy but
	// shouldn't) still wins: it is a choice, and its own steer already says so.
	if !v.Conserve && v.AllBlocked() {
		b.WriteString("Your shop stock of these bought-in goods is running low, and there is no way to replenish it just now. This is what stands in the way:\n")
		for _, it := range v.Items {
			renderBlockedItem(b, it)
		}
		b.WriteString("\n")
		return
	}
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
		// LLM-298: the bare "You are low on X" named a want with no outlet — a weak
		// model (llama-3.3-70b) filled the vacuum by inventing a "Market" to move_to.
		// Self-resolve the line: say what to do INSTEAD (hold, sell first, restock
		// later) so it never dangles a lack the model has to improvise a destination
		// for. The section lead carries the same steer; restating it per-item stops
		// the model latching onto one item bullet in isolation on a restock-wakeup turn.
		if v.Conserve {
			fmt.Fprintf(b, "- You are low on %s — no errand for it now; sell first, then restock once your purse recovers.\n", sanitizeInline(it.ItemLabel))
			continue
		}
		// LLM-406: this item has no buy path, but others in the section do — name the
		// blocked suppliers and their reasons rather than letting the item vanish while
		// its neighbours render normally. Same restraint as the all-blocked lead above:
		// no destination id, no buy imperative.
		if it.blocked() {
			renderBlockedItem(b, it)
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
		// The week's demand, spoken as a judgment with its evidence (LLM-427): the
		// tier carries the conclusion, the unit count grounds it. What died here is
		// the LLM-63 aggregate P&L tail ("at a cost of X coins and sales of Y
		// coins") — coins paid for everything bought against revenue from only the
		// units that sold, never apples-to-apples, which the model subtracted into a
		// phantom loss on any good bought faster than it sells (the live Josiah
		// cheese refusal, on a line that earned a coin per unit). Silent when no
		// sale is on record, so a new or dormant good asserts no demand.
		switch restockDemandTierOf(it.RecentSalesUnits) {
		case demandSlow:
			fmt.Fprintf(b, " It sells slowly — about %d this past week.", it.RecentSalesUnits)
		case demandSteady:
			fmt.Fprintf(b, " It sells steadily — about %d this past week.", it.RecentSalesUnits)
		case demandBrisk:
			fmt.Fprintf(b, " It sells briskly — about %d this past week.", it.RecentSalesUnits)
		}
		// The per-unit economics, spoken as a judgment (LLM-427): one fixed clause
		// per margin tier with both displayed rates interpolated — replacing the two
		// warning-tailed anchor/ceiling sentences (LLM-295 / LLM-385) the model was
		// left to reconcile on its own. The rates stay exact and per-unit (never a
		// fill total — the LLM-63 copy-paste-max-offer guard), and the losing tier
		// names the loss with both ways out (buy cheaper / charge more) but never
		// holds off the restock: the shelf is low and the demand real, so the
		// decision stays the keeper's, and a co-present "Buy it now" below reconciles
		// by countering lower rather than by contradiction. No "ask"/"price" token
		// (the HOME-386 speaking-loop guard).
		// LLM-492: every clause is scoped to the COIN trade ("in coin", "in coin
		// alone"). Both rates come from the price book, which by design records only
		// coin legs — a pure-barter settlement is skipped outright, because booking
		// its zero Amount would enter a free reading and poison every rate derived
		// from it (ZBBS-HOME-393). That exclusion is right, but it means the verdict
		// is computed from a SAMPLE of the trade, and village-wide barter runs about
		// a third of settlements — concentrated in exactly the raw/intermediate goods
		// this section governs. The old unscoped wording ("it earns you nothing on
		// its own") asserted the whole economics of the line from the coin legs alone
		// and was live-observed talking a keeper out of the input leg of a working
		// trade: Josiah's wheat was 61% barter by volume, swapped ~1:1 to the mill
		// for flour he resold at double, and he declined it eight ticks running while
		// quoting the cue's own reasoning back at it. Scoping does not tell him what
		// the barter leg is worth — nothing here can, and inventing a coin value for
		// it would be the LLM-475 defect wearing a different hat (uncertainty stays
		// silent) — it only stops the engine claiming more than it observed.
		// One uniform shape across every tier: the two rates, then the verdict as a
		// short trailing label. Each clause previously had its own sentence structure,
		// so the model had to parse a different grammar per tier to find the same two
		// numbers; identical framing means only the label varies.
		//
		// Every label carries "on coin" because the scope has to reach the MODEL, not
		// just a reader of this file (code_review). Both rates come from the coin price
		// book, which records only what was paid in coin; a bare "breakeven" reads as a
		// claim about the good itself, which is the inference that talked a keeper out
		// of the input leg of a working trade whose other half settled in barter the
		// book never sees. The scope will not by itself teach the model to reason about
		// that barter — nothing here can, and inventing a coin value for it would
		// repeat the LLM-475 defect (uncertainty stays silent). It only stops the
		// engine asserting more than it observed.
		switch restockMarginTierOf(it.BuyAnchorUnit, it.ResaleUnit) {
		case marginHighlyProfitable:
			fmt.Fprintf(b, " You are buying at about %s and selling at about %s — highly profitable on coin.",
				coinsPhrase(it.BuyAnchorUnit), coinsPhrase(it.ResaleUnit))
		case marginNicelyProfitable:
			fmt.Fprintf(b, " You are buying at about %s and selling at about %s — nicely profitable on coin.",
				coinsPhrase(it.BuyAnchorUnit), coinsPhrase(it.ResaleUnit))
		case marginSlightlyProfitable:
			fmt.Fprintf(b, " You are buying at about %s and selling at about %s — slightly profitable on coin.",
				coinsPhrase(it.BuyAnchorUnit), coinsPhrase(it.ResaleUnit))
		case marginBreakEven:
			fmt.Fprintf(b, " You are buying at about %s and selling at about %s — breakeven on coin.",
				coinsPhrase(it.BuyAnchorUnit), coinsPhrase(it.ResaleUnit))
		case marginLosing:
			fmt.Fprintf(b, " You are buying at about %s and selling at about %s — losing on coin; you need to buy lower or sell for more.",
				coinsPhrase(it.BuyAnchorUnit), coinsPhrase(it.ResaleUnit))
		default:
			// Only one rate (or neither) on record — no margin to judge, so the known
			// rate keeps its pre-LLM-427 sentence. The buy anchor keeps its overpay
			// guard (nothing else carries the corrective to the buyer's self-poisoning
			// last-paid when there is no margin verdict), phrased lived-vs-seed per
			// LLM-295; the resale ceiling keeps its LLM-385 wording, referring to
			// "your resale rate" rather than the rounded integer (code_review).
			if it.BuyAnchorUnit > 0 {
				if it.BuyAnchorObserved {
					fmt.Fprintf(b, " Of late it has been going for about %s each — pay much above that and you're overpaying.",
						coinsPhrase(it.BuyAnchorUnit))
				} else {
					fmt.Fprintf(b, " It is generally worth about %s each — pay much above that and you're overpaying.",
						coinsPhrase(it.BuyAnchorUnit))
				}
			} else if it.ResaleUnit > 0 {
				fmt.Fprintf(b, " You resell it for about %s each — paying above your resale rate loses coin on each one.",
					coinsPhrase(it.ResaleUnit))
			}
		}
		// LLM-424: self-use accounting — bought far more than it SOLD this window, yet an
		// item in the restock section is below its reorder point, so the gap did not pile
		// up on the shelf: the keeper consumed it, used it to make its own wares, or
		// bartered/gave it away (a goods trade the coin price book never books as a sale, so
		// it sits entirely in this residual). "used or traded" covers all three — "used",
		// not "consumed", stays true for a production input that went into the pot, not the
		// keeper's mouth. Name that honestly instead of the LLM-385 "buy sparingly" hold-off,
		// which read exactly backwards on a sold-out, self-consumed line and talked the model
		// out of restocking (the live Josiah cheese confabulation: it invented "22 still
		// unsold" to reconcile a 0-stock shelf against an overstock steer). No "ask"/"price"
		// token (the HOME-386 speaking-loop guard) and no hold-off imperative — an empty
		// shelf needs restocking regardless of flow velocity.
		if it.UsingOwnStock {
			fmt.Fprintf(b, " You've bought about %d of these this past week, sold only %d, and used or traded the rest.",
				it.RecentBuyUnits, it.RecentSalesUnits)
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
		// A seller of this item is in the conversation right now. LLM-308: route the buy
		// imperative through the same block classification the nail/shovel co-present buys
		// use (copresent_buy.go) so the restock cue can't drive the live sage offer→decline
		// loop — soften to a hold-off when the negotiation has dead-ended / the purse can't
		// take it on / he holds nothing, and cap the "qty up to N" at his live stock when he
		// can't cover the headroom. The sub-bullet indent and the OK-path wording are
		// unchanged from ZBBS-HOME-388, so a healthy buy still renders byte-identically.
		if it.CoPresentSeller != "" {
			b.WriteString("\n  - ")
			switch {
			case it.Block == copresentBuyBlockedNoStock, it.Block == copresentBuyBlockedCoin, it.Block == copresentBuyBlockedTerms:
				// The negotiation has dead-ended (>=2 declines), the purse can't take it on, or
				// he's out of stock — soften instead of goading the buy, so the cue stops
				// pressing the seller into another no (the live 11-round sage standoff).
				renderCoPresentBuySoften(b, it.CoPresentSeller, it.ItemLabel, it.Block)
			case it.SellerStock > 0 && it.SellerStock < headroom:
				// He can't cover the whole headroom — cap the ask at what he holds so "qty up to
				// N" never exceeds his stock (the live "buy it now … a qty up to 3" against a
				// seller holding 1).
				renderCoPresentBuyCapped(b, it.CoPresentSeller, it.ItemLabel, it.kind, it.SellerStock)
			default:
				// Stock covers the headroom and the deal is plausible — the exact pay_with_item
				// call, walk-to list skipped (he is already here).
				seller := sanitizeInline(it.CoPresentSeller)
				fmt.Fprintf(b, "%s is here with you and sells %s. Buy it now — call pay_with_item with seller \"%s\", item \"%s\", a qty up to %d, a payment: coins (amount), goods you carry (pay_items), or both, with consume_now false, and your handoff line in say. Do not speak first: speaking ends your turn, and the offer would never be made. They will accept or counter your offer.\n",
					seller, sanitizeInline(it.ItemLabel), seller, sanitizeInline(string(it.kind)), headroom)
			}
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
		// No co-present seller — name the situation and let the walk-to bullets below
		// carry it. The line used to spell out the two-step buy ("use move_to to reach a
		// supplier below, then pay_with_item once you arrive"); LLM-492 drops that
		// instruction as redundant. Each bullet already carries a `(destination: <id>)`
		// token, which is what the model actually echoes into move_to, and the buy verb
		// is in the tool schema it is holding. Restating the mechanics bought nothing and
		// cost line length on a section that has accreted clauses for six revisions.
		// (The header retains no instruction either — LLM-10's reason for moving it here
		// was to stop the header hedging "if a seller is here / otherwise", and the
		// per-item situation line still does that job.)
		b.WriteString(" No seller is here now.\n")
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
		// LLM-504: name the keeper when they are standing with the buyer right now,
		// linking shop to person — the model already sees the name in "## Around
		// you", and the link is what lets it ask the man instead of walking to his
		// empty premises. A name, not an imperative.
		if vd.CoPresentKeeper != "" {
			fmt.Fprintf(b, " (%s)", sanitizeInline(vd.CoPresentKeeper))
		}
		if vd.StructureID != "" {
			fmt.Fprintf(b, " (destination: %s)", vd.StructureID)
		}
		// Means-to-pay phrasing (LLM-406), the same swap the satiation buy cue makes
		// (LLM-222): a supplier the purse can't cover but the pack can reach is steered
		// to a goods offer. Showing the coin hint here instead would name a number the
		// buyer cannot meet and invite a pay_with_item it cannot fund — while the bundle
		// it CAN fund sits in its inventory, unmentioned. Otherwise the ordinary
		// last-paid coin hint, when one is on record.
		if vd.Barter {
			b.WriteString(", which your coins won't cover — offer goods you carry in trade instead (use pay_with_item with pay_items)")
		} else if vd.CostText != "" {
			fmt.Fprintf(b, ", %s", vd.CostText)
		}
		b.WriteString("\n")
	}
}

// renderBlockedItem writes the LLM-406 blocked scene for a low item with no
// actionable buy path: the need, WHO holds it, WHY it can't be had, and what to do
// instead. It is the answer to the silence that made an illiquid keeper an absorbing
// state — suppression may take away the imperative to go buy, but it must not take
// away the facts he needs to reason about his own position.
//
// Deliberately carries NO "(destination: <id>)" token and no buy imperative. That
// token is what the weak model echoes into move_to (HOME-349), and every supplier
// here is exactly a place it must NOT walk to — handing over the ids is what drove
// the original LLM-216 touring loop. Naming the supplier in prose gives the model the
// fact without the tour target.
//
// The line self-resolves (LLM-298): it never leaves a bare want dangling, because a
// weak model fills that vacuum by inventing an errand (the phantom "Market" it tried
// to move_to). It says what to do INSTEAD, and the coda is keyed to the reason — a
// shut supplier will reopen and is worth another call another day; a purse and a pack
// with nothing in them is a wait for trade to come in. An item blocked BOTH ways gets
// both resolutions: keying the coda off one reason alone would silently drop the other
// supplier's way out (code_review).
func renderBlockedItem(b *strings.Builder, it RestockItemView) {
	fmt.Fprintf(b, "- You have %d %s on hand, and no way to restock just now.\n",
		it.CurrentQty, sanitizeInline(it.ItemLabel))
	noMeans, shut := false, false
	for _, bl := range it.Blocked {
		switch bl.Reason {
		case restockBlockNoMeans:
			noMeans = true
			fmt.Fprintf(b, "  - %s sells %s, but you have neither the coin for it nor a single good to put up in trade.\n",
				sanitizeInline(bl.StructureLabel), sanitizeInline(it.ItemLabel))
		default:
			shut = true
			fmt.Fprintf(b, "  - %s sells %s, but you called there and found it shut.\n",
				sanitizeInline(bl.StructureLabel), sanitizeInline(it.ItemLabel))
		}
	}
	switch {
	case noMeans && shut:
		b.WriteString("  Keep your shop and take what trade comes to you — once you have coin or goods to trade with you can restock, and the shut one is worth looking in on another day.\n")
	case noMeans:
		b.WriteString("  Keep your shop and take what trade comes to you — you can restock once you have coin or goods to trade with.\n")
	default:
		b.WriteString("  Look in again another day — a keeper will be tending it sooner or later.\n")
	}
}

// renderCoPresentBuy writes the shared "a seller is here with you — buy it now"
// imperative used by the owner supply errands (the LLM-277 nail repair-buy and the
// shovel farm-upkeep buy) when a qualifying seller of the item shares the buyer's
// huddle. It mirrors the model-proven pay_with_item wording renderRestocking issues
// at the co-present moment (ZBBS-HOME-388): a complete, copyable pay_with_item call
// carrying the handoff line, so the weak stateful model transacts here instead of
// narrating its need and walking off (the live Elizabeth-at-the-smith failure that
// motivated LLM-277). qty is the "up to" ceiling — the shortfall the errand needs.
// Inputs are sanitized here, so callers pass raw snapshot strings.
//
// The handoff word rides in pay_with_item's `say` (LLM-350). ZBBS-HOME-388 asked
// for it as a separate speak — correct then, unreachable since LLM-321 made speak
// terminal alongside pay_with_item: whichever landed first ended the tick, so the
// buyer either offered in silence or spoke and never offered.
func renderCoPresentBuy(b *strings.Builder, seller, itemLabel string, itemKind sim.ItemKind, qty int) {
	s := sanitizeInline(seller)
	fmt.Fprintf(b, "%s is here with you and sells %s. Buy it now — call pay_with_item with seller \"%s\", item \"%s\", a qty up to %d, a payment: coins (amount), goods you carry (pay_items), or both, with consume_now false, and your handoff line in say. Do not speak first: speaking ends your turn, and the offer would never be made. They will accept or counter your offer.\n",
		s, sanitizeInline(itemLabel), s, sanitizeInline(string(itemKind)), qty)
}
