package cascade

import (
	"context"
	"log"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// idle_backstop.go — engine-injected liveness for NPCs no other warrant
// has engaged. Replaces v1's chronicler-attend-to dispatch role; design
// pass documented in shared/notes/codebase/salem-engine-v2/cascade.
//
// The cascade slice owns the goroutine driver + cadence; the
// criterion + scope + stamp policy lives in sim/idle_backstop_commands.go
// as the substrate Command sim.EvaluateIdleBackstop. Same split as
// consolidation (sim.FindConsolidationCandidates + sim.ApplyConsolidation
// in the substrate; sweep goroutine here).
//
// Lifecycle:
//
//   RegisterIdleBackstop(ctx, w)
//   └─> go runIdleBackstopSweep(ctx, w)
//        ├─> immediate first sweep (no initial-interval wait)
//        └─> time.Ticker @ IdleBackstopSweepInterval until ctx.Done
//
// Failure modes:
//
//   - World SendContext error → log + return (sweep is shut down and
//     the world goroutine is gone; nothing to do).
//   - Other Command errors are not raised by EvaluateIdleBackstop's Fn
//     today; the only failure paths surface via the telemetry struct,
//     not error returns.

// RegisterIdleBackstop spawns the idle-backstop sweep goroutine. The
// goroutine returns when ctx is cancelled. Call once at world startup;
// order relative to RegisterEncounter / RegisterConsolidation / the
// tick-handler registrations / substrate runners doesn't matter
// functionally, but keep the registrations grouped for readability.
//
// Panics on nil w to fail fast at wiring time rather than silently
// no-op.
func RegisterIdleBackstop(ctx context.Context, w *sim.World) {
	if w == nil {
		panic("cascade: RegisterIdleBackstop requires a non-nil world")
	}
	go runIdleBackstopSweep(ctx, w)
}

// runIdleBackstopSweep is the goroutine body. Runs an immediate first
// sweep on entry (so an actor past threshold at world startup doesn't
// have to wait a full cadence interval — though World.LoadedAt's
// cold-start anchor makes the past-threshold-at-startup case rare in
// practice), then ticks at IdleBackstopSweepInterval.
func runIdleBackstopSweep(ctx context.Context, w *sim.World) {
	interval := readSweepInterval(ctx, w)
	if ctx.Err() != nil {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Immediate first sweep.
	runOneIdleBackstopSweep(ctx, w)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOneIdleBackstopSweep(ctx, w)
		}
	}
}

// runOneIdleBackstopSweep executes one EvaluateIdleBackstop pass on the
// world goroutine. The whole scan runs inside a single Command.Fn —
// reading actor state and stamping warrants without inter-step
// SendContext.
//
// Honors ctx cancellation: a shutdown during a sweep returns from the
// SendContext promptly without burning the rest of the goroutine.
func runOneIdleBackstopSweep(ctx context.Context, w *sim.World) {
	if ctx.Err() != nil {
		return
	}
	now := time.Now().UTC()
	res, err := w.SendContext(ctx, sim.EvaluateIdleBackstop(now))
	if err != nil {
		// Don't log on context cancellation — that's a normal shutdown
		// path, not a failure.
		if ctx.Err() == nil {
			log.Printf("cascade/idle_backstop: evaluate: %v", err)
		}
		return
	}
	tm, ok := res.(sim.IdleBackstopTelemetry)
	if !ok {
		log.Printf("cascade/idle_backstop: evaluate returned %T, want sim.IdleBackstopTelemetry", res)
		return
	}
	if tm.Stamped > 0 {
		log.Printf("cascade/idle_backstop: stamped=%d skipped_scope=%d skipped_recent=%d skipped_warranted=%d skipped_in_flight=%d",
			tm.Stamped, tm.SkippedScope, tm.SkippedRecentlyTicked, tm.SkippedWarranted, tm.SkippedTickInFlight)
	}
}

// defaultIdleBackstopSweepInterval is the fallback cadence when
// WorldSettings.IdleBackstopSweepInterval is unset. 5 min — detection
// latency ≤ this interval against the per-actor threshold (default
// 30 min in sim/reactor.go). Lives in cascade rather than sim because
// cascade owns the goroutine driver; sim only knows the per-actor
// criterion.
const defaultIdleBackstopSweepInterval = 5 * time.Minute

// readSweepInterval reads WorldSettings.IdleBackstopSweepInterval via a
// context-aware Command (the settings live on world-goroutine-owned
// state); falls back to defaultIdleBackstopSweepInterval when unset
// or when the read can't complete. Called once at sweep startup; if
// Settings changes mid-run, the new value takes effect on the next
// process start. Production tuning is intended to happen via
// environment config + restart, not hot-reload.
//
// Must be SendContext, not Send: this runs before the goroutine
// reaches its ctx.Done()-aware select loop. If the world isn't yet
// running (registration ordering off, shutdown racing startup), a
// plain Send blocks forever and the goroutine is unkillable. The
// SendContext + caller-side ctx.Err() check after return give a
// clean exit path when registration runs against a world that's
// already shutting down. (R1 fix.)
func readSweepInterval(ctx context.Context, w *sim.World) time.Duration {
	res, err := w.SendContext(ctx, sim.Command{Fn: func(world *sim.World) (any, error) {
		interval := world.Settings.IdleBackstopSweepInterval
		if interval <= 0 {
			interval = defaultIdleBackstopSweepInterval
		}
		return interval, nil
	}})
	if err != nil {
		// ctx cancelled, world not running, or shutting down. The
		// caller (runIdleBackstopSweep) checks ctx.Err() after this
		// returns and bails before installing the ticker. A non-zero
		// fallback is still safer than 0 (time.NewTicker would panic).
		return defaultIdleBackstopSweepInterval
	}
	interval, ok := res.(time.Duration)
	if !ok || interval <= 0 {
		return defaultIdleBackstopSweepInterval
	}
	return interval
}
