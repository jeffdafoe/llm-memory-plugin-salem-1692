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

// testStormInterval / testStormDuration are the cadence settings every
// decision world below runs with. The jitter band (±stormIntervalJitter)
// puts an armed due time in [2h15m, 3h45m) — tests that need a "definitely
// before/after any possible arming" boundary use stormArmFloor / stormArmCeil.
const (
	testStormInterval = 3 * time.Hour
	testStormDuration = 15 * time.Minute
)

var (
	stormArmFloor = time.Duration(float64(testStormInterval) * (1 - stormIntervalJitter))
	stormArmCeil  = time.Duration(float64(testStormInterval) * (1 + stormIntervalJitter))
)

// newStormDecisionWorld builds a non-running world with the given weather and
// last-change stamp, plus the default storm cadence settings. decideStorm reads
// it directly via Fn — no Run needed. StormDueAt starts unarmed (zero), as it
// is after a boot seed or any weather transition; tests that need an armed
// clock set it themselves.
func newStormDecisionWorld(weather string, lastChange time.Time) *sim.World {
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)
	w.Settings.StormInterval = testStormInterval
	w.Settings.StormDuration = testStormDuration
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

func TestDecideStorm_FiresWhenDueAndPCPresent(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	w := newStormDecisionWorld(sim.WeatherClear, now.Add(-4*time.Hour))
	w.Environment.StormDueAt = now.Add(-30 * time.Second) // armed, and due
	addPresentPC(w, now)

	if got := decide(t, w, now); got != sim.WeatherStorm {
		t.Errorf("decideStorm = %q, want %q (due + PC present)", got, sim.WeatherStorm)
	}
}

// An empty village holds the clock: a due time that came and went with nobody
// there is pushed back out to a fresh interval, so the storm is never banked
// (LLM-401).
func TestDecideStorm_NoPCPresentReArmsInsteadOfFiring(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	w := newStormDecisionWorld(sim.WeatherClear, now.Add(-4*time.Hour))
	w.Environment.StormDueAt = now.Add(-1 * time.Hour) // long overdue, but nobody is here

	if got := decide(t, w, now); got != "" {
		t.Errorf("decideStorm = %q, want \"\" (no PC present ⇒ no auto-storm)", got)
	}
	assertArmedFromNow(t, w, now)
}

func TestDecideStorm_StalePCReArmsInsteadOfFiring(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	w := newStormDecisionWorld(sim.WeatherClear, now.Add(-4*time.Hour))
	w.Environment.StormDueAt = now.Add(-1 * time.Hour)
	stale := now.Add(-5 * time.Minute) // far past the 40s staleness default
	w.Actors["ghost"] = &sim.Actor{ID: "ghost", Kind: sim.KindPC, LastPCSeenAt: &stale}

	if got := decide(t, w, now); got != "" {
		t.Errorf("decideStorm = %q, want \"\" (stale PC is absent ⇒ no auto-storm)", got)
	}
	assertArmedFromNow(t, w, now)
}

func TestDecideStorm_NoFireBeforeDue(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	w := newStormDecisionWorld(sim.WeatherClear, now.Add(-1*time.Hour))
	w.Environment.StormDueAt = now.Add(1 * time.Hour) // armed, not yet due
	addPresentPC(w, now)

	if got := decide(t, w, now); got != "" {
		t.Errorf("decideStorm = %q, want \"\" (not yet due)", got)
	}
	if !w.Environment.StormDueAt.Equal(now.Add(1 * time.Hour)) {
		t.Errorf("StormDueAt = %v, want unchanged (an attended tick must not re-arm)", w.Environment.StormDueAt)
	}
}

// A disarmed clock (post-boot-seed, or after any weather transition) arms on
// the first tick even with a PC present — it never reads as "due at the zero
// time" and fires immediately.
func TestDecideStorm_ArmsUnarmedClockWithPCPresent(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	w := newStormDecisionWorld(sim.WeatherClear, now) // StormDueAt zero
	addPresentPC(w, now)

	if got := decide(t, w, now); got != "" {
		t.Errorf("decideStorm = %q, want \"\" (unarmed clock arms, it does not fire)", got)
	}
	assertArmedFromNow(t, w, now)
}

// The bug (LLM-401): a village that sat empty for hours past the interval must
// not rain on the PC who walks into it. The interval is measured from arrival.
func TestDecideStorm_PCArrivingAtLongEmptyVillageWaitsAFullInterval(t *testing.T) {
	start := time.Date(2026, 6, 25, 6, 0, 0, 0, time.UTC)
	arrival := start.Add(6 * time.Hour)
	w := newStormDecisionWorld(sim.WeatherClear, start)

	// Six hours of unattended sweeps — twice the interval. The last one runs
	// as the PC is logging in, so the clock is armed at `arrival`.
	for at := start; !at.After(arrival); at = at.Add(stormSweepInterval) {
		if got := decide(t, w, at); got != "" {
			t.Fatalf("decideStorm at %v = %q, want \"\" (empty village never storms)", at, got)
		}
	}
	assertArmedFromNow(t, w, arrival)

	// The PC is now present, and their heartbeat keeps them present.
	addPresentPC(w, arrival)

	// Nothing was banked: no storm anywhere inside the interval floor.
	for at := arrival; at.Before(arrival.Add(stormArmFloor)); at = at.Add(stormSweepInterval) {
		addPresentPC(w, at) // heartbeat keeps presence fresh
		if got := decide(t, w, at); got != "" {
			t.Fatalf("decideStorm at arrival+%v = %q, want \"\" (the clock starts at arrival)", at.Sub(arrival), got)
		}
	}

	// ...and it does still storm once the PC has waited one out.
	sawStorm := false
	for at := arrival.Add(stormArmFloor); !at.After(arrival.Add(stormArmCeil)); at = at.Add(stormSweepInterval) {
		addPresentPC(w, at)
		if decide(t, w, at) == sim.WeatherStorm {
			sawStorm = true
			break
		}
	}
	if !sawStorm {
		t.Errorf("no storm within the jitter band after a resident PC waited out a full interval")
	}
}

// assertArmedFromNow checks the clock was (re-)armed at `now` — one full
// jittered interval out, never in the past.
func assertArmedFromNow(t *testing.T, w *sim.World, now time.Time) {
	t.Helper()
	wait := w.Environment.StormDueAt.Sub(now)
	if wait < stormArmFloor || wait >= stormArmCeil {
		t.Errorf("StormDueAt is %v out, want within [%v, %v) of now", wait, stormArmFloor, stormArmCeil)
	}
}

// The jitter has to actually scatter (a fixed interval is the metronome the
// jitter exists to break) and has to stay inside the band.
func TestPickStormInterval_ScattersWithinBand(t *testing.T) {
	s := sim.WorldSettings{StormInterval: testStormInterval}
	seen := make(map[time.Duration]bool)
	for i := 0; i < 200; i++ {
		got := pickStormInterval(s)
		if got < stormArmFloor || got >= stormArmCeil {
			t.Fatalf("pickStormInterval = %v, want within [%v, %v)", got, stormArmFloor, stormArmCeil)
		}
		seen[got] = true
	}
	if len(seen) < 2 {
		t.Error("pickStormInterval returned a single value across 200 draws — no jitter")
	}
}

// An unset StormInterval falls back to the cascade default, jittered the same.
func TestPickStormInterval_UnsetFallsBackToDefault(t *testing.T) {
	got := pickStormInterval(sim.WorldSettings{})
	lo := time.Duration(float64(defaultStormInterval) * (1 - stormIntervalJitter))
	hi := time.Duration(float64(defaultStormInterval) * (1 + stormIntervalJitter))
	if got < lo || got >= hi {
		t.Errorf("pickStormInterval (unset) = %v, want within [%v, %v)", got, lo, hi)
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
