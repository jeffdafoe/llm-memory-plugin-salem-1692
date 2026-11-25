package sim

import "math"

// commission_taken.go — LLM-1026. What a seller has just committed to when an
// accept resolved a buyer's offer as a commission (LLM-338): coin taken now for
// goods the seller has yet to make.
//
// accept_pay used to answer every Accepted settle with "The sale is settled",
// the same words a sale handed over from stock gets. A smith who wrongly believed
// he held three shovels accepted an offer for three, said "here's your three
// shovels", read "settled" as the handover, and then could not square the order
// book's "you've yet to make it" with his own memory. The tool result is the one
// line the engine speaks after the seller's decision, so it has to say which of
// the two things just happened.

// CommissionTaken describes the commission an accept just minted, from the
// seller's side. Plain values only, so it can leave the world goroutine.
type CommissionTaken struct {
	OrderID   OrderID
	BuyerName string
	// Qty is the total number of goods owed (per-consumer qty × consumers).
	Qty int
	// Noun is the count-aware noun for Qty ("shovel" / "shovels").
	Noun string
	// Charged is the coin taken at accept: the deposit on a partial-payment
	// commission (LLM-357), else the whole price.
	Charged int
	// BalanceDue is the coin still to collect at deliver_order; 0 for a full
	// prepay.
	BalanceDue int
	// PartlyOnHand is set when the seller holds some of the goods free of other
	// orders, just not all Qty: the whole order still waits, but "you have yet to
	// make" would be untrue of the units on hand.
	PartlyOnHand bool
	// ForOthers is set when the goods go to recipients other than the buyer
	// alone: the buyer paid, but the goods are owed on the order, not to them.
	ForOthers bool
}

// CommissionTakenFor reports whether ledgerID's accept left sellerID owing goods
// on a commission, and if so what. It reads the Order the accept minted
// (Order.ID == LedgerID, ZBBS-HOME-394), so it must run in the same Command as the
// accept, after it.
//
// The test is "an undelivered Ready order on a kind the seller can commission".
// Every other accept that mints an Order leaves it Delivered before the commit
// returns (take-home stock, walk-in room, mending, equipment service), except an
// advance lodging booking, which CommissionableKind excludes by capability. So no
// second copy of isCommissionOrder's stock arithmetic is needed here, and none
// could drift from it: after the accept that arithmetic would see the new order's
// own reservation and answer differently anyway.
//
// MUST run inside a Command.Fn (reads w.Orders / w.Actors / w.Recipes).
func CommissionTakenFor(w *World, sellerID ActorID, ledgerID LedgerID) (CommissionTaken, bool) {
	o := w.Orders[OrderID(ledgerID)]
	if o == nil || o.State != OrderStateReady || o.SellerID != sellerID {
		return CommissionTaken{}, false
	}
	seller := w.Actors[sellerID]
	if seller == nil || !CommissionableKind(w.Recipes, w.ItemKinds, seller.RestockPolicy, o.Item) {
		return CommissionTaken{}, false
	}
	consumers := len(o.ConsumerIDs)
	if consumers <= 0 {
		consumers = 1
	}
	if o.Qty <= 0 || o.Qty > math.MaxInt/consumers {
		return CommissionTaken{}, false
	}
	qty := o.Qty * consumers
	noun := string(o.Item)
	if def := w.ItemKinds[o.Item]; def != nil {
		if n := def.CountNoun(qty); n != "" {
			noun = n
		}
	}
	buyer := string(o.BuyerID)
	if b := w.Actors[o.BuyerID]; b != nil && b.DisplayName != "" {
		buyer = b.DisplayName
	}
	// Units on hand that no OTHER Ready order owes. This order is left out of the
	// sum before it is taken, so a huge order cannot saturate it and hide free stock.
	free := seller.Inventory[o.Item] - ReadyOrderQtyReservedExcept(w.Orders, sellerID, o.Item, o.ID)
	return CommissionTaken{
		OrderID:      o.ID,
		BuyerName:    buyer,
		Qty:          qty,
		Noun:         noun,
		Charged:      orderAmountPaidAtAccept(o),
		BalanceDue:   orderBalanceDue(o),
		PartlyOnHand: free > 0,
		ForOthers:    orderGoesToOthers(o),
	}, true
}

// orderGoesToOthers reports whether an order's goods go to anyone besides its
// buyer alone: the buyer pays, but the recipients are someone else or several.
func orderGoesToOthers(o *Order) bool {
	switch len(o.ConsumerIDs) {
	case 0:
		return false
	case 1:
		return o.ConsumerIDs[0] != o.BuyerID
	default:
		return true
	}
}
