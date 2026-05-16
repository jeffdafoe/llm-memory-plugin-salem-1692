package sim

import (
	"math"
	"time"
)

// Order is the post-acceptance fulfillment state machine for take-away
// pay-with-item transactions (Phase 3 PR S6). Created in
// `commitPayTransfer`'s !ConsumeNow branch when AcceptPay commits a
// take-home offer; closed when the seller invokes the `deliver_order`
// tool to atomically hand the goods to the buyer/consumers.
//
// Architecture context (ledger-substrate § 2):
//
//   - PayLedger (PR S4) owns offer→accept. Terminates at PayLedgerStateAccepted.
//   - Order (this file) owns post-acceptance fulfillment. Stock stays in
//     seller's inventory between accept and deliver; coins moved at accept.
//   - The split exists so the seller has narrative agency in the moment of
//     handover — "slides the bowl across," "hands over the horseshoe" — and
//     so future craft items with real lead time (hours_per_unit > 0) have
//     a state to sit in while the work happens. v1 parity: see v1's
//     engine/order_fulfillment.go for the original rationale.
//
// ConsumeNow=true offers (eat-on-the-spot) do NOT mint an Order — they
// transfer goods + satisfy needs at accept time. Order is the !ConsumeNow
// path only.

// OrderID is the engine-minted per-run identifier for an Order. uint64
// to match QuoteID/LedgerID/EventID pattern (LLM-readback friendly,
// monotonic, no collision concerns within a single world's lifetime).
type OrderID uint64

// OrderState is the macro-state of an Order. Three values:
//
//   - OrderStateReady — initial state at creation, awaiting deliver_order.
//   - OrderStateDelivered — terminal happy path; goods transferred.
//   - OrderStateExpired — terminal safety-net; TTL elapsed at Ready without
//     a successful deliver_order call. Pure state flip — goods stay in
//     seller's inventory, coins stay with seller. Signals a stuck-order
//     case for admin investigation.
//
// Pending state (for craft items with lead time) is intentionally deferred
// — every Order today goes straight to Ready at creation. Withdrawn state
// (buyer cancellation of a paid order) is deferred to a future refund
// flow. Add new states as those subsystems land.
type OrderState string

const (
	OrderStateReady     OrderState = "ready"
	OrderStateDelivered OrderState = "delivered"
	OrderStateExpired   OrderState = "expired"
)

// IsTerminal reports whether the state is a terminal one (Delivered or
// Expired). Callers should use this rather than enumerating terminal
// values directly so adding a future terminal (e.g. Withdrawn) is a
// one-line change.
func (s OrderState) IsTerminal() bool {
	return s == OrderStateDelivered || s == OrderStateExpired
}

// Order is the per-transaction post-acceptance fulfillment record. Lives
// in World.Orders keyed by OrderID. Cloned via CloneOrder at the snapshot
// + mem-repo boundary; persisted at checkpoint time via OrdersRepo.
//
// Self-contained: all fields needed for perception render + deliver_order
// validation are on the struct, so the world goroutine doesn't need to
// chase the PayLedger entry on every read. LedgerID is retained as a
// back-reference for admin trace and event-log lineage but isn't required
// for any hot read path.
//
// Multi-consumer group orders are ONE Order with len(ConsumerIDs) > 1.
// At deliver time, `transferItem` fires per consumer atomically (all or
// none). Buyer is normalized into ConsumerIDs[0] when no explicit
// consumers were given at pay time (implicit "buyer is the consumer").
type Order struct {
	ID          OrderID
	State       OrderState
	BuyerID     ActorID
	SellerID    ActorID
	Item        ItemKind
	Qty         int // per-consumer count; total goods = Qty * len(ConsumerIDs)
	Amount      int // coin total, debited at accept (S4) — informational here
	ConsumerIDs []ActorID

	// LedgerID back-references the originating PayLedger entry. Used by
	// InteractionDelivered fact text + admin replay; not part of any
	// validation path.
	LedgerID LedgerID

	CreatedAt   time.Time  // = pay-accept time
	DeliveredAt *time.Time // set on transition to Delivered
	ExpiresAt   time.Time  // = CreatedAt + WorldSettings.OrderTTL (sweep flips Ready→Expired past this)
}

// CloneOrder deep-copies an Order. ConsumerIDs slice is copied (Order
// retains exclusive ownership of its slice); DeliveredAt *time.Time is
// re-pointed so callers can mutate the original without affecting the
// clone. Used at every Snapshot republish and OrdersRepo Seed/SaveSnapshot
// boundary.
func CloneOrder(o *Order) *Order {
	if o == nil {
		return nil
	}
	cp := *o
	if o.ConsumerIDs != nil {
		cp.ConsumerIDs = make([]ActorID, len(o.ConsumerIDs))
		copy(cp.ConsumerIDs, o.ConsumerIDs)
	}
	if o.DeliveredAt != nil {
		t := *o.DeliveredAt
		cp.DeliveredAt = &t
	}
	return &cp
}

// OrderTTLDefault is the default time-to-live for an Order at Ready
// before the sweep flips it to Expired. Long enough that normal
// seller-tick activity delivers many times over; short enough that
// "stuck order >10min" surfaces as a meaningful admin signal.
// Overridable via WorldSettings.OrderTTL.
const OrderTTLDefault = 10 * time.Minute

// OrderSweepCadenceDefault is the default cadence at which the order
// aging sweep evaluates open orders for expiry. Matches the scene-quote
// + pay-ledger sweep cadences (60s) so an admin tuning any of them sees
// one mental model. Overridable via WorldSettings.OrderSweepCadence.
const OrderSweepCadenceDefault = 60 * time.Second

// effectiveOrderTTL resolves the per-world setting against the package
// default. A zero or negative value in Settings falls back to the
// package default — same pattern as effectivePayLedgerTTL.
func effectiveOrderTTL(s WorldSettings) time.Duration {
	if s.OrderTTL > 0 {
		return s.OrderTTL
	}
	return OrderTTLDefault
}

// effectiveOrderSweepCadence resolves the per-world setting against
// the package default. Same shape as effectivePayLedgerSweepCadence.
func effectiveOrderSweepCadence(s WorldSettings) time.Duration {
	if s.OrderSweepCadence > 0 {
		return s.OrderSweepCadence
	}
	return OrderSweepCadenceDefault
}

// nextOrderSeq increments the per-run order counter and returns the
// new OrderID. World-goroutine-only (called from inside Command.Fn
// when minting a new Order in commitPayTransfer's !ConsumeNow
// branch). Counter starts at 0; first minted OrderID is 1, leaving
// OrderID(0) as the unset sentinel.
func (w *World) nextOrderSeq() OrderID {
	w.orderSeq++
	return OrderID(w.orderSeq)
}

// finalizeOrderTerminal flips an Order to a terminal state, stamps the
// terminal timestamps, and emits the matching event. Shared by the
// happy-path DeliverOrder commit and the safety-net order sweep.
//
// MUST be called from inside a Command.Fn (world goroutine). The
// caller is responsible for any pre-flip validation (e.g. DeliverOrder
// re-validates the 6-gate matrix before calling); finalizeOrderTerminal
// trusts that the transition is legal.
//
// terminal must be a real terminal state (OrderStateDelivered or
// OrderStateExpired). For Delivered, DeliveredAt is stamped (the
// caller has already done the per-consumer transferItem calls).
// For Expired, only the State field changes — no goods move, no
// coins refund (per design call 5 option A).
func finalizeOrderTerminal(w *World, o *Order, terminal OrderState, at time.Time) {
	if o == nil {
		return
	}
	if !terminal.IsTerminal() {
		return
	}
	o.State = terminal
	switch terminal {
	case OrderStateDelivered:
		t := at
		o.DeliveredAt = &t
		w.emit(&OrderDelivered{
			OrderID:     o.ID,
			BuyerID:     o.BuyerID,
			SellerID:    o.SellerID,
			Item:        o.Item,
			Qty:         o.Qty,
			ConsumerIDs: append([]ActorID(nil), o.ConsumerIDs...),
			LedgerID:    o.LedgerID,
			At:          at,
		})
	case OrderStateExpired:
		w.emit(&OrderExpired{
			OrderID:     o.ID,
			BuyerID:     o.BuyerID,
			SellerID:    o.SellerID,
			Item:        o.Item,
			Qty:         o.Qty,
			ConsumerIDs: append([]ActorID(nil), o.ConsumerIDs...),
			LedgerID:    o.LedgerID,
			At:          at,
		})
	}
}

// outstandingReadyOrderQty returns the total quantity of `item` that
// the given seller has committed to deliver across all Ready Orders.
// Per-Order obligation = Qty * len(ConsumerIDs) (the same formula
// DeliverOrder uses for its stock check).
//
// Called from accept_pay's gate-9 stock check and PayWithItem's
// fast-path predicate-6 to enforce reservation accounting: post-S6,
// goods stay in the seller's inventory between accept and deliver,
// so the visible inventory doesn't reflect reservations. Without
// subtracting outstanding obligations, two pending offers against
// the same 1-stew inventory could both accept and only one could
// deliver — paid-without-fulfillable-goods (code_review PR S6 R1
// finding).
//
// MUST be called from inside a Command.Fn (world goroutine) —
// iterates w.Orders without coordination.
func outstandingReadyOrderQty(w *World, sellerID ActorID, item ItemKind) int {
	if w == nil || len(w.Orders) == 0 {
		return 0
	}
	total := 0
	for _, o := range w.Orders {
		if o == nil || o.State != OrderStateReady {
			continue
		}
		if o.SellerID != sellerID || o.Item != item {
			continue
		}
		// Defensive — both fields should be sane on a valid Order,
		// but if a future repo path loads a malformed row we'd
		// rather skip than panic.
		if o.Qty <= 0 || len(o.ConsumerIDs) <= 0 {
			continue
		}
		n := len(o.ConsumerIDs)
		// Per-Order multiplication overflow. Saturate to MaxInt so
		// the accept stock gate fails closed (treats "infinitely
		// reserved" as the safest reading of corrupt data) rather
		// than wrapping negative and re-opening the over-selling
		// path R1 patched. PR S6 R2 code_review fix.
		if o.Qty > math.MaxInt/n {
			return math.MaxInt
		}
		needed := o.Qty * n
		// Running-total overflow. Same saturation posture.
		if total > math.MaxInt-needed {
			return math.MaxInt
		}
		total += needed
	}
	return total
}

// restartExpirePendingOrders walks World.Orders at LoadWorld time and
// flips any Ready Order whose ExpiresAt has already elapsed to Expired
// in-band. Mirrors restartExpirePendingEntries for the pay-ledger side
// (pay_ledger.go).
//
// The original OrderCreated event is gone — restart re-engagement does
// NOT re-emit it. The Order survives load with its state-flip stamp;
// any future subscriber that wants restart-aware reconciliation will
// drive that off the LoadWorld pass + Snapshot, not off a synthetic
// event.
//
// MUST be called from inside Run-equivalent (LoadWorld) or a Command.Fn.
// Currently called from LoadWorld after the OrdersRepo (when it exists)
// loads the map.
func restartExpirePendingOrders(w *World, now time.Time) {
	for _, o := range w.Orders {
		if o == nil || o.State != OrderStateReady {
			continue
		}
		if o.ExpiresAt.IsZero() {
			continue
		}
		if now.Before(o.ExpiresAt) {
			continue
		}
		// Restart-flip path. We DO emit OrderExpired here (subscribers
		// haven't registered yet at LoadWorld time so the emit is
		// effectively a no-op on the event bus, but the state mutation
		// + DeliveredAt stays consistent with mid-run sweep semantics).
		finalizeOrderTerminal(w, o, OrderStateExpired, now)
	}
}
