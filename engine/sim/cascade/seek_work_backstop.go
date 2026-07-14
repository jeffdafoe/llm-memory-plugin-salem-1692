package cascade

import (
	"context"
	"log"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// seek_work_backstop.go — LLM-141/168. The goroutine driver for the idle-
// workless-worker backstop, the sibling of red_need_backstop.go. The criterion + scope +
// per-actor exponential backoff live in the substrate Command
// sim.EvaluateSeekWorkBackstop (engine/sim/seek_work_backstop_commands.go);
// this slice owns only the goroutine + sweep cadence.
//
// Cadence mirrors the red-need backstop: a short sweep interval (30 s) sets the
// detection latency for a newly-workless idle worker, while the PER-ACTOR backoff
// in the Command — not the sweep rate — is what bounds LLM cost for a worker
// who can never find work. Unlike the red-need driver this uses a fixed
// interval constant rather than a WorldSettings knob (none plumbed yet — add
// one if live tuning is wanted).

// RegisterSeekWorkBackstop spawns the seek-work backstop sweep goroutine. The
// goroutine returns when ctx is cancelled. Call once at world startup. Panics
// on nil w to fail fast at wiring time.
func RegisterSeekWorkBackstop(ctx context.Context, w *sim.World) {
	if w == nil {
		panic("cascade: RegisterSeekWorkBackstop requires a non-nil world")
	}
	// Cadence contract, declared before the goroutine starts (LLM-395).
	w.RegisterTicker("seek_work_backstop", seekWorkBackstopSweepInterval)
	go runSeekWorkBackstopSweep(ctx, w)
}

// runSeekWorkBackstopSweep is the goroutine body. An immediate first sweep on
// entry (so a worker already workless at startup doesn't wait a full cadence
// interval), then ticks at seekWorkBackstopSweepInterval.
func runSeekWorkBackstopSweep(ctx context.Context, w *sim.World) {
	if ctx.Err() != nil {
		return
	}
	ticker := time.NewTicker(seekWorkBackstopSweepInterval)
	defer ticker.Stop()

	runOneSeekWorkBackstopSweep(ctx, w)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.BeatTicker("seek_work_backstop")
			runOneSeekWorkBackstopSweep(ctx, w)
		}
	}
}

// runOneSeekWorkBackstopSweep executes one EvaluateSeekWorkBackstop pass on the
// world goroutine. Honors ctx cancellation.
func runOneSeekWorkBackstopSweep(ctx context.Context, w *sim.World) {
	if ctx.Err() != nil {
		return
	}
	now := time.Now().UTC()
	res, err := w.SendContext(ctx, sim.EvaluateSeekWorkBackstop(now))
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("cascade/seek_work_backstop: evaluate: %v", err)
		}
		return
	}
	tm, ok := res.(sim.SeekWorkBackstopTelemetry)
	if !ok {
		log.Printf("cascade/seek_work_backstop: evaluate returned %T, want sim.SeekWorkBackstopTelemetry", res)
		return
	}
	if tm.Stamped > 0 || tm.SkippedStampDeclined > 0 {
		log.Printf("cascade/seek_work_backstop: stamped=%d skipped_scope=%d skipped_not_eligible=%d skipped_red_need=%d skipped_warranted=%d skipped_in_flight=%d skipped_backoff=%d skipped_declined=%d",
			tm.Stamped, tm.SkippedScope, tm.SkippedNotEligible, tm.SkippedRedNeed, tm.SkippedWarranted, tm.SkippedTickInFlight, tm.SkippedBackoff, tm.SkippedStampDeclined)
	}
}

// seekWorkBackstopSweepInterval is the sweep cadence — the detection latency
// for a newly-workless idle worker. 30 s, matching the red-need backstop default;
// the per-actor exponential backoff (base 90 s in the Command) bounds repeat
// cost. The sweep itself is cheap: per-actor field reads on the world goroutine.
const seekWorkBackstopSweepInterval = 30 * time.Second
