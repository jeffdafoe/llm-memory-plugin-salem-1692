package sim

import (
	"testing"
	"time"
)

// needs_threshold_test.go — ZBBS-WORK-277, tick-driver producer #1. Covers the
// need-threshold warrant stamping folded into IncrementNeedsTick: who warrants,
// the level re-pressure (ZBBS-HOME-329), the tiredness-on-shift carve-out, and
// the one-per-tick gate.
// Reuses the sleepTestWorld / npc / intptr helpers from npc_sleep_test.go (same
// package).

// needsTickWorld builds a test world with the needs-tick knobs set: a per-hour
// increment amount and default thresholds (nil NeedThresholds → registry
// defaults: hunger 18, thirst 12, tiredness 20).
func needsTickWorld(amount int, actors ...*Actor) *World {
	w := sleepTestWorld(actors...)
	w.Settings.NeedsTickAmount = amount
	return w
}

// agentNPCWithNeeds returns a stateful-VA NPC seeded with the given need values
// and no schedule (always off-shift unless one is set by the caller).
func agentNPCWithNeeds(id ActorID, hunger, thirst, tiredness int) *Actor {
	return &Actor{
		ID:       id,
		Kind:     KindNPCStateful,
		LLMAgent: string(id) + "-agent",
		Needs:    map[NeedKey]int{"hunger": hunger, "thirst": thirst, "tiredness": tiredness},
	}
}

// warrantKinds returns the kinds of all warrants stamped on the actor.
func warrantKinds(a *Actor) []WarrantKind {
	out := make([]WarrantKind, 0, len(a.Warrants))
	for _, m := range a.Warrants {
		if m.Reason != nil {
			out = append(out, m.Reason.Kind())
		}
	}
	return out
}

func hasNeedThresholdWarrant(a *Actor) bool {
	for _, k := range warrantKinds(a) {
		if k == WarrantKindNeedThreshold {
			return true
		}
	}
	return false
}

// TestNeedThreshold_HungerCrossingStamps: hunger climbs from below the red
// threshold (18) to at/above it → a single need_threshold warrant, carrying
// the hunger need.
func TestNeedThreshold_HungerCrossingStamps(t *testing.T) {
	a := agentNPCWithNeeds("n", 17, 5, 5) // hunger one below the 18 red threshold
	w := needsTickWorld(1, a)

	if _, err := IncrementNeedsTick(1).Fn(w); err != nil {
		t.Fatalf("IncrementNeedsTick: %v", err)
	}
	if a.Needs["hunger"] != 18 {
		t.Fatalf("hunger = %d, want 18 (17 + 1)", a.Needs["hunger"])
	}
	if a.WarrantedSince == nil {
		t.Error("WarrantedSince not stamped after hunger crossed the red threshold")
	}
	if !hasNeedThresholdWarrant(a) {
		t.Fatalf("no need_threshold warrant stamped; kinds = %v", warrantKinds(a))
	}
	for _, m := range a.Warrants {
		if r, ok := m.Reason.(NeedThresholdWarrantReason); ok && r.Need != "hunger" {
			t.Errorf("warrant Need = %q, want hunger", r.Need)
		}
	}
}

// TestNeedThreshold_AlreadyRedRePressures: ZBBS-HOME-329 made need warrants
// LEVEL-triggered. A need already at/over its red threshold — including one
// pegged at the max (the stuck-maxed case that could never recover under the
// old edge trigger) — re-stamps a need_threshold warrant each tick while it
// stays red, so the actor keeps getting goal pressure to resolve it.
func TestNeedThreshold_AlreadyRedRePressures(t *testing.T) {
	a := agentNPCWithNeeds("n", 24, 5, 5) // hunger pegged at the max, well past 18
	w := needsTickWorld(1, a)

	if _, err := IncrementNeedsTick(1).Fn(w); err != nil {
		t.Fatalf("IncrementNeedsTick: %v", err)
	}
	if a.Needs["hunger"] != 24 {
		t.Fatalf("hunger = %d, want 24 (already clamped at max)", a.Needs["hunger"])
	}
	if a.WarrantedSince == nil {
		t.Error("WarrantedSince not stamped for a maxed-out need (level re-pressure)")
	}
	if !hasNeedThresholdWarrant(a) {
		t.Errorf("no need_threshold warrant for a need still at its red line; kinds = %v", warrantKinds(a))
	}
}

// TestNeedThreshold_TirednessOffShiftSuppressed: tiredness crossing its
// threshold (20) while off-shift does NOT warrant — overnight tiredness is the
// deterministic sleep loop's job.
func TestNeedThreshold_TirednessOffShiftSuppressed(t *testing.T) {
	a := agentNPCWithNeeds("n", 5, 5, 19) // tiredness one below the 20 red threshold
	// No schedule → always off-shift.
	w := needsTickWorld(1, a)

	if _, err := IncrementNeedsTick(1).Fn(w); err != nil {
		t.Fatalf("IncrementNeedsTick: %v", err)
	}
	if a.Needs["tiredness"] != 20 {
		t.Fatalf("tiredness = %d, want 20", a.Needs["tiredness"])
	}
	if a.WarrantedSince != nil || hasNeedThresholdWarrant(a) {
		t.Errorf("off-shift tiredness crossing should not warrant; kinds = %v", warrantKinds(a))
	}
}

// TestNeedThreshold_TirednessOnShiftStamps: the same tiredness crossing DOES
// warrant when the actor is on shift (→ deliberate → take_break).
func TestNeedThreshold_TirednessOnShiftStamps(t *testing.T) {
	a := agentNPCWithNeeds("n", 5, 5, 19)
	// Always-on-shift window. isActorOnShift treats end as EXCLUSIVE
	// (nowMinute >= start && nowMinute < end for start <= end), and
	// IncrementNeedsTick reads the wall clock itself (not injectable here), so
	// end=1440 makes every minute-of-day 0..1439 on-shift — robust regardless
	// of when the test runs. (1439 would leave 23:59 off-shift and flake.)
	a.ScheduleStartMin = intptr(0)
	a.ScheduleEndMin = intptr(1440)
	w := needsTickWorld(1, a)

	if _, err := IncrementNeedsTick(1).Fn(w); err != nil {
		t.Fatalf("IncrementNeedsTick: %v", err)
	}
	if !hasNeedThresholdWarrant(a) {
		t.Fatalf("on-shift tiredness crossing should warrant; kinds = %v", warrantKinds(a))
	}
	for _, m := range a.Warrants {
		if r, ok := m.Reason.(NeedThresholdWarrantReason); ok && r.Need != "tiredness" {
			t.Errorf("warrant Need = %q, want tiredness", r.Need)
		}
	}
}

// TestNeedThreshold_PCNotWarranted: a PC accrues needs but does not warrant —
// PCs are player-driven and don't reactor-tick.
func TestNeedThreshold_PCNotWarranted(t *testing.T) {
	seen := time.Now().UTC() // present — LLM-450 freezes needs only for an OFFLINE PC
	pc := &Actor{
		ID:            "p",
		Kind:          KindPC,
		LoginUsername: "player1",
		LastPCSeenAt:  &seen,
		Needs:         map[NeedKey]int{"hunger": 17, "thirst": 5, "tiredness": 5},
	}
	w := needsTickWorld(1, pc)

	if _, err := IncrementNeedsTick(1).Fn(w); err != nil {
		t.Fatalf("IncrementNeedsTick: %v", err)
	}
	if pc.Needs["hunger"] != 18 {
		t.Errorf("PC hunger = %d, want 18 (PCs still accrue)", pc.Needs["hunger"])
	}
	if pc.WarrantedSince != nil || hasNeedThresholdWarrant(pc) {
		t.Errorf("PC should not be warranted; kinds = %v", warrantKinds(pc))
	}
}

// TestNeedThreshold_OfflinePCFrozen: an offline (presence-stale) PC accrues NO
// needs — its character is in suspended animation while the player is away
// (LLM-450), so it doesn't return starving and exhausted.
func TestNeedThreshold_OfflinePCFrozen(t *testing.T) {
	pc := &Actor{
		ID:            "p",
		Kind:          KindPC,
		LoginUsername: "player1",
		// LastPCSeenAt nil => presence-stale => offline.
		Needs: map[NeedKey]int{"hunger": 17, "thirst": 5, "tiredness": 5},
	}
	w := needsTickWorld(1, pc)

	if _, err := IncrementNeedsTick(1).Fn(w); err != nil {
		t.Fatalf("IncrementNeedsTick: %v", err)
	}
	if pc.Needs["hunger"] != 17 || pc.Needs["thirst"] != 5 || pc.Needs["tiredness"] != 5 {
		t.Errorf("offline PC needs must be frozen; got hunger=%d thirst=%d tiredness=%d, want 17/5/5",
			pc.Needs["hunger"], pc.Needs["thirst"], pc.Needs["tiredness"])
	}
}

// TestNeedThreshold_VisitorNotWarranted: a transient visitor (KindNPCShared +
// VisitorState) accrues needs but does not warrant — its ExpiresAt lifecycle
// drives it, not need-pressure.
func TestNeedThreshold_VisitorNotWarranted(t *testing.T) {
	v := agentNPCWithNeeds("v", 17, 5, 5)
	v.Kind = KindNPCShared
	v.VisitorState = &VisitorState{Archetype: "traveler", ExpiresAt: time.Now().Add(time.Hour)}
	w := needsTickWorld(1, v)

	if _, err := IncrementNeedsTick(1).Fn(w); err != nil {
		t.Fatalf("IncrementNeedsTick: %v", err)
	}
	if v.WarrantedSince != nil || hasNeedThresholdWarrant(v) {
		t.Errorf("transient visitor should not be warranted; kinds = %v", warrantKinds(v))
	}
}

// TestNeedThreshold_AlreadyWarrantedSkipped: an actor with an open warrant
// cycle is left alone (decision A) — the pending tick's perception already
// surfaces the need.
func TestNeedThreshold_AlreadyWarrantedSkipped(t *testing.T) {
	a := agentNPCWithNeeds("n", 17, 5, 5)
	since := time.Now().Add(-time.Minute)
	a.WarrantedSince = &since
	a.Warrants = []WarrantMeta{{TriggerActorID: "n", Reason: BasicWarrantReason{K: WarrantKindNPCSpoke}}}
	w := needsTickWorld(1, a)

	if _, err := IncrementNeedsTick(1).Fn(w); err != nil {
		t.Fatalf("IncrementNeedsTick: %v", err)
	}
	// Still accrues, but no new need_threshold warrant was added.
	if a.Needs["hunger"] != 18 {
		t.Errorf("hunger = %d, want 18 (accrual still happens)", a.Needs["hunger"])
	}
	if hasNeedThresholdWarrant(a) {
		t.Errorf("already-warranted actor should not get a need_threshold warrant; kinds = %v", warrantKinds(a))
	}
}

// TestNeedThreshold_TickInFlightSkipped: an actor mid-tick (TickInFlight) with
// no open warrant cycle still accrues but does not get a need_threshold warrant
// — TickInFlight is part of the eligibility gate (code_review, 2026-05-22).
func TestNeedThreshold_TickInFlightSkipped(t *testing.T) {
	a := agentNPCWithNeeds("n", 17, 5, 5)
	a.TickInFlight = true
	w := needsTickWorld(1, a)

	if _, err := IncrementNeedsTick(1).Fn(w); err != nil {
		t.Fatalf("IncrementNeedsTick: %v", err)
	}
	if a.Needs["hunger"] != 18 {
		t.Errorf("hunger = %d, want 18 (accrual still happens)", a.Needs["hunger"])
	}
	if hasNeedThresholdWarrant(a) {
		t.Errorf("tick-in-flight actor should not get a need_threshold warrant; kinds = %v", warrantKinds(a))
	}
}

// TestNeedThreshold_MultipleNeedsOneWarrant: hunger AND thirst both cross in the
// same tick → exactly one need_threshold warrant (the first stamp sets
// WarrantedSince, gating the second).
func TestNeedThreshold_MultipleNeedsOneWarrant(t *testing.T) {
	// hunger 17→18 (crosses 18) and thirst 11→12 (crosses 12) on the same tick.
	a := agentNPCWithNeeds("n", 17, 11, 5)
	w := needsTickWorld(1, a)

	if _, err := IncrementNeedsTick(1).Fn(w); err != nil {
		t.Fatalf("IncrementNeedsTick: %v", err)
	}
	n := 0
	for _, k := range warrantKinds(a) {
		if k == WarrantKindNeedThreshold {
			n++
		}
	}
	if n != 1 {
		t.Errorf("need_threshold warrants = %d, want exactly 1 (one per actor per tick); kinds = %v", n, warrantKinds(a))
	}
}

// TestNeedThreshold_DecorativeNoAccrualNoWarrant: a decorative (no LLMAgent, no
// login) is skipped entirely — no accrual, no warrant.
func TestNeedThreshold_DecorativeNoAccrualNoWarrant(t *testing.T) {
	d := &Actor{
		ID:    "d",
		Kind:  KindDecorative,
		Needs: map[NeedKey]int{"hunger": 17, "thirst": 5, "tiredness": 5},
	}
	w := needsTickWorld(1, d)

	if _, err := IncrementNeedsTick(1).Fn(w); err != nil {
		t.Fatalf("IncrementNeedsTick: %v", err)
	}
	if d.Needs["hunger"] != 17 {
		t.Errorf("decorative hunger = %d, want 17 (no accrual)", d.Needs["hunger"])
	}
	if d.WarrantedSince != nil || hasNeedThresholdWarrant(d) {
		t.Errorf("decorative should not be warranted; kinds = %v", warrantKinds(d))
	}
}
