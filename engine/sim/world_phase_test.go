package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// TestParseHM covers the format parser used for dawn/dusk/rotation settings.
func TestParseHM(t *testing.T) {
	cases := []struct {
		in           string
		hour, minute int
		wantErr      bool
	}{
		{"07:00", 7, 0, false},
		{"19:30", 19, 30, false},
		{"00:00", 0, 0, false},
		{"23:59", 23, 59, false},
		{"7:00", 7, 0, false}, // Atoi tolerates leading zero absence
		{"24:00", 0, 0, true},
		{"07:60", 0, 0, true},
		{"abc", 0, 0, true},
		{"07", 0, 0, true},
		{"07:00:00", 0, 0, true},
	}
	for _, c := range cases {
		h, m, err := sim.ParseHM(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseHM(%q): expected error, got %d:%d", c.in, h, m)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseHM(%q): unexpected error %v", c.in, err)
			continue
		}
		if h != c.hour || m != c.minute {
			t.Errorf("ParseHM(%q) = %d:%d, want %d:%d", c.in, h, m, c.hour, c.minute)
		}
	}
}

// TestMostRecentBoundary covers the four "where am I in the cycle" cases.
func TestMostRecentBoundary(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	dawn := struct{ H, M int }{7, 0}
	dusk := struct{ H, M int }{19, 0}

	cases := []struct {
		name string
		now  time.Time
		want sim.Phase
	}{
		{"midmorning is day (past dawn, before dusk)",
			time.Date(2026, 5, 12, 10, 0, 0, 0, loc), sim.PhaseDay},
		{"evening is night (past dusk)",
			time.Date(2026, 5, 12, 21, 30, 0, 0, loc), sim.PhaseNight},
		{"pre-dawn is still last night",
			time.Date(2026, 5, 12, 5, 0, 0, 0, loc), sim.PhaseNight},
		{"exactly at dawn is day",
			time.Date(2026, 5, 12, 7, 0, 0, 0, loc), sim.PhaseDay},
		{"exactly at dusk is night",
			time.Date(2026, 5, 12, 19, 0, 0, 0, loc), sim.PhaseNight},
	}
	for _, c := range cases {
		got, _ := sim.MostRecentBoundary(c.now, dawn.H, dawn.M, dusk.H, dusk.M)
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// TestNextBoundary covers the inverse — what's coming up.
func TestNextBoundary(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	dawn := struct{ H, M int }{7, 0}
	dusk := struct{ H, M int }{19, 0}

	// 10am → next boundary is today's dusk → night.
	now := time.Date(2026, 5, 12, 10, 0, 0, 0, loc)
	gotPhase, gotAt := sim.NextBoundary(now, dawn.H, dawn.M, dusk.H, dusk.M)
	if gotPhase != sim.PhaseNight {
		t.Errorf("10am next phase: %q, want night", gotPhase)
	}
	wantAt := time.Date(2026, 5, 12, 19, 0, 0, 0, loc)
	if !gotAt.Equal(wantAt) {
		t.Errorf("10am next at: %v, want %v", gotAt, wantAt)
	}

	// 9pm → next boundary is tomorrow's dawn → day.
	now = time.Date(2026, 5, 12, 21, 0, 0, 0, loc)
	gotPhase, gotAt = sim.NextBoundary(now, dawn.H, dawn.M, dusk.H, dusk.M)
	if gotPhase != sim.PhaseDay {
		t.Errorf("9pm next phase: %q, want day", gotPhase)
	}
	wantAt = time.Date(2026, 5, 13, 7, 0, 0, 0, loc)
	if !gotAt.Equal(wantAt) {
		t.Errorf("9pm next at: %v, want %v", gotAt, wantAt)
	}
}

// TestApplyPhaseTransition exercises the command — submit, assert Phase
// changed, assert LastTransitionAt stamped, assert published snapshot
// reflects the new phase.
func TestApplyPhaseTransition(t *testing.T) {
	repo, _ := mem.NewRepository()
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	// Seed phase: day (mem default).
	initial := w.Published()
	if initial.Phase != sim.PhaseDay {
		t.Fatalf("initial phase = %q, want %q", initial.Phase, sim.PhaseDay)
	}

	// Transition to night.
	before := time.Now().UTC()
	res, err := w.Send(sim.ApplyPhaseTransition(sim.PhaseNight))
	if err != nil {
		t.Fatalf("apply transition: %v", err)
	}
	tr := res.(sim.PhaseTransitionResult)
	if tr.From != sim.PhaseDay || tr.To != sim.PhaseNight {
		t.Errorf("transition result %+v, want From=day To=night", tr)
	}
	if tr.At.Before(before) {
		t.Errorf("transition At %v predates command submit %v", tr.At, before)
	}

	snap := w.Published()
	if snap.Phase != sim.PhaseNight {
		t.Errorf("post-transition phase = %q, want night", snap.Phase)
	}
	if snap.Environment.LastTransitionAt.IsZero() {
		t.Error("LastTransitionAt not stamped")
	}
}

// TestApplyPhaseTransitionRejectsInvalid covers the validation branch.
func TestApplyPhaseTransitionRejectsInvalid(t *testing.T) {
	repo, _ := mem.NewRepository()
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	_, err = w.Send(sim.ApplyPhaseTransition(sim.Phase("twilight")))
	if err == nil {
		t.Fatal("expected error for invalid phase, got nil")
	}
}

// TestEnvironmentRepoSeed covers Seed + Load returning the seeded values.
func TestEnvironmentRepoSeed(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	repo, handles := mem.NewRepository()
	handles.Environment.Seed(
		sim.WorldEnvironment{
			Atmosphere:       "evening haze over Salem",
			LastTransitionAt: time.Date(2026, 5, 12, 23, 0, 0, 0, time.UTC),
		},
		sim.PhaseNight,
		sim.WorldSettings{
			DawnTime: "06:30",
			DuskTime: "18:30",
			Timezone: "America/New_York",
			Location: loc,
		},
	)

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	if w.Phase != sim.PhaseNight {
		t.Errorf("phase after load = %q, want night", w.Phase)
	}
	if w.Environment.Atmosphere != "evening haze over Salem" {
		t.Errorf("atmosphere after load = %q, want seeded value", w.Environment.Atmosphere)
	}
	if w.Settings.DawnTime != "06:30" {
		t.Errorf("dawn time after load = %q, want 06:30", w.Settings.DawnTime)
	}
}

// TestRunPhaseTicker_ImmediateBootCheckCorrectsStalePhase covers the boot
// check added in ZBBS-WORK-379: RunPhaseTicker re-derives the phase from the
// wall clock immediately, instead of leaving a checkpoint-restored stale
// phase in place until the first tick (up to PhaseTickerInterval later). The
// expected phase is computed via MostRecentBoundary so the assertion is
// independent of when the test runs; the world is seeded with the opposite
// phase plus a long-ago LastTransitionAt, so a flip is guaranteed.
func TestRunPhaseTicker_ImmediateBootCheckCorrectsStalePhase(t *testing.T) {
	loc := time.UTC
	now := time.Now().In(loc)
	// Dawn 00:00 / dusk 23:59. We don't rely on the resulting window — the
	// expected phase is derived the same way the ticker derives it.
	expected, _ := sim.MostRecentBoundary(now, 0, 0, 23, 59)

	stale := sim.PhaseDay
	if expected == sim.PhaseDay {
		stale = sim.PhaseNight
	}

	repo, handles := mem.NewRepository()
	handles.Environment.Seed(
		sim.WorldEnvironment{LastTransitionAt: now.Add(-365 * 24 * time.Hour)},
		stale,
		sim.WorldSettings{DawnTime: "00:00", DuskTime: "23:59", Timezone: "UTC", Location: loc},
	)

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	if w.Phase != stale {
		t.Fatalf("seeded phase = %q, want stale %q", w.Phase, stale)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	go sim.RunPhaseTicker(ctx, w)

	// The boot check corrects the phase far faster than the one-minute
	// PhaseTickerInterval; if it were missing this would only flip after ~60s.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if w.Published().Phase == expected {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("boot check did not correct phase to %q within 2s; still %q",
		expected, w.Published().Phase)
}
