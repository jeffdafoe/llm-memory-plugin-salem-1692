package sim

import "time"

// events_order.go — Phase 3 PR S6 event family for the Order
// post-acceptance fulfillment state machine.
//
// Three terminal events covering the lifecycle:
//
//   - OrderCreated: emitted by commitPayTransfer (!ConsumeNow branch)
//     after AcceptPay mints the Order. Carries the full Order shape
//     so subscribers don't need a World read.
//   - OrderDelivered: emitted by DeliverOrder Command Fn after
//     transferItem per consumer + state flip. Subscribers can use this
//     to drive narrative beats, inventory broadcasts, etc.
//   - OrderExpired: emitted by EvaluateOrderSweep per order whose
//     ExpiresAt elapsed at OrderStateReady. Audit signal — no
//     subscribers in MVP, surfaces for future cascade work.
//
// No warrant subscribers in MVP (per design call 6: baseline perception
// handles seller awareness of pending deliveries; S4's existing
// PayResolved warrant covers the post-accept tick cue).

// OrderCreated fires when commitPayTransfer mints a new Order for a
// !ConsumeNow accepted pay-with-item offer. Synchronous with the
// PayWithItemResolved{Accepted} event — same world goroutine, same
// causal root.
type OrderCreated struct {
	EventBase
	OrderID     OrderID
	BuyerID     ActorID
	SellerID    ActorID
	Item        ItemKind
	Qty         int
	ConsumerIDs []ActorID
	Amount      int
	LedgerID    LedgerID
	At          time.Time
}

func (e *OrderCreated) Kind() string { return "OrderCreated" }
func (OrderCreated) isSimEvent()     {}

// OrderDelivered fires inside DeliverOrder's Command Fn after stock has
// transferred to all consumers and Order.State has flipped to Delivered.
// Subscribers receive the canonical "this happened" signal for the
// fulfillment side; future room broadcasts, dwell-narration hooks, etc.
// will subscribe here.
//
// Amount carries the agreed-on coin total from the originating
// pay-with-item transaction (= the PayLedger entry's Amount = the
// Order's Amount, stable through the post-accept fulfillment window).
// Added in Slice 6 so the future per-(seller, item) price-book ring
// buffer can subscribe to OrderDelivered directly and append a price
// observation without a re-lookup against pay_ledger.
type OrderDelivered struct {
	EventBase
	OrderID     OrderID
	BuyerID     ActorID
	SellerID    ActorID
	Item        ItemKind
	Qty         int
	Amount      int
	ConsumerIDs []ActorID
	LedgerID    LedgerID
	At          time.Time
}

func (e *OrderDelivered) Kind() string { return "OrderDelivered" }
func (OrderDelivered) isSimEvent()     {}

// OrderExpired fires when an Order at Ready crosses its ExpiresAt (the sweep,
// or an in-band deliver_order gate-4 flip). settleExpiredOrder has already run
// the coin/goods settlement: seller-fault (never forged) refunds what the buyer
// paid; buyer-fault on a partial-payment commission (forged, uncollected)
// forfeits the deposit to the seller and returns the goods to sellable stock.
// Forfeited reports which happened so narration/telemetry can tell "your coins
// came back" from "the buyer never came — you kept the deposit." LLM-357.
type OrderExpired struct {
	EventBase
	OrderID     OrderID
	BuyerID     ActorID
	SellerID    ActorID
	Item        ItemKind
	Qty         int
	ConsumerIDs []ActorID
	LedgerID    LedgerID
	// Forfeited is true when expiry FORFEITED the buyer's deposit to the seller
	// (buyer-fault: the good was forged but never collected) rather than
	// REFUNDING it (seller-fault, or a full-prepay order). LLM-357.
	Forfeited bool
	At        time.Time
}

func (e *OrderExpired) Kind() string { return "OrderExpired" }
func (OrderExpired) isSimEvent()     {}
