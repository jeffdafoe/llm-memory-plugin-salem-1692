package perception

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// source_activity_suppression_test.go — LLM-435. Per-builder coverage that each of
// the three source-activity-START cues (hearth / stall-repair / gatherable) is
// suppressed the moment the subject has a timed source activity in flight, so the
// start tool each gates (stoke / repair / gather) is withheld from an actor the
// substrate would reject with "you are already busy ...". Each test pairs a
// CONTROL (the cue present with no activity) with the suppressed case, so a
// regression that stops honoring SourceActivityKind fails here. This is the
// targeted gather/repair/refresh coverage the whole-matrix invariant
// (TestGoldensNoSourceActivityStartCueWhileMidSourceActivity) can't give on its
// own — that invariant only bites scenarios that already put the subject mid a
// source activity, and only the stoke fixture does.

func TestBuildHearth_SuppressedMidSourceActivity(t *testing.T) {
	snap, actorID, _ := keeperAtDeadHearthStorm() // owner inside at her own low hearth, wood in hand
	a := snap.Actors[actorID]
	if buildHearth(snap, actorID, a) == nil {
		t.Fatal("control: expected a hearth cue for the keeper at her own low hearth")
	}
	a.SourceActivityKind = sim.SourceActivityStoke
	if v := buildHearth(snap, actorID, a); v != nil {
		t.Errorf("buildHearth = %+v, want nil (mid a source activity — a fresh stoke bounces the busy-guard)", v)
	}
}

func TestBuildStallRepair_SuppressedMidSourceActivity(t *testing.T) {
	snap, actorID, _ := ownerAtWornStall() // owner at his own worn stall with nails
	a := snap.Actors[actorID]
	if buildStallRepair(snap, actorID, a) == nil {
		t.Fatal("control: expected a stall-repair cue for the owner at his own worn stall")
	}
	// A DIFFERENT kind than the cue's own activity — proves any in-flight source
	// activity suppresses, not only a matching one (the fail-closed contract).
	a.SourceActivityKind = sim.SourceActivityHarvest
	if v := buildStallRepair(snap, actorID, a); v != nil {
		t.Errorf("buildStallRepair = %+v, want nil (mid a source activity — a fresh repair bounces the busy-guard)", v)
	}
}

func TestFindGatherableCue_SuppressedMidSourceActivity(t *testing.T) {
	wellPin := sim.WorldPos{X: 100, Y: 100}.Tile()
	snap := gatherCueSnapshot(wellPin)
	snap.VillageObjects["well"].OwnerActorID = "hannah" // she'd otherwise see her own source (TestBuild_GatherableCue_OwnedBySelf_Shows)
	a := snap.Actors["hannah"]
	if _, _, ok := findGatherableCue(snap, "hannah", a); !ok {
		t.Fatal("control: expected the gatherable cue for the owner at her own source")
	}
	a.SourceActivityKind = sim.SourceActivityHarvest
	if _, _, ok := findGatherableCue(snap, "hannah", a); ok {
		t.Error("findGatherableCue returned ok, want suppressed (mid a source activity — a fresh gather bounces the busy-guard)")
	}
}
