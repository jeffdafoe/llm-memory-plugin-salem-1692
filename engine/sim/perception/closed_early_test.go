package perception

import (
	"testing"
	"time"
)

// closed_early_test.go — LLM-817: a live closed-early mark turns the to-work
// steer into the ClosedForDay status arm and drops the duty-pending signal; a
// lapsed mark brings both back. The rendered prompt is pinned by the golden
// keeper_closed_early_at_home_stays_home and TestGoldensNoDutySteerWhenClosedEarly.
func TestBuild_ClosedEarlyKeeper(t *testing.T) {
	snap, actorID, warrants := keeperClosedEarlyAtHome()
	p := Build(snap, actorID, warrants)
	if p.DutySteer == nil || !p.DutySteer.ClosedForDay || p.DutySteer.ToWork || p.DutySteer.TargetID != "" {
		t.Fatalf("live mark: DutySteer = %+v, want the ClosedForDay status arm with no destination", p.DutySteer)
	}
	if p.DutySteer.ShiftStartMin == nil || *p.DutySteer.ShiftStartMin != 360 {
		t.Errorf("ShiftStartMin = %v, want 360 (opens at 6 in the morning)", p.DutySteer.ShiftStartMin)
	}
	if p.DutyPending {
		t.Error("live mark: DutyPending = true, want false")
	}
	if p.Anchors == nil || !p.Anchors.ClosedForDay {
		t.Errorf("live mark: Anchors = %+v, want ClosedForDay", p.Anchors)
	}

	lapsed := snap.PublishedAt.Add(-time.Minute)
	snap.Actors[actorID].ClosedUntil = &lapsed
	p = Build(snap, actorID, warrants)
	if p.DutySteer == nil || !p.DutySteer.ToWork {
		t.Errorf("lapsed mark: DutySteer = %+v, want the to-work steer back", p.DutySteer)
	}
	if !p.DutyPending {
		t.Error("lapsed mark: DutyPending = false, want true")
	}
}
