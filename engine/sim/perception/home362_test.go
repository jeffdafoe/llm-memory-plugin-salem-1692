package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// ZBBS-HOME-362 — a red need defers the duty steer, and a tired keeper at its
// own post is told it can rest in place (take_break) rather than walking off.
// (ZBBS-HOME-400 later widened the TO-WORK arm to also defer on a mild-or-worse
// need — see TestBuildDutySteer_MildNeed_SuppressesToWork below.)
//
// Reuses dutySnap/dutyAnchors from duty_steer_test.go (same package). The
// tiredness need key uses the canonical "tiredness" string; with empty
// NeedThresholds, Get falls back to the registry default (16, LLM-85), so 24 is
// red-or-worse and 10 is not. recoveryTirednessNeed is the production tiredness
// key (shared with recovery_options.go in this package).

// home362OnShiftAwayActor is an unscheduled NPC standing away from its post;
// with the dawn/dusk window 06:00–18:00 and a 10:00 clock it is on-shift, so it
// would normally be steered to work (dutyAnchors.WorkID = "tavern").
func home362OnShiftAwayActor(tiredness int) *sim.ActorSnapshot {
	return &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		InsideStructureID: "general_store", // away from work + home
		Needs:             map[sim.NeedKey]int{recoveryTirednessNeed: tiredness},
	}
}

// TestBuildDutySteer_RedNeed_Suppressed: an on-shift NPC away from its post
// that would normally be steered to work gets NO steer while a need is red, so
// it can address the need first instead of being marched back-and-forth.
func TestBuildDutySteer_RedNeed_Suppressed(t *testing.T) {
	snap := dutySnap(600, 360, 1080) // 10:00, on shift via dawn/dusk window
	a := home362OnShiftAwayActor(24) // tiredness maxed → red
	if steer := dutySteer(snap, a, dutyAnchors); steer != nil {
		t.Errorf("expected no steer while a need is red, got %+v", steer)
	}
}

// TestBuildDutySteer_MildNeed_DoesNotSuppressToWork: ZBBS-HOME-463 narrowed the
// to-work gate back to RED-only. HOME-400 Option B had deferred the commute on any
// mild-or-worse need; that stranded chronically-needy NPCs in the mild-but-not-red
// band (blocked from work, yet not red enough to be driven to resolve). A mild
// tiredness (10, in the [8, red=20) band) now still steers the NPC to its post. Red
// still suppresses BOTH arms (TestBuildDutySteer_RedNeed_Suppressed); the restock/
// offer suppressors and the go-home arm are covered by TestBuildDutySteer_OptionBSuppression.
func TestBuildDutySteer_MildNeed_DoesNotSuppressToWork(t *testing.T) {
	snap := dutySnap(600, 360, 1080) // 10:00, on shift via dawn/dusk window
	a := home362OnShiftAwayActor(10) // mild tiredness ([10,16), LLM-85), not red
	steer := dutySteer(snap, a, dutyAnchors)
	if steer == nil || !steer.ToWork {
		t.Fatalf("want a to-work steer — a mild need must NOT suppress it (HOME-463), got %+v", steer)
	}
}

// home362Snapshot is a minimal snapshot with non-nil maps + default thresholds
// so buildRecoveryOptions runs without surfacing any external rest options.
func home362Snapshot() *sim.Snapshot {
	return &sim.Snapshot{
		NeedThresholds: sim.NeedThresholds{},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{},
		Structures:     map[sim.StructureID]*sim.Structure{},
		Actors:         map[sim.ActorID]*sim.ActorSnapshot{},
	}
}

// onShiftAtPostActor is a tired keeper standing at its own post with a 06:00–18:00
// shift; paired with home362Snapshot's clock set to a daytime minute it is on-shift.
func onShiftAtPostActor() *sim.ActorSnapshot {
	start, end := 360, 1080 // 06:00–18:00
	return &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		WorkStructureID:   "work-1",
		HomeStructureID:   "home-1",
		InsideStructureID: "work-1",
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Needs:             map[sim.NeedKey]int{recoveryTirednessNeed: 24},
	}
}

// TestBuildRecoveryOptions_AtPostTired_RestInPlace: a tired keeper standing at
// its own work structure WHILE ON SHIFT gets RestInPlace set.
func TestBuildRecoveryOptions_AtPostTired_RestInPlace(t *testing.T) {
	snap := home362Snapshot()
	now := 600 // 10:00 → inside the 06:00–18:00 shift
	snap.LocalMinuteOfDay = &now
	v := buildRecoveryOptions(snap, "actor-1", onShiftAtPostActor())
	if v == nil {
		t.Fatal("expected a recovery view, got nil")
	}
	if !v.RestInPlace {
		t.Error("RestInPlace = false, want true (tired, at own post, on shift)")
	}
}

// TestBuildRecoveryOptions_AtPostOffShift_NoRestInPlace is the LLM-100 regression:
// a tired keeper standing at its own post but OFF shift (evening, past its shift
// window) must NOT get the rest-in-place cue — there is no shift to step away from,
// so take_break would be a phantom action. Mirrors Ezekiel Crane (07:00–16:00)
// walking back into his own smithy at 22:08 after a foraging loop.
func TestBuildRecoveryOptions_AtPostOffShift_NoRestInPlace(t *testing.T) {
	snap := home362Snapshot()
	now := 1325 // 22:05 → past the shift window below
	snap.LocalMinuteOfDay = &now
	a := onShiftAtPostActor()
	start, end := 420, 960 // 07:00–16:00
	a.ScheduleStartMin = &start
	a.ScheduleEndMin = &end
	if v := buildRecoveryOptions(snap, "actor-1", a); v != nil && v.RestInPlace {
		t.Error("RestInPlace = true, want false (tired, at post, but off shift — LLM-100)")
	}
}

// TestBuildRecoveryOptions_AtPostUnscheduled_NoRestInPlace: an unscheduled actor is
// always off-shift (OnShiftAtMinute returns false on nil bounds), so even tired at
// its own post it gets no in-place rest cue — matches the LLM-62 home-bed sibling.
func TestBuildRecoveryOptions_AtPostUnscheduled_NoRestInPlace(t *testing.T) {
	snap := home362Snapshot()
	now := 600
	snap.LocalMinuteOfDay = &now
	a := onShiftAtPostActor()
	a.ScheduleStartMin = nil // unscheduled → always off shift
	a.ScheduleEndMin = nil
	if v := buildRecoveryOptions(snap, "actor-1", a); v != nil && v.RestInPlace {
		t.Error("RestInPlace = true, want false (unscheduled actor is off-shift — LLM-100)")
	}
}

// TestBuildRecoveryOptions_AtPostNilClock_NoRestInPlace: with no snapshot clock the
// shift can't be confirmed, so the in-place cue is suppressed (conservative — don't
// advertise stepping away from a shift we can't confirm is running). LLM-100.
func TestBuildRecoveryOptions_AtPostNilClock_NoRestInPlace(t *testing.T) {
	snap := home362Snapshot() // LocalMinuteOfDay stays nil
	if v := buildRecoveryOptions(snap, "actor-1", onShiftAtPostActor()); v != nil && v.RestInPlace {
		t.Error("RestInPlace = true, want false (nil clock suppresses — LLM-100)")
	}
}

// TestBuildRecoveryOptions_AtPostNilSnapshot_NoRestInPlace: a nil snapshot returns
// the early nil view (the buildRecoveryOptions top guard), so the on-shift predicate
// never derefs snap. Guards that the LLM-100 clock-deref stays behind that guard.
func TestBuildRecoveryOptions_AtPostNilSnapshot_NoRestInPlace(t *testing.T) {
	if v := buildRecoveryOptions(nil, "actor-1", onShiftAtPostActor()); v != nil && v.RestInPlace {
		t.Error("RestInPlace = true, want false (nil snapshot suppresses)")
	}
}

// TestBuildRecoveryOptions_AtPostOvernightShift_RestInPlace exercises the LLM-100
// on-shift clause through a wrap-midnight (start > end) schedule: a tired keeper at
// its own post is on-shift inside the overnight window (RestInPlace fires) and
// off-shift in the daytime gap (suppressed). Covers the schedule/clock plumbing this
// change routes through buildRecoveryOptions, not just OnShiftAtMinute in isolation.
func TestBuildRecoveryOptions_AtPostOvernightShift_RestInPlace(t *testing.T) {
	start, end := 1320, 360 // 22:00–06:00 overnight shift
	atPost := func(nowMin int) *RecoveryOptionsView {
		a := onShiftAtPostActor()
		a.ScheduleStartMin = &start
		a.ScheduleEndMin = &end
		snap := home362Snapshot()
		snap.LocalMinuteOfDay = &nowMin
		return buildRecoveryOptions(snap, "actor-1", a)
	}
	for _, now := range []int{1380, 120} { // 23:00 and 02:00 → inside the overnight window
		if v := atPost(now); v == nil || !v.RestInPlace {
			t.Errorf("at %d min: RestInPlace want true (on-shift overnight at own post), got %+v", now, v)
		}
	}
	if v := atPost(720); v != nil && v.RestInPlace { // 12:00 → daytime gap, off shift
		t.Errorf("at 720 min (overnight off-shift gap): RestInPlace want false, got %+v", v)
	}
}

// TestBuildRecoveryOptions_TiredAwayFromPost_NoRestInPlace: tired but standing
// somewhere other than its own post — the in-place cue must not fire.
func TestBuildRecoveryOptions_TiredAwayFromPost_NoRestInPlace(t *testing.T) {
	snap := home362Snapshot()
	a := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		WorkStructureID:   "work-1",
		HomeStructureID:   "home-1",
		InsideStructureID: "home-1", // at home, not at the post
		Needs:             map[sim.NeedKey]int{recoveryTirednessNeed: 24},
	}
	if v := buildRecoveryOptions(snap, "actor-1", a); v != nil && v.RestInPlace {
		t.Error("RestInPlace = true, want false (tired but away from post)")
	}
}

// TestBuildRecoveryOptions_AtPostNotTired_NoRestInPlace: at the post but rested
// — no rest cue at all (and the in-place flag stays off).
func TestBuildRecoveryOptions_AtPostNotTired_NoRestInPlace(t *testing.T) {
	snap := home362Snapshot()
	a := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		WorkStructureID:   "work-1",
		HomeStructureID:   "home-1",
		InsideStructureID: "work-1",
		Needs:             map[sim.NeedKey]int{recoveryTirednessNeed: 5},
	}
	if v := buildRecoveryOptions(snap, "actor-1", a); v != nil && v.RestInPlace {
		t.Error("RestInPlace = true, want false (not tired)")
	}
}

// TestRenderRecoveryOptions_RestInPlace_RendersBullet: the RestInPlace flag
// renders the take_break lead bullet under the section heading.
func TestRenderRecoveryOptions_RestInPlace_RendersBullet(t *testing.T) {
	var b strings.Builder
	renderRecoveryOptions(&b, &RecoveryOptionsView{RestInPlace: true})
	out := b.String()
	if !strings.Contains(out, "## How you can rest") {
		t.Errorf("missing section heading; got:\n%s", out)
	}
	if !strings.Contains(out, "take_break") || !strings.Contains(out, "without leaving your post") {
		t.Errorf("missing the rest-in-place bullet; got:\n%s", out)
	}
}
