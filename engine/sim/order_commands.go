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

// MaxOrderReadyInDays caps how far ahead a lodging booking can be placed.
// A month is plenty for the village's cadence and it bounds the derived
// ExpiresAt horizon, so a fat-fingered ready_in_days can't park an order
// Ready for years. ZBBS-HOME-403.
const MaxOrderReadyInDays = 30

// resolveOrderReadyBy computes an order's booked date from the buyer's
// ready_in_days offset, returning midnight UTC of the resolved calendar date
// (orderDateUTC). Rules (ZBBS-HOME-403):
//
//   - days <= 0 → carry the parent's booked date when this is a
//     counter-response (a price haggle never moves the date the buyer asked
//     for), else today (immediate / same-day — the default for every
//     non-lodging order and for lodging booked for tonight). ready_in_days
//     decodes to 0 whether omitted or explicitly today, so a haggle that
//     leaves it out preserves the original booking. A carried FUTURE date is
//     still subject to the lodging-only rule, so a counter can't swap a
//     lodging booking for a non-lodging item and keep the future date.
//   - days > 0 → advance booking, lodging-only, and it overrides any parent
//     date (the buyer is re-specifying terms, as in_response_to already
//     allows for qty/item). A physical good is handed over when paid for, so
//     a future date on a non-lodging item would just strand the order at
//     Ready until it expired; reject it up front with a model-legible reason.
//     Capped at MaxOrderReadyInDays.
//
// MUST be called from inside a Command.Fn (reads w.PayLedger / w.Settings).
func resolveOrderReadyBy(w *World, kind ItemKind, parentID LedgerID, days int, at time.Time) (time.Time, error) {
	today := orderDateUTC(at, w.Settings.Location)
	if days <= 0 {
		if parentID != 0 {
			if parent := w.PayLedger[parentID]; parent != nil && !parent.ReadyBy.IsZero() {
				// A carried FUTURE date is an advance booking and is lodging-only,
				// same as a fresh ready_in_days — so a counter that swaps a lodging
				// booking for a non-lodging item can't inherit the future date. A
				// same-day-or-past carried date is harmless on any item.
				if parent.ReadyBy.After(today) && !itemHasCapability(w, kind, "lodging") {
					return time.Time{}, fmt.Errorf(
						"ready_in_days is only for booking lodging ahead — %s is delivered when you pay for it.",
						kind,
					)
				}
				return parent.ReadyBy, nil
			}
		}
		return today, nil
	}
	if !itemHasCapability(w, kind, "lodging") {
		return time.Time{}, fmt.Errorf(
			"ready_in_days is only for booking lodging ahead — %s is delivered when you pay for it (drop ready_in_days).",
			kind,
		)
	}
	if days > MaxOrderReadyInDays {
		return time.Time{}, fmt.Errorf(
			"ready_in_days is too far ahead (got %d, max %d).",
			days, MaxOrderReadyInDays,
		)
	}
	return today.AddDate(0, 0, days), nil
}

// lodgingNightKey normalizes a booking date to its UTC calendar day so coverage
// comparison is location-independent: ReadyBy is materialized as midnight UTC of
// the booked date (orderDateUTC / the repo/pg/orders.go DATE round-trip), and
// this guards against any value that isn't exactly UTC-midnight.
func lodgingNightKey(t time.Time) string { return t.UTC().Format("2006-01-02") }

// advancePastHeldLodging advances a lodging booking's ready_by to the first
// night the buyer does NOT already hold from this seller, so a "renewal" extends
// the stay by a night instead of re-booking a night already held (LLM-47). A
// renew for tonight when the buyer already has tonight would otherwise mint a
// second nights_stay for the same (buyer, seller, ready_by); delivering it
// violates the pay_ledger_lodging_active_once unique index and wedges every
// checkpoint (the 2026-06-19 Ezekiel↔John Ellis incident).
//
// "Held" is a DELIVERED lodging grant, matching the unique index's predicate
// (state='accepted' AND fulfillment_status='delivered') — so coverage is read
// from w.Orders (fulfillment lives on the Order, not the PayLedger entry), where
// each delivered lodging order covers [ReadyBy, ReadyBy+Qty) nights. A
// not-yet-delivered booking (OrderStateReady) is deliberately NOT counted: it
// isn't a real grant yet, and the deliver-time backstop in transferOrderGoods
// resolves any residual same-night overlap when the second one delivers. The walk
// starts at the requested night and returns the first uncovered night; a night
// already past all coverage is returned unchanged (an explicit future booking is
// never pushed further out). excludeOrderID skips one order — the one being
// delivered, so it doesn't count itself; pass 0 at accept time (no order exists
// yet). MUST run on the world goroutine (reads w.Orders).
func advancePastHeldLodging(w *World, buyerID, sellerID ActorID, readyBy time.Time, excludeOrderID OrderID) time.Time {
	covered := make(map[string]struct{})
	for _, o := range w.Orders {
		if o == nil || o.ID == excludeOrderID || o.State != OrderStateDelivered {
			continue
		}
		if o.BuyerID != buyerID || o.SellerID != sellerID || o.ReadyBy.IsZero() {
			continue
		}
		if !itemHasCapability(w, o.Item, "lodging") {
			continue
		}
		nights := o.Qty
		if nights < 1 {
			nights = 1
		}
		for n := 0; n < nights; n++ {
			covered[lodgingNightKey(o.ReadyBy.AddDate(0, 0, n))] = struct{}{}
		}
	}
	// At most len(covered) nights are occupied, so walking len(covered)+1
	// consecutive nights must reach a free one — a tight, guaranteed bound.
	out := readyBy
	for i := 0; i <= len(covered); i++ {
		if _, held := covered[lodgingNightKey(out)]; !held {
			return out
		}
		out = out.AddDate(0, 0, 1)
	}
	return out
}

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
	// ReadyBy defaults to the creation date for a same-day / immediate order;
	// a lodging advance booking carries a future date stamped at intake
	// (ZBBS-HOME-403). The lodging-aware ExpiresAt derives from it so a future
	// booking survives until it is actually due rather than expiring on the
	// 10-minute takeaway TTL.
	readyBy := entry.ReadyBy
	if readyBy.IsZero() {
		readyBy = orderDateUTC(at, w.Settings.Location)
	}
	// Order.ID IS the pay_ledger row id (== LedgerID): an Order is its
	// durable pay_ledger row, 1:1. Adopt entry.ID rather than a separate
	// per-run counter so Order.ID == LedgerID — the domain invariant the
	// checkpoint enforces (pg orders SaveSnapshot) and the same id the load
	// path keys on — which makes every persistence write target the correct
	// row. ZBBS-HOME-394.
	id := OrderID(entry.ID)
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
		ReadyBy:     readyBy,
		ExpiresAt:   orderExpiresAt(w, entry.ItemKind, readyBy, at),
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

			// Gate 4b: advance booking not yet due (ZBBS-HOME-403). A lodging
			// order booked for a future date can't be checked in early. Enforced
			// here, not only in perception — a direct/tool deliver_order with the
			// id would otherwise grant the room before the guest's booked date.
			// Same-day bookings (ReadyBy == today) pass; the immediate take-home
			// path never reaches DeliverOrder.
			today := orderDateUTC(at, w.Settings.Location)
			if !o.ReadyBy.IsZero() && o.ReadyBy.After(today) {
				return nil, fmt.Errorf("deliver_order: order %d is booked for %s — not ready to deliver yet",
					orderID, o.ReadyBy.Format("2006-01-02"))
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

			// Gate 5: seller exists + stock. The stock check is skipped for
			// "service"-capability items (e.g. nights_stay) — they carry no
			// inventory (ZBBS-HOME-296; mirrors the pay_with_item gate-10
			// skip). The seller-existence check always runs.
			seller, ok := w.Actors[sellerID]
			if !ok || seller == nil {
				return nil, fmt.Errorf("deliver_order: seller %q not found", sellerID)
			}
			if !itemHasCapability(w, o.Item, "service") {
				requiredQty := o.Qty * len(o.ConsumerIDs)
				if seller.Inventory[o.Item] < requiredQty {
					return nil, fmt.Errorf("deliver_order: seller %q has %d %s, need %d for order %d",
						sellerID, seller.Inventory[o.Item], o.Item, requiredQty, orderID)
				}
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

			// Fulfillment — shared with the ZBBS-HOME-398 immediate-handover
			// path (commitPayTransfer). transferOrderGoods grants the lodging
			// room or moves the goods to each consumer. Co-presence (gate 6),
			// catalog (gate 7), the SalientFacts below, and finalizeOrderTerminal
			// stay on this deferred-delivery caller.
			if err := transferOrderGoods(w, o, seller, consumers); err != nil {
				return nil, err
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

// transferOrderGoods executes an Order's physical handover: it grants the
// lodging room for a "lodging"-capability item, or moves the goods to each
// consumer for an ordinary item. It does NOT flip the Order state, write
// SalientFacts, or run the deliver_order gate matrix — the caller owns those.
//
// Shared by two callers:
//   - DeliverOrder (the deferred deliver_order tool — lodging check-in and
//     future craft), after its 7-gate matrix and before its Delivered
//     SalientFacts + finalize. This is the only caller that exercises the
//     lodging branch below.
//   - fulfillTakeHomeOrderAtAccept (ZBBS-HOME-398 immediate handover), right
//     after a PHYSICAL order is minted at accept; the accept gate matrix
//     already validated co-presence and stock, so the error paths below are
//     defensive there (and the lodging branch is never reached via that path).
//
// consumers MUST be the resolved, non-nil *Actor pointers for o.ConsumerIDs,
// already validated co-present with the seller by the caller. MUST run on the
// world goroutine.
//
// Capability contract: lodging IMPLIES service (no inventory). A
// lodging-but-not-service item is a misconfigured catalog and is rejected
// loudly rather than treated as an unconsumed physical good.
func transferOrderGoods(w *World, o *Order, seller *Actor, consumers []*Actor) error {
	isLodging := itemHasCapability(w, o.Item, "lodging")
	if isLodging && !itemHasCapability(w, o.Item, "service") {
		return fmt.Errorf("order %d: item %q has the lodging capability without service — misconfigured catalog", o.ID, o.Item)
	}
	if isLodging {
		// Lodging grants the room to the BUYER; the lodger is bedded into it
		// (InsideRoomID) at the actual bed-down, NOT here at check-in (LLM-14).
		// The caller validated co-presence of the
		// CONSUMERS, so enforce the single-self-consumer scope: a
		// buyer-not-in-consumers (or multi-consumer) order would grant +
		// teleport an actor whose co-presence was never checked and strand the
		// listed consumers. Booking a room on another's behalf is out of scope.
		if len(o.ConsumerIDs) != 1 || o.ConsumerIDs[0] != o.BuyerID {
			return fmt.Errorf("order %d: lodging order must have the buyer as its sole consumer (buyer=%q consumers=%v)", o.ID, o.BuyerID, o.ConsumerIDs)
		}
		if seller.WorkStructureID == "" {
			return fmt.Errorf("order %d: keeper %q has no work structure to lodge in", o.ID, seller.ID)
		}
		// LLM-47 backstop: never deliver a nights_stay onto a night the buyer
		// already holds from this seller — a second delivered (buyer, seller,
		// ready_by) row violates pay_ledger_lodging_active_once and wedges every
		// checkpoint. The accept-time advance (advancePastHeldLodging in
		// PayWithItem) normally prevents this, so in the common path the night is
		// unchanged; this is defense-in-depth against any other path that mints a
		// same-night booking. excludeID o.LedgerID so the order doesn't count
		// itself.
		if adjusted := advancePastHeldLodging(w, o.BuyerID, o.SellerID, o.ReadyBy, o.ID); !adjusted.Equal(o.ReadyBy) {
			log.Printf("sim/lodging: order %d ready_by advanced %s → %s to avoid double-booking a night %s already holds at %s",
				o.ID, o.ReadyBy.Format("2006-01-02"), adjusted.Format("2006-01-02"), o.BuyerID, seller.DisplayName)
			o.ReadyBy = adjusted
			// The checkpoint persists pay_ledger.ready_by from the Order
			// (repo/pg/orders.go), so o.ReadyBy is the value that reaches the DB;
			// keep the PayLedger read-model (perception, ledger readers) in step.
			if le := w.PayLedger[o.LedgerID]; le != nil {
				le.ReadyBy = adjusted
			}
		}
		// The grant runs for o.Qty nights from the booked date (ReadyBy), not
		// from the check-in instant — an advance booking checked in on its
		// ready_by date still gets the nights it paid for. ReadyBy == the
		// creation date for a same-day booking, so this is unchanged for the
		// common path. ZBBS-HOME-403.
		expiresAt := ComputeLodgerUntil(o.ReadyBy, o.Qty, w.Settings.LodgingCheckOutHour, w.Settings.Location)
		res, err := AssignBedroomForLodger(StructureID(seller.WorkStructureID), o.BuyerID, int64(o.LedgerID), expiresAt).Fn(w)
		if err != nil {
			if err == ErrNoPrivateRooms {
				return fmt.Errorf("order %d: %s has no bedrooms — not set up for lodging", o.ID, seller.DisplayName)
			}
			return fmt.Errorf("order %d: assign bedroom: %w", o.ID, err)
		}
		abr, _ := res.(AssignBedroomResult)
		if abr.RoomID == 0 {
			return fmt.Errorf("order %d: all bedrooms at %s are occupied — try again shortly", o.ID, seller.DisplayName)
		}
		return nil
	}
	// Ordinary goods. The atomic-commit contract requires every per-consumer
	// transfer to succeed or none to mutate state. Preflight the AGGREGATE
	// required stock (and nil consumers) BEFORE any mutation so a multi-consumer
	// order can't half-commit — transfer to the first consumer, then fail on a
	// later one. Both callers' gates already validate this (DeliverOrder gate 5;
	// pay-accept gate 10), so this makes the helper self-enforce the atomicity
	// contract rather than trust the caller. "service" items carry no inventory
	// (infinite stock) — skip their stock check, mirroring gate 5 (lodging,
	// which implies service, already returned above).
	n := len(consumers)
	if n == 0 {
		return fmt.Errorf("order %d: no consumers", o.ID)
	}
	for _, consumer := range consumers {
		if consumer == nil {
			return fmt.Errorf("order %d: nil consumer in preflight", o.ID)
		}
	}
	if !itemHasCapability(w, o.Item, "service") {
		if o.Qty <= 0 {
			return fmt.Errorf("order %d: invalid quantity %d", o.ID, o.Qty)
		}
		if o.Qty > math.MaxInt/n {
			return fmt.Errorf("order %d: aggregate quantity overflows int (qty=%d consumers=%d)", o.ID, o.Qty, n)
		}
		if required := o.Qty * n; seller.Inventory[o.Item] < required {
			return fmt.Errorf("order %d: insufficient stock for %d consumers (have %d, need %d)",
				o.ID, n, seller.Inventory[o.Item], required)
		}
	}
	for _, consumer := range consumers {
		if err := transferItem(w, seller, consumer, o.Item, o.Qty); err != nil {
			// A substrate invariant violation, not a domain failure — the
			// gates + preflight should have caught it.
			return fmt.Errorf("order %d: transferItem to %q: %w", o.ID, consumer.ID, err)
		}
	}
	return nil
}

// mintAndTransferTakeHomeOrder mints the take-home Order for an accepted
// PHYSICAL !ConsumeNow offer and moves the goods to the buyer in the same tick
// (ZBBS-HOME-398), returning the still-Ready Order. The caller (commitPayTransfer)
// flips it to Delivered AFTER writing the Paid/PaidBy facts, so OrderDelivered
// fires after the payment facts exist (a subscriber on OrderDelivered can
// assume the Paid facts are already present). Lodging is routed elsewhere by
// the caller — it keeps the deferred book→check-in flow — so this path only
// ever sees ordinary goods.
//
// The order is handed over at accept (no deferred deliver_order beat, no
// buyer-seller rendezvous), so the buyer can never be charged for goods that
// then fail to be delivered. The Order is still MINTED (not skipped) so that,
// once the caller flips it to Delivered, its durable pay_ledger row persists at
// the next checkpoint — that row is what the price-book restart seed reads
// (OrdersRepo.LoadRecentPrices, state='accepted' regardless of
// fulfillment_status). Skipping the Order entirely would silently drop
// cross-restart price memory for every purchase. (A crash between accept and
// the next checkpoint loses this tick whole — the coin debit, the goods move,
// and the price observation together — so the durability matches the engine's
// transient-state-lossy / persistent-state-consistent crash model.)
//
// The accept gate matrix already validated co-presence and aggregate stock, so
// transferOrderGoods cannot fail in practice; a non-nil return is a substrate
// invariant violation surfaced to the caller, consistent with its other
// defensive-error returns. MUST run on the world goroutine.
func mintAndTransferTakeHomeOrder(w *World, entry *PayLedgerEntry, seller *Actor, at time.Time) (*Order, error) {
	id := createOrderForPayWithItem(w, entry, at)
	o := w.Orders[id]
	if o == nil {
		return nil, fmt.Errorf("order %d: vanished immediately after mint", id)
	}
	consumers := make([]*Actor, 0, len(o.ConsumerIDs))
	for _, cid := range o.ConsumerIDs {
		c, ok := w.Actors[cid]
		if !ok || c == nil {
			return nil, fmt.Errorf("order %d: consumer %q not found at immediate handover", id, cid)
		}
		consumers = append(consumers, c)
	}
	if err := transferOrderGoods(w, o, seller, consumers); err != nil {
		return nil, err
	}
	return o, nil
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
