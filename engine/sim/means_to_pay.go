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

// HoldsBarterableGoodsExcept reports whether an inventory carries anything that could
// go up in a pay_with_item bundle, ignoring `except`. Any other held ItemKind with a
// positive quantity counts: pay_items accepts whatever the buyer carries and the
// seller decides accept or decline, so this gates only on whether goods EXIST to
// offer — never on whether a given seller would take these particular goods. That
// adjudication is the seller's own turn, which is the line perception draws at
// knowable/hard facts. Coins are counted separately by the caller.
//
// `except` is the item being BOUGHT, and it is excluded because a good is not payment
// for itself: a keeper down to his last few carrots cannot buy carrots by offering
// carrots. Counting it would let the buy cue survive on a fiction — the buyer is sent
// to a supplier it has no way to settle with, which is the wasted trip the whole gate
// exists to prevent. Pass "" to count every held good (the LLM-222 consumer-buy
// behavior, where the buyer is paying for a consumable it means to eat, not restocking
// the same line of stock).
func HoldsBarterableGoodsExcept(inventory map[ItemKind]int, except ItemKind) bool {
	for item, qty := range inventory {
		if qty > 0 && item != except {
			return true
		}
	}
	return false
}
