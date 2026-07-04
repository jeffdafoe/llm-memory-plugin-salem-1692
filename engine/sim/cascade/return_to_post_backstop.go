package cascade

import (
	"context"
	"log"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// return_to_post_backstop.go — LLM-268. The goroutine driver for the off-post-
// laboring-worker backstop, the sibling of seek_work_backstop.go. The criterion +
// scope + per-actor exponential backoff live in the substrate Command
// sim.EvaluateReturnToPostBackstop (engine/sim/return_to_post_backstop_commands.go);
// this slice owns only the goroutine + sweep cadence.
//
// Cadence mirrors the seek-work backstop: a short sweep interval (30 s) sets the
// detection latency for a worker who just wandered off her post, while the
// PER-ACTOR backoff in the Command — not the sweep rate — bounds LLM cost for a
// worker who stays off-post. Fixed interval constant rather than a WorldSettings
// knob (none plumbed yet — add one if live tuning is wanted).

// RegisterReturnToPostBackstop spawns the return-to-post backstop sweep goroutine.
// The goroutine returns when ctx is cancelled. Call once at world startup. Panics
// on nil w to fail fast at wiring time.
func RegisterReturnToPostBackstop(ctx context.Context, w *sim.World) {
	if w == nil {
		panic("cascade: RegisterReturnToPostBackstop requires a non-nil world")
	}
	go runReturnToPostBackstopSweep(ctx, w)
}

// runReturnToPostBackstopSweep is the goroutine body. An immediate first sweep on
// entry (so a worker already off-post at startup doesn't wait a full cadence
// interval), then ticks at returnToPostBackstopSweepInterval.
func runReturnToPostBackstopSweep(ctx context.Context, w *sim.World) {
	if ctx.Err() != nil {
		return
	}
	ticker := time.NewTicker(returnToPostBackstopSweepInterval)
	defer ticker.Stop()

	runOneReturnToPostBackstopSweep(ctx, w)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.BeatTicker("return_to_post_backstop")
			runOneReturnToPostBackstopSweep(ctx, w)
		}
	}
}

// runOneReturnToPostBackstopSweep executes one EvaluateReturnToPostBackstop pass on
// the world goroutine. Honors ctx cancellation.
func runOneReturnToPostBackstopSweep(ctx context.Context, w *sim.World) {
	if ctx.Err() != nil {
		return
	}
	now := time.Now().UTC()
	res, err := w.SendContext(ctx, sim.EvaluateReturnToPostBackstop(now))
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("cascade/return_to_post_backstop: evaluate: %v", err)
		}
		return
	}
	tm, ok := res.(sim.ReturnToPostBackstopTelemetry)
	if !ok {
		log.Printf("cascade/return_to_post_backstop: evaluate returned %T, want sim.ReturnToPostBackstopTelemetry", res)
		return
	}
	if tm.Stamped > 0 || tm.SkippedStampDeclined > 0 {
		log.Printf("cascade/return_to_post_backstop: stamped=%d skipped_scope=%d skipped_not_eligible=%d skipped_red_need=%d skipped_warranted=%d skipped_in_flight=%d skipped_backoff=%d skipped_declined=%d",
			tm.Stamped, tm.SkippedScope, tm.SkippedNotEligible, tm.SkippedRedNeed, tm.SkippedWarranted, tm.SkippedTickInFlight, tm.SkippedBackoff, tm.SkippedStampDeclined)
	}
}

// returnToPostBackstopSweepInterval is the sweep cadence — the detection latency
// for a worker who just wandered off her post. 30 s, matching the seek-work
// backstop; the per-actor exponential backoff (base 90 s in the Command) bounds
// repeat cost. The sweep itself is cheap: per-actor field reads on the world
// goroutine.
const returnToPostBackstopSweepInterval = 30 * time.Second
