package sim

import (
	"context"
	"log"
	"sort"
	"time"
)

// order_sweep.go — Phase 3 PR S6 aging sweep for Order entries.
//
// RunOrderSweep periodically scans World.Orders for entries in
// OrderStateReady whose ExpiresAt has passed and flips them to
// OrderStateExpired terminal, emitting OrderExpired per flip. Goods
// stay in the seller's inventory (they never moved); the buyer's coins
// are refunded on the flip (ZBBS-HOME-403, finalizeOrderTerminal →
// flipOrderTerminal) so a lapsed booking doesn't leave the buyer charged.
//
// Same coalesced AfterFunc self-rearm shape as the PR S3 scene-quote
// sweep and PR S4 pay-ledger sweep. See pay_ledger_sweep.go for
// the canonical lifecycle commentary.
//
// Cadence: WorldSettings.OrderSweepCadence (default 60s via
// OrderSweepCadenceDefault). Matches the pay-ledger and scene-quote
// sweep cadences so an admin tuning any of them sees one mental
// model.
//
// DeliverOrder also drives an in-band Expired check during its
// validation matrix (the live-state TTL gate). The sweep is the
// backstop for orders nobody tries to deliver: it ensures every
// Ready entry eventually reaches a terminal state regardless of
// reactor activity on the seller.
//
// Lifecycle:
//
//   RunOrderSweep(ctx, w)
//   └─> kickOrderSweep            // initial arm via the cmd channel
//        └─> armNextOrderSweep    // schedules the first AfterFunc
//             └─> [cadence] fireScheduledOrderSweep
//                  └─> SendContext(evaluateOrdersAndRearm(now))
//                       └─> Fn: clear flag, run scan, re-arm

// RunOrderSweep owns the Order aging sweep's periodic schedule. Caller
// starts this in a goroutine alongside World.Run (typically next to
// RunPayLedgerSweep / RunSceneQuoteSweep); returns when ctx is
// cancelled. The first sweep is kicked immediately so a Ready Order
// whose ExpiresAt is already in the past at startup doesn't wait a
// full cadence interval before being flipped.
func RunOrderSweep(ctx context.Context, w *World) {
	_, err := w.SendContext(ctx, kickOrderSweep())
	if err != nil && ctx.Err() == nil {
		log.Printf("sim/order: initial sweep arm failed: %v", err)
	}
	<-ctx.Done()
}

// kickOrderSweep returns a Command whose Fn arms the first sweep on
// the world goroutine — mirrors kickPayLedgerSweep.
func kickOrderSweep() Command {
	return Command{
		Fn: func(w *World) (any, error) {
			armNextOrderSweep(w)
			return nil, nil
		},
	}
}

// armNextOrderSweep schedules the next sweep after one cadence
// interval. MUST be called from inside a Command.Fn — touches
// w.orderSweep.scheduled without coordination.
//
// Coalescing: no-op when a sweep is already scheduled. The flag
// clears at the start of the scheduled Fn (evaluateOrdersAndRearm),
// so a re-arm during that Fn queues the next sweep rather than
// no-opping.
func armNextOrderSweep(w *World) {
	if w.orderSweep.scheduled {
		return
	}
	w.orderSweep.scheduled = true
	cadence := effectiveOrderSweepCadence(w.Settings)
	// Re-declare the live-tunable cadence on each re-arm (LLM-395) — see
	// armNextEvaluation for why the re-arm, not boot, is the moment that matters.
	w.RegisterTicker("order_sweep", cadence)
	time.AfterFunc(cadence, func() { fireScheduledOrderSweep(w) })
}

// fireScheduledOrderSweep is the AfterFunc callback body. Factored
// out so tests can drive the post-shutdown path directly (matches
// fireScheduledPayLedgerSweep).
//
// Uses LifecycleContext so a shutdown-while-armed unblocks
// SendContext instead of deadlocking on a send to a dead cmds
// channel.
func fireScheduledOrderSweep(w *World) {
	ctx := w.LifecycleContext()
	if ctx.Err() != nil {
		// Shutdown raced us. Clearing the flag would race with the
		// world goroutine; fresh worlds come from LoadWorld /
		// NewWorld, so a post-shutdown stale flag has no effect.
		return
	}
	w.beatTicker("order_sweep")
	_, err := w.SendContext(ctx, evaluateOrdersAndRearm(time.Now()))
	if err != nil && ctx.Err() == nil {
		log.Printf("sim/order: scheduled sweep failed: %v", err)
	}
}

// evaluateOrdersAndRearm clears the scheduled flag, runs one sweep,
// and re-arms — all in one Fn on the world goroutine. Clearing the
// flag first means the re-arm starts a fresh chain rather than seeing
// the still-set flag and no-opping.
func evaluateOrdersAndRearm(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			w.orderSweep.scheduled = false
			res, err := EvaluateOrderSweep(now).Fn(w)
			armNextOrderSweep(w)
			return res, err
		},
	}
}

// EvaluateOrderSweep returns a Command that flips every Ready Order
// past its ExpiresAt to the Expired terminal state and emits
// OrderExpired for each. Exposed as a Command (not just an internal
// Fn) so tests can drive sweeps deterministically without the
// AfterFunc timing chain.
//
// Iteration order is sorted by OrderID so OrderExpired events emit
// in a stable order — important for replay tests and for admin trace
// readability when a burst of orders expire at the same sweep tick.
func EvaluateOrderSweep(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if len(w.Orders) == 0 {
				return nil, nil
			}
			// Collect first, mutate after — w.emit dispatches
			// subscribers synchronously and a subscriber could in
			// principle mutate the Orders map; iterating while
			// mutating is unsafe even on the single world goroutine.
			expired := make([]OrderID, 0)
			for id, o := range w.Orders {
				if o == nil || o.State != OrderStateReady {
					continue
				}
				if o.ExpiresAt.IsZero() {
					continue
				}
				if now.Before(o.ExpiresAt) {
					continue
				}
				expired = append(expired, id)
			}
			if len(expired) == 0 {
				return nil, nil
			}
			sort.Slice(expired, func(i, j int) bool { return expired[i] < expired[j] })
			for _, id := range expired {
				o, ok := w.Orders[id]
				if !ok || o == nil || o.State != OrderStateReady {
					// Subscriber from an earlier expire in this batch
					// could have flipped this one already (unlikely
					// but defensive — matches the pay-ledger sweep
					// posture).
					continue
				}
				finalizeOrderTerminal(w, o, OrderStateExpired, now)
			}
			return nil, nil
		},
	}
}
