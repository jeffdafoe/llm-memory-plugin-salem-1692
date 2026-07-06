package perception

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// trade_value.go — LLM-125. The "## What your wares fetch" cue. In a barter
// (goods-for-goods) an NPC has no coin yardstick to value the wares changing
// hands, so it invents numbers: the live Ezekiel⇄John trade had a homeless smith
// guess his own nails at "1 coin" with nothing behind it, while the tavernkeeper
// drifted stew 4→3 coins tick to tick. The engine already HOLDS the value —
// item_recipe carries a wholesale and a retail price per unit — it just never
// rendered it into the trade moment.
//
// This cue surfaces, when the actor is in company (a huddle — where a trade can
// actually happen), the coin worth of the goods of THEIR OWN TRADE: the
// wholesale–retail spread as the bargaining range, plus what they have actually
// been getting for it of late (sellerRecentSales over the weekly window) when
// they have a coin sales history to draw on.
//
// Scoped to the actor's OWN wares ON PURPOSE — the goods it produces
// (RestockPolicy.ProduceEntries) AND the goods it resells (BuyEntries) — never the
// going rate of wares outside its trade: knowledge of others' prices stays "earned
// by patronage" (the PriceBook buyer-side path in recovery_options / consumable_
// vendors). For a resold good the cue also surfaces the actor's OWN recent purchase
// cost (buyerRecentPurchases) so a reseller can mark up off what it actually paid —
// the supply-side price anchor a pure reseller (empty ProduceEntries) otherwise
// lacked entirely (LLM-191). A PRODUCED good whose recipe has inputs gets the
// matching cost anchor from its ingredient side (LLM-226): each input priced by the
// actor's own purchase history, recipe wholesale as the fallback, spoken per-unit so
// the model never divides. Unlike "## Time to produce" this is NOT gated on being
// at the workplace: a smith knows a nail is worth 1–2 coins whether stood at the
// forge or pitching it across a tavern table.

// TradeValueView is the content-gated "## What your wares fetch" section. A nil
// view (or empty Items) means render omits the section.
type TradeValueView struct {
	Items []TradeValueItem
}

// TradeValueItem is one of the actor's own wares — produced or resold — with its
// coin worth. Low/High are the wholesale–retail spread (Low ≤ High); a good priced
// with a single number has Low == High. RecentUnit is the actor's recent realized
// per-unit SALE price over the weekly window, 0 when it has no coin sales of the good
// to draw on (a pure-barter good like nails). PaidUnit is the reseller's recent
// per-unit PURCHASE cost over the same window — the cost basis to mark up from — set
// only for resold (buy-restock) goods and 0 otherwise. Render omits each clause when
// its value is 0.
//
// CostBatch/CostQty carry the produce-side cost basis (LLM-226): the estimated
// ingredient cost of one recipe batch and the batch's output count, set only for a
// produced good whose recipe has inputs. Kept as the exact pair — not a pre-divided
// per-unit coin — so render can phrase the fraction honestly instead of rounding it
// away. CostFloor marks a cost sum missing an unpriceable input, which render
// qualifies as "at least". CostBatch 0 omits the clause.
type TradeValueItem struct {
	ItemLabel  string
	itemKind   sim.ItemKind // unexported sort tiebreak
	Low        int
	High       int
	RecentUnit int
	PaidUnit   int
	CostBatch  int
	CostQty    int
	CostFloor  bool

	// WholesaleTo, when non-empty, marks this as a wholesale producer's OWN
	// produce (sim.IsOwnProduce) and names the village distributor it sells to —
	// the sole legitimate buyer (LLM-223/252). Render then draws the wholesale-
	// channel line instead of a retail spread, so a producer isn't nudged to hawk
	// its produce to whoever it's standing with (the retail negotiation the
	// PayWithItem wholesale gate then refuses — live hud-9b23…, LLM-291). Low
	// carries the wholesale unit price (what the distributor pays) for that line.
	WholesaleTo string
}

// buildTradeValue builds the wares-worth view for an actor that has goods of its
// own trade AND is in company (inHuddle — the situation where a trade can occur),
// else nil. Pure over the snapshot.
func buildTradeValue(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot, inHuddle bool) *TradeValueView {
	if snap == nil || actorSnap == nil || actorSnap.RestockPolicy == nil || snap.Recipes == nil {
		return nil
	}
	if !inHuddle {
		return nil // no one to trade with — nothing to value against
	}
	// Resolved once for the scan: the label a wholesale producer's own-produce
	// lines route buyers to. Cheap (one object-map pass) and the cue is already
	// gated to inHuddle + has-own-wares, so it isn't a hot path.
	distLabel := distributorLabel(snap)
	var items []TradeValueItem
	seen := make(map[sim.ItemKind]bool)
	// valueGood appends the wares-worth line for one of the actor's own goods. isResale
	// marks a buy-restock good, for which the cost-basis (what it paid) clause applies;
	// a produced good leaves PaidUnit 0. seen dedupes a kind listed under both sources.
	valueGood := func(item sim.ItemKind, isResale bool) {
		if seen[item] {
			return
		}
		recipe := snap.Recipes[item]
		if recipe == nil {
			return
		}
		lo, hi := recipe.WholesalePrice, recipe.RetailPrice
		if lo > hi {
			lo, hi = hi, lo
		}
		if lo <= 0 {
			lo = hi // only one price configured (no wholesale floor) — collapse to it
		}
		if hi <= 0 {
			return // no price configured at all — nothing to surface
		}
		recentUnit := 0
		// sellerRecentSales is the actor's OWN realized coin sales of this good over
		// the weekly window — the lived "what I've been getting for it" signal, only
		// shown when there is history (a coin market for the good actually exists).
		if units, coins := sellerRecentSales(snap, actorID, item, restockSalesWindow); units > 0 {
			recentUnit = (coins + units/2) / units // round to nearest coin
		}
		// For a resold good, buyerRecentPurchases is the actor's OWN recent per-unit
		// cost — the anchor a reseller marks up from. Window-averaged and rounded like
		// the sale price; 0 (clause omitted) when it hasn't restocked the good of late.
		paidUnit := 0
		if isResale {
			if units, coins := buyerRecentPurchases(snap, actorID, item, restockSalesWindow); units > 0 {
				paidUnit = (coins + units/2) / units
			}
		}
		// For a produced good with real recipe inputs, estimate the cost of goods —
		// the produce-side sibling of the reseller cost-basis clause (LLM-226).
		// Each input is priced by the actor's own recent purchases (what it actually
		// pays for its milk), falling back to the input's catalog price (wholesale,
		// else retail) when it has no purchase history. An input priceable by
		// neither is left out of the sum and flags the total as a floor.
		costBatch, costQty, costFloor := 0, 0, false
		if !isResale && len(recipe.Inputs) > 0 {
			costQty = recipe.OutputQty
			if costQty <= 0 {
				costQty = 1
			}
			for _, in := range recipe.Inputs {
				unitCost := 0
				// History prices by CEILING division, unlike paidUnit's
				// nearest-rounding: paidUnit reports what was paid, this feeds a
				// don't-sell-below-this floor, where rounding a bulk-bought cheap
				// input (1 coin for 10 milk) down to a free ingredient silently
				// understates the cost the clause exists to reveal. Zero-coin
				// history is no price signal — fall through to the catalog.
				if units, coins := buyerRecentPurchases(snap, actorID, in.Item, restockSalesWindow); units > 0 && coins > 0 {
					unitCost = (coins + units - 1) / units
				} else if inRecipe := snap.Recipes[in.Item]; inRecipe != nil {
					unitCost = inRecipe.WholesalePrice
					if unitCost <= 0 {
						unitCost = inRecipe.RetailPrice
					}
				}
				if unitCost <= 0 {
					costFloor = true
					continue
				}
				costBatch += in.Qty * unitCost
			}
			if costBatch <= 0 {
				// No positive input cost was found — no useful cost signal, omit the clause.
				costQty, costFloor = 0, false
			}
		}
		// LLM-291: a wholesale producer's own produce sells only to the village
		// distributor, never retail — so this good gets the wholesale-channel line,
		// not a retail spread + "set a fair price" framing that nudges the producer
		// to hawk it to whoever it's standing with (the sale the PayWithItem
		// wholesale gate then refuses). Keyed on the SAME sim.IsOwnProduce the
		// Consume guard and eat-cue filter on, so cue and block agree; item-scoped,
		// so a wholesaler's RESOLD retail goods (isResale) are untouched. The retail
		// spread / recent-sale / cost clauses don't apply to the wholesale line, so
		// clear them — render draws it from WholesaleTo + Low (the wholesale unit
		// price) alone. Cost-of-goods for a wholesale-priced input good (the mill's
		// flour) is out of scope here; the carrot case that motivated this has none.
		wholesaleTo := ""
		if sim.IsOwnProduce(snap.VillageObjects, actorSnap.WorkStructureID, actorSnap.RestockPolicy, item) {
			wholesaleTo = distLabel
			recentUnit, paidUnit = 0, 0
			costBatch, costQty, costFloor = 0, 0, false
		}
		seen[item] = true
		items = append(items, TradeValueItem{
			ItemLabel:   itemDisplayLabel(snap, item),
			itemKind:    item,
			Low:         lo,
			High:        hi,
			RecentUnit:  recentUnit,
			PaidUnit:    paidUnit,
			CostBatch:   costBatch,
			CostQty:     costQty,
			CostFloor:   costFloor,
			WholesaleTo: wholesaleTo,
		})
	}
	// Produced goods first, so a kind somehow listed under both sources values as
	// own-production (no cost-basis clause); resold goods (LLM-191) then extend the cue
	// to the pure reseller, which produces nothing and otherwise got no anchor at all.
	for _, e := range actorSnap.RestockPolicy.ProduceEntries() {
		valueGood(e.Item, false)
	}
	for _, e := range actorSnap.RestockPolicy.BuyEntries() {
		valueGood(e.Item, true)
	}
	if len(items) == 0 {
		return nil
	}
	// Stable, deterministic order: by label, then kind.
	sort.Slice(items, func(i, j int) bool {
		if items[i].ItemLabel != items[j].ItemLabel {
			return items[i].ItemLabel < items[j].ItemLabel
		}
		return items[i].itemKind < items[j].itemKind
	})
	return &TradeValueView{Items: items}
}

// renderTradeValue writes the "## What your wares fetch" section. Content-gated:
// a nil/empty view writes nothing. One line per own ware — the coin range to anchor
// a price, plus the recent purchase cost (resold goods) and the recent realized sale
// price, each when on record. For a resold good the two together bracket the markup:
// what it cost, what it has fetched.
func renderTradeValue(b *strings.Builder, v *TradeValueView) {
	if v == nil || len(v.Items) == 0 {
		return
	}
	b.WriteString("## What your wares fetch\n")
	b.WriteString("What your wares fetch in coin — use it to set a fair price or weigh a trade:\n")
	for _, it := range v.Items {
		// LLM-291: a wholesale producer's own produce isn't sold retail — it goes
		// to the village distributor. Draw the wholesale-channel line (who buys it,
		// what they pay, where to send other would-be buyers) instead of a retail
		// spread, so the producer doesn't negotiate a street sale the PayWithItem
		// wholesale gate then refuses. Low is the wholesale unit price.
		if it.WholesaleTo != "" {
			to := sanitizeInline(it.WholesaleTo)
			fmt.Fprintf(b,
				"- %s: your own produce, sold wholesale to %s, the village distributor — not retail. The distributor pays about %s each; send other buyers to %s.\n",
				sanitizeInline(it.ItemLabel), to, coinsPhrase(it.Low), to,
			)
			continue
		}
		worth := fmt.Sprintf("%s each", coinsPhrase(it.High))
		if it.Low != it.High {
			worth = fmt.Sprintf("%d to %s each", it.Low, coinsPhrase(it.High))
		}
		// Cost basis first (what a resold good cost you), then the achieved sale price;
		// together they bracket the markup. Each clause is omitted when its value is 0.
		clauses := ""
		if it.PaidUnit > 0 {
			clauses += fmt.Sprintf("; you have lately paid about %s each for it", coinsPhrase(it.PaidUnit))
		}
		if it.RecentUnit > 0 {
			clauses += fmt.Sprintf("; of late you have sold for about %s each", coinsPhrase(it.RecentUnit))
		}
		// A produced good's cost of goods (LLM-226). All arithmetic is done HERE —
		// the model gets a per-unit phrase to compare against its price, never a
		// batch fraction to divide. Stated as a fact, not a pricing directive
		// (LLM-227): the NPC decides what to do with its cost; a command here
		// over-anchors weak models into rigid haggling and fights a deliberately
		// set loss-leader price.
		if it.CostBatch > 0 && it.CostQty > 0 {
			costPhrase := costEachPhrase(it.CostBatch, it.CostQty)
			if it.CostFloor {
				// The sum is missing an unpriceable input, so the true cost exceeds
				// it — state the known part as a whole-coin floor (ceiling division;
				// erring high is the safe direction for a cost estimate).
				ceilUnit := (it.CostBatch + it.CostQty - 1) / it.CostQty
				costPhrase = fmt.Sprintf("at least %s each", coinsPhrase(ceilUnit))
			}
			clauses += fmt.Sprintf("; the makings run you %s", costPhrase)
		}
		fmt.Fprintf(b, "- %s: %s%s.\n", sanitizeInline(it.ItemLabel), worth, clauses)
	}
	b.WriteString("\n")
}

// distributorLabel names the village distributor for a wholesale producer's
// own-produce cue — the person to route would-be buyers to. The snapshot-side
// sibling of sim.DistributorSteerLabel (which reads live *Actor): prefer the
// distributor structure's owner display name, fall back to the object's own
// display name, then a generic phrase. Best-effort — degrades to "the village
// distributor" when none is configured, matching the engine steer's fallback so
// cue and reject stay in step. First match wins (one distributor by convention).
func distributorLabel(snap *sim.Snapshot) string {
	if snap == nil {
		return "the village distributor"
	}
	for _, obj := range snap.VillageObjects {
		if !sim.IsDistributorStructure(obj) {
			continue
		}
		if obj.OwnerActorID != "" {
			if owner := snap.Actors[obj.OwnerActorID]; owner != nil && owner.DisplayName != "" {
				return owner.DisplayName
			}
		}
		if obj.DisplayName != "" {
			return obj.DisplayName
		}
	}
	return "the village distributor"
}

// coinsPhrase renders a coin count with singular/plural unit ("1 coin", "5 coins").
func coinsPhrase(n int) string {
	if n == 1 {
		return "1 coin"
	}
	return fmt.Sprintf("%d coins", n)
}

// costEachPhrase renders a batch cost as bucketed per-unit prose ("nearly 1 coin
// each", "a little over 2 coins each"). Weak models can compare a phrase against
// their asking price but cannot be trusted to divide batch/qty themselves, so the
// division happens here and the fraction is spoken, not rounded away — 8 coins per
// 10 bowls must NOT collapse to "about 1 coin each" (that erases the margin the
// clause exists to reveal). The half-and-up bucket rounds the phrase UPWARD
// ("nearly N+1") so approximation never understates cost — the failure mode this
// cue guards against is pricing below cost, not above it.
func costEachPhrase(batch, qty int) string {
	if batch <= 0 || qty <= 0 {
		return "" // defensive: render gates on both being positive already
	}
	whole, rem := batch/qty, batch%qty
	switch {
	case rem == 0:
		return fmt.Sprintf("about %s each", coinsPhrase(whole))
	case whole == 0 && rem*2 < qty:
		return "under half a coin each"
	case rem*2 < qty:
		return fmt.Sprintf("a little over %s each", coinsPhrase(whole))
	default:
		return fmt.Sprintf("nearly %s each", coinsPhrase(whole+1))
	}
}
