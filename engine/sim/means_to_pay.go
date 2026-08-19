package sim

// means_to_pay.go — LLM-406. The shared "can this actor pay AT ALL" predicate.
//
// pay_with_item settles in coins, goods (pay_items), or both, and the SELLER
// adjudicates the bundle. So an actor holding goods is never a payment dead-end,
// whatever its purse says. A coins-only affordability test asks the wrong question —
// "can you pay in coin?" rather than "can you pay?" — and silently erases a goods-rich,
// coin-poor buyer from its own supply chain.
//
// That erasure was the live Josiah Thorne deadlock (2026-07-14): the village's
// only distributor stood at his post with 3 coins, a pack of goods, and NO restock
// cue and no restock warrant at all. The coins-only gate dropped every supplier
// whose remembered price exceeded his purse; with every supplier dropped, every
// low item was omitted; with every item omitted, the section rendered nil. He could
// not earn (empty shelves, nothing to sell) and so could never climb back over a
// supplier's price — an absorbing state, and, because the wholesale tier makes the
// distributor the sole legal channel between the farms and the village, one that
// jammed the whole retail food chain behind him.
//
// LLM-222 established the coin-OR-goods gate for the consumer buy cue
// (perception/satiation.go, gatherSatiationVendors); LLM-406 brings the restock cue
// and its warrant onto the same footing. This is the one definition all of them
// read — perception.holdsBarterableGoods on the snapshot side, buyerCanTransact
// (restock_tick.go) on the live-World warrant side — so the cue and the warrant can
// never drift on what "has something to pay with" means.

// KindBarterable reports whether a catalog kind could go up in a pay_with_item /
// offer_trade bundle AT ALL — the same class resolvePayItems enforces at intake
// (LLM-445): a "service" is not a transferable good (its delivery is bound to the
// seller's establishment, ZBBS-HOME-424), and an EatHereOnly consumable (porridge,
// stew, a poured drink) is eaten where it's served and can't be carried off as
// payment. Every means-to-pay gate reads this so a cue never advertises a barter
// the resolver rejects. A nil def (a held kind absent from the catalog — sparse
// test fixtures, a freshly-minted discovery kind) degrades PERMISSIVE, mirroring
// EatHereOnly / itemDispositionClass: the resolver, not the cue, is the backstop
// for those.
func KindBarterable(def *ItemKindDef) bool {
	if def == nil {
		return true
	}
	return !def.HasCapability("service") && !def.EatHereOnly()
}

// kindSatisfiesHunger reports whether the kind is food — it carries a hunger
// entry in Satisfies. Used by the visitor persona derivation (LLM-503): a
// traveler whose buy errand binds a food good is a "provisioner" stocking for
// the road, not a "<good>-buyer". Nil def → false (an uncataloged kind can't
// claim to feed anyone).
func kindSatisfiesHunger(def *ItemKindDef) bool {
	if def == nil {
		return false
	}
	for _, s := range def.Satisfies {
		if s.Attribute == "hunger" {
			return true
		}
	}
	return false
}

// BarterHolder is what the spoken-for reservation (SpokenFor) knows about a
// payer: its restock policy, its pack, and whether it is a clothing/stock
// HOLDER rather than a user of what it carries. Built from the live *Actor
// (LiveBarterHolder) or the *ActorSnapshot (SnapshotBarterHolder) so the cue,
// the carry-line annotation and the pay_with_item intake gate all resolve the
// one answer from the same facts — the shared-predicate posture wearsGarments
// takes for the same reason.
type BarterHolder struct {
	Policy    *RestockPolicy
	Inventory map[ItemKind]int
	// Stockholder marks the two roles whose held goods are SALE STOCK, not
	// things they use — the distributor and a visitor on a trade errand (the
	// factor). Exactly the actorWearsGarments exclusion (garment_wear.go): their
	// garments don't wear because they aren't worn, and by the same token their
	// buy lines are wares to trade, not makings to keep.
	Stockholder bool
}

// LiveBarterHolder is the BarterHolder view of a live actor.
func LiveBarterHolder(w *World, a *Actor) BarterHolder {
	if a == nil {
		return BarterHolder{}
	}
	return BarterHolder{
		Policy:      a.RestockPolicy,
		Inventory:   a.Inventory,
		Stockholder: ActorHasTradeErrand(a) || ActorIsDistributor(w.VillageObjects, a.WorkStructureID),
	}
}

// SnapshotBarterHolder is the BarterHolder view of an actor snapshot — the same
// two role facts SnapshotWearsGarments reads.
func SnapshotBarterHolder(snap *Snapshot, a *ActorSnapshot) BarterHolder {
	if snap == nil || a == nil {
		return BarterHolder{}
	}
	return BarterHolder{
		Policy:      a.RestockPolicy,
		Inventory:   a.Inventory,
		Stockholder: (a.VisitorState != nil && a.VisitorState.Trade != nil) || ActorIsDistributor(snap.VillageObjects, a.WorkStructureID),
	}
}

// SpokenForReason names why held units of a kind are not up for barter, for the
// steer / annotation phrasing. SpokenForNone means every held unit is spare.
type SpokenForReason int

const (
	SpokenForNone    SpokenForReason = iota
	SpokenForMakings                 // stock the holder keeps to work with — a buy line it consumes rather than sells on
	SpokenForGarment                 // the one garment unit the holder is wearing
)

// SpokenForClaim is one kind's reservation: how many held units are not spare,
// and why.
type SpokenForClaim struct {
	Qty    int
	Reason SpokenForReason
}

// SpokenFor returns, per kind the holder carries, how many units are NOT up for
// barter — the goods a pay_with_item / offer_trade bundle must leave in the
// pack. nil when nothing is reserved. LLM-636.
//
// The rule generalises the `except` carve-out HoldsBarterableGoodsExcept always
// had ("a good is not payment for itself") to the goods the holder is going to
// USE: a keeper cannot buy salt with the thread she just bought for mending any
// more than she can buy thread with thread. Live case — Hannah Boggs, coin-broke
// after the 08-13→17 deadlock, was told on every low restock line "offer goods
// you carry in trade", named her thread, salt, firewood, skillet and the homespun
// the working-clothes cue had just sent her for, and Josiah accepted every
// bundle: 103 buy-then-pay-back-in-kind laps between the two of them in a week,
// and the mending line never held a spool. Two claims:
//
//   - MAKINGS: a `buy` line of the holder's own policy (explicit or derived —
//     EffectiveBuyEntries, the same demand set the restock cue and warrant read)
//     is reserved up to its ReorderFloor when the item is a required input of one
//     of the holder's produce recipes, else — for a kind nobody eats or drinks
//     (kindFoodOrDrink) — up to the line's cap. The floor branch is deliberately
//     the LLM-609 number, not a new one, so what the wares cue calls "makings,
//     not wares" and what this gate refuses to hand over agree exactly on a
//     recipe input; the cap branch covers what a service draws on (mending's
//     thread), a speed input (the smith's iron), fuel, a tool bought for the
//     bench, a working garment on a rebuy line — the goods a keeper only ever
//     USES. A consumable on a non-recipe buy line makes no claim: that is a
//     larder or a resale line (the Tavern's bought-in cheese and milk go to its
//     guests, and the live vendor scan lists them for sale), and only the LLM-609
//     floor ever calls a food a making. A non-consumable line with no cap
//     reserves everything held. Held units ABOVE the reservation are spare — a
//     maker with a surplus trades it as before. A STOCKHOLDER
//     (BarterHolder.Stockholder) makes no makings claim at all: the distributor's
//     buy lines are the wares he pays with, and reserving them would recreate
//     the LLM-406 absorbing state this predicate's caller was built to break
//     (illiquid_distributor_barters_for_stock).
//   - GARMENT: one unit of every wearable garment kind held (GarmentWearMinutes
//     > 0 — working clothes and warms alike) is the unit on the holder's back
//     (Actor.GarmentWear is per kind on the in-use unit; a second unit is a fresh
//     spare and stays tradeable). Stockholders excepted, as above.
//
// Produce and forage lines make no claim — they are the holder's OWN wares, and
// exactly what a coin-broke maker should be putting up. Nor does anything the
// policy does not cover (a laborer's loaf, a found pelt).
func SpokenFor(kinds map[ItemKind]*ItemKindDef, recipes map[ItemKind]*ItemRecipe, h BarterHolder) map[ItemKind]SpokenForClaim {
	if h.Stockholder || len(h.Inventory) == 0 {
		return nil
	}
	var out map[ItemKind]SpokenForClaim
	claim := func(kind ItemKind, qty int, reason SpokenForReason) {
		if qty <= 0 {
			return
		}
		if out == nil {
			out = make(map[ItemKind]SpokenForClaim)
		}
		c := out[kind]
		if reason == SpokenForGarment || c.Reason == SpokenForNone {
			c.Reason = reason // a garment on a rebuy line is "your own clothes", not "makings"
		}
		c.Qty += qty
		out[kind] = c
	}
	floors := ReorderFloors(recipes, h.Policy)
	for _, e := range EffectiveBuyEntries(recipes, h.Policy) {
		held := h.Inventory[e.Item]
		if held <= 0 {
			continue
		}
		reserve := floors[e.Item]
		if reserve <= 0 {
			// A larder / resale line is spare to trade; so is a kind the catalog
			// doesn't know (the KindBarterable permissive degrade — sparse fixtures).
			if def := kinds[e.Item]; def == nil || kindFoodOrDrink(def) {
				continue
			}
			reserve = e.Cap()
		}
		if reserve <= 0 || reserve > held {
			reserve = held
		}
		claim(e.Item, reserve, SpokenForMakings)
	}
	for kind, held := range h.Inventory {
		if held > 0 && GarmentWearMinutes(kinds, kind) > 0 {
			claim(kind, 1, SpokenForGarment)
		}
	}
	for kind, c := range out {
		if held := h.Inventory[kind]; c.Qty > held {
			c.Qty = held // a garment that is also a buy line can't reserve more than is held
			out[kind] = c
		}
	}
	return out
}

// kindFoodOrDrink reports whether the kind is something someone eats or drinks
// — a catalog food/drink category, or any Satisfies entry. Read by the makings
// claim to tell a larder / resale buy line (the Tavern's cheese) from a bench
// input nobody consumes (thread, salt, iron); raw meat is category food with no
// Satisfies, so both signals are consulted. Nil-safe: an unknown kind is neither.
func kindFoodOrDrink(def *ItemKindDef) bool {
	if def == nil {
		return false
	}
	return def.Consumable() || def.Category == ItemCategoryFood || def.Category == ItemCategoryDrink
}

// SpareQty is how many units of kind the holder could put up in a bundle: held
// less SpokenFor, never negative. spokenFor is the map SpokenFor returned for
// this holder (nil is fine — nothing reserved).
func SpareQty(inventory map[ItemKind]int, spokenFor map[ItemKind]SpokenForClaim, kind ItemKind) int {
	spare := inventory[kind] - spokenFor[kind].Qty
	if spare < 0 {
		return 0
	}
	return spare
}

// HoldsBarterableGoodsExcept reports whether the holder carries anything that could
// go up in a pay_with_item bundle, ignoring `except`. A held ItemKind counts when it
// has at least one SPARE unit (SpokenFor — LLM-636) and its catalog class is
// tradeable at all (KindBarterable — not a service, not eat-here-only; LLM-445):
// pay_items accepts whatever the buyer carries and the seller decides accept or
// decline, so this gates on whether OFFERABLE goods exist — never on whether a
// given seller would take these particular goods. That adjudication is the
// seller's own turn, which is the line perception draws at knowable/hard facts.
// Coins are counted separately by the caller.
//
// `kinds` is the item catalog (World.ItemKinds live-side, Snapshot.ItemKinds on the
// perception side) consulted for the per-kind class; a kind missing from it counts
// (see KindBarterable's permissive degrade). `recipes` is the recipe catalog the
// makings claim reads.
//
// `except` is the item being BOUGHT, and it is excluded because a good is not payment
// for itself: a keeper down to his last few carrots cannot buy carrots by offering
// carrots. Counting it would let the buy cue survive on a fiction — the buyer is sent
// to a supplier it has no way to settle with, which is the wasted trip the whole gate
// exists to prevent. Pass "" to count every held tradeable good (the LLM-222
// consumer-buy behavior, where the buyer is paying for a consumable it means to eat,
// not restocking the same line of stock).
func HoldsBarterableGoodsExcept(kinds map[ItemKind]*ItemKindDef, recipes map[ItemKind]*ItemRecipe, h BarterHolder, except ItemKind) bool {
	spokenFor := SpokenFor(kinds, recipes, h)
	for item, qty := range h.Inventory {
		if qty > 0 && item != except && KindBarterable(kinds[item]) && SpareQty(h.Inventory, spokenFor, item) > 0 {
			return true
		}
	}
	return false
}
