package cascade

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// storm.go — world-level storm weather cascade slice (LLM-117 Half A).
// The sim-package primitives (ApplyWeatherChange / SeedWeatherClear, the
// WeatherClear / WeatherStorm tokens) live in engine/sim/weather.go;
// this file owns the long-running goroutine that drives the automatic
// storm cadence.
//
// Modeled on cascade/atmosphere.go, but mechanical: weather is a dice-
// free state machine, so unlike atmosphere this slice needs no llm.Client
// (the decision is "has enough time elapsed?", not prose). All timing
// state lives on World.Environment (Weather + LastWeatherChangeAt) — the
// sweep goroutine is stateless, so a single source of truth drives the
// DTO, the prompt, and the wire frame, and the decision is testable by
// advancing a clock against a constructed world.
//
// Lifecycle:
//
//	RegisterStorm(ctx, w)
//	└─> go runStormSweep(ctx, w)
//	     ├─> SeedWeatherClear (boot to clear, seed the change clock)
//	     ├─> time.Ticker @ stormSweepInterval until ctx.Done
//	     └─> each tick: decideStorm (read) → ApplyWeatherChange (apply)
//
// The state machine (decideStorm), per tick:
//
//   - storm held >= StormDuration            → clear   (NOT PC-gated; an
//     in-flight storm rides out its full duration even if the last PC
//     left — only the START is gated)
//   - clear held >= StormInterval AND a PC is present → storm
//   - otherwise                              → no transition
//
// PC-presence gate (acceptance criteria 2-4): an automatic storm only
// STARTS when at least one non-stale PC is present (the /pc/me 10s
// heartbeat primitive in engine/sim/pc_presence.go). Storms are a visual
// payoff — wasted on an empty village. The operator force-path (httpapi
// umbilical /weather) is deliberately NOT gated, so a storm can be summoned
// on an empty village for demo/testing.

const (
	// defaultStormInterval is the fallback gap between automatic storms
	// (clear → storm) when WorldSettings.StormInterval is unset. 3h.
	// Lives in cascade (not sim) for the same reason as
	// defaultAtmosphereRefreshInterval — cascade owns the goroutine driver.
	defaultStormInterval = 3 * time.Hour

	// defaultStormDuration is the fallback storm hold time (storm → clear)
	// when WorldSettings.StormDuration is unset. 15m.
	defaultStormDuration = 15 * time.Minute

	// stormSweepInterval is how often the sweep re-evaluates the weather
	// state machine. A const (not a setting), matching PCPresenceSweepInterval
	// — it sets only the detection latency for a storm start/clear boundary,
	// not the cadence itself (that's StormInterval / StormDuration). 30s is
	// fine granularity for a cosmetic ambient effect; the per-tick decision is
	// a handful of field reads on the world goroutine.
	stormSweepInterval = 30 * time.Second
)

// RegisterStorm spawns the storm weather sweep goroutine. The goroutine
// returns when ctx is cancelled. Call once at world startup (from
// RegisterProductionCascades); order relative to the other Register*
// helpers doesn't matter.
//
// No llm.Client parameter — weather is mechanical (see file header).
//
// Panics on nil w to fail fast at wiring time rather than silently no-op.
func RegisterStorm(ctx context.Context, w *sim.World) {
	if w == nil {
		panic("cascade: RegisterStorm requires a non-nil world")
	}
	go runStormSweep(ctx, w)
}

// runStormSweep is the goroutine body. Resets weather to clear on entry
// (SeedWeatherClear — discards a persisted mid-storm weather and seeds the
// change clock so the first auto-storm waits a full StormInterval), then
// ticks at stormSweepInterval running the weather state machine until ctx
// is cancelled.
//
// Exported as a package-private symbol for tests; integration tests drive
// single decisions via runOneStormDecision directly.
func runStormSweep(ctx context.Context, w *sim.World) {
	// Boot to clear. SendContext so shutdown unblocks cleanly even if the
	// world goroutine has already exited.
	if _, err := w.SendContext(ctx, sim.SeedWeatherClear(time.Now().UTC())); err != nil {
		if ctx.Err() == nil {
			log.Printf("cascade/storm: boot seed failed: %v", err)
		}
		return
	}
	if ctx.Err() != nil {
		return
	}

	ticker := time.NewTicker(stormSweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.BeatTicker("storm")
			runOneStormDecision(ctx, w)
		}
	}
}

// runOneStormDecision runs one tick of the weather state machine: read the
// world to decide the next weather (decideStorm), and if a transition is
// due, apply it via the shared ApplyWeatherChange command — the same code
// the umbilical force-path runs. Two SendContext round-trips (decide, then
// apply) mirror atmosphere's fetch→apply split and keep the decision a pure
// read that's testable in isolation.
//
// Honors ctx cancellation between the round-trips so a shutdown mid-tick
// returns promptly.
func runOneStormDecision(ctx context.Context, w *sim.World) {
	if ctx.Err() != nil {
		return
	}
	now := time.Now().UTC()
	res, err := w.SendContext(ctx, decideStorm(now))
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("cascade/storm: decide failed: %v", err)
		}
		return
	}
	target, _ := res.(string)
	if target == "" {
		return // no transition this tick
	}
	if ctx.Err() != nil {
		return
	}
	if _, err := w.SendContext(ctx, sim.ApplyWeatherChange(target, now)); err != nil {
		if ctx.Err() == nil {
			log.Printf("cascade/storm: apply %q failed: %v", target, err)
		}
	}
}

// decideStorm returns a Command that reads the world and yields the weather
// to transition TO at `now`, or "" for no transition this tick. Pure read
// — it mutates nothing, so runOneStormDecision (and tests) can apply the
// result through the shared ApplyWeatherChange command.
//
// Runs on the world goroutine (it reads w.Settings, w.Environment, and
// w.Actors), so the actor scan for PC presence is race-safe.
func decideStorm(now time.Time) sim.Command {
	return sim.Command{Fn: func(w *sim.World) (any, error) {
		interval := w.Settings.StormInterval
		if interval <= 0 {
			interval = defaultStormInterval
		}
		duration := w.Settings.StormDuration
		if duration <= 0 {
			duration = defaultStormDuration
		}
		elapsed := now.Sub(w.Environment.LastWeatherChangeAt)

		if strings.TrimSpace(w.Environment.Weather) == sim.WeatherStorm {
			// In-flight storm: clear once it has held its full duration.
			// Not PC-gated — a storm that started while a PC was present
			// rides out even if the last PC has since left.
			if elapsed >= duration {
				return sim.WeatherClear, nil
			}
			return "", nil
		}

		// Clear (or unset): start a storm only once the interval has
		// elapsed AND at least one PC is present.
		if elapsed >= interval && anyPCPresent(w, now) {
			return sim.WeatherStorm, nil
		}
		return "", nil
	}}
}

// anyPCPresent reports whether at least one non-stale PC is in the world at
// `now` — the gate on starting an automatic storm. Uses the shared
// PCPresenceStale / PCPresenceStaleAfter primitives (engine/sim/pc_presence.go)
// so the storm gate reads the same staleness threshold the presence sweep and
// encounter cascades use. Must run on the world goroutine (iterates w.Actors).
func anyPCPresent(w *sim.World, now time.Time) bool {
	staleAfter := sim.PCPresenceStaleAfter(w)
	for _, a := range w.Actors {
		if a.Kind != sim.KindPC {
			continue
		}
		if !sim.PCPresenceStale(a.LastPCSeenAt, now, staleAfter) {
			return true
		}
	}
	return false
}
