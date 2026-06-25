package sim

import (
	"fmt"
	"strings"
	"time"
)

// weather.go — world-level weather substrate (LLM-117 Half A). The
// off-world sweep goroutine that drives automatic storms lives in
// engine/sim/cascade/storm.go; this file is sim-package primitives
// only, mirroring the atmosphere split (engine/sim/atmosphere.go vs
// engine/sim/cascade/atmosphere.go).
//
// Weather is a single free string on World.Environment.Weather. Half A
// drives only the two values below, but the field stays a free string
// and ApplyWeatherChange stays additive, so later states (rain, fog)
// drop in without a schema change.
//
// Unlike atmosphere — which is purely a sim-state install — a real
// weather transition emits a WeatherChanged event so the httpapi hub
// can push a weather_changed frame to connected clients (the storm FX
// consumer). Both the automatic sweep (cascade/storm.go) and the
// operator force-path (httpapi umbilical /weather) funnel through
// ApplyWeatherChange, so a hand-triggered storm exercises the exact
// code the timer does.
//
// Persistence note: Environment.Weather IS written to world_state by
// the checkpoint (engine/sim/repo/pg/environment.go), but storms are
// transient ambient events — the engine boots to clear regardless of
// what was persisted. SeedWeatherClear (run once at storm-sweep
// startup) discards any persisted mid-storm weather and seeds the
// change clock. Environment.LastWeatherChangeAt is itself restart-lossy
// (not persisted), matching LastAtmosphereRefreshAt.

const (
	// WeatherClear is the calm/default weather. Rendered as no weather
	// line in the atmosphere prompt (buildAtmospherePrompt treats it the
	// same as the empty string) and as "no storm FX" in the client.
	WeatherClear = "clear"

	// WeatherStorm is the only active weather Half A drives: rain +
	// scene darkening + lightning in the Godot client.
	WeatherStorm = "storm"
)

// ApplyWeatherChange returns a Command that installs `weather` as the new
// World.Environment.Weather, stamps LastWeatherChangeAt, and emits a
// WeatherChanged event so the transition reaches connected clients.
//
// Dedup: if the trimmed weather matches the trimmed current weather
// exactly, the apply is a no-op — no write, no stamp, no event. Returns
// `false` in that case (the auto-sweep never hands it an identical
// value, but the operator force-path can, and the dedup keeps a
// redundant force from spamming a frame). Returns `true` when a real
// transition occurred.
//
// Errors on empty text (after trim) — a defensive substrate-invariant
// guard mirroring ApplyAtmosphereRefresh. Clear is the literal
// WeatherClear token, never "", so an empty value is always a caller
// bug, not a "set to calm" request.
func ApplyWeatherChange(weather string, at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			trimmed := strings.TrimSpace(weather)
			if trimmed == "" {
				return false, fmt.Errorf("ApplyWeatherChange: empty weather")
			}
			if trimmed == strings.TrimSpace(w.Environment.Weather) {
				return false, nil
			}
			w.Environment.Weather = trimmed
			w.Environment.LastWeatherChangeAt = at
			w.emit(&WeatherChanged{Weather: trimmed, At: at})
			return true, nil
		},
	}
}

// SeedWeatherClear forces weather to clear and stamps the change clock at
// boot. Unconditional (no dedup) and emits no event — it runs from the
// storm sweep's startup before any client is connected, to:
//
//   - discard a persisted mid-storm weather so the engine boots to clear
//     (Environment.Weather is persisted in world_state, but storms are
//     transient — see the file header), and
//   - seed LastWeatherChangeAt so the first automatic storm waits a full
//     StormInterval rather than firing immediately (LastWeatherChangeAt
//     is restart-lossy, so it reads zero on boot, which would otherwise
//     make the elapsed-since-last-change check fire on the next tick).
func SeedWeatherClear(at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			w.Environment.Weather = WeatherClear
			w.Environment.LastWeatherChangeAt = at
			return nil, nil
		},
	}
}
