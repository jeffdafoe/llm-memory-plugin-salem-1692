package cascade

import (
	"context"
	"log"
	mathrand "math/rand/v2"
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
// state lives on World.Environment (Weather + LastWeatherChangeAt +
// StormDueAt) — the sweep goroutine is stateless, so a single source of
// truth drives the DTO, the prompt, and the wire frame, and the decision
// is testable by advancing a clock against a constructed world.
//
// Lifecycle:
//
//	RegisterStorm(ctx, w)
//	└─> go runStormSweep(ctx, w)
//	     ├─> SeedWeatherClear (boot to clear, seed the change clock)
//	     ├─> time.Ticker @ stormSweepInterval until ctx.Done
//	     └─> each tick: decideStorm (decide + arm) → ApplyWeatherChange (apply)
//
// The state machine (decideStorm), per tick:
//
//   - storm held >= StormDuration            → clear   (NOT PC-gated; an
//     in-flight storm rides out its full duration even if the last PC
//     left — only the START is gated)
//   - clear, no PC present (or unarmed)      → re-arm StormDueAt to
//     now + pickStormInterval(); no transition
//   - clear, PC present, now >= StormDueAt   → storm
//   - otherwise                              → no transition
//
// PC-presence gate (acceptance criteria 2-4): an automatic storm only
// STARTS when at least one non-stale PC is present (the WS presence
// heartbeat primitive in engine/sim/pc_presence.go, LLM-342). Storms are a visual
// payoff — wasted on an empty village. The operator force-path (httpapi
// umbilical /weather) is deliberately NOT gated, so a storm can be summoned
// on an empty village for demo/testing.
//
// The gate defers the FIRE, so the clock must not keep accruing behind it
// (LLM-401): an empty village that banked hours of clear sky would otherwise
// release a storm on the first sweep after someone logged in — every session
// opening in the rain. Hence the re-arm on every unattended clear tick: the
// interval a PC waits out is measured from their arrival, and a PC who leaves
// hands back a full interval rather than a partial one. Storms are meant to be
// rare from the player's seat — a PC has to be resident for a full (jittered)
// StormInterval to see one.

const (
	// defaultStormInterval is the fallback gap between automatic storms
	// (clear → storm) when WorldSettings.StormInterval is unset. 3h.
	// Lives in cascade (not sim) for the same reason as
	// defaultAtmosphereRefreshInterval — cascade owns the goroutine driver.
	defaultStormInterval = 3 * time.Hour

	// defaultStormDuration is the fallback storm hold time (storm → clear)
	// when WorldSettings.StormDuration is unset. 15m.
	defaultStormDuration = 15 * time.Minute

	// stormIntervalJitter scatters each armed storm interval by ±25% of
	// StormInterval, so a PC resident long enough to see two storms doesn't
	// get them on a metronome.
	stormIntervalJitter = 0.25

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
	// Cadence contract, declared before the goroutine starts (LLM-395): a ticker
	// that never comes up must still be visible to the staleness alarm.
	w.RegisterTicker("storm", stormSweepInterval)
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

// runOneStormDecision runs one tick of the weather state machine: decide the
// next weather (decideStorm), and if a transition is due, apply it via the
// shared ApplyWeatherChange command — the same code the umbilical force-path
// runs. Two SendContext round-trips (decide, then apply) mirror atmosphere's
// fetch→apply split and keep the decision testable in isolation.
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

// decideStorm returns a Command that yields the weather to transition TO at
// `now`, or "" for no transition this tick — and, on a clear tick, arms or
// re-arms Environment.StormDueAt (the only thing it writes; the weather
// transition itself goes through the shared ApplyWeatherChange command, so
// runOneStormDecision and tests apply the result the same way the operator
// force-path does).
//
// Runs on the world goroutine (it reads w.Settings, w.Environment, and
// w.Actors), so the actor scan for PC presence is race-safe.
func decideStorm(now time.Time) sim.Command {
	return sim.Command{Fn: func(w *sim.World) (any, error) {
		duration := w.Settings.StormDuration
		if duration <= 0 {
			duration = defaultStormDuration
		}

		if strings.TrimSpace(w.Environment.Weather) == sim.WeatherStorm {
			// In-flight storm: clear once it has held its full duration.
			// Not PC-gated — a storm that started while a PC was present
			// rides out even if the last PC has since left.
			if now.Sub(w.Environment.LastWeatherChangeAt) >= duration {
				return sim.WeatherClear, nil
			}
			return "", nil
		}

		// Clear (or unset). Nobody here — or a weather transition just
		// disarmed the clock — so hold: push the due time out to a fresh
		// interval from now. A PC therefore always walks into a full
		// interval of clear sky, never into a storm the empty village
		// banked while they were away (LLM-401).
		if !anyPCPresent(w, now) || w.Environment.StormDueAt.IsZero() {
			w.Environment.StormDueAt = now.Add(pickStormInterval(w.Settings))
			return "", nil
		}
		if !now.Before(w.Environment.StormDueAt) {
			return sim.WeatherStorm, nil
		}
		return "", nil
	}}
}

// pickStormInterval returns the wait until the next automatic storm: a draw
// from [StormInterval-jitter, StormInterval+jitter), where jitter is
// stormIntervalJitter of the configured interval (defaultStormInterval when
// unset).
//
// Sampled once per arming and stored as an absolute StormDueAt — the same
// shape as the reactor's WarrantDueAt (engine/sim/reactor.go). Re-rolling the
// band on every 30s sweep instead would pull the fire toward the low edge of
// the band (a hundred-odd chances to roll low across an interval), which is a
// metronome with extra steps.
//
// StormInterval is admin-settable (storm_interval_minutes), so a jitter that
// doesn't come out to a sane fraction of the interval — zero, or wide enough
// that doubling it would overflow the int64 nanoseconds Int64N takes — falls
// back to the bare interval rather than panicking on the draw.
func pickStormInterval(s sim.WorldSettings) time.Duration {
	interval := s.StormInterval
	if interval <= 0 {
		interval = defaultStormInterval
	}
	jitter := time.Duration(float64(interval) * stormIntervalJitter)
	if jitter <= 0 || jitter > interval/2 {
		return interval
	}
	return interval - jitter + time.Duration(mathrand.Int64N(int64(jitter)*2))
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
