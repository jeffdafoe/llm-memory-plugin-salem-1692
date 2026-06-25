package sim

import (
	"testing"
	"time"
)

// recordWeatherChanged subscribes a recorder and returns the slice it appends
// every emitted *WeatherChanged into. Reuses newAtmosphereTestWorld's struct-
// literal world (same sim package) — emit is zero-value safe.
func recordWeatherChanged(w *World) *[]*WeatherChanged {
	got := new([]*WeatherChanged)
	w.Subscribe(SubscriberFunc(func(_ *World, evt Event) {
		if e, ok := evt.(*WeatherChanged); ok {
			*got = append(*got, e)
		}
	}))
	return got
}

func TestApplyWeatherChange_InstallsStampsAndEmits(t *testing.T) {
	w := newAtmosphereTestWorld(t)
	w.Environment.Weather = WeatherClear
	got := recordWeatherChanged(w)

	at := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	res, err := ApplyWeatherChange(WeatherStorm, at).Fn(w)
	if err != nil {
		t.Fatalf("ApplyWeatherChange: %v", err)
	}
	if wrote, _ := res.(bool); !wrote {
		t.Fatalf("ApplyWeatherChange returned %v, want true (real transition)", res)
	}
	if w.Environment.Weather != WeatherStorm {
		t.Errorf("Weather = %q, want %q", w.Environment.Weather, WeatherStorm)
	}
	if !w.Environment.LastWeatherChangeAt.Equal(at) {
		t.Errorf("LastWeatherChangeAt = %v, want %v", w.Environment.LastWeatherChangeAt, at)
	}
	if len(*got) != 1 {
		t.Fatalf("emitted %d WeatherChanged, want 1", len(*got))
	}
	if (*got)[0].Weather != WeatherStorm {
		t.Errorf("event Weather = %q, want %q", (*got)[0].Weather, WeatherStorm)
	}
	if !(*got)[0].At.Equal(at) {
		t.Errorf("event At = %v, want %v", (*got)[0].At, at)
	}
}

func TestApplyWeatherChange_DedupsIdentical(t *testing.T) {
	w := newAtmosphereTestWorld(t)
	w.Environment.Weather = WeatherStorm
	stamp := time.Date(2026, 6, 25, 9, 0, 0, 0, time.UTC)
	w.Environment.LastWeatherChangeAt = stamp
	got := recordWeatherChanged(w)

	res, err := ApplyWeatherChange(WeatherStorm, stamp.Add(time.Hour)).Fn(w)
	if err != nil {
		t.Fatalf("ApplyWeatherChange: %v", err)
	}
	if wrote, _ := res.(bool); wrote {
		t.Errorf("ApplyWeatherChange returned true, want false (dedup on identical weather)")
	}
	if !w.Environment.LastWeatherChangeAt.Equal(stamp) {
		t.Errorf("LastWeatherChangeAt = %v, want unchanged %v (dedup must not stamp)", w.Environment.LastWeatherChangeAt, stamp)
	}
	if len(*got) != 0 {
		t.Errorf("emitted %d WeatherChanged on dedup, want 0", len(*got))
	}
}

func TestApplyWeatherChange_RejectsEmpty(t *testing.T) {
	w := newAtmosphereTestWorld(t)
	w.Environment.Weather = WeatherClear
	got := recordWeatherChanged(w)

	res, err := ApplyWeatherChange("   ", time.Now()).Fn(w)
	if err == nil {
		t.Fatalf("ApplyWeatherChange(empty) returned nil error, want a rejection")
	}
	if wrote, _ := res.(bool); wrote {
		t.Errorf("ApplyWeatherChange(empty) returned true, want false")
	}
	if w.Environment.Weather != WeatherClear {
		t.Errorf("Weather = %q, want unchanged %q", w.Environment.Weather, WeatherClear)
	}
	if len(*got) != 0 {
		t.Errorf("emitted %d WeatherChanged on reject, want 0", len(*got))
	}
}

func TestSeedWeatherClear_ForcesClearAndStampsNoEmit(t *testing.T) {
	w := newAtmosphereTestWorld(t)
	// Simulate a persisted mid-storm weather loaded from world_state.
	w.Environment.Weather = WeatherStorm
	got := recordWeatherChanged(w)

	at := time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC)
	if _, err := SeedWeatherClear(at).Fn(w); err != nil {
		t.Fatalf("SeedWeatherClear: %v", err)
	}
	if w.Environment.Weather != WeatherClear {
		t.Errorf("Weather = %q, want %q (boot-to-clear discards persisted storm)", w.Environment.Weather, WeatherClear)
	}
	if !w.Environment.LastWeatherChangeAt.Equal(at) {
		t.Errorf("LastWeatherChangeAt = %v, want %v (seeded so the first auto-storm waits a full interval)", w.Environment.LastWeatherChangeAt, at)
	}
	if len(*got) != 0 {
		t.Errorf("emitted %d WeatherChanged on boot seed, want 0 (no client connected at boot)", len(*got))
	}
}
