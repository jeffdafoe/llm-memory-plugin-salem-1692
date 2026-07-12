package sim

import (
	"testing"
	"time"
)

// dwell_arrival_floor_test.go — LLM-376. An object dwell source stamps an
// open-ended credit on arrival AFTER applying the arrival burst. If the burst
// alone drives the need to the floor, a credit born at 0 is immortal — the
// floor-hit terminator (dwell_tick.go) fires only on a preNeed>0 -> postNeed==0
// transition — so perception keeps asserting "you are drinking … until your
// thirst is quenched" forever and the actor sits (Lewis at the well for 3+
// hours). The grant guard in applyObjectRefreshEffect must skip the credit when
// the arrival application already reached the floor. Reuses rearmTestWorld /
// thirstCreditFor from dwell_rearm_test.go (same package).

// TestArrival_NoDwellCreditWhenBurstQuenches: the regression. Arriving with a
// thirst the -8 burst fully quenches (5 -> 0) must NOT stamp a dwell credit —
// there is nothing left to drip, and a credit here would never terminate.
func TestArrival_NoDwellCreditWhenBurstQuenches(t *testing.T) {
	w, actor, _ := rearmTestWorld()
	actor.Needs["thirst"] = 5 // -8 burst clamps to 0

	if _, err := ApplyObjectRefreshAtArrival(actor.ID).Fn(w); err != nil {
		t.Fatalf("ApplyObjectRefreshAtArrival: %v", err)
	}
	if got := actor.Needs["thirst"]; got != 0 {
		t.Fatalf("thirst = %d, want 0 (5 - 8 burst, clamped)", got)
	}
	if c, ok := thirstCreditFor(actor, "well"); ok {
		t.Errorf("a dwell credit was stamped at the floor (immortal): %+v", c)
	}
}

// TestArrival_NoDwellCreditWhenBurstHitsFloorExactly: boundary — a burst that
// lands the need exactly on 0 is still "nothing left to recover", so no credit.
func TestArrival_NoDwellCreditWhenBurstHitsFloorExactly(t *testing.T) {
	w, actor, _ := rearmTestWorld()
	actor.Needs["thirst"] = 8 // -8 burst lands exactly on 0

	if _, err := ApplyObjectRefreshAtArrival(actor.ID).Fn(w); err != nil {
		t.Fatalf("ApplyObjectRefreshAtArrival: %v", err)
	}
	if got := actor.Needs["thirst"]; got != 0 {
		t.Fatalf("thirst = %d, want 0 (8 - 8 burst)", got)
	}
	if _, ok := thirstCreditFor(actor, "well"); ok {
		t.Error("a dwell credit was stamped at the exact floor (immortal)")
	}
}

// TestArrival_StampsDwellCreditWhenNeedRemains: the guard must not over-fire. A
// burst that leaves the need above the floor (13 -> 5) still arms the open-ended
// credit so the drip continues on later ticks — the normal drink-down path.
func TestArrival_StampsDwellCreditWhenNeedRemains(t *testing.T) {
	w, actor, _ := rearmTestWorld()
	// rearmTestWorld defaults thirst to 13; -8 burst leaves 5 > 0.

	if _, err := ApplyObjectRefreshAtArrival(actor.ID).Fn(w); err != nil {
		t.Fatalf("ApplyObjectRefreshAtArrival: %v", err)
	}
	if got := actor.Needs["thirst"]; got != 5 {
		t.Fatalf("thirst = %d, want 5 (13 - 8 burst)", got)
	}
	c, ok := thirstCreditFor(actor, "well")
	if !ok {
		t.Fatal("no dwell credit stamped for a still-unmet need after arrival")
	}
	if c.DwellDelta != -2 || c.DwellPeriodMinutes != 30 {
		t.Errorf("credit dwell config = (%d, %d), want (-2, 30)", c.DwellDelta, c.DwellPeriodMinutes)
	}
}

// TestDwellTick_RetiresStaleObjectCreditAtFloor: the LLM-376 lifecycle cleanup
// (code_review follow-up). Dwell credits persist in actor_dwell_credit and reload
// across restart, so a pre-fix immortal credit — an object credit whose need is
// already at the floor — survives a deploy. The grant guard stops NEW ones; this
// retires an EXISTING one: the dwell tick deletes it silently (no floor-hit
// completion narration, since nothing was recovered) so it stops pinning the
// actor and drops out of the next checkpoint.
func TestDwellTick_RetiresStaleObjectCreditAtFloor(t *testing.T) {
	w, actor, _ := rearmTestWorld()
	actor.Needs["thirst"] = 0 // already quenched — the immortal shape
	now := time.Unix(1_700_000_000, 0).UTC()
	actor.DwellCredits = map[DwellCreditKey]*DwellCredit{
		{ObjectID: "well", Attribute: "thirst", Source: DwellSourceObject}: {
			ObjectID: "well", Attribute: "thirst", Source: DwellSourceObject,
			LastCreditedAt: now.Add(-30 * time.Minute), // ripe
			DwellDelta:     -2, DwellPeriodMinutes: 30,
		},
	}

	res, err := ApplyDwellTick(now).Fn(w)
	if err != nil {
		t.Fatalf("ApplyDwellTick: %v", err)
	}
	if _, ok := thirstCreditFor(actor, "well"); ok {
		t.Error("stale object credit at the floor was not retired")
	}
	for _, c := range res.(DwellTickResult).Completions {
		if c.FloorHit {
			t.Errorf("stale-at-floor retirement must not emit a floor-hit 'you finished' completion: %+v", c)
		}
	}
}
