package cascade

import (
	"context"
	"log"
	"math/rand"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// visitor.go — Phase 3 Group A visitor cascade driver. Pumps
// sim.TickVisitorCascade on a configurable cadence; the substrate
// (engine/sim/visitor.go) owns the spawn / despawn / cleanup logic.
//
// Lifecycle:
//
//   RegisterVisitor(ctx, w)
//   └─> go runVisitorTicker(ctx, w)
//        ├─> immediate first tick (no initial-interval wait)
//        └─> time.Ticker @ VisitorTickInterval until ctx.Done
//
// Why off-world: the visitor tick reads a moderate amount of state
// (every actor, terrain bytes, walk grid build) but runs O(actors) — at
// Hannah scale (≤10 actors) this is microseconds. The substrate Command
// runs on the world goroutine for atomicity; the driver lives off-world
// only because the ticker itself can't run on the single goroutine.
//
// Failure modes:
//   - World SendContext error → log + return (sweep is shut down).
//   - TickVisitorCascade returns telemetry; the driver logs it when any
//     interesting field is non-zero.
//   - No idempotency guard against double-registration. Wiring guards
//     live at the registration site.

// defaultVisitorTickInterval is the fallback cadence when
// WorldSettings.VisitorTickInterval is zero. Matches v1's
// runServerTickOnce cadence the visitor handlers piggybacked on.
const defaultVisitorTickInterval = sim.DefaultVisitorTickInterval

// RegisterVisitor spawns the visitor cascade ticker goroutine. The
// goroutine returns when ctx is cancelled. Call once at world startup
// alongside the other cascade Register* helpers.
//
// Panics on nil w to fail fast at wiring time.
//
// Idempotency: registering twice would double-tick (two parallel rolls
// per real interval, so spawn rate doubles). Wiring guards live at the
// call site.
func RegisterVisitor(ctx context.Context, w *sim.World) {
	if w == nil {
		panic("cascade: RegisterVisitor requires a non-nil world")
	}
	go runVisitorTicker(ctx, w)
}

// runVisitorTicker is the goroutine body. Reads the configured interval,
// runs an immediate first tick, then ticks until ctx is cancelled.
//
// The rand source is seeded once at registration with the current
// monotonic time — deterministic enough that two parallel engine
// processes (split-brain bug case) wouldn't roll the same spawn-chance
// on the same tick. Per-tick reseeding is unnecessary and would prevent
// edge-tile shuffle from working as intended.
func runVisitorTicker(ctx context.Context, w *sim.World) {
	interval := readVisitorTickInterval(ctx, w)
	if ctx.Err() != nil {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	runOneVisitorTick(ctx, w, r)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.BeatTicker("visitor")
			runOneVisitorTick(ctx, w, r)
		}
	}
}

// runOneVisitorTick executes one TickVisitorCascade pass on the world
// goroutine and logs the telemetry when anything happened.
func runOneVisitorTick(ctx context.Context, w *sim.World, r *rand.Rand) {
	if ctx.Err() != nil {
		return
	}
	res, err := w.SendContext(ctx, sim.TickVisitorCascade(sim.VisitorTickInputs{
		Now:  time.Now().UTC(),
		Rand: r,
	}))
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("cascade/visitor: tick: %v", err)
		}
		return
	}
	tm, ok := res.(sim.VisitorCascadeTelemetry)
	if !ok {
		log.Printf("cascade/visitor: tick returned %T, want sim.VisitorCascadeTelemetry", res)
		return
	}
	if tm.Spawned > 0 || tm.DespawnsStarted > 0 || tm.CleanedUp > 0 ||
		tm.RoundsPaced > 0 || tm.CircuitToLodging > 0 {
		log.Printf("cascade/visitor: spawned=%d despawns=%d cleaned=%d rounds_paced=%d to_lodging=%d",
			tm.Spawned, tm.DespawnsStarted, tm.CleanedUp, tm.RoundsPaced, tm.CircuitToLodging)
	}
}

// readVisitorTickInterval reads WorldSettings.VisitorTickInterval via a
// context-aware Command (settings live on world-goroutine-owned state).
// Falls back to defaultVisitorTickInterval when unset or when the read
// can't complete. Called once at sweep startup; production tuning is
// intended to happen via environment config + restart, not hot-reload.
//
// Must be SendContext, not Send: this runs before the goroutine reaches
// its ctx.Done()-aware select loop. If the world isn't running (registration
// ordering off, shutdown racing startup), a plain Send blocks forever.
func readVisitorTickInterval(ctx context.Context, w *sim.World) time.Duration {
	res, err := w.SendContext(ctx, sim.Command{Fn: func(world *sim.World) (any, error) {
		interval := world.Settings.VisitorTickInterval
		if interval <= 0 {
			interval = defaultVisitorTickInterval
		}
		return interval, nil
	}})
	if err != nil {
		return defaultVisitorTickInterval
	}
	interval, ok := res.(time.Duration)
	if !ok || interval <= 0 {
		return defaultVisitorTickInterval
	}
	return interval
}
