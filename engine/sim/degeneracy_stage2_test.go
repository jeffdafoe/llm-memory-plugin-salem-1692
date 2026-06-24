package sim

import (
	"testing"
	"time"
)

// degeneracy_stage2_test.go — LLM-94 Stage-2 internals: the ambient/salient
// warrant partition, the all-ambient cycle predicate, the tryStampWarrant
// salient re-arm, and the observer-disabled stage unwind. (The throttle gate
// itself is covered end-to-end in degeneracy_throttle_test.go.) Reuses
// newDegenWorld / futileResult from degeneracy_test.go (same package).

func TestIsAmbientWarrantKind(t *testing.T) {
	ambient := []WarrantKind{
		WarrantKindIdleBackstop,
		WarrantKindStranded,
		WarrantKindShiftDuty,
		WarrantKindRestock,
		WarrantKindDwellStarted,
		WarrantKindDwellTickApplied,
		WarrantKindDwellEnded,
	}
	for _, k := range ambient {
		if !isAmbientWarrantKind(k) {
			t.Errorf("kind %q should be ambient (throttle-deferrable)", k)
		}
	}
	// A representative spread of salient kinds — anything carrying a fresh
	// external interaction, a threshold, or an operator signal — plus the
	// unknown sentinel, all default to salient (never throttle-deferred).
	salient := []WarrantKind{
		WarrantKindPCSpoke, WarrantKindNPCSpoke,
		WarrantKindHuddleJoined, WarrantKindHuddlePeerJoined,
		WarrantKindNeedThreshold, WarrantKindPayOffer, WarrantKindPayResolved,
		WarrantKindSceneQuoteTargeted, WarrantKindServeHandover, WarrantKindPaid,
		WarrantKindAdmin, WarrantKindImpulse, WarrantKindArrived,
		WarrantKindUnknown,
	}
	for _, k := range salient {
		if isAmbientWarrantKind(k) {
			t.Errorf("kind %q should be salient (never throttle-deferred)", k)
		}
	}
}

func TestWarrantCycleAllAmbient(t *testing.T) {
	idle := WarrantMeta{Reason: IdleBackstopWarrantReason{}}
	dwell := WarrantMeta{Reason: BasicWarrantReason{K: WarrantKindDwellEnded}}
	spoke := WarrantMeta{Reason: PCSpeechWarrantReason{SpeechID: 1, Speaker: "p"}}

	if warrantCycleAllAmbient(nil) {
		t.Error("an empty cycle must not read as all-ambient")
	}
	if !warrantCycleAllAmbient([]WarrantMeta{idle, dwell}) {
		t.Error("an all-ambient cycle must read as all-ambient")
	}
	if warrantCycleAllAmbient([]WarrantMeta{idle, spoke}) {
		t.Error("a cycle with one salient warrant must not read as all-ambient")
	}
}

func TestTryStampWarrant_SalientReArmsFarOutDueTime(t *testing.T) {
	w, _ := newDegenWorld(WorldSettings{})
	now := time.Unix(10_000, 0).UTC()
	since := now.Add(-time.Minute)
	farOut := now.Add(5 * time.Minute) // the throttle's effect

	a := &Actor{
		ID:             "a1",
		Kind:           KindNPCStateful,
		WarrantedSince: &since,
		WarrantDueAt:   &farOut,
		Warrants:       []WarrantMeta{{Reason: IdleBackstopWarrantReason{}}},
	}

	// A salient stamp (a PC spoke) pulls the parked due time back toward now.
	if !tryStampWarrant(w, a, WarrantMeta{Reason: PCSpeechWarrantReason{SpeechID: 9, Speaker: "player"}}, now) {
		t.Fatal("a salient stamp should be recorded")
	}
	if a.WarrantDueAt == nil || a.WarrantDueAt.After(now.Add(10*time.Second)) {
		t.Errorf("salient stamp did not re-arm the parked due time: %v", a.WarrantDueAt)
	}
}

func TestTryStampWarrant_AmbientDoesNotReArm(t *testing.T) {
	w, _ := newDegenWorld(WorldSettings{})
	now := time.Unix(20_000, 0).UTC()
	since := now.Add(-time.Minute)
	farOut := now.Add(5 * time.Minute)
	a := &Actor{
		ID:             "a1",
		Kind:           KindNPCStateful,
		WarrantedSince: &since,
		WarrantDueAt:   &farOut,
		Warrants:       []WarrantMeta{{Reason: IdleBackstopWarrantReason{}}},
	}
	// A second ambient stamp must NOT undo the throttle's far-out due time.
	tryStampWarrant(w, a, WarrantMeta{Reason: StrandedWarrantReason{}}, now)
	if a.WarrantDueAt == nil || !a.WarrantDueAt.Equal(farOut) {
		t.Errorf("an ambient stamp must not re-arm the due time: got %v want %v", a.WarrantDueAt, farOut)
	}
}

func TestUpdateDegeneracy_DisabledUnwindsStage(t *testing.T) {
	// Observer disabled, but the actor carries a stage from when it was on. A
	// scored tick reaching updateDegeneracy must unwind it, so turning the
	// observer off lifts the Stage-1/2 responses rather than leaving them stuck.
	w, sink := newDegenWorld(WorldSettings{}) // disabled
	since := time.Unix(1, 0).UTC()
	a := &Actor{ID: "a1", DegenStage: DegeneracyThrottled, DegenStreak: 25, DegenStreakSince: &since}

	updateDegeneracy(w, a, futileResult(), time.Unix(100, 0).UTC())
	if a.DegenStage != DegeneracyNone || a.DegenStreak != 0 || a.DegenStreakSince != nil {
		t.Errorf("disabled observer did not unwind: stage=%v streak=%d since=%v", a.DegenStage, a.DegenStreak, a.DegenStreakSince)
	}
	if len(sink.records) == 0 || sink.records[len(sink.records)-1].Kind != "recovered" {
		t.Errorf("expected a `recovered` record on the disabled unwind, got %+v", sink.records)
	}
}

func TestSnapshotActor_ProjectsEffectiveDegenStage(t *testing.T) {
	a := &Actor{ID: "a1", Kind: KindNPCStateful, DegenStage: DegeneracyThrottled}

	// Observer enabled → the real stage shows through to the snapshot readers.
	if got := snapshotActor(a, 0, true).DegenStage; got != DegeneracyThrottled {
		t.Errorf("enabled: snapshot DegenStage = %v, want throttled", got)
	}
	// Observer disabled → forced to None so the snapshot-only Stage-1 readers
	// (perception thinning, the move_to gate) lift immediately, without waiting
	// for the actor's next scored tick to clear the live stage.
	if got := snapshotActor(a, 0, false).DegenStage; got != DegeneracyNone {
		t.Errorf("disabled: snapshot DegenStage = %v, want none (effective projection)", got)
	}
	// The projection must not mutate the live actor's stage.
	if a.DegenStage != DegeneracyThrottled {
		t.Errorf("projection mutated the live actor stage: %v", a.DegenStage)
	}
}
