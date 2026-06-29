package sim

import (
	"testing"
	"time"
)

// npc_sleep_evening_test.go — LLM-148. A SCHEDULED homed agent gets an evening:
// it beds only when off-shift AND inside the civil-night window
// [LodgingBedtimeHour, DawnTime), not the moment its work shift ends. The
// off-shift half guards a night-shift home==work keeper from bedding mid-shift at
// 22:00, and keeps the bed gate (off-shift) and wake gate (on-shift) from ever
// firing on the same minute.

// eveningWorld is sleepTestWorld with the night window configured: bedtime 22:00,
// dawn 07:00 → window [1320, 420) wrapping midnight. The dawn/dusk fallback is
// irrelevant to the scheduled-homed gate (it reads the explicit schedule).
func eveningWorld(actors ...*Actor) *World {
	w := sleepTestWorld(actors...)
	w.Settings.DawnTime = "07:00"
	w.Settings.LodgingBedtimeHour = 22
	return w
}

// scheduledHomed is a homed agent NPC with an explicit shift, standing at home.
func scheduledHomed(id ActorID, startMin, endMin int) *Actor {
	return &Actor{
		ID:                id,
		Kind:              KindNPCStateful,
		HomeStructureID:   "home1",
		InsideStructureID: "home1",
		ScheduleStartMin:  intptr(startMin),
		ScheduleEndMin:    intptr(endMin),
	}
}

// Day shift 09:00–19:00. The evening [19:00, 22:00) is awake; bedtime is 22:00;
// the window wraps to dawn (07:00). The DoD case: off-shift pre-22:00 at home is
// NOT bedded, post-22:00 at home IS bedded.
func TestNpcSleepHere_ScheduledHomed_BedsAtNightNotShiftEnd(t *testing.T) {
	a := scheduledHomed("a", 540, 1140) // 09:00–19:00
	w := eveningWorld(a)
	cases := []struct {
		hour, min int
		want      bool
		why       string
	}{
		{12, 0, false, "on-shift at noon — never bedded"},
		{19, 30, false, "off-shift just after shift-end, pre-bedtime — gets the evening"},
		{21, 59, false, "off-shift, one minute before bedtime — still up"},
		{22, 0, true, "off-shift at bedtime — bedded"},
		{23, 30, true, "off-shift, late evening — bedded"},
		{6, 0, true, "off-shift, pre-dawn (window wraps midnight) — bedded"},
		{8, 0, false, "off-shift after dawn but before shift — awake (window closed)"},
	}
	for _, c := range cases {
		at := time.Date(2026, 6, 29, c.hour, c.min, 0, 0, time.UTC)
		if got := npcSleepHere(w, a, at); got != c.want {
			t.Errorf("%02d:%02d (%s): npcSleepHere = %v, want %v", c.hour, c.min, c.why, got, c.want)
		}
	}
}

// A night-shift home==work keeper (a tavernkeeper, 16:00–03:00) must not be
// bedded mid-shift at 22:00 just because the clock entered the night window —
// the off-shift half of the gate is what prevents that. It beds once off-shift
// (after 03:00) while still inside the window.
func TestNpcSleepHere_ScheduledHomed_NightShiftKeeperNotBeddedMidShift(t *testing.T) {
	keeper := scheduledHomed("k", 960, 180) // 16:00–03:00, wraps midnight
	w := eveningWorld(keeper)
	onShift := time.Date(2026, 6, 29, 22, 0, 0, 0, time.UTC)
	if npcSleepHere(w, keeper, onShift) {
		t.Errorf("night-shift keeper bedded at 22:00 mid-shift; want NOT bedded (off-shift gate)")
	}
	offShift := time.Date(2026, 6, 29, 3, 30, 0, 0, time.UTC)
	if !npcSleepHere(w, keeper, offShift) {
		t.Errorf("night-shift keeper NOT bedded at 03:30 (off-shift, in window); want bedded")
	}
}

// Scope guard: an UNSCHEDULED homed NPC (one of the four salem-vendors) is
// untouched by LLM-148 — home is its default resting state, so it still beds on
// any off-shift moment. At 20:00 it is bedded where a scheduled agent would be up.
func TestNpcSleepHere_UnscheduledHomed_NoEveningGate(t *testing.T) {
	vendor := &Actor{
		ID:                "v",
		Kind:              KindNPCStateful,
		HomeStructureID:   "home1",
		InsideStructureID: "home1",
		// no schedule, no worker attribute → always off-shift
	}
	w := eveningWorld(vendor)
	evening := time.Date(2026, 6, 29, 20, 0, 0, 0, time.UTC)
	if !npcSleepHere(w, vendor, evening) {
		t.Errorf("unscheduled homed vendor NOT bedded at 20:00; want bedded (always-off, unchanged by LLM-148)")
	}
}

// Wake stays shift-driven: a scheduled homed agent bedded at night wakes at its
// shift-start, not at dawn — and the two gates never thrash, since bedding needs
// off-shift and waking needs on-shift. Shift 09:00–19:00, window [22:00, 07:00).
func TestWakeExpiredNPCSleepers_ScheduledHomedWakesAtShiftStart(t *testing.T) {
	a := scheduledHomed("a", 540, 1140)                     // 09:00–19:00
	farCap := time.Date(2026, 6, 30, 23, 0, 0, 0, time.UTC) // far cap; never cap-wakes here
	a.SleepingUntil = &farCap
	w := eveningWorld(a)

	// 07:30 — past dawn (window closed) but before shift start: stays asleep.
	postDawn := time.Date(2026, 6, 29, 7, 30, 0, 0, time.UTC)
	if _, err := WakeExpiredNPCSleepers(postDawn).Fn(w); err != nil {
		t.Fatalf("wake (post-dawn): %v", err)
	}
	if a.SleepingUntil == nil {
		t.Fatalf("scheduled homed agent woke at 07:30 (post-dawn, pre-shift); want still asleep — wake is shift-driven")
	}
	// 09:00 — shift start: wakes.
	shiftStart := time.Date(2026, 6, 29, 9, 0, 0, 0, time.UTC)
	if _, err := WakeExpiredNPCSleepers(shiftStart).Fn(w); err != nil {
		t.Fatalf("wake (shift-start): %v", err)
	}
	if a.SleepingUntil != nil {
		t.Errorf("scheduled homed agent did not wake at 09:00 (shift start); want woken")
	}
}
