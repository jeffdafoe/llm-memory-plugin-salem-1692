package cascade

import (
	"context"
	"log"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// red_need_backstop.go — ZBBS-HOME-363. The fast, cost-paced companion to
// the hourly needs-tick re-warrant: it re-engages an NPC sitting on an
// unresolved red need that has gone idle, instead of leaving it frozen until
// the next game-hour boundary (needs tick) or the 30-min idle backstop.
//
// Same split as the idle backstop (and consolidation): the criterion +
// scope + the per-actor exponential backoff that bounds LLM cost live in the
// substrate Command sim.EvaluateRedNeedBackstop
// (engine/sim/red_need_backstop_commands.go); this slice owns only the
// goroutine driver + sweep cadence.
//
// Cadence is the idle backstop's, just faster: a short sweep interval
// (default 30 s) sets the detection latency for a newly-red actor, while the
// PER-ACTOR backoff in the Command — not the sweep rate — is what bounds
// cost for a stuck actor. The sweep itself is cheap: per-actor field reads on
// the world goroutine, no allocations.
//
// Lifecycle (mirrors RunIdleBackstopSweep):
//
//   RegisterRedNeedBackstop(ctx, w)
//   └─> go runRedNeedBackstopSweep(ctx, w)
//        ├─> immediate first sweep (no initial-interval wait)
//        └─> time.Ticker @ RedNeedBackstopSweepInterval until ctx.Done

// RegisterRedNeedBackstop spawns the red-need backstop sweep goroutine. The
// goroutine returns when ctx is cancelled. Call once at world startup;
// ordering relative to the other Register* helpers doesn't matter
// functionally. Panics on nil w to fail fast at wiring time.
func RegisterRedNeedBackstop(ctx context.Context, w *sim.World) {
	if w == nil {
		panic("cascade: RegisterRedNeedBackstop requires a non-nil world")
	}
	go runRedNeedBackstopSweep(ctx, w)
}

// runRedNeedBackstopSweep is the goroutine body. An immediate first sweep on
// entry (so an actor already red at startup doesn't wait a full cadence
// interval), then ticks at RedNeedBackstopSweepInterval.
func runRedNeedBackstopSweep(ctx context.Context, w *sim.World) {
	interval := readRedNeedSweepInterval(ctx, w)
	if ctx.Err() != nil {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	runOneRedNeedBackstopSweep(ctx, w)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.BeatTicker("red_need_backstop")
			runOneRedNeedBackstopSweep(ctx, w)
		}
	}
}

// runOneRedNeedBackstopSweep executes one EvaluateRedNeedBackstop pass on the
// world goroutine. Honors ctx cancellation: a shutdown during a sweep returns
// from SendContext promptly without burning the rest of the goroutine.
func runOneRedNeedBackstopSweep(ctx context.Context, w *sim.World) {
	if ctx.Err() != nil {
		return
	}
	now := time.Now().UTC()
	res, err := w.SendContext(ctx, sim.EvaluateRedNeedBackstop(now))
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("cascade/red_need_backstop: evaluate: %v", err)
		}
		return
	}
	tm, ok := res.(sim.RedNeedBackstopTelemetry)
	if !ok {
		log.Printf("cascade/red_need_backstop: evaluate returned %T, want sim.RedNeedBackstopTelemetry", res)
		return
	}
	if tm.Stamped > 0 || tm.SkippedStampDeclined > 0 {
		log.Printf("cascade/red_need_backstop: stamped=%d skipped_scope=%d skipped_no_red=%d skipped_warranted=%d skipped_in_flight=%d skipped_backoff=%d skipped_declined=%d",
			tm.Stamped, tm.SkippedScope, tm.SkippedNoRedNeed, tm.SkippedWarranted, tm.SkippedTickInFlight, tm.SkippedBackoff, tm.SkippedStampDeclined)
	}
}

// defaultRedNeedBackstopSweepInterval is the fallback cadence when
// WorldSettings.RedNeedBackstopSweepInterval is unset. 30 s — detection
// latency for a newly-red actor is ≤ this; the per-actor exponential backoff
// (base default 90 s in sim/reactor.go) is what bounds repeat cost. Lives in
// cascade rather than sim because cascade owns the goroutine driver.
const defaultRedNeedBackstopSweepInterval = 30 * time.Second

// readRedNeedSweepInterval reads WorldSettings.RedNeedBackstopSweepInterval
// via a context-aware Command (settings live on world-goroutine-owned state);
// falls back to the default when unset or unreadable. SendContext (not Send)
// for the same reason as the idle backstop: this runs before the goroutine
// reaches its ctx.Done()-aware select loop, so a plain Send against a
// not-yet-running or shutting-down world would block forever.
func readRedNeedSweepInterval(ctx context.Context, w *sim.World) time.Duration {
	res, err := w.SendContext(ctx, sim.Command{Fn: func(world *sim.World) (any, error) {
		interval := world.Settings.RedNeedBackstopSweepInterval
		if interval <= 0 {
			interval = defaultRedNeedBackstopSweepInterval
		}
		return interval, nil
	}})
	if err != nil {
		return defaultRedNeedBackstopSweepInterval
	}
	interval, ok := res.(time.Duration)
	if !ok || interval <= 0 {
		return defaultRedNeedBackstopSweepInterval
	}
	return interval
}
