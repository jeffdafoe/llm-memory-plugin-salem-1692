package sim

import (
	"fmt"
	"log"
	"time"
)

// order_lapsed.go — LLM-993. When a Ready order expires, settleExpiredOrder
// moves coins with neither party's turn in flight: a refund takes the buyer's
// payment back out of the seller's purse (below zero if he has already spent
// it), a forfeit lets the seller keep a deposit. Before this, nothing carried
// the outcome into perception: the seller's purse was simply lower the next
// morning (live 2026-11-22: Ezekiel Crane woke at -31 coins, believing the
// order records were "a record-keeping error on someone's part").
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
// their count ("5 shovels"); amount is the coins that moved (the deposit for a
// partial-payment commission, else the full price). debt is how far below zero
// the refund left the seller (0 when it did not).
func OrderLapsedSellerNarration(buyerName, lot string, amount int, deposit, forfeited bool, debt int) string {
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
	line := fmt.Sprintf("%s's order for %s ran out of time before you handed it over, so %s went back to them out of your purse.",
		buyerName, lot, paid)
	if debt > 0 {
		line += fmt.Sprintf(" You had spent it already, so that leaves you %d coins in debt.", debt)
	}
	return line
}

// OrderLapsedBuyerNarration is the buyer's twin.
func OrderLapsedBuyerNarration(sellerName, lot string, amount int, deposit, forfeited bool) string {
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
	return fmt.Sprintf("%s never handed over the %s you paid for in time, so %s came back to you.",
		sellerName, lot, paid)
}

// narrateLapsedOrder stamps the lapsed-order beat on both parties and writes
// each one's durable `order_lapsed` row. Called from flipOrderTerminal after
// settleExpiredOrder, so the purses already reflect the settlement. MUST run on
// the world goroutine.
func narrateLapsedOrder(w *World, o *Order, forfeited bool, at time.Time) {
	if o == nil || itemHasCapability(w, o.Item, "lodging") {
		return
	}
	amount := orderAmountPaidAtAccept(o)
	if amount <= 0 {
		return
	}
	buyer := w.Actors[o.BuyerID]
	seller := w.Actors[o.SellerID]
	if buyer == nil || seller == nil {
		// A party has left the world (a despawned visitor): there is no one to
		// tell on that side and no counterparty name to tell the other.
		return
	}
	units := o.Qty
	if n := len(o.ConsumerIDs); n > 1 {
		units = o.Qty * n
	}
	lot := inputCountPhrase(w, o.Item, units)
	deposit := o.Deposit > 0 && o.Deposit < o.Amount
	debt := 0
	if !forfeited && seller.Coins < 0 {
		debt = -seller.Coins
	}
	tell := func(party, counterparty *Actor, narration string) {
		tryStampWarrant(w, party, WarrantMeta{
			TriggerActorID: counterparty.ID,
			Reason: OrderLapsedWarrantReason{
				OrderID:       o.ID,
				Counterparty:  counterparty.ID,
				NarrationText: narration,
			},
			SourceActorID: counterparty.ID,
			OccurredAt:    at,
		}, at)
		if _, err := AppendActionLogEntry(ActionLogEntry{
			ActorID:          party.ID,
			OccurredAt:       at,
			ActionType:       ActionTypeOrderLapsed,
			Text:             narration,
			CounterpartyName: counterparty.DisplayName,
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
				"deposit":      deposit,
				"forfeited":    forfeited,
				"counterparty": counterparty.DisplayName,
				"text":         narration,
			},
			SpeakerName: party.DisplayName,
			Source:      "engine",
		})
	}
	tell(seller, buyer, OrderLapsedSellerNarration(buyer.DisplayName, lot, amount, deposit, forfeited, debt))
	tell(buyer, seller, OrderLapsedBuyerNarration(seller.DisplayName, lot, amount, deposit, forfeited))
}
