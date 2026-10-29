package sim

import "math"

// recordVisitorProvenance keeps VisitorState.ReceivedFrom (LLM-875) for one
// goods move of qty units of kind from `from` to `to`. Every place goods change
// hands between two actors calls it after the move has committed (both
// inventories already updated): transferItem and the pay_items leg of
// commitPayTransfer. The consume-now settlements, which split one departure
// between an eater and the buyer's pack, use recordConsumeNowProvenance.
//
// A resident→visitor move credits the visitor's row for that resident; a
// visitor→resident move debits the same row (floor 0, delete-on-zero), so a
// stack he took and handed back is no longer counted as the resident's. A
// visitor→visitor move credits nothing: the provenance the cue speaks is "off
// your own shelf", which only a resident has.
//
// Goods are fungible, so the rows are also kept at or under what the visitor
// actually holds of the kind (clampVisitorProvenance) — after anything leaves
// his pack, and again just before new stock of the kind lands. Without the
// second clamp, wheat Josiah handed him and he has since parted with (sold to
// Hannah, eaten) would leave Josiah's row standing, and the next wheat to reach
// the pack from anyone would read back to Josiah as "the same five he had off
// you" (code_review).
func recordVisitorProvenance(from, to *Actor, kind ItemKind, qty int) {
	if from == nil || to == nil || qty <= 0 || from.ID == to.ID {
		return
	}
	if from.VisitorState != nil {
		if to.VisitorState == nil {
			debitVisitorProvenance(from.VisitorState, to.ID, kind, qty)
		}
		clampVisitorProvenance(from.VisitorState, kind, from.Inventory[kind])
	}
	if to.VisitorState != nil {
		clampVisitorProvenance(to.VisitorState, kind, to.Inventory[kind]-qty)
		if from.VisitorState == nil {
			creditVisitorProvenance(to.VisitorState, from.ID, kind, qty)
		}
	}
}

// recordConsumeNowProvenance is recordVisitorProvenance for one consumer's split
// of a consume-now settlement: eat units went into consumer, kept units into
// buyer's pack. Call it once per split, after the seller's inventory reflects
// the split's whole departure and buyer's reflects the kept units.
//
// A visitor seller has both parts debited before his rows are clamped once.
// Two recordVisitorProvenance calls would clamp after the first part against an
// inventory the second part had already left, then debit the second part again
// (code_review, round 2). Eaten units never enter a pack, so on the buying side
// only the kept units can be credited.
func recordConsumeNowProvenance(seller, buyer, consumer *Actor, kind ItemKind, eat, kept int) {
	if seller == nil || buyer == nil {
		return
	}
	if seller.VisitorState == nil {
		recordVisitorProvenance(seller, buyer, kind, kept)
		return
	}
	if eat > 0 && consumer != nil && consumer.VisitorState == nil {
		debitVisitorProvenance(seller.VisitorState, consumer.ID, kind, eat)
	}
	if kept > 0 && buyer.VisitorState == nil {
		debitVisitorProvenance(seller.VisitorState, buyer.ID, kind, kept)
	}
	clampVisitorProvenance(seller.VisitorState, kind, seller.Inventory[kind])
	if kept > 0 && buyer.VisitorState != nil {
		clampVisitorProvenance(buyer.VisitorState, kind, buyer.Inventory[kind]-kept)
	}
}

func creditVisitorProvenance(vs *VisitorState, resident ActorID, kind ItemKind, qty int) {
	if vs.ReceivedFrom == nil {
		vs.ReceivedFrom = make(map[ActorID]map[ItemKind]int)
	}
	row := vs.ReceivedFrom[resident]
	if row == nil {
		row = make(map[ItemKind]int)
		vs.ReceivedFrom[resident] = row
	}
	// Saturate rather than wrap, the same posture as TradeErrand.Delivered.
	if row[kind] > math.MaxInt-qty {
		row[kind] = math.MaxInt
	} else {
		row[kind] += qty
	}
}

func debitVisitorProvenance(vs *VisitorState, resident ActorID, kind ItemKind, qty int) {
	row := vs.ReceivedFrom[resident]
	if row == nil {
		return
	}
	setVisitorProvenance(vs, resident, row, kind, row[kind]-qty)
}

// clampVisitorProvenance caps every resident's count of kind at held.
func clampVisitorProvenance(vs *VisitorState, kind ItemKind, held int) {
	for resident, row := range vs.ReceivedFrom {
		if n, ok := row[kind]; ok && n > held {
			setVisitorProvenance(vs, resident, row, kind, held)
		}
	}
}

// setVisitorProvenance writes one count, deleting it at or below zero and the
// resident's row when it empties.
func setVisitorProvenance(vs *VisitorState, resident ActorID, row map[ItemKind]int, kind ItemKind, n int) {
	if n > 0 {
		row[kind] = n
		return
	}
	delete(row, kind)
	if len(row) == 0 {
		delete(vs.ReceivedFrom, resident)
	}
}
