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
// the model never divides. Unlike "## Your trade" this is NOT gated on being
// at the workplace: a smith knows a nail is worth 1–2 coins whether stood at the
// forge or pitching it across a tavern table.

// TradeValueView is the content-gated "## What your wares fetch" section. A nil
// view (or empty Items with no RepairReserve) means render omits the section.
type TradeValueView struct {
	Items []TradeValueItem

	// RepairReserve, when non-nil, appends the earmarked-goods line (LLM-292): the
	// actor owns a business worn to the repair threshold and carries some of the
	// nails the mend takes, so those nails are spoken for — not wares. Live: Josiah
	// bought repair nails from the smith, then resold 5 of them before mending
	// (nothing in perception marked them earmarked, and the role-agnostic coin band
	// made any in-band offer look fair), landing back at 0 nails with the mend nag
	// still live. This line rides the wares cue because the sale it guards against
	// happens exactly where this cue renders: weighing a buyer's offer in company.
	RepairReserve *RepairReserveView

	// SellFirst marks the coin-poor-but-stock-rich keeper (LLM-294): purse below the
	// operator working-capital floor AND overstocked (merchantConserve). When set —
	// and the cue only builds inHuddle, so an audience is co-present — render appends a
	// nudge to offer the overstocked ware to someone here via the sell tool, the
	// actionable half of the conserve steer (the buy-cue softening is Tier 1, in
	// restock.go). SellFirstWare is the display label of the most-overstocked ware;
	// SellFirstCoins is the purse for the prose.
	SellFirst      bool
	SellFirstWare  string
	SellFirstCoins int
}

// RepairReserveView is the earmark behind the repair-reserve line: how many nails
// the standing mend takes, how many the actor carries, and which business needs
// them. Built only for an OWNER with an active mend obligation (the same
// WearableStallToMend + StallRepairable pair every repair cue keys on, so earmark
// and mend nag can't drift) who holds at least one nail.
type RepairReserveView struct {
	ItemLabel    string // display label of the repair material ("nails")
	BusinessName string // the worn business's display name; "" → generic noun
	Needed       int    // nails one repair consumes
	Held         int    // nails the actor currently carries (>= 1 when the view exists)
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

	// BulkUnit / ShopUnit carry the two figures on the wholesale-channel line
	// (WholesaleTo set), resolved observed-first (LLM-295): BulkUnit is what the
	// producer has actually been getting selling the good (its own sellerRecentSales),
	// ShopUnit is what the shops have actually been reselling it for (observedShopSales),
	// each falling back to the catalog seed (Low / High) only until real transactions
	// exist. *Observed flags record which source won so render phrases a lived rate
	// ("the shop has lately paid you about N") apart from a seed estimate ("the fair
	// bulk rate is about N"). Both 0 only if the good has no catalog price either.
	BulkUnit     int
	BulkObserved bool
	ShopUnit     int
	ShopObserved bool
}

// buildTradeValue builds the wares-worth view for an actor that has goods of its
// own trade AND is in company (inHuddle — the situation where a trade can occur),
// else nil. The repair-reserve earmark (LLM-292) shares the gate: an offer for
// earmarked nails also arrives in company, so the two surface together. Pure over
// the snapshot.
func buildTradeValue(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot, inHuddle bool) *TradeValueView {
	if snap == nil || actorSnap == nil {
		return nil
	}
	if !inHuddle {
		return nil // no one to trade with — nothing to value against
	}
	if actorSnap.RestockPolicy == nil || snap.Recipes == nil {
		// No own trade to value — but a worn-business owner still carries the
		// repair-reserve earmark (it keys on ownership + inventory, not the policy).
		if reserve := buildRepairReserve(snap, actorID, actorSnap); reserve != nil {
			return &TradeValueView{RepairReserve: reserve}
		}
		return nil
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
		// clear them — render draws it from WholesaleTo + BulkUnit/ShopUnit. Cost-of-
		// goods for a wholesale-priced input good (the mill's flour) is out of scope
		// here; the carrot case that motivated this has none.
		wholesaleTo := ""
		bulkUnit, bulkObserved := 0, false
		shopUnit, shopObserved := 0, false
		if sim.IsOwnProduce(snap.VillageObjects, actorSnap.WorkStructureID, actorSnap.RestockPolicy, item) {
			wholesaleTo = distLabel
			recentUnit, paidUnit = 0, 0
			costBatch, costQty, costFloor = 0, 0, false
			// LLM-295: both figures observed-first, catalog band as seed fallback.
			// Bulk = what the producer has actually been getting for the good (its own
			// realized sales — i.e. what the shop has paid it). Shop = what the shops
			// have actually been reselling it for (observedShopSales). The catalog
			// wholesale (lo) / retail (hi) stand in only until real trades exist.
			if units, coins := sellerRecentSales(snap, actorID, item, restockSalesWindow); units > 0 {
				bulkUnit, bulkObserved = (coins+units/2)/units, true
			} else {
				bulkUnit = lo
			}
			if units, coins := observedShopSales(snap, item, restockSalesWindow); units > 0 {
				shopUnit, shopObserved = (coins+units/2)/units, true
			} else {
				shopUnit = hi
			}
		}
		seen[item] = true
		items = append(items, TradeValueItem{
			ItemLabel:    itemDisplayLabel(snap, item),
			itemKind:     item,
			Low:          lo,
			High:         hi,
			RecentUnit:   recentUnit,
			PaidUnit:     paidUnit,
			CostBatch:    costBatch,
			CostQty:      costQty,
			CostFloor:    costFloor,
			WholesaleTo:  wholesaleTo,
			BulkUnit:     bulkUnit,
			BulkObserved: bulkObserved,
			ShopUnit:     shopUnit,
			ShopObserved: shopObserved,
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
	reserve := buildRepairReserve(snap, actorID, actorSnap)
	if len(items) == 0 && reserve == nil {
		return nil
	}
	// Stable, deterministic order: by label, then kind.
	sort.Slice(items, func(i, j int) bool {
		if items[i].ItemLabel != items[j].ItemLabel {
			return items[i].ItemLabel < items[j].ItemLabel
		}
		return items[i].itemKind < items[j].itemKind
	})
	view := &TradeValueView{Items: items, RepairReserve: reserve}
	// LLM-294 Tier 2: a coin-poor + overstocked keeper is nudged to offer its most-
	// overstocked ware to whoever is co-present (the cue is inHuddle-gated, so an
	// audience is present) via the sell tool. Shared determination with the Tier-1
	// buy-cue softening (buildRestocking).
	if c := merchantConserve(snap, actorID, actorSnap); c.Active {
		view.SellFirst = true
		view.SellFirstWare = c.OverstockedWare
		view.SellFirstCoins = c.Coins
	}
	return view
}

// buildRepairReserve builds the earmarked-nails view (LLM-292), or nil. Non-nil
// only when the actor OWNS a business worn to the repair threshold (hired menders
// are excluded — the nails at stake are the owner's own repair supplies, the same
// carve-out the buy-nails errand cues make) and carries at least one nail. Keys on
// the SAME WearableStallToMend + StallRepairable pair as the repair cues, so the
// earmark appears exactly while the mend nag is live and clears when the mend
// lands or the wear decays below threshold.
func buildRepairReserve(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot) *RepairReserveView {
	stall, hired := sim.WearableStallToMend(snap.VillageObjects, snap.LaborLedger, actorID)
	if stall == nil || hired {
		return nil
	}
	if !sim.StallRepairable(stall, snap.StallWearRepairThreshold, snap.StallWearDegradeThreshold) {
		return nil
	}
	needed := snap.StallNailsPerRepair
	if needed <= 0 {
		return nil // a repair costs no nails (feature off / misconfig) — nothing to earmark
	}
	held := actorSnap.Inventory[sim.NailItemKind]
	if held <= 0 {
		return nil // carrying none — the buy-nails errand cues own this state
	}
	return &RepairReserveView{
		ItemLabel:    itemDisplayLabel(snap, sim.NailItemKind),
		BusinessName: resolveDwellPinLabel(snap, stall.ID),
		Needed:       needed,
		Held:         held,
	}
}

// renderTradeValue writes the "## What your wares fetch" section. Content-gated:
// a nil/empty view writes nothing. One line per own ware — the coin range to anchor
// a price, plus the recent purchase cost (resold goods) and the recent realized sale
// price, each when on record. For a resold good the two together bracket the markup:
// what it cost, what it has fetched.
func renderTradeValue(b *strings.Builder, v *TradeValueView) {
	if v == nil || (len(v.Items) == 0 && v.RepairReserve == nil) {
		return
	}
	b.WriteString("## What your wares fetch\n")
	b.WriteString("What your wares fetch in coin — use it to set a fair price or weigh a trade:\n")
	for _, it := range v.Items {
		// LLM-291: a wholesale producer's own produce isn't sold retail — it goes
		// in bulk to the shop that stocks it. Draw the wholesale-channel line (who
		// buys it, what they pay, where to send other would-be buyers) instead of a
		// retail spread, so the producer doesn't negotiate a street sale the
		// PayWithItem wholesale gate then refuses. LLM-292 added the two ends of
		// the band stated by role — folk pay the HIGH end at the shop's shelf, the
		// fair bulk rate selling TO the shop is the LOW end — so the producer stops
		// taking a shelf price for a bulk sale (the live Elizabeth milk leg: the
		// role-agnostic "1 to 2 coins" band let her take 2+ from the store's bulk
		// buys). LLM-295: both figures are observed-first — a lived rate is phrased as
		// such ("the shop has lately paid you about N") and a catalog SEED as a
		// designer estimate ("the fair bulk rate is about N") — so the producer is
		// never told a hand-authored guess is a rate the market has set. Copy
		// constraint (Jeff): never the mechanic-role word "distributor" — the NPC is
		// told who stocks its goods, not what engine role that actor holds.
		if it.WholesaleTo != "" {
			to := sanitizeInline(it.WholesaleTo)
			label := sanitizeInline(it.ItemLabel)
			bulk := fmt.Sprintf("the fair bulk rate selling to the shop is about %s each", coinsPhrase(it.BulkUnit))
			if it.BulkObserved {
				bulk = fmt.Sprintf("the shop has lately paid you about %s each", coinsPhrase(it.BulkUnit))
			}
			if it.ShopUnit > it.BulkUnit {
				// Shop markup worth naming: folk-pay leads, the bulk rate follows with "but".
				folk := fmt.Sprintf("Folk pay about %s each in the shops", coinsPhrase(it.ShopUnit))
				if it.ShopObserved {
					folk = fmt.Sprintf("Folk have lately paid about %s each in the shops", coinsPhrase(it.ShopUnit))
				}
				fmt.Fprintf(b,
					"- %s: your own produce — it sells in bulk to %s, whose shop stocks it for the village, not to folk directly. %s, but %s. Send other buyers to %s.\n",
					label, to, folk, bulk, to,
				)
			} else {
				// No meaningful markup to show — just the bulk rate, capitalized for the
				// sentence start (both phrasings begin with the ASCII word "the").
				bulkLead := strings.ToUpper(bulk[:1]) + bulk[1:]
				fmt.Fprintf(b,
					"- %s: your own produce — it sells in bulk to %s, whose shop stocks it for the village, not to folk directly. %s. Send other buyers to %s.\n",
					label, to, bulkLead, to,
				)
			}
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
		// LLM-292: the resale cost clause carries its stake. The bare fact failed
		// live — Josiah's rendered line held "paid about 2… sold for about 1" and he
		// accepted a 1-coin offer on that very prompt; all data, no consequence. Per
		// the prompt-prose principle the clause says what the number is FOR. Resale
		// goods only (PaidUnit is only set for them) — the produced-goods makings
		// clause below deliberately stays a directive-free fact (LLM-227), because a
		// producer may run a loss-leader; a reseller selling below its own cost is
		// never that, just a leak.
		if it.PaidUnit > 0 {
			clauses += " — selling below what you paid loses you coin"
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
	// LLM-292: the earmarked repair nails, last — not a ware with a price but a
	// holding a buyer's offer must be weighed against. States the obligation and
	// the stake (Jeff's live case: Josiah resold 5 repair nails before mending —
	// 10 coins lost, shop still broken). When he carries MORE than the mend takes,
	// the keep-back split is stated so the surplus stays sellable.
	if r := v.RepairReserve; r != nil {
		name := sanitizeInline(r.BusinessName)
		if name == "" {
			name = "place of business"
		}
		label := sanitizeInline(r.ItemLabel)
		if r.Held <= r.Needed {
			fmt.Fprintf(b, "- %s: you need %d of these to mend your %s — the %d you carry are for that mend, not for sale. Selling them leaves your business broken.\n",
				label, r.Needed, name, r.Held)
		} else {
			fmt.Fprintf(b, "- %s: you need %d of these to mend your %s — keep %d back for the mend; only the %d beyond that are yours to sell.\n",
				label, r.Needed, name, r.Needed, r.Held-r.Needed)
		}
	}
	// LLM-294 Tier 2: coin-poor + overstocked → nudge a sale to someone co-present.
	// The cue builds only inHuddle, so there IS an audience to offer to; naming the
	// single most-overstocked ware gives the weak model one concrete thing to act on.
	// Points at the `sell` tool (scene_quote's model-facing name) — a general commerce
	// tool available with an audience — not a buy verb. Stake stated (short of coin).
	if v.SellFirst && v.SellFirstWare != "" {
		fmt.Fprintf(b, "You are short of coin (%d) and holding more %s than folk have been buying. If anyone here would take some, offer it to them now — use the sell tool to name your price.\n",
			v.SellFirstCoins, sanitizeInline(v.SellFirstWare))
	}
	b.WriteString("\n")
}

// distributorLabel names the village distributor for a wholesale producer's
// own-produce cue — the person to route would-be buyers to. The snapshot-side
// sibling of sim.DistributorSteerLabel (which reads live *Actor): prefer the
// distributor structure's owner display name, fall back to the object's own
// display name, then a generic phrase. Best-effort — degrades to "the village
// storekeeper" when none is configured, matching the engine steer's fallback so
// cue and reject stay in step (an in-world phrase on purpose: rendered prose
// never carries the mechanic-role word "distributor", LLM-292). First match wins
// (one distributor by convention).
func distributorLabel(snap *sim.Snapshot) string {
	if snap == nil {
		return "the village storekeeper"
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
	return "the village storekeeper"
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
