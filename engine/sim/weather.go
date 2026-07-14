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

// The sky as a felt sentence — one vocabulary, shared by every surface that
// describes the weather in prose (LLM-399). NPC perception renders these
// (perception/render.go weatherProse) and the atmosphere prompt states them as
// the given fact the mood line must not contradict (cascade/atmosphere.go).
// Sharing the wording is the point: two surfaces that describe the sky in their
// own words are two surfaces that can describe different skies.
const (
	// WeatherStormScene is the storm sky. A scene, not a "weather: storm"
	// stat — it hands the model something concrete to reason over (shelter,
	// mud) and lets the scene be the argument.
	WeatherStormScene = "Rain falls steady over the village, and the lanes are turning to mud."

	// WeatherClearScene is the calm sky. Stated plainly and in the negative
	// ("no rain") because the model's failure mode is inventing rain that
	// isn't there, not omitting rain that is — an atmosphere prompt that says
	// nothing about a clear sky leaves the model free to keep raining
	// (LLM-399).
	WeatherClearScene = "The sky is clear over the village, and no rain falls."
)

// WeatherScene renders World.Environment.Weather as its felt sentence. An
// unrecognized future token (fog, snow) renders "" rather than leak a raw stat
// — additive by design: a new state surfaces once its scene is written above,
// the same graceful-degradation posture as the atmosphere digest's verb map.
// Empty weather reads as clear (the calm default).
func WeatherScene(weather string) string {
	switch strings.TrimSpace(weather) {
	case WeatherStorm:
		return WeatherStormScene
	case WeatherClear, "":
		return WeatherClearScene
	default:
		return ""
	}
}

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
// boot. It runs from the storm sweep's startup, to:
//
//   - discard a persisted mid-storm weather so the engine boots to clear
//     (Environment.Weather is persisted in world_state, but storms are
//     transient — see the file header), and
//   - seed LastWeatherChangeAt so the first automatic storm waits a full
//     StormInterval rather than firing immediately (LastWeatherChangeAt
//     is restart-lossy, so it reads zero on boot, which would otherwise
//     make the elapsed-since-last-change check fire on the next tick).
//
// Emits WeatherChanged when the seed ACTUALLY changed the weather — i.e. the
// checkpoint restored a village under a non-clear sky (a storm, or any future
// token) and boot-to-clear discarded it (LLM-399). Discarding an unknown token
// emits too, and should: boot-to-clear is unconditional, so by the time this
// returns the sky IS clear, and a consumer told otherwise would be wrong about
// the world regardless of whether we have a scene written for what it was.
// Without the event, that discard was silent: the atmosphere
// cascade's boot sweep races this seed on its own goroutine, and if it won the
// race it snapshotted the persisted `storm`, wrote rain prose, and then nothing
// told it the sky had been forced clear — leaving contradictory prose standing
// for a full refresh interval. The event makes the boot path self-healing
// regardless of which goroutine wins: commands only execute once World.Run
// starts draining w.cmds, which is after every Subscribe has landed, so the
// atmosphere subscriber is always registered by the time this fires.
//
// Returns true when the weather changed, false when it was already clear (the
// common boot). No dedup on the STAMP — LastWeatherChangeAt is seeded on every
// boot either way, since a restart-lossy zero clock is the thing it exists to
// fix.
func SeedWeatherClear(at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			// Empty reads as clear (a virgin world that has never had weather
			// set), so it is not a change — there is no stale sky to correct.
			prior := strings.TrimSpace(w.Environment.Weather)
			changed := prior != "" && prior != WeatherClear
			w.Environment.Weather = WeatherClear
			w.Environment.LastWeatherChangeAt = at
			if changed {
				w.emit(&WeatherChanged{Weather: WeatherClear, At: at})
			}
			return changed, nil
		},
	}
}
