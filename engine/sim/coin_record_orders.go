package sim

import (
	"fmt"
	"log"
	"time"
)

// coin_record_orders.go — LLM-1015. The two order legs that moved coin without
// reaching the coin record (coin_record.go):
//
//   - the commission BALANCE paid at deliver_order (LLM-357). The deposit is
//     recorded at accept by the pay-resolved subscriber; the balance had no row and
//     no credit, so the record understated every partial-payment commission by its
//     balance. Recorded here as an ordinary `paid` row from the buyer, marked
//     order_balance so the boot seed classifies it as goods.
//
//   - the REFUND of a lapsed order (refundOrderPayment). The record still said the
//     buyer had paid the seller for goods after the coins went back, and the
//     `## Coin between you` line contradicted the LLM-993 lapse line in the same
//     prompt. Recorded as coin going back — seller to buyer, CoinPaymentRefund —
//     rather than subtracted from the original payment: the record only ever adds,
//     and a partial refund (the seller had spent it) stays true instead of leaving
//     a remainder labelled "for goods" on goods that never came.
//
// Both write the durable row the boot seed reads, so the record agrees after a
// restart, and both credit the live tally in the same place, the RecordCoinPaid
// co-location rule.

// orderUnitsPhrase is the "for" text of an order's goods, in the shape the
// pay-resolved row uses ("5x shovel", or the bare kind for one unit), so the
// balance row narrates like the deposit row before it.
func orderUnitsPhrase(o *Order) string {
	units := o.Qty
	if n := len(o.ConsumerIDs); n > 1 {
		units = o.Qty * n
	}
	if units <= 1 {
		return string(o.Item)
	}
	return fmt.Sprintf("%dx %s", units, o.Item)
}

// durableSourceFor is the agent_action_log source for an actor's own row:
// "player" for a PC, "agent" otherwise (the cascade's actorDisplayAndSource).
func durableSourceFor(a *Actor) string {
	if a != nil && a.LoginUsername != "" {
		return "player"
	}
	return "agent"
}

// recordOrderBalancePaid credits the commission balance the buyer paid at
// deliver_order: the buyer's `paid` ring entry, the durable `paid` row (marker
// order_balance) and the coin record, as goods. MUST run on the world goroutine,
// after the coin moved.
func recordOrderBalancePaid(w *World, o *Order, buyer, seller *Actor, balance int, at time.Time) {
	if o == nil || buyer == nil || seller == nil || balance <= 0 {
		return
	}
	forText := orderUnitsPhrase(o)
	if _, err := AppendActionLogEntry(ActionLogEntry{
		ActorID:          buyer.ID,
		OccurredAt:       at,
		ActionType:       ActionTypePaid,
		Text:             forText,
		HuddleID:         buyer.CurrentHuddleID,
		CounterpartyName: seller.DisplayName,
		Amount:           balance,
	}).Fn(w); err != nil {
		log.Printf("sim/order: append balance paid for %s (order %d): %v", buyer.DisplayName, o.ID, err)
	}
	w.AppendActionLogDurable(DurableActionLogRow{
		ActorID:    buyer.ID,
		OccurredAt: at,
		ActionType: ActionTypePaid,
		Payload: map[string]any{
			"recipient":          seller.DisplayName,
			"recipient_actor_id": string(seller.ID),
			"amount":             balance,
			"for":                forText,
			// Goods marker (engine-owned, like ledger_id): the balance of an order
			// whose goods are handed over in the same command. Not ledger_id itself —
			// the settlements view and the boot ledger-id floor key on that column,
			// and the deposit row already carries it.
			"order_balance": uint64(o.ID),
		},
		SpeakerName: buyer.DisplayName,
		HuddleID:    buyer.CurrentHuddleID,
		Source:      durableSourceFor(buyer),
	})
	w.RecordCoinPaid(buyer.ID, seller.ID, balance, at, CoinPaymentForGoods)
}

// recordOrderRefund credits a lapsed order's refund as coin going back from the
// seller to the buyer: the durable `refunded` row (written from the seller's side,
// the payer of the refund, in the `paid` row's shape) and the coin record. No ring
// entry — the LLM-993 `order_lapsed` beat already tells both parties in words.
// Skipped when either party is gone: the refund to a departed buyer is blocked
// before any coin moves, and a departed seller paid nothing (the buyer was
// credited without a debit) and has no actor row for the durable FK. MUST run on
// the world goroutine, after refundOrderPayment.
func recordOrderRefund(w *World, o *Order, refunded int, at time.Time) {
	if o == nil || refunded <= 0 {
		return
	}
	seller := w.Actors[o.SellerID]
	buyer := w.Actors[o.BuyerID]
	if seller == nil || buyer == nil {
		return
	}
	w.AppendActionLogDurable(DurableActionLogRow{
		ActorID:    seller.ID,
		OccurredAt: at,
		ActionType: ActionTypeRefunded,
		Payload: map[string]any{
			"recipient":          buyer.DisplayName,
			"recipient_actor_id": string(buyer.ID),
			"amount":             refunded,
			"order_id":           uint64(o.ID),
			"item":               string(o.Item),
		},
		SpeakerName: seller.DisplayName,
		Source:      "engine",
	})
	w.RecordCoinPaid(seller.ID, buyer.ID, refunded, at, CoinPaymentRefund)
}
