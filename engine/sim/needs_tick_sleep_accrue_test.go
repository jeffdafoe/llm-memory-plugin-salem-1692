package sim

import (
	"testing"
	"time"
)

// needs_tick_sleep_accrue_test.go — LLM-135. Hunger + thirst accrue while an
// actor sleeps (so it wakes hungry and seeks a meal), tiredness is held (the
// sleep loop recovers it), and the climbing need is NOT surfaced:
// actorActionableRedNeed returns nothing for a sleeping actor, so neither the
// hourly warrant producer nor the red-need backstop sweep wakes it.

// TestNeedsTickSleep_RedHungerAccruesButDoesNotWarrant: a sleeping NPC already
// past its red hunger line keeps accruing hunger but is NOT warranted awake —
// the "don't surface it" guarantee. An identical AWAKE NPC would warrant (the
// crossing case is covered in needs_threshold_test.go), so this isolates the
// sleep suppression.
func TestNeedsTickSleep_RedHungerAccruesButDoesNotWarrant(t *testing.T) {
	a := agentNPCWithNeeds("n", 19, 5, 5) // hunger already past the 18 red line
	until := time.Now().UTC().Add(time.Hour)
	a.SleepingUntil = &until
	w := needsTickWorld(1, a)

	if _, err := IncrementNeedsTick(1).Fn(w); err != nil {
		t.Fatalf("IncrementNeedsTick: %v", err)
	}
	if got := a.Needs["hunger"]; got != 20 {
		t.Errorf("hunger = %d, want 20 (a red need still accrues during sleep)", got)
	}
	if got := a.Needs["tiredness"]; got != 5 {
		t.Errorf("tiredness = %d, want 5 (held during sleep)", got)
	}
	if a.WarrantedSince != nil {
		t.Errorf("WarrantedSince set on a sleeping actor; should stay nil")
	}
	if hasNeedThresholdWarrant(a) {
		t.Errorf("need_threshold warrant stamped on a sleeping actor; kinds = %v", warrantKinds(a))
	}
}

// TestActorActionableRedNeed_SleepingSuppresses: the shared predicate behind
// both warrant paths reports nothing while asleep, then reports the red need
// once awake. Pinning it here guards the single point that keeps the backstop
// sweep and the hourly producer from waking a sleeper.
func TestActorActionableRedNeed_SleepingSuppresses(t *testing.T) {
	a := agentNPCWithNeeds("n", 19, 5, 5)
	w := needsTickWorld(1, a)
	now := time.Now().UTC()
	nowMinute := localMinuteOfDay(w, now)

	until := now.Add(time.Hour)
	a.SleepingUntil = &until
	if need, ok := actorActionableRedNeed(w, a, now, nowMinute); ok {
		t.Errorf("actorActionableRedNeed = (%q, true) while asleep; want (\"\", false)", need)
	}

	a.SleepingUntil = nil
	if need, ok := actorActionableRedNeed(w, a, now, nowMinute); !ok || need != "hunger" {
		t.Errorf("actorActionableRedNeed = (%q, %v) awake; want (hunger, true)", need, ok)
	}
}

// TestNeedsTickSleep_NoWakeWhileAsleep_ThenBackstopWarrantsOnWake: a sleeper
// whose hunger CROSSES the red line mid-sleep is warranted by neither the
// hourly tick nor the backstop sweep while asleep; once awake the same
// backstop sweep surfaces the standing red need and warrants it. This locks
// down the "climb silently, surface on wake" transition end-to-end.
func TestNeedsTickSleep_NoWakeWhileAsleep_ThenBackstopWarrantsOnWake(t *testing.T) {
	a := agentNPCWithNeeds("n", 17, 5, 5) // one below the 18 red line
	until := time.Now().UTC().Add(time.Hour)
	a.SleepingUntil = &until
	w := needsTickWorld(1, a)

	// Hourly tick: hunger crosses 18 while asleep — accrues, must not warrant.
	if _, err := IncrementNeedsTick(1).Fn(w); err != nil {
		t.Fatalf("IncrementNeedsTick: %v", err)
	}
	if got := a.Needs["hunger"]; got != 18 {
		t.Fatalf("hunger = %d, want 18 (crossed the red line while asleep)", got)
	}
	if a.WarrantedSince != nil || hasNeedThresholdWarrant(a) || a.TickInFlight {
		t.Fatalf("hourly tick warranted a sleeper: warranted=%v inFlight=%v kinds=%v",
			a.WarrantedSince != nil, a.TickInFlight, warrantKinds(a))
	}

	// Backstop sweep while still asleep: also must not warrant.
	now := time.Now().UTC()
	if _, err := EvaluateRedNeedBackstop(now).Fn(w); err != nil {
		t.Fatalf("EvaluateRedNeedBackstop (asleep): %v", err)
	}
	if a.WarrantedSince != nil || hasNeedThresholdWarrant(a) {
		t.Fatalf("backstop warranted a sleeper: warranted=%v kinds=%v",
			a.WarrantedSince != nil, warrantKinds(a))
	}

	// Wake the actor — the standing red hunger now surfaces and the backstop
	// stamps a single need_threshold warrant.
	a.SleepingUntil = nil
	if _, err := EvaluateRedNeedBackstop(now).Fn(w); err != nil {
		t.Fatalf("EvaluateRedNeedBackstop (awake): %v", err)
	}
	if !hasNeedThresholdWarrant(a) {
		t.Fatalf("backstop did not warrant the red need on wake; kinds = %v", warrantKinds(a))
	}
}
