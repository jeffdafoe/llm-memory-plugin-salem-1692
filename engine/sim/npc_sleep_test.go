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
		Actors: m,
		// Narration registry so the retire-farewell draw has its pool
		// (ZBBS-WORK-399) — NewWorld seeds this in production.
		NarrationPools: narrationSeedPools(),
		Settings:       WorldSettings{Location: time.UTC, NPCSleepMaxDurationHours: 12},
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

// spokeRecorder captures Spoke events emitted on a bare test world.
type spokeRecorder struct{ spokes []Spoke }

func (r *spokeRecorder) Handle(_ *World, evt Event) {
	if s, ok := evt.(*Spoke); ok {
		r.spokes = append(r.spokes, *s)
	}
}

// TestExecuteNPCSleep_LeavesHuddleWithFarewell: an NPC bedded while in an
// active huddle speaks a deterministic retire line to its partners, then
// leaves the huddle (the partner remains; the huddle is not concluded). The
// stationary AutoBedAtHomeNPCs path is what reaches this — a lodger bedding
// mid-conversation in the inn common room.
func TestExecuteNPCSleep_LeavesHuddleWithFarewell(t *testing.T) {
	now := time.Date(2026, 5, 22, 22, 0, 0, 0, time.UTC)
	sleeper := npc("sleeper", KindNPCShared)
	partner := npc("partner", KindNPCShared)
	sleeper.CurrentHuddleID = "h1"
	partner.CurrentHuddleID = "h1"
	w := sleepTestWorld(sleeper, partner)
	w.Huddles = map[HuddleID]*Huddle{
		"h1": {ID: "h1", Members: map[ActorID]struct{}{"sleeper": {}, "partner": {}}, StartedAt: now},
	}
	w.actorsByHuddle = map[HuddleID]map[ActorID]struct{}{
		"h1": {"sleeper": {}, "partner": {}},
	}
	rec := &spokeRecorder{}
	w.Subscribe(rec)

	if !executeNPCSleep(w, sleeper, now) {
		t.Fatal("executeNPCSleep should bed the awake NPC")
	}
	if len(rec.spokes) != 1 {
		t.Fatalf("emitted %d Spoke events, want 1 (the farewell)", len(rec.spokes))
	}
	got := rec.spokes[0]
	if got.SpeakerID != "sleeper" {
		t.Errorf("farewell SpeakerID = %q, want sleeper", got.SpeakerID)
	}
	if got.Text != w.renderRetireLine("sleeper", now) {
		t.Errorf("farewell Text = %q, want the deterministic retire line %q", got.Text, w.renderRetireLine("sleeper", now))
	}
	if len(got.RecipientIDs) != 1 || got.RecipientIDs[0] != "partner" {
		t.Errorf("farewell RecipientIDs = %v, want [partner]", got.RecipientIDs)
	}
	if sleeper.CurrentHuddleID != "" {
		t.Errorf("sleeper still in huddle %q after bed-down", sleeper.CurrentHuddleID)
	}
	if partner.CurrentHuddleID != "h1" {
		t.Errorf("partner left the huddle too: %q", partner.CurrentHuddleID)
	}
	if w.Huddles["h1"].ConcludedAt != nil {
		t.Error("huddle was concluded though the partner remains")
	}
	if sleeper.SleepingUntil == nil {
		t.Error("sleeper not bedded")
	}
}

// TestExecuteNPCSleep_SilentWhenNotHuddled: an NPC bedding with no active
// huddle (alone, or via the arrival path that already dropped its huddle on
// the walk) beds silently — no farewell Spoke.
func TestExecuteNPCSleep_SilentWhenNotHuddled(t *testing.T) {
	now := time.Date(2026, 5, 22, 22, 0, 0, 0, time.UTC)
	a := npc("solo", KindNPCShared)
	w := sleepTestWorld(a)
	rec := &spokeRecorder{}
	w.Subscribe(rec)

	if !executeNPCSleep(w, a, now) {
		t.Fatal("executeNPCSleep should bed the awake NPC")
	}
	if len(rec.spokes) != 0 {
		t.Errorf("emitted %d Spoke events bedding alone, want 0", len(rec.spokes))
	}
	if a.SleepingUntil == nil {
		t.Error("solo NPC not bedded")
	}
}

// TestExecuteNPCSleep_SilentWhenHuddleConcluded: a stale CurrentHuddleID
// pointing at an already-concluded huddle is not "active" — no farewell, and
// the actor still beds.
func TestExecuteNPCSleep_SilentWhenHuddleConcluded(t *testing.T) {
	now := time.Date(2026, 5, 22, 22, 0, 0, 0, time.UTC)
	a := npc("stale", KindNPCShared)
	a.CurrentHuddleID = "gone"
	w := sleepTestWorld(a)
	concluded := now.Add(-time.Hour)
	w.Huddles = map[HuddleID]*Huddle{
		"gone": {ID: "gone", Members: map[ActorID]struct{}{}, ConcludedAt: &concluded},
	}
	rec := &spokeRecorder{}
	w.Subscribe(rec)

	if !executeNPCSleep(w, a, now) {
		t.Fatal("executeNPCSleep should bed the awake NPC")
	}
	if len(rec.spokes) != 0 {
		t.Errorf("emitted %d Spoke events for a concluded huddle, want 0", len(rec.spokes))
	}
	if a.SleepingUntil == nil {
		t.Error("NPC not bedded")
	}
}

// TestExecuteNPCSleep_SilentWhenSoleHuddleMember: a huddle can transiently
// hold only the bedding actor (everyone else already left; conclusion fires at
// zero members). The active-huddle gate is true, but there's no one to excuse
// to — emit no farewell, still leave (which concludes the now-empty huddle).
func TestExecuteNPCSleep_SilentWhenSoleHuddleMember(t *testing.T) {
	now := time.Date(2026, 5, 22, 22, 0, 0, 0, time.UTC)
	a := npc("lonely", KindNPCShared)
	a.CurrentHuddleID = "h1"
	w := sleepTestWorld(a)
	w.Huddles = map[HuddleID]*Huddle{
		"h1": {ID: "h1", Members: map[ActorID]struct{}{"lonely": {}}, StartedAt: now},
	}
	w.actorsByHuddle = map[HuddleID]map[ActorID]struct{}{"h1": {"lonely": {}}}
	rec := &spokeRecorder{}
	w.Subscribe(rec)

	if !executeNPCSleep(w, a, now) {
		t.Fatal("executeNPCSleep should bed the awake NPC")
	}
	if len(rec.spokes) != 0 {
		t.Errorf("emitted %d Spoke events to an empty room, want 0", len(rec.spokes))
	}
	if a.CurrentHuddleID != "" {
		t.Errorf("sole member did not leave the huddle: %q", a.CurrentHuddleID)
	}
	if w.Huddles["h1"].ConcludedAt == nil {
		t.Error("emptied huddle was not concluded after the sole member left")
	}
	if a.SleepingUntil == nil {
		t.Error("NPC not bedded")
	}
}

// TestAutoSleepOnArrival_OnBreak: LLM-62 — an OFF-shift actor arriving home on a
// (self-renewing) break is now bedded for the night, with its break cleared so the
// "never both rest windows" invariant still holds (executeNPCSleep clears it). The
// break no longer shields it from the overnight reset. An ON-shift actor's break is
// still respected: npcSleepHere requires off-shift, so a mid-shift stop home is not
// sleep-darted.
func TestAutoSleepOnArrival_OnBreak(t *testing.T) {
	// Off-shift (unscheduled → always off-shift) + home + on break → bedded, break cleared.
	offShift := time.Date(2026, 5, 22, 22, 0, 0, 0, time.UTC)
	a := npc("k", KindNPCStateful)
	bu := offShift.Add(2 * time.Hour)
	a.BreakUntil = &bu
	w := sleepTestWorld(a)
	handleAutoSleepOnArrival(w, &ActorArrived{ActorID: a.ID, FinalStructureID: "home", At: offShift})
	if a.SleepingUntil == nil {
		t.Error("off-shift on-break NPC arriving home should now be bedded (LLM-62)")
	}
	if a.BreakUntil != nil {
		t.Errorf("break should be cleared when bedded, got %v", a.BreakUntil)
	}

	// On-shift + home + on break → NOT bedded (npcSleepHere off-shift gate), break intact.
	onShift := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC) // 10:00 → minute 600
	b := npc("v", KindNPCStateful)
	b.ScheduleStartMin = intptr(420) // 07:00
	b.ScheduleEndMin = intptr(960)   // 16:00 → on shift at 10:00
	bbu := onShift.Add(2 * time.Hour)
	b.BreakUntil = &bbu
	w2 := sleepTestWorld(b)
	handleAutoSleepOnArrival(w2, &ActorArrived{ActorID: b.ID, FinalStructureID: "home", At: onShift})
	if b.SleepingUntil != nil {
		t.Errorf("on-shift on-break NPC must not be bedded mid-shift: %v", b.SleepingUntil)
	}
	if b.BreakUntil == nil || !b.BreakUntil.Equal(bbu) {
		t.Errorf("on-shift break disturbed: %v", b.BreakUntil)
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
	// Off-shift (unscheduled) on-break vendor → now bedded for the night, break
	// cleared (LLM-62): the break no longer evades the stationary auto-bed.
	onBreakOffShift := npc("vendor", KindNPCStateful)
	breakEnd := now.Add(30 * time.Minute)
	onBreakOffShift.BreakUntil = &breakEnd
	// On-shift on-break keeper → NOT bedded: npcSleepHere requires off-shift.
	// Scheduled 18:00–23:59 so it is on shift at the 22:00 sweep.
	onBreakOnShift := npc("nightkeeper", KindNPCStateful)
	onBreakOnShift.ScheduleStartMin = intptr(1080)
	onBreakOnShift.ScheduleEndMin = intptr(1439)
	onShiftBreakEnd := now.Add(30 * time.Minute)
	onBreakOnShift.BreakUntil = &onShiftBreakEnd
	away := npc("wanderer", KindNPCStateful)
	away.InsideStructureID = "market"
	pc := npc("player", KindPC)

	w := sleepTestWorld(atHome, onBreakOffShift, onBreakOnShift, away, pc)
	res, _ := AutoBedAtHomeNPCs(now).Fn(w)
	if n := res.(int); n != 2 {
		t.Fatalf("bedded = %d, want 2 (stationary at-home + off-shift on-break, LLM-62)", n)
	}
	if atHome.SleepingUntil == nil {
		t.Error("at-home off-shift NPC should be bedded")
	}
	if onBreakOffShift.SleepingUntil == nil {
		t.Error("off-shift on-break NPC should now be bedded (LLM-62)")
	}
	if onBreakOffShift.BreakUntil != nil {
		t.Errorf("off-shift on-break NPC's break should be cleared when bedded, got %v", onBreakOffShift.BreakUntil)
	}
	if onBreakOnShift.SleepingUntil != nil {
		t.Error("on-shift on-break keeper must not be bedded (npcSleepHere off-shift gate)")
	}
	if onBreakOnShift.BreakUntil == nil {
		t.Error("on-shift on-break keeper's break should be left intact")
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
		// ZBBS-HOME-329 #3/#4 — timestamp-driven (enum left Idle) interrupt
		// matrix. Sleep is sacrosanct even under a red need or operator nudge;
		// a break yields to either.
		{"sleeping + need (sacrosanct)", func(a *Actor) {
			a.SleepingUntil = &future
			a.Warrants = []WarrantMeta{{Reason: NeedThresholdWarrantReason{Need: "hunger"}}}
		}, false},
		{"sleeping + operator nudge (sacrosanct)", func(a *Actor) {
			a.SleepingUntil = &future
			a.Warrants = []WarrantMeta{{Force: true, Reason: BasicWarrantReason{K: WarrantKindAdmin}}}
		}, false},
		{"on break + need interrupts", func(a *Actor) {
			a.BreakUntil = &future
			a.Warrants = []WarrantMeta{{Reason: NeedThresholdWarrantReason{Need: "hunger"}}}
		}, true},
		{"on break + operator nudge interrupts", func(a *Actor) {
			a.BreakUntil = &future
			a.Warrants = []WarrantMeta{{Force: true, Reason: BasicWarrantReason{K: WarrantKindAdmin}}}
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := npc("a", KindNPCStateful)
			a.State = StateIdle
			tc.mutate(a)
			w := sleepTestWorld(a)
			eligible, stale := actorCanReactNow(w, a, now)
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

// TestAutoBedAtHomeNPCs_SkipsInFlightAndWalking (ZBBS-HOME-435): the sweep
// never beds an actor whose reactor tick is in flight (the bed-stamp would
// race the tick's commits — the live Prudence Ward walking sleeper) or one
// mid-walk (passing through home, not resting). Both re-qualify next sweep.
func TestAutoBedAtHomeNPCs_SkipsInFlightAndWalking(t *testing.T) {
	now := time.Date(2026, 5, 22, 22, 0, 0, 0, time.UTC)

	midTick := npc("deliberating", KindNPCStateful)
	midTick.TickInFlight = true
	midWalk := npc("walking", KindNPCStateful)
	midWalk.MoveIntent = &MoveIntent{}
	settled := npc("settled", KindNPCShared)

	w := sleepTestWorld(midTick, midWalk, settled)
	res, _ := AutoBedAtHomeNPCs(now).Fn(w)
	if n := res.(int); n != 1 {
		t.Fatalf("bedded = %d, want 1 (only the settled NPC)", n)
	}
	if midTick.SleepingUntil != nil {
		t.Error("NPC with tick in flight was bedded — races the tick's commits")
	}
	if midWalk.SleepingUntil != nil {
		t.Error("mid-walk NPC was bedded — it is passing through, not resting")
	}
	if settled.SleepingUntil == nil {
		t.Error("settled at-home NPC should still be bedded")
	}
}

// TestAutoSleepOnArrival_SkipsInFlightTick (ZBBS-HOME-435): same race on the
// arrival path — an arrival landing while the actor's tick is in flight must
// not bed them mid-deliberation.
func TestAutoSleepOnArrival_SkipsInFlightTick(t *testing.T) {
	offShift := time.Date(2026, 5, 22, 22, 0, 0, 0, time.UTC)
	a := npc("k", KindNPCStateful)
	a.TickInFlight = true
	w := sleepTestWorld(a)

	handleAutoSleepOnArrival(w, &ActorArrived{ActorID: a.ID, FinalStructureID: "home", At: offShift})

	if a.SleepingUntil != nil {
		t.Errorf("NPC with tick in flight was bedded on arrival: SleepingUntil = %v", a.SleepingUntil)
	}
}
