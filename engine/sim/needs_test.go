package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// TestNeedTier covers the four-band classification edge cases.
func TestNeedTier(t *testing.T) {
	hunger, _ := sim.FindNeed("hunger")
	threshold := sim.DefaultHungerRedThreshold // 18

	cases := []struct {
		value int
		want  sim.NeedTier
		label string
	}{
		{0, sim.NeedSilent, ""},
		{9, sim.NeedSilent, ""},       // below the awareness floor (10, LLM-85)
		{10, sim.NeedMild, "peckish"}, // at the floor — first surfaced value
		{17, sim.NeedMild, "peckish"},
		{18, sim.NeedRed, "hungry"},
		{23, sim.NeedRed, "hungry"},
		{24, sim.NeedPeak, "starving"},
	}
	for _, c := range cases {
		gotTier := hunger.Tier(c.value, threshold)
		if gotTier != c.want {
			t.Errorf("Tier(%d) = %d, want %d", c.value, gotTier, c.want)
		}
		gotLabel := hunger.Label(gotTier)
		if gotLabel != c.label {
			t.Errorf("Label for value=%d: %q, want %q", c.value, gotLabel, c.label)
		}
	}
}

// TestClampNeed covers the bounding helper.
func TestClampNeed(t *testing.T) {
	cases := []struct{ in, want int }{
		{-5, 0},
		{0, 0},
		{12, 12},
		{24, 24},
		{50, 24},
	}
	for _, c := range cases {
		got := sim.ClampNeed(c.in)
		if got != c.want {
			t.Errorf("ClampNeed(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestNeedResolveThreshold covers the hysteresis floor.
func TestNeedResolveThreshold(t *testing.T) {
	cases := []struct{ red, want int }{
		{18, 16}, // red - 2
		{8, 6},   // small red, still floored above 1
		{3, 1},   // red - 2 would be 1, stays 1
		{2, 1},   // red - 2 is 0, floors to 1
	}
	for _, c := range cases {
		got := sim.NeedResolveThreshold(c.red)
		if got != c.want {
			t.Errorf("NeedResolveThreshold(%d) = %d, want %d", c.red, got, c.want)
		}
	}
}

// TestApplyConsumption covers the happy path + clamping at both bounds.
func TestApplyConsumption(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {
			ID:       "hannah",
			LLMAgent: "hannah-innkeeper",
			Needs: map[sim.NeedKey]int{
				"hunger":    18,
				"thirst":    12,
				"tiredness": 6,
			},
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	// Drop hunger by 12, raise tiredness by 5, leave thirst alone.
	res, err := w.Send(sim.ApplyConsumption("hannah", sim.NeedDelta{
		"hunger":    -12,
		"tiredness": 5,
	}))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	cr := res.(sim.ConsumptionResult)
	if cr.Needs.Get("hunger") != 6 {
		t.Errorf("hunger = %d, want 6", cr.Needs.Get("hunger"))
	}
	if cr.Needs.Get("thirst") != 12 {
		t.Errorf("thirst = %d, want 12 (unchanged)", cr.Needs.Get("thirst"))
	}
	if cr.Needs.Get("tiredness") != 11 {
		t.Errorf("tiredness = %d, want 11", cr.Needs.Get("tiredness"))
	}

	// Clamp lower bound — drop hunger another 50, should floor at 0.
	res, _ = w.Send(sim.ApplyConsumption("hannah", sim.NeedDelta{"hunger": -50}))
	cr = res.(sim.ConsumptionResult)
	if cr.Needs.Get("hunger") != 0 {
		t.Errorf("clamp low: hunger = %d, want 0", cr.Needs.Get("hunger"))
	}

	// Clamp upper bound — raise thirst by 50, should cap at NeedMax.
	res, _ = w.Send(sim.ApplyConsumption("hannah", sim.NeedDelta{"thirst": 50}))
	cr = res.(sim.ConsumptionResult)
	if cr.Needs.Get("thirst") != sim.NeedMax {
		t.Errorf("clamp high: thirst = %d, want %d", cr.Needs.Get("thirst"), sim.NeedMax)
	}
}

// TestApplyConsumptionUnknownActor covers the not-found error path.
func TestApplyConsumptionUnknownActor(t *testing.T) {
	repo, _ := mem.NewRepository()
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	_, err = w.Send(sim.ApplyConsumption("ghost", sim.NeedDelta{"hunger": -1}))
	if err == nil {
		t.Fatal("expected error for unknown actor, got nil")
	}
}

// TestIncrementNeedsTick covers happy path, decorative skip, sleeping skip,
// and capping behavior (caller-supplied capped hours times settings amount).
func TestIncrementNeedsTick(t *testing.T) {
	repo, handles := mem.NewRepository()
	future := time.Now().UTC().Add(1 * time.Hour)
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"npc": {
			ID:       "npc",
			LLMAgent: "salem-vendor",
			Needs:    map[sim.NeedKey]int{"hunger": 5, "thirst": 5, "tiredness": 5},
		},
		"pc": {
			ID:            "pc",
			LoginUsername: "player1",
			Needs:         map[sim.NeedKey]int{"hunger": 5, "thirst": 5, "tiredness": 5},
		},
		"decorative": {
			ID:    "decorative",
			Needs: map[sim.NeedKey]int{"hunger": 5, "thirst": 5, "tiredness": 5},
		},
		"sleeper": {
			ID:            "sleeper",
			LLMAgent:      "salem-vendor",
			SleepingUntil: &future,
			Needs:         map[sim.NeedKey]int{"hunger": 5, "thirst": 5, "tiredness": 5},
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	res, err := w.Send(sim.IncrementNeedsTick(2)) // 2 hours @ 1/h = +2
	if err != nil {
		t.Fatalf("increment: %v", err)
	}
	if touched := res.(int); touched != 3 {
		t.Errorf("touched = %d, want 3 (npc + pc + sleeper, skip decorative)", touched)
	}

	// Verify per-actor outcomes via snapshot.
	snap := w.Published()
	checkNeed := func(id sim.ActorID, want int) {
		t.Helper()
		a, ok := snap.Actors[id]
		if !ok {
			t.Fatalf("missing %s in snapshot", id)
		}
		if got := a.Needs["hunger"]; got != want {
			t.Errorf("%s hunger = %d, want %d", id, got, want)
		}
	}
	checkNeed("npc", 7)        // 5 + 2
	checkNeed("pc", 7)         // 5 + 2
	checkNeed("decorative", 5) // unchanged (no agent + no login)
	checkNeed("sleeper", 7)    // LLM-135: hunger accrues during sleep (5 + 2)

	// LLM-135: the sleeper accrues hunger + thirst but tiredness is held — the
	// sleep loop is recovering it, so the needs tick must not push it up.
	sleeper := snap.Actors["sleeper"]
	if got := sleeper.Needs["thirst"]; got != 7 {
		t.Errorf("sleeper thirst = %d, want 7 (accrues during sleep)", got)
	}
	if got := sleeper.Needs["tiredness"]; got != 5 {
		t.Errorf("sleeper tiredness = %d, want 5 (held — recovered by the sleep loop)", got)
	}
}

// TestIncrementNeedsTickClamps covers the upper-bound clamp.
func TestIncrementNeedsTickClamps(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"a": {
			ID:       "a",
			LLMAgent: "x",
			Needs:    map[sim.NeedKey]int{"hunger": 23, "thirst": 5, "tiredness": 5},
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	_, err = w.Send(sim.IncrementNeedsTick(5)) // +5; hunger 23 → clamp to 24
	if err != nil {
		t.Fatalf("increment: %v", err)
	}
	snap := w.Published()
	if snap.Actors["a"].Needs["hunger"] != sim.NeedMax {
		t.Errorf("clamped hunger = %d, want %d", snap.Actors["a"].Needs["hunger"], sim.NeedMax)
	}
}

// TestApplyMovementFatigue covers distance math and short-walk floor.
func TestApplyMovementFatigue(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"walker": {
			ID:    "walker",
			Needs: map[sim.NeedKey]int{"hunger": 0, "thirst": 0, "tiredness": 0},
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	// Default MovementFatiguePerTileX100 = 12. Walk 320px (= 10 tiles) →
	// 10 * 12 / 100 = 1.2 → int = 1.
	res, err := w.Send(sim.ApplyMovementFatigue("walker", 0, 0, 320, 0))
	if err != nil {
		t.Fatalf("fatigue: %v", err)
	}
	if bump := res.(int); bump != 1 {
		t.Errorf("320px bump = %d, want 1", bump)
	}

	// Short walk (16px = 0.5 tiles) → 0.5 * 12 / 100 = 0.06 → int floors to 0.
	res, _ = w.Send(sim.ApplyMovementFatigue("walker", 0, 0, 16, 0))
	if bump := res.(int); bump != 0 {
		t.Errorf("short walk bump = %d, want 0", bump)
	}

	// Long walk: 1600px (50 tiles) → 50 * 12 / 100 = 6.
	res, _ = w.Send(sim.ApplyMovementFatigue("walker", 0, 0, 1600, 0))
	if bump := res.(int); bump != 6 {
		t.Errorf("long walk bump = %d, want 6", bump)
	}

	snap := w.Published()
	// 1 + 0 + 6 = 7
	if got := snap.Actors["walker"].Needs["tiredness"]; got != 7 {
		t.Errorf("tiredness after three walks = %d, want 7", got)
	}
}

// TestApplyMovementFatigueDisabled covers the per-tile=0 disable switch.
func TestApplyMovementFatigueDisabled(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"walker": {ID: "walker", Needs: map[sim.NeedKey]int{}},
	})
	handles.Environment.Seed(
		sim.WorldEnvironment{},
		sim.PhaseDay,
		sim.WorldSettings{
			NeedsTickAmount:            sim.DefaultNeedsTickAmount,
			NeedThresholds:             sim.DefaultNeedThresholds(),
			MovementFatiguePerTileX100: 0, // disabled
		},
	)
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	res, _ := w.Send(sim.ApplyMovementFatigue("walker", 0, 0, 10000, 0))
	if bump := res.(int); bump != 0 {
		t.Errorf("disabled walker bump = %d, want 0", bump)
	}
}

// TestDefaultNeedThresholds covers the registry-default builder.
func TestDefaultNeedThresholds(t *testing.T) {
	thr := sim.DefaultNeedThresholds()
	if thr.Get("hunger") != sim.DefaultHungerRedThreshold {
		t.Errorf("hunger default = %d, want %d", thr.Get("hunger"), sim.DefaultHungerRedThreshold)
	}
	if thr.Get("thirst") != sim.DefaultThirstRedThreshold {
		t.Errorf("thirst default = %d, want %d", thr.Get("thirst"), sim.DefaultThirstRedThreshold)
	}
	if thr.Get("tiredness") != sim.DefaultTirednessRedThreshold {
		t.Errorf("tiredness default = %d, want %d", thr.Get("tiredness"), sim.DefaultTirednessRedThreshold)
	}
	// Unknown key falls back to registry-zero (no Need with that key).
	if thr.Get("mood") != 0 {
		t.Errorf("unknown need default = %d, want 0", thr.Get("mood"))
	}
}

// TestNeedLabel covers the convenience function.
func TestNeedLabel(t *testing.T) {
	cases := []struct {
		key       sim.NeedKey
		value     int
		threshold int
		want      string
	}{
		{"hunger", 5, 18, ""},          // silent
		{"hunger", 10, 18, "peckish"},  // mild
		{"hunger", 20, 18, "hungry"},   // red
		{"hunger", 24, 18, "starving"}, // peak
		{"unknown", 20, 18, ""},        // unknown key
	}
	for _, c := range cases {
		got := sim.NeedLabel(c.key, c.value, c.threshold)
		if got != c.want {
			t.Errorf("NeedLabel(%q, %d, %d) = %q, want %q", c.key, c.value, c.threshold, got, c.want)
		}
	}
}
