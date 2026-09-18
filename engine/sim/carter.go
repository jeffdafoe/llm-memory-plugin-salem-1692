package sim

import (
	"log"
	"sort"
	"strconv"
	"time"
)

// carter.go — the carter, the inside-supply visitor. Goods are the village's
// currency: the distributor pays for everything in stock because his purse is
// empty, so coin never reaches producers and the goods he hands over land where
// nobody uses them — RESIDUE, stock a holder has no restock line for. Live on
// 2026-09-17 the dairy held 25 of the village's 31 wheat while the miller had 3
// and a flour batch takes 5; 25 iron sat at the mill and the dairy while the
// smith had none; the mill held 74 ale and 40 cheese it can never sell. Nobody
// inside the village can play middleman: the distributor has no coin, and every
// other buyer's restock directory sees only first-hand producers and the
// distributor (LLM-252) behind the wholesale tier.
//
// The carter is an out-of-towner with his own bounded purse who arbitrages the
// village against itself and leaves. The engine computes two maps — residue
// (residueLots) and demand (carterDemands) — and plans a route through both
// (planCarterRoute): BUY legs at residue holders, paid in COIN at the going
// rate, and SELL legs at buyers who hold a line for the good, at the going rate
// plus one. A buy leg is MECHANICAL — the carter is a visitor with no LLM, and
// residue is by definition stock the holder had no plan for, so the engine
// settles the buy on arrival (settleCarterBuyLeg) and writes it to the action
// log as a purchase. A sell leg stays villager-driven through the peddler's
// keeper cue and pay_with_item: spending coin is the keeper's decision.
// Whatever finds no buyer leaves town with him.
//
// Coin only moves INTO the village on the buy side, so the planner is strict
// about it: a matched buy leg is planned only when the buyer on the other end
// can pay coin for the lot — otherwise every failed sell leg is a coin injection
// in disguise. The one exception is a keeper with a STANDING SHORTAGE
// (WorldEnvironment.InputShortages): the carter takes that run whether or not
// the keeper has coin, since the shortage peddler would have brought the same
// goods from outside and taken barter for them. Pure export — buying residue
// with no buyer lined up and carrying it away — is gated on the coin band:
// only while resident coin is under visitor_coin_band_high.
//
// The carter SUPERSEDES the shortage peddler when the village already holds the
// goods: dispatchVisitorSpawn asks dueCarter first, and a route that serves a
// standing shortage takes the spawn ahead of the peddler. The peddler is the
// outside-supply mechanism and stays — and it never waits on a carter who
// cannot make the run this tick (the holder away or abed, nothing in town
// covering the lack), or a keeper's work would stand still while the residue
// sat on a shut shelf. One visitor, one source, one settlement — no mixed run.
//
// Everything downstream — arrival target, `## Your rounds`, the keeper's
// `## A trader's come to deal`, errand confinement, dusk wind-down, daybreak
// departure — is the merchant-visitor machinery. The route is a list of legs on
// the errand (TradeErrand.Legs); the CURRENT leg is projected onto the errand's
// Good / Counterparty / Keeper / ShipmentQty / Delivered (projectCarterLeg) so
// every gate that reads those fields works per leg unchanged.

const (
	// DefaultCarterDays is the cooldown between residue-triggered carter visits,
	// in days. 0 is the off-switch (the farm-upkeep convention). A standing
	// shortage the village can cover ignores the cooldown — the shortage's own
	// per-entry cooldown (InputShortage.LastPeddlerAt) governs repeats.
	DefaultCarterDays = 3
	// DefaultCarterResidueFloorCoins is the least a residue lot may be worth at
	// the going rate for the carter to stop for it — he never tours the village
	// for one homespun.
	DefaultCarterResidueFloorCoins = 4
	// DefaultCarterResidueSpawnCoins is the total residue value, at the going
	// rate, that brings a carter on his own with no buyer lined up (a pure
	// export run). Only reachable while the coin band is open.
	DefaultCarterResidueSpawnCoins = 40
	// DefaultCarterPurseMax bounds what the carter arrives carrying — and so how
	// much coin one visit can put into the village.
	DefaultCarterPurseMax = 100

	// carterSellMarkup is what he asks over the going rate on a sell leg: one
	// coin a unit, his margin and a small drain in the direction the band wants.
	carterSellMarkup = 1
	// carterTravelReserve is the coin he keeps back for a room and a supper —
	// the ordinary traveler's purse, not spent on residue.
	carterTravelReserve = 20
	// carterLarderKeep is how many units of a consumable — a good that eases a
	// need, a keeper's larder or his hearth fuel — the carter leaves a holder
	// even when it carries no restock line. Everything above it is residue: the
	// mill's 74 ale is not a larder.
	carterLarderKeep = 10
	// carterPriceWindow bounds how far back the village-wide going rate reads
	// for a good with no recipe (an import — iron, salt, thread, cloth).
	carterPriceWindow = 30 * 24 * time.Hour

	// CarterArchetype is the carter's persona label ("Asa Larkin the carter").
	CarterArchetype = "carter"
)

// CarterLeg is one stop on the carter's route: a BUY of residue from its holder
// or a SELL to a keeper who holds a line for the good. Counterparty is the
// holder's / buyer's work structure (navigation keys on it); Keeper is the
// person, by id (the settle and the cues key on him). Unit is the going rate a
// unit was priced at when the route was planned — what he pays on a buy leg,
// and one carterSellMarkup under what he asks on a sell leg.
type CarterLeg struct {
	Buy          bool
	Good         ItemKind
	Qty          int
	Counterparty StructureID
	Keeper       ActorID
	Unit         int
	Done         bool
}

// Price is the coin for the whole lot: paid on a buy leg, asked on a sell leg.
func (l CarterLeg) Price() int {
	if l.Buy {
		return l.Qty * l.Unit
	}
	return l.Qty * (l.Unit + carterSellMarkup)
}

// CarterLeg returns the carter's current leg — the first not yet done — or nil
// when he is not a carter or every leg is done. Read by perception for the
// rounds and keeper cues; the sim projects the same leg onto the errand fields.
func (tr *TradeErrand) CarterLeg() *CarterLeg {
	if tr == nil || !tr.Carter {
		return nil
	}
	for i := range tr.Legs {
		if !tr.Legs[i].Done {
			return &tr.Legs[i]
		}
	}
	return nil
}

// CarterBuying reports whether the carter's current leg is a buy — the
// mechanical leg, where his commerce tools stay withheld and the holder needs
// nothing but a word.
func (tr *TradeErrand) CarterBuying() bool {
	leg := tr.CarterLeg()
	return leg != nil && leg.Buy
}

// projectCarterLeg copies the carter's current leg onto the errand fields every
// downstream gate reads — Good, Counterparty, Keeper, and for a sell leg the
// shipment baseline the LLM-553 settle measures against. A sell leg for a good
// he turned out not to hold (the residue was gone when he got there) is marked
// done and skipped. With no leg left the errand is Settled and the wind-down
// prose takes over. Idempotent.
func projectCarterLeg(tr *TradeErrand, carrying map[ItemKind]int) {
	if tr == nil || !tr.Carter {
		return
	}
	for {
		leg := tr.CarterLeg()
		if leg == nil {
			tr.Settled = true
			return
		}
		if leg.Qty <= 0 || leg.Unit <= 0 || leg.Good == "" || leg.Counterparty == "" || leg.Keeper == "" {
			// Never planned — an out-of-band leg, buy or sell. Skipped here, the one
			// place every route passes through, so it is neither walked to nor
			// advertised at a zero price (code_review).
			leg.Done = true
			continue
		}
		if !leg.Buy {
			if held := carrying[leg.Good]; held <= 0 {
				leg.Done = true
				continue
			} else if held < leg.Qty {
				leg.Qty = held
			}
		}
		tr.Good = leg.Good
		tr.Counterparty = leg.Counterparty
		tr.Keeper = leg.Keeper
		tr.Delivered = 0
		tr.ShipmentQty = 0
		if !leg.Buy {
			tr.ShipmentQty = leg.Qty
		}
		return
	}
}

// carterHolder reports whether a is a resident keeper whose shelves the carter
// reads for residue: the sweep's own keeper notion (shortageKeeper — a resident
// NPC with a post, a policy and a claim on the post), at a structure-backed
// post he can walk to, and never the distributor, whose shelf IS the market.
func carterHolder(w *World, a *Actor) bool {
	if !shortageKeeper(w, a) {
		return false
	}
	if ActorIsDistributor(w.VillageObjects, a.WorkStructureID) {
		return false
	}
	return structureIDValid(w, VillageObjectID(a.WorkStructureID))
}

// carterUnitPrice is the going rate for one unit of item, in whole coins: the
// recipe's wholesale price when the village makes it, else what the village has
// actually paid for it recently across every seller (an import — iron, salt,
// thread, cloth — has no recipe, only a price book). 0 when nothing prices it,
// and an unpriced good is never residue: silence, never a guessed number.
func carterUnitPrice(w *World, item ItemKind, now time.Time) int {
	if r := w.Recipes[item]; r != nil && r.WholesalePrice > 0 {
		return r.WholesalePrice
	}
	if w.PriceBook == nil {
		return 0
	}
	cutoff := now.Add(-carterPriceWindow)
	var units, coins int64
	for key, buf := range w.PriceBook {
		if key.Item != item || buf == nil || buf.Len() == 0 {
			continue
		}
		for _, obs := range buf.Snapshot() {
			if obs.Amount <= 0 || obs.At.Before(cutoff) {
				continue // barter legs carry no price; old legs are not the going rate
			}
			units += ObservationUnits(obs)
			coins += int64(obs.Amount)
		}
	}
	if units <= 0 || coins <= 0 {
		return 0
	}
	return int((coins + units/2) / units)
}

// residueLot is one holder's spare stock of a good it has no line for.
type residueLot struct {
	holder *Actor
	item   ItemKind
	qty    int
	unit   int
}

func (l residueLot) value() int { return l.qty * l.unit }

// residueSpare is how many units of item the holder could part with as
// residue: nothing when its policy manages the kind (a produce, buy or forage
// line — its wares or its makings), nothing of a garment it wears or a service,
// and for a consumable everything above the larder keep-back. Read live at the
// settle as well as at planning, so a lot that moved in between sells what is
// there, not what was.
func residueSpare(w *World, holder *Actor, item ItemKind) int {
	if holder == nil || holder.RestockPolicy == nil || holder.RestockPolicy.Manages(item) {
		return 0
	}
	def := w.ItemKinds[item]
	if !KindBarterable(def) {
		return 0
	}
	spare := SpareQty(holder.Inventory, SpokenFor(w.ItemKinds, w.Recipes, LiveBarterHolder(w, holder)), item)
	if kindFoodOrDrink(def) {
		spare -= carterLarderKeep // the makings claim's own food test: raw meat is category food with no Satisfies
	}
	if spare < 0 {
		return 0
	}
	return spare
}

// residueLots lists every residue lot in the village worth at least floor coins
// at the going rate, sorted by (holder, item) so the route is deterministic.
func residueLots(w *World, floor int, now time.Time) []residueLot {
	var lots []residueLot
	for _, a := range w.Actors {
		if !carterHolder(w, a) {
			continue
		}
		for item := range a.Inventory {
			qty := residueSpare(w, a, item)
			if qty <= 0 {
				continue
			}
			unit := carterUnitPrice(w, item, now)
			if unit <= 0 || qty*unit < floor {
				continue
			}
			lots = append(lots, residueLot{holder: a, item: item, qty: qty, unit: unit})
		}
	}
	sort.Slice(lots, func(i, j int) bool {
		if lots[i].holder.ID != lots[j].holder.ID {
			return lots[i].holder.ID < lots[j].holder.ID
		}
		return lots[i].item < lots[j].item
	})
	return lots
}

// carterDemand is one keeper's want the carter could fill: a buy line (explicit
// or derived from a recipe input) below its reorder point, with room under its
// cap. shortage marks a standing entry in the shortage record for it.
type carterDemand struct {
	buyer    *Actor
	item     ItemKind
	room     int
	input    bool // a production input (has a batch floor), ahead of larder and resale lines
	shortage bool
}

// carterDemands lists every want in the village, standing shortages first, then
// production inputs, then the rest, and by (buyer, item) within a tier.
func carterDemands(w *World) []carterDemand {
	standing := map[shortageKey]struct{}{}
	for _, s := range w.Environment.InputShortages {
		standing[shortageKey{keeper: s.KeeperID, item: s.Item}] = struct{}{}
	}
	var out []carterDemand
	for _, a := range w.Actors {
		if !shortageKeeper(w, a) || !structureIDValid(w, VillageObjectID(a.WorkStructureID)) {
			continue
		}
		floors := ReorderFloors(w.Recipes, a.RestockPolicy)
		for _, e := range EffectiveBuyEntries(w.Recipes, a.RestockPolicy) {
			if e.Item == "" || !KindBarterable(w.ItemKinds[e.Item]) {
				continue
			}
			held := a.Inventory[e.Item]
			floor := floors[e.Item]
			if !RestockReorderThresholdMet(held, e.Cap(), w.Settings.RestockReorderPct, floor) {
				continue
			}
			room := e.Cap() - held
			if e.Cap() <= 0 {
				room = floor - held // a capless input reorders on batch coverage alone
			}
			if room <= 0 {
				continue
			}
			_, short := standing[shortageKey{keeper: a.ID, item: e.Item}]
			out = append(out, carterDemand{buyer: a, item: e.Item, room: room, input: floor > 0, shortage: short})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].shortage != out[j].shortage {
			return out[i].shortage
		}
		if out[i].input != out[j].input {
			return out[i].input
		}
		if out[i].buyer.ID != out[j].buyer.ID {
			return out[i].buyer.ID < out[j].buyer.ID
		}
		return out[i].item < out[j].item
	})
	return out
}

// carterMatch is one want the route serves: the buy legs that fill it and the
// one sell leg that lands it.
type carterMatch struct {
	buyer ActorID
	buys  []CarterLeg
	sell  CarterLeg
}

// carterLegsByBuyer orders the matched legs so the carter calls on each buyer
// once: buyers in the order the planner first served them (priority order), and
// for each, every buy for him and then his sells back to back at his post.
func carterLegsByBuyer(matched []carterMatch) []CarterLeg {
	var order []ActorID
	byBuyer := map[ActorID][]carterMatch{}
	for _, m := range matched {
		if _, seen := byBuyer[m.buyer]; !seen {
			order = append(order, m.buyer)
		}
		byBuyer[m.buyer] = append(byBuyer[m.buyer], m)
	}
	var legs []CarterLeg
	for _, id := range order {
		for _, m := range byBuyer[id] {
			legs = append(legs, m.buys...)
		}
		for _, m := range byBuyer[id] {
			legs = append(legs, m.sell)
		}
	}
	return legs
}

// planCarterRoute lays out the carter's legs against the village as it stands:
// for each want, in priority order, buy legs at the residue lots of that good
// and ONE sell leg to the buyer for the sum, sized to the lots, the buyer's
// room, what the buyer can pay in coin (unless his shortage stands), and the
// purse; the matched legs are then ordered so he calls on each buyer once
// (carterLegsByBuyer); then, while the band is open, buy legs for whatever
// residue is left, by value, as pure export. Empty when nothing is worth the
// trip. Pure over the world.
func planCarterRoute(w *World, purse int, floor int, bandOpen bool, now time.Time) []CarterLeg {
	lots := residueLots(w, floor, now)
	if len(lots) == 0 {
		return nil
	}
	budget := purse - carterTravelReserve
	var matched []carterMatch
	// What each buyer's purse still covers once the wants planned ahead of this
	// one are paid for: five wants each checked against the whole purse planned
	// John Ellis 89 coin of sell legs against 32.
	coinLeft := map[ActorID]int{}
	for _, d := range carterDemands(w) {
		if budget <= 0 {
			break
		}
		if _, seen := coinLeft[d.buyer.ID]; !seen {
			coinLeft[d.buyer.ID] = d.buyer.Coins
		}
		m := carterMatch{buyer: d.buyer.ID}
		sold := 0
		for i := range lots {
			lot := &lots[i]
			if lot.item != d.item || lot.qty <= 0 || lot.holder.ID == d.buyer.ID || d.room <= 0 {
				continue
			}
			take := min(lot.qty, d.room)
			if !d.shortage {
				// The buyer must be able to pay coin for what he is brought — a leg
				// he can only barter for would put coin into the holder's purse and
				// goods, not coin, into the carter's. A standing shortage is the
				// exception: the peddler would have taken his goods.
				take = min(take, coinLeft[d.buyer.ID]/(lot.unit+carterSellMarkup))
			}
			take = min(take, budget/lot.unit)
			if take <= 0 || take*lot.unit < floor {
				continue
			}
			m.buys = append(m.buys, CarterLeg{Buy: true, Good: lot.item, Qty: take, Counterparty: lot.holder.WorkStructureID, Keeper: lot.holder.ID, Unit: lot.unit})
			// One sell leg for the want, however many shelves filled it: a second
			// call on the same keeper minutes after the first reads to the carter
			// as business already done, and he walks off with the goods.
			// Priced off the first lot; the going rate is per good, so every lot
			// of a want shares it.
			if sold == 0 {
				m.sell = CarterLeg{Good: lot.item, Counterparty: d.buyer.WorkStructureID, Keeper: d.buyer.ID, Unit: lot.unit}
			}
			m.sell.Qty += take
			sold += take
			// A shortage want is asked of his purse too — he may pay it in goods,
			// but what he does pay in coin is not there for the next want.
			coinLeft[d.buyer.ID] = max(0, coinLeft[d.buyer.ID]-take*(lot.unit+carterSellMarkup))
			budget -= take * lot.unit
			lot.qty -= take
			d.room -= take
		}
		if sold > 0 {
			matched = append(matched, m)
		}
	}
	legs := carterLegsByBuyer(matched)
	if bandOpen {
		rest := make([]residueLot, 0, len(lots))
		for _, lot := range lots {
			if lot.qty > 0 {
				rest = append(rest, lot)
			}
		}
		sort.SliceStable(rest, func(i, j int) bool { return rest[i].value() > rest[j].value() })
		for _, lot := range rest {
			if budget <= 0 {
				break
			}
			take := min(lot.qty, budget/lot.unit)
			if take <= 0 || take*lot.unit < floor {
				continue
			}
			legs = append(legs, CarterLeg{Buy: true, Good: lot.item, Qty: take, Counterparty: lot.holder.WorkStructureID, Keeper: lot.holder.ID, Unit: lot.unit})
			budget -= take * lot.unit
		}
	}
	return legs
}

// carterRouteBuyTotal is the coin the route's buy legs come to — what the
// carter must arrive carrying, beside his travel reserve.
func carterRouteBuyTotal(legs []CarterLeg) int {
	total := 0
	for _, l := range legs {
		if l.Buy {
			total += l.Price()
		}
	}
	return total
}

// carterRouteHasShortage reports whether any sell leg serves a keeper with a
// standing shortage of that good — the run that ignores the cooldown.
func carterRouteHasShortage(w *World, legs []CarterLeg) bool {
	for _, l := range legs {
		if l.Buy {
			continue
		}
		for _, s := range w.Environment.InputShortages {
			if s.KeeperID == l.Keeper && s.Item == l.Good {
				return true
			}
		}
	}
	return false
}

// carterSettings reads the carter knobs with their defaults applied: days is 0
// only when the operator set it so (the off-switch); the others re-default at
// zero.
func carterSettings(w *World) (days, floor, spawnCoins, purseMax int) {
	days = w.Settings.CarterDays
	floor = w.Settings.CarterResidueFloorCoins
	if floor <= 0 {
		floor = DefaultCarterResidueFloorCoins
	}
	spawnCoins = w.Settings.CarterResidueSpawnCoins
	if spawnCoins <= 0 {
		spawnCoins = DefaultCarterResidueSpawnCoins
	}
	purseMax = w.Settings.CarterPurseMax
	if purseMax <= 0 {
		purseMax = DefaultCarterPurseMax
	}
	return days, floor, spawnCoins, purseMax
}

// carterBandOpen reports whether pure export is allowed: the coin band is
// configured and resident coin is under its high mark. Above band the village
// has too much coin already, and the fix for too many goods must not add to it.
// An unconfigured band (high 0) reads closed: with no judgment of the money
// supply to lean on, the carter does only matched legs, which move coin
// between villagers rather than into the village.
func carterBandOpen(w *World) bool {
	high := w.Settings.VisitorCoinBandHigh
	return high > 0 && residentCoinOnMap(w) < high
}

// dueCarter decides whether a carter should come right now and, if so, plans
// his route and purse. He comes when the route holds a matched leg (a buyer
// with a line for a residue good) and the cooldown has passed — or a standing
// shortage he can cover, cooldown or not — or, with no buyer at all, when the
// residue on offer is worth the spawn threshold and the band is open. The first
// leg's keeper must be at his post so the carter has somewhere to go on
// arrival (the bindShortageErrand posture; a shut shop is tried again next
// tick). Runs on the world goroutine.
func dueCarter(w *World, now time.Time) (legs []CarterLeg, purse int, ok bool) {
	days, floor, spawnCoins, purseMax := carterSettings(w)
	if days <= 0 {
		return nil, 0, false
	}
	legs = planCarterRoute(w, purseMax, floor, carterBandOpen(w), now)
	if len(legs) == 0 {
		return nil, 0, false
	}
	matched := false
	for _, l := range legs {
		if !l.Buy {
			matched = true
			break
		}
	}
	cooldown := time.Duration(days) * 24 * time.Hour
	onCooldown := !w.Environment.LastCarterAt.IsZero() && now.Sub(w.Environment.LastCarterAt) < cooldown
	switch {
	case carterRouteHasShortage(w, legs):
		// a keeper's work has stood still for days and the goods are in town
	case onCooldown:
		return nil, 0, false
	case matched:
	case carterRouteBuyTotal(legs) < spawnCoins:
		return nil, 0, false
	}
	first := legs[0]
	if !keeperPresentAt(w, first.Counterparty) || !actorTendsPost(w, w.Actors[first.Keeper], first.Counterparty) {
		return nil, 0, false
	}
	purse = carterRouteBuyTotal(legs) + carterTravelReserve
	if purse > purseMax {
		purse = purseMax
	}
	return legs, purse, true
}

// actorTendsPost reports whether a is at structureID now and awake — the
// per-person form of keeperPresentAt, for the one holder or buyer a leg names.
func actorTendsPost(w *World, a *Actor, structureID StructureID) bool {
	if a == nil || a.State == StateSleeping {
		return false
	}
	return conversationalScopeStructure(w, a) == structureID
}

// stampCarterShortages marks the shortage entries a committed carter route
// serves with LastPeddlerAt, so the peddler cooldown covers the carter's run
// too — one visitor per lack per cooldown, whichever kind came.
func stampCarterShortages(w *World, legs []CarterLeg, now time.Time) {
	for i := range w.Environment.InputShortages {
		s := &w.Environment.InputShortages[i]
		for _, l := range legs {
			if !l.Buy && l.Keeper == s.KeeperID && l.Good == s.Item {
				s.LastPeddlerAt = now
			}
		}
	}
}

// advanceCarter moves a carter along his route from the visitor pacing pass:
// a buy leg settles when he stands with the holder at the holder's post; a sell
// leg is done when the shipment he projected has substantially landed (the
// LLM-553 settle), and the next leg is projected. Called once per pacing tick
// on the world goroutine; a no-op for anyone but a carter with legs left.
func advanceCarter(w *World, actor *Actor, now time.Time) {
	tr := ActorTradeErrand(actor)
	if tr == nil || !tr.Carter || tr.Settled {
		return
	}
	leg := tr.CarterLeg()
	if leg == nil {
		projectCarterLeg(tr, actor.Inventory)
		return
	}
	switch {
	case leg.Buy:
		if actor.MoveIntent != nil || conversationalScopeStructure(w, actor) != leg.Counterparty {
			return // not there yet
		}
		holder := w.Actors[leg.Keeper]
		if !actorTendsPost(w, holder, leg.Counterparty) {
			return // the holder is away or abed — wait, the pacing pass tries again
		}
		settleCarterBuyLeg(w, actor, holder, leg, now)
		leg.Done = true
	case sellErrandDelivered(tr.Delivered, tr.ShipmentQty):
		leg.Done = true
	default:
		return
	}
	projectCarterLeg(tr, actor.Inventory)
}

// settleCarterBuyLeg is the mechanical buy: the carter counts out coin for the
// residue and the goods change hands, at the leg's unit rate, for as much of
// the lot as is still spare and as his purse covers. Written to the action log
// as a purchase on both the live tally and the durable row (the co-located
// write the coin record requires, LLM-572/615), with `carter_leg` as the goods
// marker the boot seed classifies by. Nothing to settle — the residue is gone,
// or the purse is short — logs and leaves the leg to be marked done by the
// caller: a stop that found nothing is still over.
func settleCarterBuyLeg(w *World, carter, holder *Actor, leg *CarterLeg, now time.Time) {
	if leg == nil || leg.Qty <= 0 || leg.Unit <= 0 {
		return // never planned; an out-of-band leg must not move goods for free (code_review)
	}
	// SpendableCoins, not the wallet (LLM-644): what he may put into a purchase is
	// what he arrived with, so a sell leg's takings can never widen a later buy.
	qty := min(leg.Qty, residueSpare(w, holder, leg.Good))
	if carter.SpendableCoins()/leg.Unit < qty {
		qty = carter.SpendableCoins() / leg.Unit
	}
	if qty <= 0 {
		log.Printf("sim/carter: %s found no %s to buy at %s (spare %d, spendable %d)",
			carter.DisplayName, leg.Good, holder.DisplayName, residueSpare(w, holder, leg.Good), carter.SpendableCoins())
		return
	}
	price := qty * leg.Unit
	if err := transferItem(w, holder, carter, leg.Good, qty); err != nil {
		log.Printf("sim/carter: %s buying %d %s from %s: %v", carter.DisplayName, qty, leg.Good, holder.DisplayName, err)
		return
	}
	carter.Coins -= price
	drawVisitorSpend(carter, price)
	holder.Coins += price
	leg.Qty = qty
	forText := formatCarterLot(leg.Good, qty)
	entry := ActionLogEntry{
		ActorID:          carter.ID,
		OccurredAt:       now,
		ActionType:       ActionTypePaid,
		Text:             forText,
		HuddleID:         carter.CurrentHuddleID,
		CounterpartyName: holder.DisplayName,
		Amount:           price,
	}
	if _, err := AppendActionLogEntry(entry).Fn(w); err != nil {
		log.Printf("sim/carter: append action log for %s: %v", carter.DisplayName, err)
	}
	w.RecordCoinPaid(carter.ID, holder.ID, price, now, CoinPaymentForGoods)
	w.AppendActionLogDurable(DurableActionLogRow{
		ActorID:    carter.ID,
		OccurredAt: now,
		ActionType: ActionTypePaid,
		Payload: map[string]any{
			"recipient":          holder.DisplayName,
			"recipient_actor_id": string(holder.ID),
			"amount":             price,
			"for":                forText,
			// carter_leg is this path's goods marker, the counterpart of ledger_id
			// on a pay-with-item settlement and lodging_grant on a rebook: it names
			// the residue the coin bought, so the boot seed classifies the row from
			// the settlement the engine made and never from the `for` text.
			"carter_leg": string(leg.Good),
		},
		SpeakerName: carter.DisplayName,
		HuddleID:    carter.CurrentHuddleID,
		Source:      "engine",
	})
	log.Printf("sim/carter: %s paid %s %d coin for %s", carter.DisplayName, holder.DisplayName, price, forText)
}

// describeCarterRoute is the log form of a route: "buy 25 wheat at Ellis Farm
// (Elizabeth Ellis) for 25; sell 25 wheat at Mill (Joseph Scott) for 50".
func describeCarterRoute(w *World, legs []CarterLeg) string {
	out := ""
	for i, l := range legs {
		if i > 0 {
			out += "; "
		}
		verb := "sell"
		if l.Buy {
			verb = "buy"
		}
		who := string(l.Keeper)
		if a := w.Actors[l.Keeper]; a != nil && a.DisplayName != "" {
			who = a.DisplayName
		}
		where := string(l.Counterparty)
		if st := w.Structures[l.Counterparty]; st != nil && st.DisplayName != "" {
			where = st.DisplayName
		}
		out += verb + " " + strconv.Itoa(l.Qty) + " " + string(l.Good) + " at " + where + " (" + who + ") for " + strconv.Itoa(l.Price())
	}
	return out
}

// formatCarterLot names a lot the way a pay row does: "25x wheat" — the kind
// key, as handlePayResolvedActionLog writes it.
func formatCarterLot(item ItemKind, qty int) string {
	return strconv.Itoa(qty) + "x " + string(item)
}
