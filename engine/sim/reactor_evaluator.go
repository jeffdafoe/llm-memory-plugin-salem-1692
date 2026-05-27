package sim

import (
	"context"
	"log"
	"time"
)

// Reactor evaluator — the periodic goroutine that drives the scan of
// warranted actors. Uses an AfterFunc self-rearm chain (not a permanent
// time.Ticker) so the evaluator can't backlog commands if the world
// goroutine is busy or shutting down.
//
// Lifecycle:
//
//   RunReactorEvaluator(ctx, w)        // started by caller in a goroutine
//   └─> armNextEvaluation(w)            // schedules the first AfterFunc
//        └─> [cadence] fire callback
//             └─> w.SendContext(EvaluateReactors(now))
//                  └─> Fn body scans + emits + clears in-flight scheduled flag
//                       └─> armNextEvaluation(w)  // re-arm at end of Fn
//
// armNextEvaluation runs inside Command.Fn — the scheduled flag in
// reactorEvaluatorState is read/written exclusively from the world
// goroutine, so no mutex is needed. The flag prevents double-arming if
// two paths try to schedule a follow-up in the same Fn.
//
// Shutdown: when ctx is cancelled, the AfterFunc callback's SendContext
// returns ctx.Err() (LifecycleContext is already cancelled) and the
// callback exits without firing another command. RunReactorEvaluator
// blocks on ctx.Done() so the caller can use it as a goroutine
// lifecycle handle.

// RunReactorEvaluator owns the reactor evaluator's periodic schedule.
// Caller starts this in a goroutine alongside World.Run. Returns when
// ctx is cancelled.
//
// Kicks off the first AfterFunc immediately so warrants stamped before
// Run started don't wait a full cadence cycle to fire. The cadence comes
// from World.Settings.ReactorEvaluatorCadence (falls back to
// defaultReactorEvaluatorCadence if unset).
func RunReactorEvaluator(ctx context.Context, w *World) {
	// Initial arm via the same path the periodic chain uses. Sending
	// through SendContext routes through the world goroutine, so the
	// scheduled-flag check runs there.
	_, err := w.SendContext(ctx, kickReactorEvaluator())
	if err != nil && ctx.Err() == nil {
		log.Printf("sim/reactor: initial evaluator arm failed: %v", err)
	}
	<-ctx.Done()
}

// kickReactorEvaluator returns a Command whose Fn calls armNextEvaluation.
// Exists so the initial arm goes through the command channel (consistent
// with every other state mutation) rather than touching reactorEval
// directly from RunReactorEvaluator's goroutine.
func kickReactorEvaluator() Command {
	return Command{
		Fn: func(w *World) (any, error) {
			armNextEvaluation(w)
			return nil, nil
		},
	}
}

// armNextEvaluation schedules the next EvaluateReactors command after one
// cadence interval. Must be called from inside a Command.Fn — touches
// w.reactorEval.scheduled without coordination.
//
// Coalescing: if a fire is already scheduled, this is a no-op. The single
// scheduled flag clears when the AfterFunc body actually invokes
// SendContext on the world (start of the callback), so re-arms during
// the body's Fn run see scheduled=false and queue the next one.
//
// Cadence comes from settings each time it's armed — config changes take
// effect on the next re-arm.
func armNextEvaluation(w *World) {
	if w.reactorEval.scheduled {
		return
	}
	w.reactorEval.scheduled = true
	cadence := w.Settings.ReactorEvaluatorCadence
	if cadence <= 0 {
		cadence = defaultReactorEvaluatorCadence
	}
	time.AfterFunc(cadence, func() { fireScheduledEvaluation(w) })
}

// fireScheduledEvaluation is the body of the AfterFunc callback. Factored
// out so tests can drive the post-shutdown path synchronously (same
// pattern as world_phase.go's fireScheduledFlip).
//
// Uses LifecycleContext so a shutdown-while-timer-armed unblocks
// SendContext rather than deadlocking on a send to a dead cmds channel.
// Pulled fresh each call — the world may be on its second or third Run
// in some future test setup, even if production is run-once today.
func fireScheduledEvaluation(w *World) {
	ctx := w.LifecycleContext()
	if ctx.Err() != nil {
		// Shutdown raced us. Clearing the flag would race with the world
		// goroutine; LoadWorld / NewWorld create fresh worlds so a
		// post-shutdown stale flag has no effect anyway.
		return
	}
	w.beatTicker("reactor")
	_, err := w.SendContext(ctx, evaluateAndRearm(time.Now()))
	if err != nil && ctx.Err() == nil {
		log.Printf("sim/reactor: scheduled evaluation failed: %v", err)
	}
}

// evaluateAndRearm wraps EvaluateReactors with a guarantee that the
// scheduled flag clears before the scan runs, so the re-arm at the end of
// the scan correctly queues the NEXT evaluation. (EvaluateReactors itself
// re-arms; clearing here means that re-arm starts a fresh chain rather
// than seeing the still-set flag and no-opping.)
func evaluateAndRearm(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			w.reactorEval.scheduled = false
			// Inline rather than chaining commands: we're already inside
			// the world goroutine, so just run the scan and the re-arm
			// in one pass.
			res, err := EvaluateReactors(now).Fn(w)
			return res, err
		},
	}
}
