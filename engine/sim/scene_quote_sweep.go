package sim

import (
	"context"
	"log"
	"sort"
	"time"
)

// scene_quote_sweep.go — Phase 3 PR S3 aging sweep.
//
// RunSceneQuoteSweep periodically scans World.Quotes for active
// entries whose ExpiresAt has passed and flips them to the terminal
// Expired state. Same coalesced AfterFunc self-rearm shape as the
// PR 2 reactor evaluator and PR 4 locomotion ticker — see
// reactor_evaluator.go for the canonical lifecycle commentary.
//
// Lifecycle:
//
//   RunSceneQuoteSweep(ctx, w)
//   └─> kickSceneQuoteSweep            // initial arm via the cmd channel
//        └─> armNextSceneQuoteSweep    // schedules the first AfterFunc
//             └─> [interval] fireScheduledSceneQuoteSweep
//                  └─> SendContext(evaluateSceneQuotesAndRearm(now))
//                       └─> Fn: clear flag, run scan, re-arm

// RunSceneQuoteSweep owns the scene-quote aging sweep's periodic
// schedule. Caller starts this in a goroutine alongside World.Run
// (typically next to RunReactorEvaluator and RunLocomotionTicker);
// returns when ctx is cancelled. The first sweep is kicked
// immediately so a quote stamped just before the sweeper started
// doesn't wait a full cadence interval before its expiry boundary
// is evaluated.
func RunSceneQuoteSweep(ctx context.Context, w *World) {
	_, err := w.SendContext(ctx, kickSceneQuoteSweep())
	if err != nil && ctx.Err() == nil {
		log.Printf("sim/scene_quote: initial sweep arm failed: %v", err)
	}
	<-ctx.Done()
}

// kickSceneQuoteSweep returns a Command whose Fn arms the first sweep
// on the world goroutine — same pattern as kickLocomotionTicker.
func kickSceneQuoteSweep() Command {
	return Command{
		Fn: func(w *World) (any, error) {
			armNextSceneQuoteSweep(w)
			return nil, nil
		},
	}
}

// armNextSceneQuoteSweep schedules the next sweep after one cadence
// interval. MUST be called from inside a Command.Fn — touches
// w.sceneQuoteSweep.scheduled without coordination.
//
// Coalescing: no-op when a sweep is already scheduled. The flag
// clears at the start of the scheduled Fn (evaluateSceneQuotesAndRearm),
// so a re-arm during that Fn queues the next sweep rather than
// no-opping.
func armNextSceneQuoteSweep(w *World) {
	if w.sceneQuoteSweep.scheduled {
		return
	}
	w.sceneQuoteSweep.scheduled = true
	cadence := effectiveSceneQuoteSweepCadence(w.Settings)
	time.AfterFunc(cadence, func() { fireScheduledSceneQuoteSweep(w) })
}

// fireScheduledSceneQuoteSweep is the AfterFunc callback body.
// Factored out so tests can drive the post-shutdown path directly
// (matches fireScheduledLocomotionTick).
//
// Uses LifecycleContext so a shutdown-while-armed unblocks
// SendContext instead of deadlocking on a send to a dead cmds channel.
func fireScheduledSceneQuoteSweep(w *World) {
	ctx := w.LifecycleContext()
	if ctx.Err() != nil {
		// Shutdown raced us. Clearing the flag would race with the
		// world goroutine; fresh worlds come from LoadWorld /
		// NewWorld, so a post-shutdown stale flag has no effect.
		return
	}
	_, err := w.SendContext(ctx, evaluateSceneQuotesAndRearm(time.Now()))
	if err != nil && ctx.Err() == nil {
		log.Printf("sim/scene_quote: scheduled sweep failed: %v", err)
	}
}

// evaluateSceneQuotesAndRearm clears the scheduled flag, runs one
// sweep, and re-arms — all in one Fn on the world goroutine.
// Clearing the flag first means the re-arm starts a fresh chain
// rather than seeing the still-set flag and no-opping.
func evaluateSceneQuotesAndRearm(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			w.sceneQuoteSweep.scheduled = false
			res, err := EvaluateSceneQuoteSweep(now).Fn(w)
			armNextSceneQuoteSweep(w)
			return res, err
		},
	}
}

// EvaluateSceneQuoteSweep returns a Command that flips every active
// SceneQuote past its ExpiresAt to the Expired terminal state and
// emits SceneQuoteExpired{Reason: "ttl"} for each. Exposed as a
// Command (not just an internal Fn) so tests can drive sweeps
// deterministically without the AfterFunc timing chain.
//
// Iteration order is sorted by QuoteID so SceneQuoteExpired events
// emit in a stable order — important for replay tests and for
// admin trace readability when a burst of quotes expire at the
// same sweep tick.
func EvaluateSceneQuoteSweep(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if len(w.Quotes) == 0 {
				return nil, nil
			}
			// Collect first, mutate after — w.emit dispatches
			// subscribers synchronously and a subscriber could
			// in principle mutate the Quotes map; iterating
			// while mutating is unsafe even on the single
			// world goroutine.
			expired := make([]QuoteID, 0)
			for id, q := range w.Quotes {
				if q == nil || q.State != SceneQuoteStateActive {
					continue
				}
				if q.ExpiresAt.IsZero() {
					continue
				}
				if now.Before(q.ExpiresAt) {
					continue
				}
				expired = append(expired, id)
			}
			if len(expired) == 0 {
				return nil, nil
			}
			sort.Slice(expired, func(i, j int) bool { return expired[i] < expired[j] })
			for _, id := range expired {
				q, ok := w.Quotes[id]
				if !ok || q == nil || q.State != SceneQuoteStateActive {
					// Subscriber from an earlier expire in this
					// batch could have flipped this one already
					// (unlikely but defensive).
					continue
				}
				scene := w.Scenes[q.SceneID]
				flipQuoteTerminal(w, scene, q, SceneQuoteStateExpired, SceneQuoteExpiredReasonTTL, now)
			}
			return nil, nil
		},
	}
}
