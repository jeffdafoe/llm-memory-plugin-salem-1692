package cascade

import (
	"context"
	"log"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// hired_repair_backstop.go — LLM-280. The goroutine driver for the hired-worker
// repair backstop, the sibling of return_to_post_backstop.go. The criterion + scope +
// per-actor exponential backoff live in the substrate Command
// sim.EvaluateHiredRepairBackstop (engine/sim/hired_repair_backstop_commands.go);
// this slice owns only the goroutine + sweep cadence.
//
// Cadence mirrors the return-to-post backstop: a short sweep interval (30 s) sets the
// detection latency for a hired worker who just declined her one-shot repair wake,
// while the PER-ACTOR backoff in the Command — not the sweep rate — bounds LLM cost
// for a worker who keeps declining. Fixed interval constant rather than a
// WorldSettings knob (none plumbed yet — add one if live tuning is wanted).

// RegisterHiredRepairBackstop spawns the hired-repair backstop sweep goroutine. The
// goroutine returns when ctx is cancelled. Call once at world startup. Panics on nil
// w to fail fast at wiring time.
func RegisterHiredRepairBackstop(ctx context.Context, w *sim.World) {
	if w == nil {
		panic("cascade: RegisterHiredRepairBackstop requires a non-nil world")
	}
	// Cadence contract, declared before the goroutine starts (LLM-395).
	w.RegisterTicker("hired_repair_backstop", hiredRepairBackstopSweepInterval)
	go runHiredRepairBackstopSweep(ctx, w)
}

// runHiredRepairBackstopSweep is the goroutine body. An immediate first sweep on
// entry (so a worker already shelved-but-repair-ready at startup doesn't wait a full
// cadence interval), then ticks at hiredRepairBackstopSweepInterval.
func runHiredRepairBackstopSweep(ctx context.Context, w *sim.World) {
	if ctx.Err() != nil {
		return
	}
	ticker := time.NewTicker(hiredRepairBackstopSweepInterval)
	defer ticker.Stop()

	runOneHiredRepairBackstopSweep(ctx, w)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.BeatTicker("hired_repair_backstop")
			runOneHiredRepairBackstopSweep(ctx, w)
		}
	}
}

// runOneHiredRepairBackstopSweep executes one EvaluateHiredRepairBackstop pass on the
// world goroutine. Honors ctx cancellation.
func runOneHiredRepairBackstopSweep(ctx context.Context, w *sim.World) {
	if ctx.Err() != nil {
		return
	}
	now := time.Now().UTC()
	res, err := w.SendContext(ctx, sim.EvaluateHiredRepairBackstop(now))
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("cascade/hired_repair_backstop: evaluate: %v", err)
		}
		return
	}
	tm, ok := res.(sim.HiredRepairBackstopTelemetry)
	if !ok {
		log.Printf("cascade/hired_repair_backstop: evaluate returned %T, want sim.HiredRepairBackstopTelemetry", res)
		return
	}
	if tm.Stamped > 0 || tm.SkippedStampDeclined > 0 {
		log.Printf("cascade/hired_repair_backstop: stamped=%d skipped_scope=%d skipped_not_eligible=%d skipped_red_need=%d skipped_warranted=%d skipped_in_flight=%d skipped_backoff=%d skipped_declined=%d",
			tm.Stamped, tm.SkippedScope, tm.SkippedNotEligible, tm.SkippedRedNeed, tm.SkippedWarranted, tm.SkippedTickInFlight, tm.SkippedBackoff, tm.SkippedStampDeclined)
	}
}

// hiredRepairBackstopSweepInterval is the sweep cadence — the detection latency for a
// hired worker who just declined her repair wake. 30 s, matching the return-to-post
// backstop; the per-actor exponential backoff (base 90 s in the Command) bounds
// repeat cost. The sweep itself is cheap: per-actor field reads on the world
// goroutine.
const hiredRepairBackstopSweepInterval = 30 * time.Second
