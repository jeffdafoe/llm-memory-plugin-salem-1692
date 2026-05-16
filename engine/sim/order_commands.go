package sim

import (
	"fmt"
	"log"
	"math"
	"strings"
	"time"
)

// order_commands.go — Phase 3 PR S6 commands for the post-acceptance
// fulfillment state machine.
//
// Two flows:
//
//   - createOrderForPayWithItem — internal helper called from
//     commitPayTransfer's !ConsumeNow branch when AcceptPay commits a
//     take-home offer. Mints the Order, emits OrderCreated.
//
//   - DeliverOrder — exposed Command for the deliver_order tool. Runs
//     the 7-gate validation matrix (existence + auth + state + TTL +
//     seller-stock + co-presence + catalog), atomically transfers
//     goods to each consumer, writes bidirectional buyer↔seller
//     SalientFacts, and flips the Order to Delivered.

// createOrderForPayWithItem mints a new Order for a !ConsumeNow
// pay-with-item acceptance. Called from commitPayTransfer on the
// world goroutine. Returns the new OrderID.
//
// ConsumerIDs is normalized at creation — when the PayLedger entry
// had no explicit consumers, we populate ConsumerIDs with [buyerID]
// (matches the architecture-lock "buyer is the consumer when
// implicit" semantic and simplifies downstream code).
//
// Goods do NOT transfer here — that's the deliver_order tool's job.
// Stock stays in the seller's inventory until DeliverOrder fires its
// transferItem call per consumer.
func createOrderForPayWithItem(w *World, entry *PayLedgerEntry, at time.Time) OrderID {
	consumers := append([]ActorID(nil), entry.ConsumerIDs...)
	if len(consumers) == 0 {
		consumers = []ActorID{entry.BuyerID}
	}
	ttl := effectiveOrderTTL(w.Settings)
	id := w.nextOrderSeq()
	o := &Order{
		ID:          id,
		State:       OrderStateReady,
		BuyerID:     entry.BuyerID,
		SellerID:    entry.SellerID,
		Item:        entry.ItemKind,
		Qty:         entry.Qty,
		Amount:      entry.Amount,
		ConsumerIDs: consumers,
		LedgerID:    entry.ID,
		CreatedAt:   at,
		ExpiresAt:   at.Add(ttl),
	}
	w.Orders[id] = o
	w.emit(&OrderCreated{
		OrderID:     id,
		BuyerID:     o.BuyerID,
		SellerID:    o.SellerID,
		Item:        o.Item,
		Qty:         o.Qty,
		ConsumerIDs: append([]ActorID(nil), consumers...),
		Amount:      o.Amount,
		LedgerID:    o.LedgerID,
		At:          at,
	})
	return id
}

// DeliverOrder returns a Command that finalizes an Order: validates
// the 7-gate matrix, transfers goods to each consumer, writes
// SalientFacts, flips state to Delivered, and emits OrderDelivered.
//
// Validation gates (first-failure-wins):
//
//  1. Order exists.
//  2. Auth: caller == Order.SellerID.
//  3. State: Order.State == OrderStateReady (idempotent rejects on
//     Delivered/Expired).
//  4. Live TTL: if at >= Order.ExpiresAt, flip Ready→Expired in-band
//     and reject. Mirrors PayLedger's accept_pay TTL gate (PR S4).
//  5. Seller stock: seller.Inventory[Item] >= Qty * len(ConsumerIDs).
//     Should always hold since accept reserved goods by NOT moving
//     them; defensive against the seller having consumed/transferred
//     their own stock between accept and deliver.
//  6. Co-presence per consumer: each ConsumerID shares CurrentHuddleID
//     with the seller. Take-home implies the recipient is present to
//     receive; if a consumer wandered off, the Order stays Ready and
//     the seller can retry when they come back (or the safety-net
//     sweep eventually expires).
//  7. ItemKind catalog: World.ItemKinds[Item] != nil. Defensive
//     against catalog deprecation between accept and deliver.
//
// On all-gate-pass: atomic transfer (one transferItem per consumer),
// bidirectional buyer↔seller InteractionDelivered/InteractionReceived
// facts, terminal flip + OrderDelivered emit. Multi-consumer group
// orders are all-or-none — if any transferItem call fails (which
// should never happen post-gate-5; this is defensive), the partial
// transfers ALREADY committed for earlier consumers remain (no
// rollback). That's a substrate bug, not a domain failure.
//
// On gate failure: Order is left at OrderStateReady (gates 1-3, 5-7)
// or transitions to OrderStateExpired (gate 4 only). Returns a
// descriptive error for the tool dispatcher to surface to the LLM.
func DeliverOrder(sellerID ActorID, orderID OrderID, at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			// Gate 1: existence.
			o, ok := w.Orders[orderID]
			if !ok || o == nil {
				return nil, fmt.Errorf("deliver_order: order %d not found", orderID)
			}

			// Gate 2: auth.
			if o.SellerID != sellerID {
				return nil, fmt.Errorf("deliver_order: order %d belongs to %q, not %q", orderID, o.SellerID, sellerID)
			}

			// Gate 3: state.
			switch o.State {
			case OrderStateDelivered:
				return nil, fmt.Errorf("deliver_order: order %d already delivered", orderID)
			case OrderStateExpired:
				return nil, fmt.Errorf("deliver_order: order %d already expired", orderID)
			case OrderStateReady:
				// fall through
			default:
				return nil, fmt.Errorf("deliver_order: order %d in state %q, expected %q", orderID, o.State, OrderStateReady)
			}

			// Gate 4: live TTL. Sweep cadence is 60s but this gate
			// catches the gap between TTL elapsing and the next sweep.
			// Flip in-band like PayLedger's accept_pay does.
			if !o.ExpiresAt.IsZero() && !at.Before(o.ExpiresAt) {
				finalizeOrderTerminal(w, o, OrderStateExpired, at)
				return nil, fmt.Errorf("deliver_order: order %d expired at %v", orderID, o.ExpiresAt)
			}

			// Defensive Order-shape gates (PR S6 R1 code_review fix):
			// invalid Qty / empty ConsumerIDs / overflow can come from
			// future repo loads or test hooks; reject before the stock
			// multiplication so a zero-consumer order doesn't deliver
			// nothing-but-stamp-delivered.
			if o.Qty <= 0 {
				return nil, fmt.Errorf("deliver_order: order %d has invalid quantity %d", orderID, o.Qty)
			}
			if len(o.ConsumerIDs) == 0 {
				return nil, fmt.Errorf("deliver_order: order %d has no consumers", orderID)
			}
			if o.Qty > math.MaxInt/len(o.ConsumerIDs) {
				return nil, fmt.Errorf("deliver_order: order %d total quantity overflows int (qty=%d, consumers=%d)",
					orderID, o.Qty, len(o.ConsumerIDs))
			}

			// Gate 5: seller exists + stock.
			seller, ok := w.Actors[sellerID]
			if !ok || seller == nil {
				return nil, fmt.Errorf("deliver_order: seller %q not found", sellerID)
			}
			requiredQty := o.Qty * len(o.ConsumerIDs)
			if seller.Inventory[o.Item] < requiredQty {
				return nil, fmt.Errorf("deliver_order: seller %q has %d %s, need %d for order %d",
					sellerID, seller.Inventory[o.Item], o.Item, requiredQty, orderID)
			}

			// Gate 6: co-presence per consumer. Also resolve each
			// consumer pointer once so we don't re-look-up in the
			// transfer loop.
			consumers := make([]*Actor, 0, len(o.ConsumerIDs))
			for _, cid := range o.ConsumerIDs {
				consumer, ok := w.Actors[cid]
				if !ok || consumer == nil {
					return nil, fmt.Errorf("deliver_order: consumer %q not found", cid)
				}
				if seller.CurrentHuddleID == "" {
					return nil, fmt.Errorf("deliver_order: seller %q not in a huddle (cannot deliver)", sellerID)
				}
				if consumer.CurrentHuddleID == "" || consumer.CurrentHuddleID != seller.CurrentHuddleID {
					return nil, fmt.Errorf("deliver_order: consumer %q not co-present with seller", cid)
				}
				consumers = append(consumers, consumer)
			}

			// Gate 7: ItemKind catalog.
			if w.ItemKinds[o.Item] == nil {
				return nil, fmt.Errorf("deliver_order: item %q no longer in catalog", o.Item)
			}

			// All gates pass. The atomic-commit contract requires every
			// per-consumer transfer to succeed or none of them to mutate
			// state. transferItem can fail on three modes (qty <= 0,
			// missing actor, insufficient stock) — gates above already
			// ruled out all three for the live world state. Preflight
			// each prospective transfer in a dry-run loop so any
			// surprise failure (future transferItem mode, or a corrupt
			// loaded Order) is caught BEFORE any mutation lands.
			for _, consumer := range consumers {
				if consumer == nil {
					return nil, fmt.Errorf("deliver_order: nil consumer in preflight")
				}
			}
			// All preflights passed — commit per-consumer transfers.
			// gate-5 + gate-6 + preflight together guarantee these
			// cannot fail; the residual error path is defensive.
			for _, consumer := range consumers {
				if err := transferItem(w, seller, consumer, o.Item, o.Qty); err != nil {
					// Reaching here is a substrate invariant
					// violation, not a domain failure. The earlier
					// gates + preflight should have caught it.
					return nil, fmt.Errorf("deliver_order: transferItem to %q: %w", consumer.ID, err)
				}
			}

			// Bidirectional buyer↔seller SalientFacts. Multi-consumer
			// per-consumer writes intentionally omitted (matches the
			// posture from S4's commitPayTransfer).
			buyer := w.Actors[o.BuyerID]
			buyerName, sellerName := string(o.BuyerID), string(o.SellerID)
			if buyer != nil {
				buyerName = buyer.DisplayName
			}
			if seller != nil {
				sellerName = seller.DisplayName
			}
			sellerFact := orderDeliveredFactText(buyerName, o.Item, o.Qty, len(o.ConsumerIDs), false)
			buyerFact := orderDeliveredFactText(sellerName, o.Item, o.Qty, len(o.ConsumerIDs), true)
			if _, err := RecordInteraction(o.SellerID, o.BuyerID, InteractionDelivered, sellerFact, at).Fn(w); err != nil {
				log.Printf("sim.DeliverOrder: RecordInteraction seller→buyer: %v", err)
			}
			if _, err := RecordInteraction(o.BuyerID, o.SellerID, InteractionReceived, buyerFact, at).Fn(w); err != nil {
				log.Printf("sim.DeliverOrder: RecordInteraction buyer→seller: %v", err)
			}

			// Terminal flip + OrderDelivered emit.
			finalizeOrderTerminal(w, o, OrderStateDelivered, at)
			return nil, nil
		},
	}
}

// orderDeliveredFactText composes the InteractionDelivered /
// InteractionReceived fact text. Mirrors the v1 deliver narration
// pattern but keeps it minimal — item kind + qty + counterparty.
// For group orders (>1 consumer), the buyer-side fact omits the
// per-consumer breakdown; the buyer didn't necessarily watch each
// handover.
//
// buyerSide=true renders the buyer's view ("Hannah delivered me 2
// stew"); buyerSide=false renders the seller's view ("I delivered
// 2 stew to Jefferey"). For multi-consumer group orders, the
// seller's view says "...to Jefferey and 2 others" since the
// SalientFact lives on the buyer↔seller pair specifically.
func orderDeliveredFactText(counterpartyName string, item ItemKind, qty, consumerCount int, buyerSide bool) string {
	itemDesc := string(item)
	if qty > 1 {
		itemDesc = fmt.Sprintf("%d %s", qty, item)
	}
	if buyerSide {
		return fmt.Sprintf("%s delivered me %s.", counterpartyName, itemDesc)
	}
	// Seller side.
	var b strings.Builder
	fmt.Fprintf(&b, "I delivered %s to %s", itemDesc, counterpartyName)
	if consumerCount > 1 {
		fmt.Fprintf(&b, " and %d others", consumerCount-1)
	}
	b.WriteString(".")
	return b.String()
}
