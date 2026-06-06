package sim

import (
	"testing"
	"time"
)

// rest_state_heal_test.go — ZBBS-HOME-410. Reuses npc / sleepTestWorld from
// npc_sleep_test.go (same package).

// TestHealOrphanedRestStates: an agent NPC stranded in a rest macro-state with
// NO live window is reset to idle; an actor with a live window (genuinely
// resting/sleeping) is left untouched.
func TestHealOrphanedRestStates(t *testing.T) {
	now := time.Date(2026, 6, 6, 16, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)

	orphanRest := npc("orphan-rest", KindNPCStateful)
	orphanRest.State = StateResting // stranded: enum set, BreakUntil nil

	orphanSleep := npc("orphan-sleep", KindNPCStateful)
	orphanSleep.State = StateSleeping // stranded: enum set, SleepingUntil nil

	liveBreak := npc("live-break", KindNPCStateful)
	liveBreak.BreakUntil = &future
	liveBreak.State = StateResting

	liveSleep := npc("live-sleep", KindNPCStateful)
	liveSleep.SleepingUntil = &future
	liveSleep.State = StateSleeping

	idle := npc("idle", KindNPCStateful)
	idle.State = StateIdle

	w := sleepTestWorld(orphanRest, orphanSleep, liveBreak, liveSleep, idle)
	res, err := HealOrphanedRestStates().Fn(w)
	if err != nil {
		t.Fatalf("HealOrphanedRestStates: %v", err)
	}
	if n := res.(int); n != 2 {
		t.Errorf("healed = %d, want 2 (the two orphans only)", n)
	}

	if orphanRest.State != StateIdle {
		t.Errorf("orphan resting State = %q, want %q", orphanRest.State, StateIdle)
	}
	if orphanSleep.State != StateIdle {
		t.Errorf("orphan sleeping State = %q, want %q", orphanSleep.State, StateIdle)
	}
	if liveBreak.State != StateResting || liveBreak.BreakUntil == nil {
		t.Errorf("live break disturbed: State=%q BreakUntil=%v", liveBreak.State, liveBreak.BreakUntil)
	}
	if liveSleep.State != StateSleeping || liveSleep.SleepingUntil == nil {
		t.Errorf("live sleep disturbed: State=%q SleepingUntil=%v", liveSleep.State, liveSleep.SleepingUntil)
	}
	if idle.State != StateIdle {
		t.Errorf("idle actor disturbed: State=%q", idle.State)
	}
}

// TestHealOrphanedRestStates_SkipsNonAgents: a non-agent actor (decorative) in a
// rest state with no window is left alone — its sprite is set-dressing, not a
// stuck, reactor-driven agent.
func TestHealOrphanedRestStates_SkipsNonAgents(t *testing.T) {
	deco := npc("deco", KindDecorative)
	deco.State = StateSleeping // static sleeping sprite, no SleepingUntil

	w := sleepTestWorld(deco)
	res, err := HealOrphanedRestStates().Fn(w)
	if err != nil {
		t.Fatalf("HealOrphanedRestStates: %v", err)
	}
	if n := res.(int); n != 0 {
		t.Errorf("healed = %d, want 0 (non-agent skipped)", n)
	}
	if deco.State != StateSleeping {
		t.Errorf("decorative woken: State=%q, want %q (set-dressing left alone)", deco.State, StateSleeping)
	}
}

// TestClearRestForReset_AgentEndsBreak: the set-needs reset path ends an agent
// NPC's live break properly — window nil AND macro-state reset to idle (the bug
// was nil-ing the window while leaving StateResting behind).
func TestClearRestForReset_AgentEndsBreak(t *testing.T) {
	now := time.Date(2026, 6, 6, 16, 0, 0, 0, time.UTC)
	a := npc("k", KindNPCStateful)
	future := now.Add(time.Hour)
	a.BreakUntil = &future
	cur := now.Add(-10 * time.Minute)
	a.LastTirednessRecoveryAt = &cur
	a.State = StateResting
	w := sleepTestWorld(a)

	ClearRestForReset(w, a)

	if a.BreakUntil != nil {
		t.Errorf("BreakUntil not cleared: %v", a.BreakUntil)
	}
	if a.LastTirednessRecoveryAt != nil {
		t.Errorf("recovery cursor not dropped: %v", a.LastTirednessRecoveryAt)
	}
	if a.State != StateIdle {
		t.Errorf("State = %q, want %q", a.State, StateIdle)
	}
}

// TestClearRestForReset_AgentHealsOrphan: the reset also recovers an already-
// stranded agent NPC (enum set, no window).
func TestClearRestForReset_AgentHealsOrphan(t *testing.T) {
	a := npc("k", KindNPCStateful)
	a.State = StateResting // orphan: no BreakUntil
	w := sleepTestWorld(a)

	ClearRestForReset(w, a)

	if a.State != StateIdle {
		t.Errorf("orphan State = %q, want %q", a.State, StateIdle)
	}
}

// TestClearRestForReset_NonAgentNilsWindowsOnly: for a non-agent actor the reset
// nils any rest window but leaves the macro-state untouched (pre-HOME-410
// behavior — its state isn't reactor-driven).
func TestClearRestForReset_NonAgentNilsWindowsOnly(t *testing.T) {
	now := time.Date(2026, 6, 6, 16, 0, 0, 0, time.UTC)
	a := npc("p", KindDecorative)
	future := now.Add(time.Hour)
	a.BreakUntil = &future
	a.State = StateResting
	w := sleepTestWorld(a)

	ClearRestForReset(w, a)

	if a.BreakUntil != nil {
		t.Errorf("non-agent BreakUntil not cleared: %v", a.BreakUntil)
	}
	if a.State != StateResting {
		t.Errorf("non-agent State = %q, want %q (left untouched)", a.State, StateResting)
	}
}

// TestEndRestState_DualWindow locks the postcondition for the malformed overlap
// where an actor holds BOTH a break and a sleep window (mutually exclusive in
// normal operation, but endRestState defends it): after the call both windows
// are nil, the recovery cursor is dropped, and State is idle — regardless of
// which rest enum the actor carried. Guards against a future endBreak/wakeNPC
// refactor silently breaking the dual-window path (the reviewed concern).
func TestEndRestState_DualWindow(t *testing.T) {
	now := time.Date(2026, 6, 6, 16, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	for _, st := range []ActorState{StateResting, StateSleeping} {
		a := npc("dual", KindNPCStateful)
		a.BreakUntil = &future
		a.SleepingUntil = &future
		cur := now.Add(-5 * time.Minute)
		a.LastTirednessRecoveryAt = &cur
		a.State = st
		w := sleepTestWorld(a)

		endRestState(w, a)

		if a.BreakUntil != nil || a.SleepingUntil != nil {
			t.Errorf("start=%q: windows not both cleared: break=%v sleep=%v", st, a.BreakUntil, a.SleepingUntil)
		}
		if a.LastTirednessRecoveryAt != nil {
			t.Errorf("start=%q: recovery cursor not dropped: %v", st, a.LastTirednessRecoveryAt)
		}
		if a.State != StateIdle {
			t.Errorf("start=%q: State = %q, want %q", st, a.State, StateIdle)
		}
	}
}
