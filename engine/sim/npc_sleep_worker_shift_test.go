package sim

import (
	"testing"
	"time"
)

// npc_sleep_worker_shift_test.go — LLM-137. An unscheduled WORKER (AttrWorker)
// is day-active on the world dawn/dusk window, so it is NOT sleep-darted at home
// mid-afternoon. Non-workers and explicitly-scheduled actors are unchanged.

// workerShiftWorld is sleepTestWorld with dawn/dusk set — sleepTestWorld leaves
// them empty, which would make effectiveShiftWindow fail (ok=false).
func workerShiftWorld(actors ...*Actor) *World {
	w := sleepTestWorld(actors...)
	w.Settings.DawnTime = "07:00"
	w.Settings.DuskTime = "19:00"
	w.Settings.LodgingBedtimeHour = 22 // civil-night bedtime — the [22:00, dawn) bed window
	return w
}

// homedWorker is a homed, unscheduled worker standing inside its own home.
func homedWorker(id ActorID) *Actor {
	return &Actor{
		ID:                id,
		Kind:              KindNPCShared,
		LLMAgent:          VendorAgentName,
		HomeStructureID:   "home1",
		InsideStructureID: "home1",
		Attributes:        map[string][]byte{AttrWorker: {}},
	}
}

// Day window is [dawn 07:00, dusk 19:00) = [420, 1140).
func TestActorOnShift_UnscheduledWorkerIsDayActive(t *testing.T) {
	w := workerShiftWorld()
	worker := homedWorker("w")
	if !actorOnShift(w, worker, 720) { // 12:00 — inside the day window
		t.Errorf("unscheduled worker off-shift at noon; want on-shift (dawn/dusk day window)")
	}
	if actorOnShift(w, worker, 1380) { // 23:00 — night
		t.Errorf("unscheduled worker on-shift at 23:00; want off-shift")
	}
	if actorOnShift(w, worker, 360) { // 06:00 — before dawn
		t.Errorf("unscheduled worker on-shift at 06:00 (pre-dawn); want off-shift")
	}
}

func TestActorOnShift_UnscheduledNonWorkerAlwaysOff(t *testing.T) {
	w := workerShiftWorld()
	plain := homedWorker("p")
	plain.Attributes = nil // strip the worker marker → rest-at-home default
	if actorOnShift(w, plain, 720) {
		t.Errorf("unscheduled non-worker on-shift at noon; want always off-shift (unchanged)")
	}
}

func TestActorOnShift_ExplicitScheduleGovernsWorker(t *testing.T) {
	w := workerShiftWorld()
	worker := homedWorker("w")
	worker.ScheduleStartMin = intptr(420) // 07:00
	worker.ScheduleEndMin = intptr(960)   // 16:00
	if !actorOnShift(w, worker, 600) {    // 10:00 — inside the explicit schedule
		t.Errorf("scheduled worker off-shift at 10:00; want on-shift per explicit schedule")
	}
	// 18:00 is outside the explicit 07:00–16:00 schedule but inside the dawn/dusk
	// day window — the explicit schedule must win.
	if actorOnShift(w, worker, 1080) {
		t.Errorf("scheduled worker on-shift at 18:00; explicit schedule (07:00–16:00) must govern, not the dawn/dusk fallback")
	}
}

func TestNpcSleepHere_UnscheduledWorker_NotBeddedMidday_HasEvening_BeddedAtNight(t *testing.T) {
	worker := homedWorker("w")
	w := workerShiftWorld(worker)
	noon := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	evening := time.Date(2026, 6, 27, 20, 0, 0, 0, time.UTC) // after dusk (19:00), before bedtime (22:00)
	night := time.Date(2026, 6, 27, 23, 0, 0, 0, time.UTC)
	if npcSleepHere(w, worker, noon) {
		t.Errorf("homed worker bedded at noon; want NOT bedded (day-active)")
	}
	// LLM-352: the fix. An unscheduled worker is off-shift at 20:00 but inside its
	// evening [dusk, bedtime), so it must NOT be bedded — the old !actorOnShift gate
	// bedded it here, at dusk, with no evening.
	if npcSleepHere(w, worker, evening) {
		t.Errorf("homed worker bedded at 20:00; want NOT bedded (evening, [dusk, bedtime))")
	}
	if !npcSleepHere(w, worker, night) {
		t.Errorf("homed worker NOT bedded at 23:00; want bedded (inside the civil-night window)")
	}
}

func TestNpcSleepHere_UnscheduledNonWorker_BeddedMidday_Unchanged(t *testing.T) {
	plain := homedWorker("p")
	plain.Attributes = nil // not a worker → keeps the rest-at-home default
	w := workerShiftWorld(plain)
	noon := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	if !npcSleepHere(w, plain, noon) {
		t.Errorf("homed non-worker NOT bedded at noon; want bedded (unscheduled = always off-shift, unchanged)")
	}
}

// A sleeping homed worker wakes at its day-shift start (dawn), symmetric with
// the dawn/dusk bedding gate, rather than being stranded asleep to the 12h cap.
func TestWakeExpiredNPCSleepers_HomedWorkerWakesAtDawn(t *testing.T) {
	worker := homedWorker("w")
	farFuture := time.Date(2026, 6, 27, 23, 0, 0, 0, time.UTC) // far cap; never cap-wakes here
	worker.SleepingUntil = &farFuture
	w := workerShiftWorld(worker)

	// 06:00 — before the 07:00 dawn day-window start: off-shift, stays asleep.
	preDawn := time.Date(2026, 6, 27, 6, 0, 0, 0, time.UTC)
	if _, err := WakeExpiredNPCSleepers(preDawn).Fn(w); err != nil {
		t.Fatalf("wake (pre-dawn): %v", err)
	}
	if worker.SleepingUntil == nil {
		t.Fatalf("homed worker woke at 06:00 (pre-dawn); want still asleep")
	}
	// 08:00 — inside the day window: the worker is on-shift, wakes at day-shift start.
	postDawn := time.Date(2026, 6, 27, 8, 0, 0, 0, time.UTC)
	if _, err := WakeExpiredNPCSleepers(postDawn).Fn(w); err != nil {
		t.Fatalf("wake (post-dawn): %v", err)
	}
	if worker.SleepingUntil != nil {
		t.Errorf("homed worker did not wake at 08:00 (after dawn); want woken")
	}
}
