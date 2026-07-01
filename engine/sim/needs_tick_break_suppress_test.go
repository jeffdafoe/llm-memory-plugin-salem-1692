package sim

import (
	"testing"
	"time"
)

// needs_tick_break_suppress_test.go — LLM-211. An actor on a take_break is
// suppressed the same way a sleeper is (LLM-135): no red need warrants it while
// resting, and its tiredness is held (the break's recovery sweep restores it)
// while hunger + thirst still accrue. This stops the cross-tick take_break churn
// where the reactor kept ending a break to service the actor's own recovering
// need.

// TestNeedsTickBreak_RedHungerAccruesButDoesNotWarrant: an on-break NPC already
// past its red hunger line keeps accruing hunger + thirst but is NOT warranted
// awake, and its tiredness is held. Mirrors the sleep case
// (TestNeedsTickSleep_RedHungerAccruesButDoesNotWarrant).
func TestNeedsTickBreak_RedHungerAccruesButDoesNotWarrant(t *testing.T) {
	a := agentNPCWithNeeds("n", 19, 5, 5) // hunger already past the 18 red line
	until := time.Now().UTC().Add(time.Hour)
	a.BreakUntil = &until
	w := needsTickWorld(1, a)

	if _, err := IncrementNeedsTick(1).Fn(w); err != nil {
		t.Fatalf("IncrementNeedsTick: %v", err)
	}
	if got := a.Needs["hunger"]; got != 20 {
		t.Errorf("hunger = %d, want 20 (a red need still accrues during a break)", got)
	}
	if got := a.Needs["thirst"]; got != 6 {
		t.Errorf("thirst = %d, want 6 (still accrues during a break)", got)
	}
	if got := a.Needs["tiredness"]; got != 5 {
		t.Errorf("tiredness = %d, want 5 (held during a break — recovered by the break sweep)", got)
	}
	if a.WarrantedSince != nil {
		t.Errorf("WarrantedSince set on an on-break actor; should stay nil")
	}
	if hasNeedThresholdWarrant(a) {
		t.Errorf("need_threshold warrant stamped on an on-break actor; kinds = %v", warrantKinds(a))
	}
}

// TestActorActionableRedNeed_OnBreakSuppresses: the shared predicate behind both
// warrant paths (hourly producer + red-need backstop) reports nothing while on a
// break, then reports the red need once the break ends. Mirrors the sleep case.
func TestActorActionableRedNeed_OnBreakSuppresses(t *testing.T) {
	a := agentNPCWithNeeds("n", 19, 5, 5)
	w := needsTickWorld(1, a)
	now := time.Now().UTC()
	nowMinute := localMinuteOfDay(w, now)

	until := now.Add(time.Hour)
	a.BreakUntil = &until
	if need, ok := actorActionableRedNeed(w, a, now, nowMinute); ok {
		t.Errorf("actorActionableRedNeed = (%q, true) while on break; want (\"\", false)", need)
	}

	a.BreakUntil = nil
	if need, ok := actorActionableRedNeed(w, a, now, nowMinute); !ok || need != "hunger" {
		t.Errorf("actorActionableRedNeed = (%q, %v) off break; want (hunger, true)", need, ok)
	}
}

// TestActorActionableRedNeed_OnBreakSuppressesOwnTiredness: the exact churn case.
// An on-shift keeper that is exhausted (tiredness past its red line, hunger/thirst
// fine) must NOT warrant while on a break — the break is curing the very tiredness
// that would otherwise wake it (the reactor's endBreak-at-emit). Once the break
// ends, the standing red tiredness surfaces (on shift), so it is a suppression, not
// a permanent silence.
func TestActorActionableRedNeed_OnBreakSuppressesOwnTiredness(t *testing.T) {
	a := agentNPCWithNeeds("keeper", 5, 5, 20) // exhausted; hunger/thirst below their red lines
	start, end := 480, 1080                    // 08:00–18:00 shift
	a.ScheduleStartMin, a.ScheduleEndMin = &start, &end
	w := needsTickWorld(1, a)
	now := time.Now().UTC()
	nowMinute := 600 // 10:00 — inside the shift window, so tiredness is not off-shift-gated

	until := now.Add(time.Hour)
	a.BreakUntil = &until
	if need, ok := actorActionableRedNeed(w, a, now, nowMinute); ok {
		t.Errorf("actorActionableRedNeed = (%q, true) for an on-break exhausted keeper; want (\"\", false)", need)
	}

	a.BreakUntil = nil
	if need, ok := actorActionableRedNeed(w, a, now, nowMinute); !ok || need != "tiredness" {
		t.Errorf("actorActionableRedNeed = (%q, %v) after the break ends; want (tiredness, true)", need, ok)
	}
}
