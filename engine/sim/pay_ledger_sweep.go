package sim

import (
	"context"
	"log"
	"sort"
	"time"
)

// pay_ledger_sweep.go — Phase 3 PR S4 step 8. Aging sweep for
// PayLedger pending entries.
//
// RunPayLedgerSweep periodically scans World.PayLedger for entries in
// PayLedgerStatePending whose ExpiresAt has passed and flips them to
// PayLedgerStateExpired terminal, emitting PayWithItemResolved{Expired}
// + projecting to the sink for each. Coalesced AfterFunc self-rearm
// chain — same shape as the PR S3 scene-quote sweep and the PR 4
// locomotion ticker.
//
// Cadence: WorldSettings.PayLedgerSweepCadence (default 60s via
// PayLedgerSweepCadenceDefault). Matches the scene-quote sweep cadence
// so an admin tuning either substrate sees one mental model.
//
// Lifecycle:
//
//   RunPayLedgerSweep(ctx, w)
//   └─> kickPayLedgerSweep            // initial arm via the cmd channel
//        └─> armNextPayLedgerSweep    // schedules the first AfterFunc
//             └─> [cadence] fireScheduledPayLedgerSweep
//                  └─> SendContext(evaluatePayLedgerAndRearm(now))
//                       └─> Fn: clear flag, run scan, re-arm
//
// AcceptPay also drives an in-band Expired flip when a pending entry
// is accepted past its TTL (gate 5 of the 10-gate revalidation
// matrix). The sweep is the backstop for offers nobody tries to
// accept: it ensures every pending entry eventually reaches a
// terminal state regardless of cascade activity.

// RunPayLedgerSweep owns the pay-ledger aging sweep's periodic
// schedule. Caller starts this in a goroutine alongside World.Run
// (typically next to RunSceneQuoteSweep / RunReactorEvaluator /
// RunLocomotionTicker); returns when ctx is cancelled. The first
// sweep is kicked immediately so an entry whose ExpiresAt is already
// in the past at startup doesn't wait a full cadence interval before
// being flipped.
func RunPayLedgerSweep(ctx context.Context, w *World) {
	_, err := w.SendContext(ctx, kickPayLedgerSweep())
	if err != nil && ctx.Err() == nil {
		log.Printf("sim/pay_ledger: initial sweep arm failed: %v", err)
	}
	<-ctx.Done()
}

// kickPayLedgerSweep returns a Command whose Fn arms the first sweep
// on the world goroutine — mirrors kickSceneQuoteSweep.
func kickPayLedgerSweep() Command {
	return Command{
		Fn: func(w *World) (any, error) {
			armNextPayLedgerSweep(w)
			return nil, nil
		},
	}
}

// armNextPayLedgerSweep schedules the next sweep after one cadence
// interval. MUST be called from inside a Command.Fn — touches
// w.payLedgerSweep.scheduled without coordination.
//
// Coalescing: no-op when a sweep is already scheduled. The flag
// clears at the start of the scheduled Fn (evaluatePayLedgerAndRearm),
// so a re-arm during that Fn queues the next sweep rather than
// no-opping.
func armNextPayLedgerSweep(w *World) {
	if w.payLedgerSweep.scheduled {
		return
	}
	w.payLedgerSweep.scheduled = true
	cadence := effectivePayLedgerSweepCadence(w.Settings)
	time.AfterFunc(cadence, func() { fireScheduledPayLedgerSweep(w) })
}

// fireScheduledPayLedgerSweep is the AfterFunc callback body.
// Factored out so tests can drive the post-shutdown path directly
// (matches fireScheduledSceneQuoteSweep).
//
// Uses LifecycleContext so a shutdown-while-armed unblocks SendContext
// instead of deadlocking on a send to a dead cmds channel.
func fireScheduledPayLedgerSweep(w *World) {
	ctx := w.LifecycleContext()
	if ctx.Err() != nil {
		// Shutdown raced us. Clearing the flag would race with the
		// world goroutine; fresh worlds come from LoadWorld / NewWorld,
		// so a post-shutdown stale flag has no effect.
		return
	}
	_, err := w.SendContext(ctx, evaluatePayLedgerAndRearm(time.Now()))
	if err != nil && ctx.Err() == nil {
		log.Printf("sim/pay_ledger: scheduled sweep failed: %v", err)
	}
}

// evaluatePayLedgerAndRearm clears the scheduled flag, runs one sweep,
// and re-arms — all in one Fn on the world goroutine. Clearing the
// flag first means the re-arm starts a fresh chain rather than seeing
// the still-set flag and no-opping.
func evaluatePayLedgerAndRearm(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			w.payLedgerSweep.scheduled = false
			res, err := EvaluatePayLedgerSweep(now).Fn(w)
			armNextPayLedgerSweep(w)
			return res, err
		},
	}
}

// EvaluatePayLedgerSweep returns a Command that flips every pending
// PayLedgerEntry past its ExpiresAt to the Expired terminal state and
// emits PayWithItemResolved{TerminalState: Expired} + projects to the
// sink for each. Exposed as a Command (not just an internal Fn) so
// tests can drive sweeps deterministically without the AfterFunc
// timing chain.
//
// Iteration order is sorted by LedgerID so PayWithItemResolved events
// emit in a stable order — important for replay tests and admin trace
// readability when a burst of entries expire at the same sweep tick.
func EvaluatePayLedgerSweep(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if len(w.PayLedger) == 0 {
				return nil, nil
			}
			// Collect first, mutate after — w.emit dispatches
			// subscribers synchronously and a subscriber could in
			// principle mutate the PayLedger map; iterating while
			// mutating is unsafe even on the single world goroutine.
			expired := make([]LedgerID, 0)
			for id, e := range w.PayLedger {
				if e == nil || e.State != PayLedgerStatePending {
					continue
				}
				if e.ExpiresAt.IsZero() {
					continue
				}
				if now.Before(e.ExpiresAt) {
					continue
				}
				expired = append(expired, id)
			}
			if len(expired) == 0 {
				return nil, nil
			}
			sort.Slice(expired, func(i, j int) bool { return expired[i] < expired[j] })
			for _, id := range expired {
				e, ok := w.PayLedger[id]
				if !ok || e == nil || e.State != PayLedgerStatePending {
					// Subscriber from an earlier expire in this batch
					// could have flipped this one already (unlikely
					// but defensive — matches the scene-quote sweep
					// posture).
					continue
				}
				finalizePayLedgerTerminal(w, e, PayTerminalStateExpired, "", now)
			}
			return nil, nil
		},
	}
}
