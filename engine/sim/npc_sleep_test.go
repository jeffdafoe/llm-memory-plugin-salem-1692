package sim

import (
	"testing"
	"time"
)

func intptr(v int) *int { return &v }

// sleepTestWorld builds a minimal in-memory World (no goroutine, no repo) for
// exercising the sleep helpers/commands directly on the world struct.
func sleepTestWorld(actors ...*Actor) *World {
	m := make(map[ActorID]*Actor, len(actors))
	for _, a := range actors {
		m[a.ID] = a
	}
	return &World{
		Actors:   m,
		Settings: WorldSettings{Location: time.UTC, NPCSleepMaxDurationHours: 12},
	}
}

func npc(id ActorID, kind ActorKind) *Actor {
	return &Actor{
		ID:                id,
		Kind:              kind,
		HomeStructureID:   "home",
		InsideStructureID: "home",
		Needs:             map[NeedKey]int{"tiredness": 20},
	}
}

func TestIsActorOnShift(t *testing.T) {
	unscheduled := &Actor{}
	if isActorOnShift(unscheduled, 600) {
		t.Error("unscheduled actor should never be on shift")
	}

	day := &Actor{ScheduleStartMin: intptr(420), ScheduleEndMin: intptr(960)} // 07:00–16:00
	cases := []struct {
		min  int
		want bool
	}{
		{419, false}, {420, true}, {600, true}, {959, true}, {960, false}, {1320, false},
	}
	for _, c := range cases {
		if got := isActorOnShift(day, c.min); got != c.want {
			t.Errorf("day shift minute %d: got %v want %v", c.min, got, c.want)
		}
	}

	wrap := &Actor{ScheduleStartMin: intptr(960), ScheduleEndMin: intptr(180)} // 16:00–03:00
	wrapCases := []struct {
		min  int
		want bool
	}{
		{1320, true}, {960, true}, {60, true}, {179, true}, {180, false}, {600, false},
	}
	for _, c := range wrapCases {
		if got := isActorOnShift(wrap, c.min); got != c.want {
			t.Errorf("wrap shift minute %d: got %v want %v", c.min, got, c.want)
		}
	}
}

func TestExecuteNPCSleep(t *testing.T) {
	now := time.Date(2026, 5, 22, 22, 0, 0, 0, time.UTC)
	a := npc("n", KindNPCStateful)
	w := sleepTestWorld(a)

	if !executeNPCSleep(w, a, now) {
		t.Fatal("executeNPCSleep should bed an awake NPC")
	}
	if a.SleepingUntil == nil || !a.SleepingUntil.Equal(now.Add(12*time.Hour)) {
		t.Errorf("SleepingUntil = %v, want %v", a.SleepingUntil, now.Add(12*time.Hour))
	}
	if a.LastTirednessRecoveryAt == nil || !a.LastTirednessRecoveryAt.Equal(now) {
		t.Errorf("recovery cursor = %v, want %v (stamped at window open)", a.LastTirednessRecoveryAt, now)
	}
	if a.State != StateSleeping {
		t.Errorf("State = %q, want %q (soft-set on bed-down)", a.State, StateSleeping)
	}

	// Idempotent: already sleeping → no-op.
	prev := *a.SleepingUntil
	if executeNPCSleep(w, a, now.Add(time.Hour)) {
		t.Error("executeNPCSleep should no-op an already-sleeping NPC")
	}
	if !a.SleepingUntil.Equal(prev) {
		t.Errorf("SleepingUntil changed on no-op: %v != %v", *a.SleepingUntil, prev)
	}
}

// TestAutoSleepOnArrival_SkipsOnBreak guards the no-both-windows invariant
// (ZBBS-HOME-284 #4 review): an on-break NPC arriving home off-shift — which
// would otherwise be bedded — must NOT also get a SleepingUntil window.
func TestAutoSleepOnArrival_SkipsOnBreak(t *testing.T) {
	offShift := time.Date(2026, 5, 22, 22, 0, 0, 0, time.UTC)
	a := npc("k", KindNPCStateful) // unscheduled + home → would normally bed
	bu := offShift.Add(2 * time.Hour)
	a.BreakUntil = &bu
	w := sleepTestWorld(a)

	handleAutoSleepOnArrival(w, &ActorArrived{ActorID: a.ID, FinalStructureID: "home", At: offShift})

	if a.SleepingUntil != nil {
		t.Errorf("on-break NPC was bedded on arrival: SleepingUntil = %v", a.SleepingUntil)
	}
	if a.BreakUntil == nil || !a.BreakUntil.Equal(bu) {
		t.Errorf("BreakUntil disturbed: %v", a.BreakUntil)
	}
}

func TestAutoSleepOnArrival(t *testing.T) {
	offShift := time.Date(2026, 5, 22, 22, 0, 0, 0, time.UTC) // 22:00 → minute 1320
	onShift := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)  // 10:00 → minute 600
	dayShift := func(a *Actor) *Actor {
		a.ScheduleStartMin = intptr(420)
		a.ScheduleEndMin = intptr(960)
		return a
	}

	tests := []struct {
		name       string
		actor      *Actor
		at         time.Time
		finalStruc StructureID
		wantBedded bool
	}{
		{"npc home off-shift", npc("a", KindNPCStateful), offShift, "home", true},
		{"shared-VA npc home off-shift", npc("a", KindNPCShared), offShift, "home", true},
		{"on-shift not bedded", dayShift(npc("a", KindNPCStateful)), onShift, "home", false},
		{"off-shift scheduled bedded", dayShift(npc("a", KindNPCStateful)), offShift, "home", true},
		{"pc not bedded", npc("a", KindPC), offShift, "home", false},
		{"decorative not bedded", npc("a", KindDecorative), offShift, "home", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := sleepTestWorld(tc.actor)
			handleAutoSleepOnArrival(w, &ActorArrived{ActorID: tc.actor.ID, FinalStructureID: tc.finalStruc, At: tc.at})
			bedded := tc.actor.SleepingUntil != nil
			if bedded != tc.wantBedded {
				t.Errorf("bedded = %v, want %v", bedded, tc.wantBedded)
			}
		})
	}
}

func TestAutoSleepOnArrivalNotAtHome(t *testing.T) {
	now := time.Date(2026, 5, 22, 22, 0, 0, 0, time.UTC)
	a := npc("a", KindNPCStateful)
	a.InsideStructureID = "tavern" // away from home
	w := sleepTestWorld(a)
	handleAutoSleepOnArrival(w, &ActorArrived{ActorID: "a", FinalStructureID: "tavern", At: now})
	if a.SleepingUntil != nil {
		t.Error("NPC arriving somewhere other than home should not be bedded")
	}
}

func TestAutoSleepOnArrivalStaleEvent(t *testing.T) {
	now := time.Date(2026, 5, 22, 22, 0, 0, 0, time.UTC)
	a := npc("a", KindNPCStateful) // currently inside "home"
	w := sleepTestWorld(a)
	// Arrival event references a different structure than the actor's current
	// one — a superseding move already happened; the event is stale.
	handleAutoSleepOnArrival(w, &ActorArrived{ActorID: "a", FinalStructureID: "field", At: now})
	if a.SleepingUntil != nil {
		t.Error("stale arrival event (structure mismatch) should not bed the NPC")
	}
}

func TestAutoBedAtHomeNPCs(t *testing.T) {
	now := time.Date(2026, 5, 22, 22, 0, 0, 0, time.UTC) // off-shift for a day worker

	atHome := npc("farmer", KindNPCShared) // home==work, stationary
	onBreak := npc("vendor", KindNPCStateful)
	breakEnd := now.Add(30 * time.Minute)
	onBreak.BreakUntil = &breakEnd
	away := npc("wanderer", KindNPCStateful)
	away.InsideStructureID = "market"
	pc := npc("player", KindPC)

	w := sleepTestWorld(atHome, onBreak, away, pc)
	res, _ := AutoBedAtHomeNPCs(now).Fn(w)
	if n := res.(int); n != 1 {
		t.Fatalf("bedded = %d, want 1 (only the stationary at-home NPC)", n)
	}
	if atHome.SleepingUntil == nil {
		t.Error("at-home off-shift NPC should be bedded")
	}
	if onBreak.SleepingUntil != nil {
		t.Error("on-break NPC should stay awake")
	}
	if away.SleepingUntil != nil {
		t.Error("NPC away from home should not be bedded")
	}
	if pc.SleepingUntil != nil {
		t.Error("PC should not be auto-bedded")
	}
}

// TestActorCanReactNowGatesRestWindows verifies the reactor won't fire an LLM
// tick against an NPC the sleep lifecycle has bedded / put on break — the gap
// work flagged (the enum gate alone misses timestamp-driven sleep).
func TestActorCanReactNowGatesRestWindows(t *testing.T) {
	now := time.Now().UTC()
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)

	cases := []struct {
		name         string
		mutate       func(*Actor)
		wantEligible bool
	}{
		{"awake", func(a *Actor) {}, true},
		{"sleeping", func(a *Actor) { a.SleepingUntil = &future }, false},
		{"on break", func(a *Actor) { a.BreakUntil = &future }, false},
		{"sleep window expired", func(a *Actor) { a.SleepingUntil = &past }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := npc("a", KindNPCStateful)
			a.State = StateIdle
			tc.mutate(a)
			w := sleepTestWorld(a)
			eligible, stale := actorCanReactNow(w, a)
			if eligible != tc.wantEligible || stale {
				t.Errorf("got (eligible=%v, stale=%v), want (eligible=%v, stale=false)", eligible, stale, tc.wantEligible)
			}
		})
	}
}

func TestWakeExpiredNPCSleepers(t *testing.T) {
	now := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC) // minute 600 — inside a 07:00–16:00 shift

	// Cap reached: SleepingUntil in the past.
	capped := npc("capped", KindNPCStateful)
	past := now.Add(-time.Minute)
	capped.SleepingUntil = &past
	cur := now.Add(-time.Hour)
	capped.LastTirednessRecoveryAt = &cur

	// On-shift wake: future cap, but it's shift time now.
	onShift := npc("onshift", KindNPCStateful)
	onShift.ScheduleStartMin = intptr(420)
	onShift.ScheduleEndMin = intptr(960)
	future := now.Add(6 * time.Hour)
	onShift.SleepingUntil = &future

	// Off-shift, cap not reached: stays asleep (HOME-282 — no tiredness wake).
	asleep := npc("asleep", KindNPCStateful)
	asleep.ScheduleStartMin = intptr(420)
	asleep.ScheduleEndMin = intptr(960)
	asleep.InsideStructureID = "home"
	farFuture := now.Add(6 * time.Hour)
	// shift it off-shift by giving a window that excludes minute 600... use a
	// night shift 20:00–04:00 so 10:00 is off-shift.
	asleep.ScheduleStartMin = intptr(1200)
	asleep.ScheduleEndMin = intptr(240)
	asleep.SleepingUntil = &farFuture

	w := sleepTestWorld(capped, onShift, asleep)
	res, _ := WakeExpiredNPCSleepers(now).Fn(w)
	if n := res.(int); n != 2 {
		t.Fatalf("woken = %d, want 2 (cap + on-shift)", n)
	}
	if capped.SleepingUntil != nil || capped.LastTirednessRecoveryAt != nil {
		t.Error("capped NPC should wake and clear its recovery cursor")
	}
	if capped.State != StateIdle {
		t.Errorf("woken NPC State = %q, want %q", capped.State, StateIdle)
	}
	if onShift.SleepingUntil != nil {
		t.Error("on-shift NPC should wake at shift start")
	}
	if asleep.SleepingUntil == nil {
		t.Error("off-shift NPC under the cap should stay asleep (no tiredness wake)")
	}
}
