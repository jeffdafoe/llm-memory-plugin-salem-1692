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

	// RecentSalesUnits is how many units of this item the reseller has SOLD within
	// the trailing restockSalesWindow (Qty×Consumers per accepted sale, read from
	// the seller view of the price book). 0 when no sale is on record in the window.
	// LLM-63: render surfaces it as the empirical demand signal ("you've sold about
	// N over the past week") the reseller sizes its restock against — replacing the
	// cap/fill-to-cap anchor that biased the weak model into filling straight to cap
	// and draining its working capital. Silent at 0 so a new or dormant good asserts
	// no rate rather than a misleading zero.
	RecentSalesUnits int

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
		// Recent sell-through is the empirical demand signal the reseller sizes its
		// restock against (LLM-63). 0 (no sale in the window) leaves the line silent.
		recentSold := sellerRecentSalesUnits(snap, actorID, e.Item, restockSalesWindow)
		coName, coID := coPresentSellerForItem(snap, actorID, actorSnap, e.Item)
		items = append(items, RestockItemView{
			ItemLabel:                     itemDisplayLabel(snap, e.Item),
			CurrentQty:                    current,
			Cap:                           cap,
			Vendors:                       findItemVendors(snap, actorID, actorSnap, e.Item),
			CoPresentSeller:               coName,
			PendingOfferToCoPresentSeller: coID != "" && hasPendingOfferTo(snap, actorID, coID, e.Item),
			AffordableQty:                 affordable,
			RecentSalesUnits:              recentSold,
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
	}
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

// sellerRecentSalesUnits totals the units the actor has SOLD of `item` within the
// trailing `window`, read from the seller view of the price book (snap.PriceBook,
// keyed by (seller, item)). Units are Qty×Consumers per accepted sale — the
// bundle's true unit count, the same measure observationUnits gives the buyer side
// — summed over observations no older than snap.PublishedAt − window. Returns 0
// when no sale is on record in the window, so a new or dormant good asserts no rate
// rather than a misleading zero. This is the empirical demand signal the restock
// cue surfaces in place of the cap anchor (LLM-63). Per-seller by construction: the
// ring is keyed by seller, so this reads only this actor's own sales. int64 guards
// the sum; the result is clamped into int range.
func sellerRecentSalesUnits(snap *sim.Snapshot, sellerID sim.ActorID, item sim.ItemKind, window time.Duration) int {
	if snap == nil || snap.PriceBook == nil {
		return 0
	}
	buf, ok := snap.PriceBook[sim.PriceBookKey{SellerID: sellerID, Item: item}]
	if !ok || buf == nil || buf.Len() == 0 {
		return 0
	}
	cutoff := snap.PublishedAt.Add(-window)
	var total int64
	for _, obs := range buf.Snapshot() {
		if obs.At.Before(cutoff) {
			continue
		}
		total += observationUnits(obs)
	}
	if total > int64(math.MaxInt32) {
		total = int64(math.MaxInt32)
	}
	return int(total)
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

// restockSalesWindow is the trailing window over which the restock cue measures a
// reseller's recent sell-through (LLM-63 / sellerRecentSalesUnits). Game time is
// wall-clock, so this is a literal week — long enough to smooth a low-volume good's
// sparse sales into a stable demand read. The "past week" wording in
// renderRestocking is tied to it; change both together. A tuning knob, kept a
// constant for now; promote to RestockReorderPct's setting pattern if it needs
// live tuning.
const restockSalesWindow = 7 * 24 * time.Hour

// renderRestocking writes the "## Restocking" section. Content-gated: a
// nil/empty view writes nothing. Each low item leads with its on-hand count, then —
// when on record — the recent sell-through line (LLM-63: the empirical demand
// signal the reseller sizes the buy against, in place of the cap/fill-to-cap anchor
// that biased the weak model into filling straight to cap and draining its working
// capital). The cap is deliberately NOT surfaced; it still bounds the buy at the
// command layer. Then EITHER a "buy it here now" imperative when a seller of that
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
		// Lead with on-hand only — the cap/headroom is deliberately NOT surfaced
		// (LLM-63): "room for N more" + a fill-to-cap price anchored the weak model
		// to fill straight to cap and drain its working capital. The cap still bounds
		// the buy at the command layer; headroom remains the co-present imperative's
		// "qty up to N" ceiling below.
		fmt.Fprintf(b, "- %s: %d on hand.", sanitizeInline(it.ItemLabel), it.CurrentQty)
		// LLM-63: recent sell-through is the demand fact the reseller sizes its
		// restock against, in place of the removed cap anchor. Silent at 0 (no sale
		// on record in the window) so a new or dormant good asserts no rate.
		if it.RecentSalesUnits > 0 {
			fmt.Fprintf(b, " You've sold about %d over the past week.", it.RecentSalesUnits)
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
