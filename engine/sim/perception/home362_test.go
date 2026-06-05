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
// NeedThresholds, Get falls back to the registry default (20), so 24 is red and
// 10 is not. recoveryTirednessNeed is the production tiredness key (shared with
// recovery_options.go in this package).

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

// TestBuildDutySteer_MildNeed_SuppressesToWork: ZBBS-HOME-400 Option B SUPERSEDED
// HOME-362's old "a sub-red need still steers to work" behavior. The to-work cue
// now defers on a mild-or-worse need too (matching the shift-duty warrant, which
// already need-suppressed the to-work nudge), so a mild tiredness (10, in the
// [8, red=20) mild band) no longer marches the NPC to its post. Red still
// suppresses BOTH arms; the go-home arm is never need-suppressed — both covered
// by TestBuildDutySteer_OptionBSuppression.
func TestBuildDutySteer_MildNeed_SuppressesToWork(t *testing.T) {
	snap := dutySnap(600, 360, 1080) // 10:00, on shift via dawn/dusk window
	a := home362OnShiftAwayActor(10) // mild tiredness ([8,20)), not red
	if steer := dutySteer(snap, a, dutyAnchors); steer != nil {
		t.Fatalf("want nil — a mild need now suppresses the to-work steer (HOME-400), got %+v", steer)
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

// TestBuildRecoveryOptions_AtPostTired_RestInPlace: a tired keeper standing at
// its own work structure gets RestInPlace set.
func TestBuildRecoveryOptions_AtPostTired_RestInPlace(t *testing.T) {
	snap := home362Snapshot()
	a := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		WorkStructureID:   "work-1",
		HomeStructureID:   "home-1",
		InsideStructureID: "work-1",
		Needs:             map[sim.NeedKey]int{recoveryTirednessNeed: 24},
	}
	v := buildRecoveryOptions(snap, "actor-1", a)
	if v == nil {
		t.Fatal("expected a recovery view, got nil")
	}
	if !v.RestInPlace {
		t.Error("RestInPlace = false, want true (tired, at own post)")
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
