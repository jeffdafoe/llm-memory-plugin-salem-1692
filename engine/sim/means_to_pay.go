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

// HoldsBarterableGoodsExcept reports whether an inventory carries anything that could
// go up in a pay_with_item bundle, ignoring `except`. A held ItemKind with a positive
// quantity counts when its catalog class is tradeable at all (KindBarterable — not a
// service, not eat-here-only; LLM-445): pay_items accepts whatever the buyer carries
// and the seller decides accept or decline, so this gates on whether OFFERABLE goods
// exist — never on whether a given seller would take these particular goods. That
// adjudication is the seller's own turn, which is the line perception draws at
// knowable/hard facts. Coins are counted separately by the caller.
//
// `kinds` is the item catalog (World.ItemKinds live-side, Snapshot.ItemKinds on the
// perception side) consulted for the per-kind class; a kind missing from it counts
// (see KindBarterable's permissive degrade).
//
// `except` is the item being BOUGHT, and it is excluded because a good is not payment
// for itself: a keeper down to his last few carrots cannot buy carrots by offering
// carrots. Counting it would let the buy cue survive on a fiction — the buyer is sent
// to a supplier it has no way to settle with, which is the wasted trip the whole gate
// exists to prevent. Pass "" to count every held tradeable good (the LLM-222
// consumer-buy behavior, where the buyer is paying for a consumable it means to eat,
// not restocking the same line of stock).
func HoldsBarterableGoodsExcept(kinds map[ItemKind]*ItemKindDef, inventory map[ItemKind]int, except ItemKind) bool {
	for item, qty := range inventory {
		if qty > 0 && item != except && KindBarterable(kinds[item]) {
			return true
		}
	}
	return false
}
