package sim

import (
	"context"
	"log"
	"math"
	"time"
)

// TerminalOrderSink is the synchronous durable-write target for Order
// terminal transitions. Implementations write the post-flip Order to
// the durable store and return any error. This sink IS allowed to block
// the world goroutine — the design intent is write-through-then-prune
// (Slice 6): the world commits the durable state before pruning the
// in-memory entry from World.Orders, so a crash between the two leaves
// the order durable in pg and dropped from memory on the next restart
// via the Ready+Pending-only LoadAll filter.
//
// Failure mode: on a non-nil error, the caller (finalizeOrderTerminal)
// logs and SKIPS the prune. The in-memory Order stays at its terminal
// state; the next checkpoint SaveSnapshot reconciles pg with the
// in-memory shape. Brief divergence window is acceptable since the
// OrderDelivered / OrderExpired event has already fired and any
// narrative subscribers have observed the transition.
//
// Wiring: optional. The default is no sink installed (w.terminalOrderSink
// == nil), which preserves the legacy behavior of letting terminal
// entries accumulate in w.Orders until restart. Tests that don't need
// the prune behavior simply don't install a sink. Production wires the
// pg impl via SetTerminalOrderSink before LoadWorld so the LoadWorld-
// time restartExpirePendingOrders pass also write-through-prunes.
type TerminalOrderSink interface {
	WriteTerminal(ctx context.Context, o *Order) error
}

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
//   - OrderStateExpired — terminal safety-net; the deadline elapsed at Ready
//     without a successful deliver_order call. Goods stay in the seller's
//     inventory (they never moved), but the buyer's coins ARE refunded
//     (ZBBS-HOME-403 — reversed in flipOrderTerminal); a stuck order should
//     not leave the buyer charged for goods they never received. Still signals
//     a fulfillment miss worth admin attention.
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

	// ReadyBy is the date the order becomes deliverable — for lodging, the
	// buyer's check-in date (advance booking; ZBBS-HOME-403). Materialized as
	// midnight UTC of a calendar date (the DATE round-trip convention; see
	// repo/pg/orders.go). Defaults to the creation date for an ordinary /
	// same-day order, so a non-lodging take-home order's ReadyBy == today and
	// the perception split treats it as ready-now. Drives the lodging room
	// grant window (ComputeLodgerUntil) and the lodging ExpiresAt below.
	ReadyBy time.Time

	// ExpiresAt is the deadline past which the sweep flips Ready→Expired (and
	// refunds — ZBBS-HOME-403). For an ordinary take-home order it is
	// CreatedAt + WorldSettings.OrderTTL; for a lodging order it is the
	// check-in deadline derived from ReadyBy (the morning after the booked
	// night), so a future booking survives until it is actually due rather
	// than expiring on the 10-minute takeaway TTL.
	ExpiresAt time.Time
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

// orderDateUTC returns midnight UTC of the calendar date that `at` falls on in
// loc. ReadyBy is a DATE conceptually (ready_by column), round-tripped from pg
// as midnight UTC; building it this way keeps the in-memory ReadyBy
// byte-identical to the value reloaded after a restart and sidesteps the
// session-TZ ::date truncation drift the v1 perception code worked around.
// ZBBS-HOME-403.
func orderDateUTC(at time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	local := at.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
}

// orderExpiresAt resolves an Order's ExpiresAt at creation. A lodging order's
// expiry is the check-in deadline — the morning after the booked night
// (ComputeLodgerUntil(readyBy, 1, checkOutHour)) — so a future booking survives
// until it is actually due rather than lapsing on the short takeaway TTL, and a
// same-day booking still gets the overnight window before it expires (and
// refunds, ZBBS-HOME-403). Every other order keeps the flat OrderTTL from
// creation; physical take-home is delivered in the same tick anyway, so its TTL
// is only a defensive backstop.
func orderExpiresAt(w *World, item ItemKind, readyBy, at time.Time) time.Time {
	if itemHasCapability(w, item, "lodging") {
		return ComputeLodgerUntil(readyBy, 1, w.Settings.LodgingCheckOutHour, w.Settings.Location)
	}
	return at.Add(effectiveOrderTTL(w.Settings))
}

// finalizeOrderTerminal flips an Order to a terminal state, stamps the
// terminal timestamps, emits the matching event, and (when a sink is
// installed) write-through-prunes the entry from World.Orders. Shared
// by the happy-path DeliverOrder commit and the safety-net order sweep.
//
// MUST be called from inside a Command.Fn (world goroutine). The
// caller is responsible for any pre-flip validation (e.g. DeliverOrder
// re-validates the 7-gate matrix before calling); finalizeOrderTerminal
// trusts that the transition is legal.
//
// terminal must be a real terminal state (OrderStateDelivered or
// OrderStateExpired). For Delivered, DeliveredAt is stamped (the
// caller has already done the per-consumer transferItem calls).
// For Expired, no goods move (they never left the seller) but the
// buyer's coins are refunded (ZBBS-HOME-403, in flipOrderTerminal).
//
// Slice 6 write-through-then-prune (active only when a non-nil
// TerminalOrderSink is wired): after the existing event emit, the
// post-flip Order is written to the durable store via the sink. On
// success, the entry is deleted from w.Orders. On error, the entry
// stays in w.Orders at its terminal state; the next checkpoint
// SaveSnapshot reconciles pg with the in-memory shape. Without a
// sink installed (typical for unit tests, and for the period before
// main.go wires the pg sink), this function preserves the legacy
// no-prune behavior so existing tests still pass.
func finalizeOrderTerminal(w *World, o *Order, terminal OrderState, at time.Time) {
	if o == nil || !terminal.IsTerminal() {
		return
	}
	if !flipOrderTerminal(w, o, terminal, at) {
		// Already terminal (or invalid) — nothing flipped, so nothing to
		// persist or prune. Guards against a double sink-write / double prune.
		return
	}

	// ZBBS-HOME-403: an Expired transition refunds the buyer's coins in
	// flipOrderTerminal (in-memory only). Persisting the terminal eagerly via
	// the sink while the refunded coins land only at the next actor checkpoint
	// would let a crash between the two strand the buyer — the order durably
	// 'expired' (so restart's Ready/Pending-only LoadAll won't reload and
	// re-expire it), the coins not yet returned. So skip the write-through for
	// Expired and let the next checkpoint persist the terminal status and the
	// refunded balances atomically (orders + actors SaveSnapshot share one
	// checkpoint Tx). The retained terminal entry is the same "bloat, not data
	// loss" the no-sink path already tolerates: SaveSnapshot upserts it as
	// 'expired' and LoadAll's filter drops it on the next restart.
	if terminal == OrderStateExpired {
		return
	}

	// Slice 6: write-through + prune. Skip entirely when no sink is
	// installed (legacy no-prune behavior; tests, pre-cutover builds).
	sink := w.terminalOrderSink
	if sink == nil {
		return
	}
	if err := sink.WriteTerminal(w.LifecycleContext(), o); err != nil {
		// Log and leave the entry in w.Orders at its terminal state.
		// Brief memory-vs-pg divergence resolves at next checkpoint;
		// the OrderDelivered/OrderExpired event has already fired so
		// narrative subscribers aren't re-notified on the next run.
		log.Printf("sim/order: terminal write-through for order %d (%s) failed: %v",
			o.ID, terminal, err)
		return
	}
	delete(w.Orders, o.ID)
}

// flipOrderTerminal flips an Order to a terminal state, stamps the
// terminal timestamps, and emits the matching OrderDelivered/OrderExpired
// event — WITHOUT the durable sink write-through or the in-memory prune
// that finalizeOrderTerminal performs.
//
// Use this when the durable pay_ledger row is NOT yet persisted at flip
// time — specifically the same-tick mint-then-deliver path (ZBBS-HOME-398
// immediate handover): the order is born at accept and delivered in the
// same command, so no checkpoint has INSERTed its row yet and the eager
// WriteTerminal UPDATE would no-op (0 rows) and log an error on every
// purchase. Leaving the terminal Order in w.Orders lets the next checkpoint
// SaveSnapshot persist it as 'delivered' (BuildCheckpointSnapshot copies the
// whole map; the LoadAll restart filter is Ready+Pending-only, so the
// delivered row never reloads as a ghost). The retained terminal entry is
// the same "bloat, not data loss" the no-sink path already tolerates and
// resets on restart.
//
// finalizeOrderTerminal wraps this with the sink write-through + prune for
// the deferred-delivery path, where a prior checkpoint has already INSERTed
// the Ready row so the UPDATE lands.
//
// Returns true when it flipped a live (non-terminal) Order, false when the
// Order was nil, the target wasn't terminal, or the Order was ALREADY terminal.
// The already-terminal guard is the idempotency backstop (ZBBS-HOME-403): a
// second call with OrderStateExpired must not refund twice or re-emit. Every
// caller filters to Ready first, but the shared chokepoint guards itself.
//
// MUST be called from inside a Command.Fn (world goroutine).
func flipOrderTerminal(w *World, o *Order, terminal OrderState, at time.Time) bool {
	if o == nil || !terminal.IsTerminal() {
		return false
	}
	if o.State.IsTerminal() {
		return false
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
			Amount:      o.Amount,
			ConsumerIDs: append([]ActorID(nil), o.ConsumerIDs...),
			LedgerID:    o.LedgerID,
			At:          at,
		})
	case OrderStateExpired:
		// Refund the buyer's coins before the event fires so any
		// OrderExpired subscriber observes the reversed balances. The
		// caller (finalizeOrderTerminal) skips the eager durable
		// write-through for Expired so this refund + the terminal status
		// persist together at the next checkpoint. ZBBS-HOME-403.
		refundExpiredOrder(w, o)
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
	return true
}

// refundExpiredOrder returns the coins a buyer paid for an order that expired
// undelivered — the deferred-path counterpart to ZBBS-HOME-398's immediate
// handover (which closed the robbery window for in-stock take-home). In v2 the
// only orders that defer to a later deliver_order are lodging bookings (a
// service item paid in coins), so a Ready→Expired transition means the buyer
// paid for a room the keeper never checked them into; reverse the coin leg the
// accept committed (buyer += Amount, seller -= Amount). Goods never moved on a
// deferred order (they transfer at deliver_order), so only coins need
// reversing.
//
// Conserves the closed economy's coin supply: the seller is debited exactly
// what the buyer is credited, even if that briefly pushes the seller negative
// (a debt they earn back) — making the buyer whole is the point of the refund.
//
// Lodging in the live economy is coin-paid, so a barter (pay_items) leg can't
// reach here — the intake rejects a goods-paid lodging booking (ZBBS-HOME-403),
// keeping every deferred order coin-only and fully reversible.
//
// Best-effort per leg so the make-whole guarantee never hinges on the seller:
//   - Credit the buyer (the leg that matters). Skipped only on the pathological
//     overflow — a buyer already at ~MaxInt coins isn't being robbed — or when
//     the buyer has left the world (a visitor who despawned: no one to refund).
//   - Debit the seller (the conservation leg). Skipped on the symmetric
//     underflow or an absent seller — a credited-buyer / absent-seller refund
//     is a touch of inflation, which beats failing to make the buyer whole.
//
// So a real buyer with a real booking is ALWAYS refunded; only the
// pathological / actor-gone edges fall back, all logged. No-op when Amount <= 0.
// MUST run on the world goroutine (mutates Actor balances).
func refundExpiredOrder(w *World, o *Order) {
	if o == nil || o.Amount <= 0 {
		return
	}
	buyer := w.Actors[o.BuyerID]
	seller := w.Actors[o.SellerID]
	credited := false
	switch {
	case buyer == nil:
		log.Printf("sim/order: expired order %d buyer %q is gone — no one to refund", o.ID, o.BuyerID)
	case buyer.Coins > math.MaxInt-o.Amount:
		log.Printf("sim/order: refund of expired order %d would overflow buyer %q (have %d, +%d) — buyer not credited",
			o.ID, o.BuyerID, buyer.Coins, o.Amount)
	default:
		buyer.Coins += o.Amount
		credited = true
		log.Printf("sim/order: refunded %d coins to buyer %q for expired order %d", o.Amount, o.BuyerID, o.ID)
	}
	// Only debit the seller when the buyer was actually credited — a debit
	// without a matching credit would destroy coins and penalize the seller for
	// a refund that didn't happen.
	if !credited {
		return
	}
	switch {
	case seller == nil:
		log.Printf("sim/order: expired order %d seller %q is gone — buyer credited, debit skipped (slight inflation)", o.ID, o.SellerID)
	case seller.Coins < math.MinInt+o.Amount:
		log.Printf("sim/order: refund of expired order %d would underflow seller %q (have %d, -%d) — seller not debited",
			o.ID, o.SellerID, seller.Coins, o.Amount)
	default:
		seller.Coins -= o.Amount
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
// Currently called from World.FinalizeLoad, which runs AFTER both the OrdersRepo
// and the ActorsRepo have loaded (see repo/pg/load_world.go) — so the
// ZBBS-HOME-403 refund this triggers can find the buyer/seller actors and make
// a booking that lapsed during downtime whole. (The refund is best-effort: if
// an actor genuinely didn't survive the restart, refundExpiredOrder logs and
// the order still terminalizes — see its doc.)
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
