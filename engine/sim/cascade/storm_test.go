package cascade

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// storm_test.go — driver-side tests for the storm weather cascade slice
// (LLM-117). The substrate Commands (sim.ApplyWeatherChange /
// sim.SeedWeatherClear) have their own surface in engine/sim/weather_test.go;
// these cover the weather state machine (decideStorm), the PC-presence gate,
// the goroutine lifecycle, and the ctx-cancel exit.

// newStormDecisionWorld builds a non-running world with the given weather and
// last-change stamp, plus the default storm cadence settings. decideStorm reads
// it directly via Fn — no Run needed.
func newStormDecisionWorld(weather string, lastChange time.Time) *sim.World {
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)
	w.Settings.StormInterval = 3 * time.Hour
	w.Settings.StormDuration = 15 * time.Minute
	w.Environment.Weather = weather
	w.Environment.LastWeatherChangeAt = lastChange
	return w
}

func addPresentPC(w *sim.World, now time.Time) {
	seen := now
	w.Actors["pc1"] = &sim.Actor{ID: "pc1", Kind: sim.KindPC, LastPCSeenAt: &seen}
}

func decide(t *testing.T, w *sim.World, now time.Time) string {
	t.Helper()
	res, err := decideStorm(now).Fn(w)
	if err != nil {
		t.Fatalf("decideStorm: %v", err)
	}
	s, _ := res.(string)
	return s
}

func TestDecideStorm_FiresWhenIntervalElapsedAndPCPresent(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	w := newStormDecisionWorld(sim.WeatherClear, now.Add(-4*time.Hour)) // interval (3h) elapsed
	addPresentPC(w, now)

	if got := decide(t, w, now); got != sim.WeatherStorm {
		t.Errorf("decideStorm = %q, want %q (interval elapsed + PC present)", got, sim.WeatherStorm)
	}
}

func TestDecideStorm_NoFireWhenNoPCPresent(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	w := newStormDecisionWorld(sim.WeatherClear, now.Add(-4*time.Hour)) // interval elapsed, but no PC
	if got := decide(t, w, now); got != "" {
		t.Errorf("decideStorm = %q, want \"\" (no PC present ⇒ no auto-storm)", got)
	}
}

func TestDecideStorm_NoFireWhenPCStale(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	w := newStormDecisionWorld(sim.WeatherClear, now.Add(-4*time.Hour))
	stale := now.Add(-5 * time.Minute) // far past the 40s staleness default
	w.Actors["ghost"] = &sim.Actor{ID: "ghost", Kind: sim.KindPC, LastPCSeenAt: &stale}
	if got := decide(t, w, now); got != "" {
		t.Errorf("decideStorm = %q, want \"\" (stale PC is absent ⇒ no auto-storm)", got)
	}
}

func TestDecideStorm_NoFireBeforeInterval(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	w := newStormDecisionWorld(sim.WeatherClear, now.Add(-1*time.Hour)) // only 1h of a 3h interval
	addPresentPC(w, now)
	if got := decide(t, w, now); got != "" {
		t.Errorf("decideStorm = %q, want \"\" (interval not yet elapsed)", got)
	}
}

func TestDecideStorm_ClearsAfterDuration(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	w := newStormDecisionWorld(sim.WeatherStorm, now.Add(-20*time.Minute)) // duration (15m) elapsed
	addPresentPC(w, now)
	if got := decide(t, w, now); got != sim.WeatherClear {
		t.Errorf("decideStorm = %q, want %q (storm held past its duration)", got, sim.WeatherClear)
	}
}

func TestDecideStorm_HoldsBeforeDuration(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	w := newStormDecisionWorld(sim.WeatherStorm, now.Add(-5*time.Minute)) // only 5m of a 15m storm
	addPresentPC(w, now)
	if got := decide(t, w, now); got != "" {
		t.Errorf("decideStorm = %q, want \"\" (storm still within its duration)", got)
	}
}

// An in-flight storm clears on schedule even with no PC present — only the
// START of an auto-storm is gated, not the clear (acceptance criterion 4).
func TestDecideStorm_InFlightStormRidesOutPCDeparture(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	w := newStormDecisionWorld(sim.WeatherStorm, now.Add(-20*time.Minute)) // duration elapsed, no PC
	if got := decide(t, w, now); got != sim.WeatherClear {
		t.Errorf("decideStorm = %q, want %q (clear is not PC-gated)", got, sim.WeatherClear)
	}
}

func TestRegisterStorm_NilWorldPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("RegisterStorm(nil) did not panic")
		}
	}()
	RegisterStorm(context.Background(), nil)
}

// RegisterStorm's sweep resets a persisted mid-storm weather to clear at boot
// (SeedWeatherClear), proving the goroutine is wired and running.
func TestRegisterStorm_BootsToClear(t *testing.T) {
	repo, _ := mem.NewRepository()
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	// Simulate a persisted mid-storm weather loaded from world_state.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Environment.Weather = sim.WeatherStorm
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed storm: %v", err)
	}

	RegisterStorm(ctx, w)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			return world.Environment.Weather, nil
		}})
		if err == nil {
			if s, _ := res.(string); s == sim.WeatherClear {
				return // booted to clear
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("weather did not boot to clear within 2s (RegisterStorm sweep not running?)")
}

func TestRunStormSweep_CtxCancelExits(t *testing.T) {
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo) // not running; cancelled ctx makes SendContext return at once
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() { runStormSweep(ctx, w); close(done) }()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runStormSweep did not exit on cancelled ctx")
	}
}
