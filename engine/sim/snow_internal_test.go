package sim

import (
	"testing"
	"time"
)

// snowTestWorld is the atmosphere test world with the default snow knobs.
func snowTestWorld(t *testing.T) *World {
	t.Helper()
	w := newAtmosphereTestWorld(t)
	w.Settings.SnowBuildPerMinuteX100 = DefaultSnowBuildPerMinuteX100
	w.Settings.SnowMeltDayPerMinuteX100 = DefaultSnowMeltDayPerMinuteX100
	w.Settings.SnowMeltNightPerMinuteX100 = DefaultSnowMeltNightPerMinuteX100
	w.Settings.SnowTierDusting = DefaultSnowTierDusting
	w.Settings.SnowTierLight = DefaultSnowTierLight
	w.Settings.SnowTierDeep = DefaultSnowTierDeep
	return w
}

// recordSnowTierChanged returns the tiers every emitted SnowTierChanged carried.
func recordSnowTierChanged(w *World) *[]SnowTier {
	got := new([]SnowTier)
	w.Subscribe(SubscriberFunc(func(_ *World, evt Event) {
		if e, ok := evt.(*SnowTierChanged); ok {
			*got = append(*got, e.Tier)
		}
	}))
	return got
}

var (
	snowJanuary = time.Date(2026, time.January, 14, 15, 0, 0, 0, time.UTC)
	snowOctober = time.Date(2026, time.October, 14, 15, 0, 0, 0, time.UTC)
)

func TestSnowTierOf_Thresholds(t *testing.T) {
	s := WorldSettings{SnowTierDusting: 1, SnowTierLight: 20, SnowTierDeep: 60}
	cases := []struct {
		cover int
		want  SnowTier
	}{
		{0, SnowTierNone},
		{99, SnowTierNone},
		{100, SnowTierDusting},
		{1999, SnowTierDusting},
		{2000, SnowTierLight},
		{5999, SnowTierLight},
		{6000, SnowTierDeep},
		{SnowCoverMax * 100, SnowTierDeep},
	}
	for _, c := range cases {
		if got := SnowTierOf(c.cover, s); got != c.want {
			t.Errorf("SnowTierOf(%d) = %s, want %s", c.cover, got, c.want)
		}
	}
	// A threshold of 0 leaves its tier out: the cover falls to the tier below.
	noDeep := WorldSettings{SnowTierDusting: 1, SnowTierLight: 20}
	if got := SnowTierOf(9000, noDeep); got != SnowTierLight {
		t.Errorf("deep off: SnowTierOf(9000) = %s, want light", got)
	}
	if got := SnowTierOf(9000, WorldSettings{}); got != SnowTierNone {
		t.Errorf("every tier off: SnowTierOf(9000) = %s, want none", got)
	}
}

// A default winter storm lays light snow, and the tier frame fires once per
// tier crossed — dusting on the first minute, light on the seventh.
func TestAdjustSnow_WinterStormBuilds(t *testing.T) {
	w := snowTestWorld(t)
	w.Environment.Weather = WeatherStorm
	got := recordSnowTierChanged(w)
	for range 15 {
		adjustSnow(w, 1, snowJanuary)
	}
	if w.Environment.SnowCoverX100 != 15*DefaultSnowBuildPerMinuteX100 {
		t.Errorf("cover after a 15-minute storm = %d, want %d", w.Environment.SnowCoverX100, 15*DefaultSnowBuildPerMinuteX100)
	}
	if tier := SnowTierNow(w); tier != SnowTierLight {
		t.Errorf("tier = %s, want light", tier)
	}
	if want := []SnowTier{SnowTierDusting, SnowTierLight}; !equalTiers(*got, want) {
		t.Errorf("tier frames = %v, want %v", *got, want)
	}
}

// Out of winter a storm is rain: it lays no snow, and melts what lies.
func TestAdjustSnow_AutumnStormMelts(t *testing.T) {
	w := snowTestWorld(t)
	w.Environment.Weather = WeatherStorm
	adjustSnow(w, 10, snowOctober)
	if w.Environment.SnowCoverX100 != 0 {
		t.Fatalf("October storm laid snow: cover = %d", w.Environment.SnowCoverX100)
	}
	w.Environment.SnowCoverX100 = 1000
	adjustSnow(w, 10, snowOctober)
	if want := 1000 - 10*DefaultSnowMeltDayPerMinuteX100; w.Environment.SnowCoverX100 != want {
		t.Errorf("cover = %d, want %d (melting by day)", w.Environment.SnowCoverX100, want)
	}
}

// A clear sky melts the snow, faster by day than at night, and never below 0.
func TestAdjustSnow_MeltsFasterByDay(t *testing.T) {
	day := snowTestWorld(t)
	day.Environment.SnowCoverX100 = 4500
	adjustSnow(day, 60, snowJanuary)

	night := snowTestWorld(t)
	night.Phase = PhaseNight
	night.Environment.SnowCoverX100 = 4500
	adjustSnow(night, 60, snowJanuary)

	if want := 4500 - 60*DefaultSnowMeltDayPerMinuteX100; day.Environment.SnowCoverX100 != want {
		t.Errorf("day cover = %d, want %d", day.Environment.SnowCoverX100, want)
	}
	if want := 4500 - 60*DefaultSnowMeltNightPerMinuteX100; night.Environment.SnowCoverX100 != want {
		t.Errorf("night cover = %d, want %d", night.Environment.SnowCoverX100, want)
	}
	if day.Environment.SnowCoverX100 >= night.Environment.SnowCoverX100 {
		t.Errorf("day melt (%d left) is not faster than night melt (%d left)", day.Environment.SnowCoverX100, night.Environment.SnowCoverX100)
	}

	day.Environment.SnowCoverX100 = 3
	adjustSnow(day, 60, snowJanuary)
	if day.Environment.SnowCoverX100 != 0 {
		t.Errorf("cover melted past zero: %d", day.Environment.SnowCoverX100)
	}
}

// The cover stops at SnowCoverMax, and a negative rate setting cannot turn
// melting into building.
func TestAdjustSnow_ClampsAndIgnoresNegativeRates(t *testing.T) {
	w := snowTestWorld(t)
	w.Environment.Weather = WeatherStorm
	adjustSnow(w, 10_000, snowJanuary)
	if w.Environment.SnowCoverX100 != SnowCoverMax*100 {
		t.Errorf("cover = %d, want the cap %d", w.Environment.SnowCoverX100, SnowCoverMax*100)
	}

	w.Environment.Weather = WeatherClear
	w.Settings.SnowMeltDayPerMinuteX100 = -50
	adjustSnow(w, 10, snowJanuary)
	if w.Environment.SnowCoverX100 != SnowCoverMax*100 {
		t.Errorf("a negative melt rate moved the cover: %d", w.Environment.SnowCoverX100)
	}
}

func TestSetSnowCover(t *testing.T) {
	w := snowTestWorld(t)
	got := recordSnowTierChanged(w)
	res, err := SetSnowCover(70, snowOctober).Fn(w)
	if err != nil {
		t.Fatalf("SetSnowCover(70): %v", err)
	}
	if res != SnowTierDeep || w.Environment.SnowCoverX100 != 7000 {
		t.Errorf("SetSnowCover(70) = %v, cover %d; want deep, 7000", res, w.Environment.SnowCoverX100)
	}
	// The same tier again sends no second frame.
	if _, err := SetSnowCover(80, snowOctober).Fn(w); err != nil {
		t.Fatalf("SetSnowCover(80): %v", err)
	}
	if want := []SnowTier{SnowTierDeep}; !equalTiers(*got, want) {
		t.Errorf("tier frames = %v, want %v", *got, want)
	}
	for _, bad := range []int{-1, SnowCoverMax + 1} {
		if _, err := SetSnowCover(bad, snowOctober).Fn(w); err == nil {
			t.Errorf("SetSnowCover(%d) accepted an out-of-range cover", bad)
		}
	}
	if w.Environment.SnowCoverX100 != 8000 {
		t.Errorf("a rejected set changed the cover: %d", w.Environment.SnowCoverX100)
	}
}

func equalTiers(a, b []SnowTier) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Thresholds and rates as large as the registry accepts neither wrap the tier
// comparison nor wrap the cover: a huge threshold leaves that tier out of reach,
// a huge build rate fills the cover, a huge melt rate clears it (code_review).
func TestSnow_HugeSettingsSaturate(t *testing.T) {
	const huge = int(^uint(0) >> 1)
	if got := SnowTierOf(100, WorldSettings{SnowTierDusting: 1, SnowTierDeep: huge}); got != SnowTierDusting {
		t.Errorf("huge deep threshold: SnowTierOf(100) = %s, want dusting", got)
	}
	if got := moveSnowCover(4500, huge, 7); got != SnowCoverMax*100 {
		t.Errorf("huge build rate: cover = %d, want the cap", got)
	}
	if got := moveSnowCover(4500, -huge, 7); got != 0 {
		t.Errorf("huge melt rate: cover = %d, want 0", got)
	}
	if got := moveSnowCover(4500, 300, 1); got != 4800 {
		t.Errorf("ordinary build: cover = %d, want 4800", got)
	}
}
