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

// A boot that discards a persisted mid-storm weather EMITS (LLM-399), so the
// atmosphere cascade learns the sky was forced clear under it. Without the
// event that discard was silent, and an atmosphere boot sweep that won the race
// against this seed would write rain prose against a world about to be clear —
// with nothing to correct it for a full refresh interval.
func TestSeedWeatherClear_DiscardsPersistedStormAndEmits(t *testing.T) {
	w := newAtmosphereTestWorld(t)
	// Simulate a persisted mid-storm weather loaded from world_state.
	w.Environment.Weather = WeatherStorm
	got := recordWeatherChanged(w)

	at := time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC)
	res, err := SeedWeatherClear(at).Fn(w)
	if err != nil {
		t.Fatalf("SeedWeatherClear: %v", err)
	}
	if changed, _ := res.(bool); !changed {
		t.Errorf("SeedWeatherClear returned changed=false, want true (it discarded a persisted storm)")
	}
	if w.Environment.Weather != WeatherClear {
		t.Errorf("Weather = %q, want %q (boot-to-clear discards persisted storm)", w.Environment.Weather, WeatherClear)
	}
	if !w.Environment.LastWeatherChangeAt.Equal(at) {
		t.Errorf("LastWeatherChangeAt = %v, want %v (seeded so the first auto-storm waits a full interval)", w.Environment.LastWeatherChangeAt, at)
	}
	if len(*got) != 1 {
		t.Fatalf("emitted %d WeatherChanged discarding a persisted storm, want 1", len(*got))
	}
	if (*got)[0].Weather != WeatherClear {
		t.Errorf("emitted Weather = %q, want %q", (*got)[0].Weather, WeatherClear)
	}
}

// The common boot — already clear — changes nothing and must stay silent, so a
// restart doesn't spend an LLM round-trip re-authoring an atmosphere for a sky
// that never moved. The stamp is still seeded (LastWeatherChangeAt is
// restart-lossy; re-seeding it every boot is the whole point).
func TestSeedWeatherClear_AlreadyClearStampsWithoutEmitting(t *testing.T) {
	for _, prior := range []string{WeatherClear, "", "   "} {
		w := newAtmosphereTestWorld(t)
		w.Environment.Weather = prior
		got := recordWeatherChanged(w)

		at := time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC)
		res, err := SeedWeatherClear(at).Fn(w)
		if err != nil {
			t.Fatalf("prior %q: SeedWeatherClear: %v", prior, err)
		}
		if changed, _ := res.(bool); changed {
			t.Errorf("prior %q: SeedWeatherClear returned changed=true, want false (the sky did not move)", prior)
		}
		if w.Environment.Weather != WeatherClear {
			t.Errorf("prior %q: Weather = %q, want %q", prior, w.Environment.Weather, WeatherClear)
		}
		if !w.Environment.LastWeatherChangeAt.Equal(at) {
			t.Errorf("prior %q: LastWeatherChangeAt = %v, want %v (seeded on every boot regardless)", prior, w.Environment.LastWeatherChangeAt, at)
		}
		if len(*got) != 0 {
			t.Errorf("prior %q: emitted %d WeatherChanged, want 0 (nothing changed)", prior, len(*got))
		}
	}
}

// WeatherScene is the single vocabulary every prose surface describes the sky
// with (LLM-399). The clear scene must be non-empty — an empty clear scene is
// exactly the bug this fixes (a prompt that says nothing about a calm sky lets
// the model keep raining).
func TestWeatherScene(t *testing.T) {
	if got := WeatherScene(WeatherClear); got != WeatherClearScene {
		t.Errorf("WeatherScene(clear) = %q, want %q", got, WeatherClearScene)
	}
	if got := WeatherScene(""); got != WeatherClearScene {
		t.Errorf("WeatherScene(\"\") = %q, want the clear scene (empty reads as calm)", got)
	}
	if got := WeatherScene("  " + WeatherStorm + " "); got != WeatherStormScene {
		t.Errorf("WeatherScene(storm) = %q, want %q (trims)", got, WeatherStormScene)
	}
	if got := WeatherScene("fog"); got != "" {
		t.Errorf("WeatherScene(fog) = %q, want \"\" — an unwritten future state renders nothing rather than leaking a raw stat", got)
	}
}
