package sim

import (
	"testing"
	"time"
)

// take_break_test.go — substrate tests for ZBBS-HOME-284 #4. Reuses the
// sleepTestWorld / npc / intptr helpers from npc_sleep_test.go (same package).

func TestResolveBreakUntil(t *testing.T) {
	// 09:00 UTC anchor for the "today" comparisons.
	at := time.Date(2026, 5, 22, 9, 0, 0, 0, time.UTC)

	t.Run("default 4h when until_hour omitted", func(t *testing.T) {
		got, err := resolveBreakUntil(time.UTC, 0, at)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := at.Add(DefaultTakeBreakHours * time.Hour)
		if !got.Equal(want) {
			t.Errorf("breakUntil = %v, want %v", got, want)
		}
	})

	t.Run("until_hour resolves to that hour today", func(t *testing.T) {
		got, err := resolveBreakUntil(time.UTC, 13, at)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Hour() != 13 || got.Day() != 22 {
			t.Errorf("breakUntil = %v, want 13:00 on the 22nd", got)
		}
	})

	t.Run("past hour rejected", func(t *testing.T) {
		_, err := resolveBreakUntil(time.UTC, 8, at) // 08:00 < 09:00
		if err == nil {
			t.Fatal("expected rejection for a past until_hour, got nil")
		}
	})

	t.Run("current hour rejected (not strictly after now)", func(t *testing.T) {
		_, err := resolveBreakUntil(time.UTC, 9, at) // 09:00 == now
		if err == nil {
			t.Fatal("expected rejection for the current hour, got nil")
		}
	})

	t.Run("timezone anchored to world location, not UTC", func(t *testing.T) {
		loc := time.FixedZone("X+5", 5*3600)
		// 04:00 UTC == 09:00 in loc. until_hour 13 must resolve to 13:00 LOCAL.
		atUTC := time.Date(2026, 5, 22, 4, 0, 0, 0, time.UTC)
		got, err := resolveBreakUntil(loc, 13, atUTC)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if h := got.In(loc).Hour(); h != 13 {
			t.Errorf("breakUntil local hour = %d, want 13", h)
		}
	})

	t.Run("nil location falls back to UTC", func(t *testing.T) {
		got, err := resolveBreakUntil(nil, 0, at)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.Equal(at.Add(DefaultTakeBreakHours * time.Hour)) {
			t.Errorf("breakUntil = %v, want default off UTC", got)
		}
	})

	t.Run("out-of-range hour rejected (direct caller guard)", func(t *testing.T) {
		for _, h := range []int{-1, 24, 99} {
			if _, err := resolveBreakUntil(time.UTC, h, at); err == nil {
				t.Errorf("untilHour=%d: want error, got nil", h)
			}
		}
	})
}

func TestTakeBreakCommand_Success(t *testing.T) {
	at := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	a := npc("k", KindNPCStateful)
	w := sleepTestWorld(a)

	var captured *TookBreak
	w.Subscribe(SubscriberFunc(func(_ *World, evt Event) {
		if tb, ok := evt.(*TookBreak); ok {
			captured = tb
		}
	}))

	if _, err := TakeBreak("k", "feeling unwell", 0, at).Fn(w); err != nil {
		t.Fatalf("TakeBreak: %v", err)
	}

	wantUntil := at.Add(DefaultTakeBreakHours * time.Hour)
	if a.BreakUntil == nil || !a.BreakUntil.Equal(wantUntil) {
		t.Errorf("BreakUntil = %v, want %v", a.BreakUntil, wantUntil)
	}
	if a.LastTirednessRecoveryAt == nil || !a.LastTirednessRecoveryAt.Equal(at) {
		t.Errorf("recovery cursor = %v, want %v (stamped at window open)", a.LastTirednessRecoveryAt, at)
	}
	if a.State != StateResting {
		t.Errorf("State = %q, want %q", a.State, StateResting)
	}
	if captured == nil {
		t.Fatal("expected a TookBreak event to be emitted")
	}
	if captured.Reason != "feeling unwell" || captured.ActorID != "k" {
		t.Errorf("TookBreak = %+v, want reason/actor preserved", captured)
	}
	if !captured.BreakUntil.Equal(wantUntil) {
		t.Errorf("TookBreak.BreakUntil = %v, want %v", captured.BreakUntil, wantUntil)
	}
}

func TestTakeBreakCommand_UntilHour(t *testing.T) {
	at := time.Date(2026, 5, 22, 9, 0, 0, 0, time.UTC)
	a := npc("k", KindNPCStateful)
	w := sleepTestWorld(a)

	if _, err := TakeBreak("k", "back after lunch", 13, at).Fn(w); err != nil {
		t.Fatalf("TakeBreak: %v", err)
	}
	if a.BreakUntil == nil || a.BreakUntil.Hour() != 13 {
		t.Errorf("BreakUntil = %v, want 13:00", a.BreakUntil)
	}
}

func TestTakeBreakCommand_AlreadyOnBreakRejected(t *testing.T) {
	at := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	a := npc("k", KindNPCStateful)
	existing := at.Add(2 * time.Hour)
	a.BreakUntil = &existing
	a.State = StateResting
	w := sleepTestWorld(a)

	if _, err := TakeBreak("k", "again", 0, at).Fn(w); err == nil {
		t.Fatal("expected rejection when already on break, got nil")
	}
	// Window untouched by the rejected call.
	if !a.BreakUntil.Equal(existing) {
		t.Errorf("BreakUntil changed on rejected take_break: %v != %v", *a.BreakUntil, existing)
	}
}

func TestTakeBreakCommand_PastHourRejected(t *testing.T) {
	at := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	a := npc("k", KindNPCStateful)
	w := sleepTestWorld(a)

	if _, err := TakeBreak("k", "too late", 8, at).Fn(w); err == nil {
		t.Fatal("expected rejection for a past until_hour, got nil")
	}
	if a.BreakUntil != nil {
		t.Errorf("BreakUntil set despite rejection: %v", a.BreakUntil)
	}
}

func TestTakeBreakCommand_MissingActor(t *testing.T) {
	at := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	w := sleepTestWorld()
	if _, err := TakeBreak("ghost", "nobody", 0, at).Fn(w); err == nil {
		t.Fatal("expected error for an actor not in world, got nil")
	}
}

func TestTakeBreakCommand_OutOfRangeHourRejected(t *testing.T) {
	at := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	a := npc("k", KindNPCStateful)
	w := sleepTestWorld(a)
	// Direct caller passes a nonsense hour — must fail loudly, not silently
	// become a 4-hour default break.
	if _, err := TakeBreak("k", "bad hour", 99, at).Fn(w); err == nil {
		t.Fatal("expected rejection for out-of-range until_hour, got nil")
	}
	if a.BreakUntil != nil {
		t.Errorf("BreakUntil set despite rejection: %v", a.BreakUntil)
	}
}

func TestExpireEndedBreaks(t *testing.T) {
	now := time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)

	ended := npc("ended", KindNPCStateful)
	past := now.Add(-time.Minute)
	ended.BreakUntil = &past
	cur := now.Add(-30 * time.Minute)
	ended.LastTirednessRecoveryAt = &cur
	ended.State = StateResting

	ongoing := npc("ongoing", KindNPCStateful)
	future := now.Add(time.Hour)
	ongoing.BreakUntil = &future
	ongoing.State = StateResting

	w := sleepTestWorld(ended, ongoing)
	res, err := ExpireEndedBreaks(now).Fn(w)
	if err != nil {
		t.Fatalf("ExpireEndedBreaks: %v", err)
	}
	if n := res.(int); n != 1 {
		t.Errorf("ended count = %d, want 1", n)
	}

	if ended.BreakUntil != nil {
		t.Errorf("ended actor still has BreakUntil: %v", ended.BreakUntil)
	}
	if ended.LastTirednessRecoveryAt != nil {
		t.Errorf("ended actor still has a recovery cursor: %v", ended.LastTirednessRecoveryAt)
	}
	if ended.State != StateIdle {
		t.Errorf("ended actor State = %q, want %q", ended.State, StateIdle)
	}

	if ongoing.BreakUntil == nil || !ongoing.BreakUntil.Equal(future) {
		t.Errorf("ongoing break cleared early: %v", ongoing.BreakUntil)
	}
	if ongoing.State != StateResting {
		t.Errorf("ongoing actor State = %q, want %q (still resting)", ongoing.State, StateResting)
	}
}

// TestEndBreakPreservesSleep guards the defensive SleepingUntil branch in
// endBreak: an actor that is somehow both on break and asleep keeps its sleep
// window's cursor + StateSleeping when the break clears.
func TestEndBreakPreservesSleep(t *testing.T) {
	now := time.Date(2026, 5, 22, 2, 0, 0, 0, time.UTC)
	a := npc("k", KindNPCStateful)
	pastBreak := now.Add(-time.Minute)
	a.BreakUntil = &pastBreak
	sleepUntil := now.Add(6 * time.Hour)
	a.SleepingUntil = &sleepUntil
	cur := now.Add(-10 * time.Minute)
	a.LastTirednessRecoveryAt = &cur
	a.State = StateSleeping
	w := sleepTestWorld(a)

	endBreak(w, a)

	if a.BreakUntil != nil {
		t.Errorf("BreakUntil not cleared: %v", a.BreakUntil)
	}
	if a.SleepingUntil == nil || !a.SleepingUntil.Equal(sleepUntil) {
		t.Errorf("SleepingUntil disturbed: %v", a.SleepingUntil)
	}
	if a.LastTirednessRecoveryAt == nil || !a.LastTirednessRecoveryAt.Equal(cur) {
		t.Errorf("recovery cursor cleared while still sleeping: %v", a.LastTirednessRecoveryAt)
	}
	if a.State != StateSleeping {
		t.Errorf("State = %q, want %q (still sleeping)", a.State, StateSleeping)
	}
}

// TestTakeBreakRecoverySeam is the #1↔#4 integration: a take_break sets up a
// window the tiredness sweep then credits against. At the production rate
// (0.04/min) 30 minutes of break recovers exactly 1 unit.
func TestTakeBreakRecoverySeam(t *testing.T) {
	at := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	a := npc("k", KindNPCStateful) // npc() seeds tiredness=20
	w := sleepTestWorld(a)
	w.Settings.TirednessRecoveryPerMinuteX100 = DefaultTirednessRecoveryPerMinuteX100 // 4 → 0.04/min

	if _, err := TakeBreak("k", "need to rest", 0, at).Fn(w); err != nil {
		t.Fatalf("TakeBreak: %v", err)
	}
	// Sweep 30 minutes into the break: 30 * 0.04 = 1.2 → 1 whole unit.
	if _, err := RecoverTiredness(at.Add(30 * time.Minute)).Fn(w); err != nil {
		t.Fatalf("RecoverTiredness: %v", err)
	}
	if got := a.Needs["tiredness"]; got != 19 {
		t.Errorf("tiredness = %d, want 19 (20 - 1 unit over 30 min at 0.04/min)", got)
	}
}
