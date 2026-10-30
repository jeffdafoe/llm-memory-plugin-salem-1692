package sim

import (
	"fmt"
	"time"
)

// snow.go — LLM-850 (seasons epic LLM-834, slice 1d). The snow that lies on the
// village.
//
// One village-wide cover level, World.Environment.SnowCoverX100, from 0 to
// SnowCoverMax×100. It builds while a storm runs in winter (a winter storm is a
// snowstorm) and melts at any other time, faster by day than at night. The
// per-minute sweep that moves it rides the cold ticker (RunColdTicker), the
// other per-minute weather exposure.
//
// The cover is DURABLE (world_state.snow_cover_x100), unlike the weather itself:
// the village restarts several times a day, and a restart must not melt the
// snow. Stored ×100 so the per-minute rates can be fractions of a unit with no
// carry to lose on restart.
//
// The cover shows as a tier (SnowTierOf): none / dusting / light / deep, at
// thresholds that are settings. The tier picks the snowy tree art
// (season_art.go), goes to clients on the world read, and a change of tier
// emits SnowTierChanged for the snow_tier_changed frame — the client swaps the
// ground sheet and sets the snow-on-top-edges shader from it.

// SnowCoverMax is the deepest the cover goes, in whole units.
const SnowCoverMax = 100

// Default snow knobs (×100 per minute; tier thresholds in whole units). A
// default 15-minute winter storm lays 45 — light snow, the snowy ground. It
// melts 3 units an hour by day and 0.6 by night, so one storm's snow lies about
// a day and a half, a second storm on top of it turns it deep.
const (
	DefaultSnowBuildPerMinuteX100     = 300
	DefaultSnowMeltDayPerMinuteX100   = 5
	DefaultSnowMeltNightPerMinuteX100 = 1
	DefaultSnowTierDusting            = 1
	DefaultSnowTierLight              = 20
	DefaultSnowTierDeep               = 60
)

// String is the tier's wire name.
func (t SnowTier) String() string {
	switch t {
	case SnowTierDusting:
		return "dusting"
	case SnowTierLight:
		return "light"
	case SnowTierDeep:
		return "deep"
	}
	return "none"
}

// SnowTierOf is the tier a cover of coverX100 shows under s. A threshold of 0 or
// less leaves its tier out, so the cover falls to the tier below.
func SnowTierOf(coverX100 int, s WorldSettings) SnowTier {
	reaches := func(threshold int) bool {
		return threshold > 0 && coverX100 >= threshold*100
	}
	switch {
	case coverX100 <= 0:
		return SnowTierNone
	case reaches(s.SnowTierDeep):
		return SnowTierDeep
	case reaches(s.SnowTierLight):
		return SnowTierLight
	case reaches(s.SnowTierDusting):
		return SnowTierDusting
	}
	return SnowTierNone
}

// SnowTierNow is the tier the village's cover shows now.
func SnowTierNow(w *World) SnowTier {
	return SnowTierOf(w.Environment.SnowCoverX100, w.Settings)
}

// Snowing reports whether snow is falling: a storm in winter.
func Snowing(weather string, month time.Month) bool {
	return weather == WeatherStorm && SeasonOf(month) == SeasonWinter
}

// snowRatePerMinuteX100 is the cover's change per minute, ×100: building while
// it snows, melting otherwise. A negative setting counts as 0, so a bad value
// can stop the snow but never turn melting into building.
func snowRatePerMinuteX100(w *World, now time.Time) int {
	month, _ := VillageMonthDay(w, now)
	if Snowing(w.Environment.Weather, month) {
		return max(0, w.Settings.SnowBuildPerMinuteX100)
	}
	if w.Phase == PhaseNight {
		return -max(0, w.Settings.SnowMeltNightPerMinuteX100)
	}
	return -max(0, w.Settings.SnowMeltDayPerMinuteX100)
}

// AdjustSnow returns a Command that applies elapsedMinutes of snowfall or melt
// to the cover, and emits SnowTierChanged when the tier it shows has changed
// since the last emit. Driven once a minute by RunColdTicker.
func AdjustSnow(elapsedMinutes int) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			return adjustSnow(w, elapsedMinutes, time.Now().UTC()), nil
		},
	}
}

// adjustSnow is AdjustSnow at the instant now, which dates the season.
func adjustSnow(w *World, elapsedMinutes int, now time.Time) SnowTier {
	if elapsedMinutes > 0 {
		cover := w.Environment.SnowCoverX100 + snowRatePerMinuteX100(w, now)*elapsedMinutes
		w.Environment.SnowCoverX100 = min(max(cover, 0), SnowCoverMax*100)
		emitSnowTierIfChanged(w, now)
	}
	return SnowTierNow(w)
}

// SetSnowCover returns a Command that sets the cover to cover whole units (the
// umbilical's /snow, for testing out of winter), and emits SnowTierChanged when
// the tier changes. Errors outside 0..SnowCoverMax.
func SetSnowCover(cover int, at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if cover < 0 || cover > SnowCoverMax {
				return SnowTierNone, fmt.Errorf("SetSnowCover: cover %d out of range 0..%d", cover, SnowCoverMax)
			}
			w.Environment.SnowCoverX100 = cover * 100
			emitSnowTierIfChanged(w, at)
			return SnowTierNow(w), nil
		},
	}
}

// emitSnowTierIfChanged emits SnowTierChanged when the tier differs from the
// last one emitted. The last-emitted tier is transient and boots at none, so a
// village that boots under snow sends one frame on the first sweep — harmless,
// the world read already carried the tier.
func emitSnowTierIfChanged(w *World, at time.Time) {
	tier := SnowTierNow(w)
	if tier == w.Environment.LastSnowTierEmitted {
		return
	}
	w.Environment.LastSnowTierEmitted = tier
	w.emit(&SnowTierChanged{Tier: tier, At: at})
}
