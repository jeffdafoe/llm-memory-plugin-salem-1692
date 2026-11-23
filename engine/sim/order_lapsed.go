package sim

import (
	"fmt"
	"log"
	"time"
)

// order_lapsed.go — LLM-993. When a Ready order expires, settleExpiredOrder
// moves coins with neither party's turn in flight: a refund takes the buyer's
// payment back out of the seller's purse (only what the purse holds, down to 0,
// if he has already spent it — LLM-995), a forfeit lets the seller keep a
// deposit. Before this, nothing carried the outcome into perception: the
// seller's purse was simply lower the next morning (live 2026-11-22: Ezekiel
// Crane woke at -31 coins, believing the order records were "a record-keeping
// error on someone's part").
//
// narrateLapsedOrder gives each party one pre-rendered line on their next turn
// (OrderLapsedWarrantReason, the LaborSettled / CarterBought posture) and a
// durable `order_lapsed` action row, so a stateful NPC's day note and dream
// carry it too. Lodging bookings are left out: a booking lapses when the GUEST
// never checks in, so "never handed over" would tell the wrong story.

// OrderLapsedWarrantReason surfaces a lapsed order's settlement to one party.
// Counterparty is the other party (the render fallback name when the narration
// is empty). DedupDiscriminator is the OrderID: one beat per order per party.
type OrderLapsedWarrantReason struct {
	OrderID       OrderID
	Counterparty  ActorID
	NarrationText string
}

func (OrderLapsedWarrantReason) isWarrantReason()             {}
func (OrderLapsedWarrantReason) Kind() WarrantKind            { return WarrantKindOrderLapsed }
func (r OrderLapsedWarrantReason) DedupDiscriminator() uint64 { return uint64(r.OrderID) }

// OrderLapsedSellerNarration is the seller's line. lot names the goods with
// their count ("5 shovels"); amount is the coins the buyer paid at accept (the
// deposit for a partial-payment commission, else the full price). refunded is
// the coins that actually went back: amount on a full refund, less when the
// seller had spent it (LLM-995), ignored on a forfeit.
func OrderLapsedSellerNarration(buyerName, lot string, amount, refunded int, deposit, forfeited bool) string {
	if buyerName == "" {
		buyerName = "Your customer"
	}
	if forfeited {
		return fmt.Sprintf("%s never came for the %s you made to order, so you keep their %d-coin deposit, and the goods are back on your shelf to sell.",
			buyerName, lot, amount)
	}
	paid := fmt.Sprintf("the %d coins they paid", amount)
	if deposit {
		paid = fmt.Sprintf("their %d-coin deposit", amount)
	}
	switch {
	case refunded >= amount:
		return fmt.Sprintf("%s's order for %s ran out of time before you handed it over, so %s went back to them out of your purse.",
			buyerName, lot, paid)
	case refunded > 0:
		return fmt.Sprintf("%s's order for %s ran out of time before you handed it over. You had already spent %s, so you could give back only the %d coins you had left.",
			buyerName, lot, paid, refunded)
	default:
		return fmt.Sprintf("%s's order for %s ran out of time before you handed it over. You had already spent %s and your purse was empty, so nothing went back to them.",
			buyerName, lot, paid)
	}
}

// OrderLapsedBuyerNarration is the buyer's twin.
func OrderLapsedBuyerNarration(sellerName, lot string, amount, refunded int, deposit, forfeited bool) string {
	if sellerName == "" {
		sellerName = "The seller"
	}
	if forfeited {
		return fmt.Sprintf("You never collected the %s you ordered from %s, so your %d-coin deposit is forfeit.",
			lot, sellerName, amount)
	}
	paid := fmt.Sprintf("your %d coins", amount)
	if deposit {
		paid = fmt.Sprintf("your %d-coin deposit", amount)
	}
	switch {
	case refunded >= amount:
		return fmt.Sprintf("%s never handed over the %s you paid for in time, so %s came back to you.",
			sellerName, lot, paid)
	case refunded > 0:
		return fmt.Sprintf("%s never handed over the %s you paid for in time, and had already spent %s, so only %d coins came back to you.",
			sellerName, lot, paid, refunded)
	default:
		return fmt.Sprintf("%s never handed over the %s you paid for in time, and had already spent %s, so none of it came back to you.",
			sellerName, lot, paid)
	}
}

// narrateLapsedOrder stamps the lapsed-order beat on both parties and writes
// each one's durable `order_lapsed` row. Called from flipOrderTerminal after
// settleExpiredOrder, so the purses already reflect the settlement; settled is
// what that settlement did. MUST run on the world goroutine.
func narrateLapsedOrder(w *World, o *Order, settled expirySettlement, at time.Time) {
	forfeited, refunded := settled.Forfeited, settled.Refunded
	if o == nil || itemHasCapability(w, o.Item, "lodging") {
		return
	}
	amount := orderAmountPaidAtAccept(o)
	if amount <= 0 {
		return
	}
	buyer := w.Actors[o.BuyerID]
	seller := w.Actors[o.SellerID]
	// Say only what the settlement did. A blocked refund (the buyer gone from the
	// world, or a credit that would overflow; refundOrderPayment) moved no coin,
	// so there is nothing true to tell either side — in particular not that the
	// seller had spent it. A forfeit needs the seller (it requires his stock),
	// and stands whether or not the buyer is still here. A party who is gone is
	// not told; the survivor is, with a fallback name if the other party's actor
	// is gone.
	if !forfeited && (settled.RefundBlocked || buyer == nil) {
		return
	}
	nameOf := func(a *Actor) string {
		if a == nil {
			return ""
		}
		return a.DisplayName
	}
	units := o.Qty
	if n := len(o.ConsumerIDs); n > 1 {
		units = o.Qty * n
	}
	lot := inputCountPhrase(w, o.Item, units)
	deposit := o.Deposit > 0 && o.Deposit < o.Amount
	tell := func(party *Actor, counterpartyID ActorID, counterpartyName, narration string) {
		if party == nil {
			return
		}
		tryStampWarrant(w, party, WarrantMeta{
			TriggerActorID: counterpartyID,
			Reason: OrderLapsedWarrantReason{
				OrderID:       o.ID,
				Counterparty:  counterpartyID,
				NarrationText: narration,
			},
			SourceActorID: counterpartyID,
			OccurredAt:    at,
		}, at)
		if _, err := AppendActionLogEntry(ActionLogEntry{
			ActorID:          party.ID,
			OccurredAt:       at,
			ActionType:       ActionTypeOrderLapsed,
			Text:             narration,
			CounterpartyName: counterpartyName,
		}).Fn(w); err != nil {
			log.Printf("sim/order: append order_lapsed for %s (order %d): %v", party.DisplayName, o.ID, err)
		}
		w.AppendActionLogDurable(DurableActionLogRow{
			ActorID:    party.ID,
			OccurredAt: at,
			ActionType: ActionTypeOrderLapsed,
			Payload: map[string]any{
				"order_id":     uint64(o.ID),
				"item":         string(o.Item),
				"qty":          units,
				"amount":       amount,
				"refunded":     refunded,
				"deposit":      deposit,
				"forfeited":    forfeited,
				"counterparty": counterpartyName,
				"text":         narration,
			},
			SpeakerName: party.DisplayName,
			Source:      "engine",
		})
	}
	tell(seller, o.BuyerID, nameOf(buyer), OrderLapsedSellerNarration(nameOf(buyer), lot, amount, refunded, deposit, forfeited))
	tell(buyer, o.SellerID, nameOf(seller), OrderLapsedBuyerNarration(nameOf(seller), lot, amount, refunded, deposit, forfeited))
}
