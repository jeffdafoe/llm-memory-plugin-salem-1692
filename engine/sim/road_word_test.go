package sim

import (
	"testing"
)

// road_word_test.go — the snapshot carries the word a traveler holds (VisitorState.Payload,
// the messenger's news since LLM-700) and who has already heard it (LLM-545), so
// perception renders both off world.Published().

// TestSnapshotActorCarriesRoadWordPayload guards the live Actor -> ActorSnapshot copy
// path (snapshotActor -> cloneVisitorState). Perception reads the road word off
// ActorSnapshot.VisitorState.Payload, so if the clone ever dropped the field (e.g.
// a refactor from the current whole-struct copy to a field-by-field literal) a
// spawned traveler would persist the road word but never actually voice it. This pins
// the field through the real snapshot builder, not a hand-built ActorSnapshot.
func TestSnapshotActorCarriesRoadWordPayload(t *testing.T) {
	const roadWord = "Ezekiel Crane turned out a plow for the Hale farm"
	a := &Actor{
		ID:          "vstr-0000abcd",
		DisplayName: "Elias Drum the peddler",
		Kind:        KindNPCShared,
		Needs:       seedVisitorNeeds(),
		Inventory:   map[ItemKind]int{},
		VisitorState: &VisitorState{
			Archetype: "peddler", Origin: "Boston", Disposition: "weary",
			Phase: VisitorPhasePresent, Payload: roadWord,
		},
	}
	snap := snapshotActor(a, 0, false)
	if snap.VisitorState == nil {
		t.Fatal("snapshot dropped VisitorState")
	}
	if snap.VisitorState.Payload != roadWord {
		t.Errorf("snapshot Payload = %q; want the road word carried through to perception", snap.VisitorState.Payload)
	}
	// LLM-566: the vocation sentence derives from Archetype at perception-build
	// time, so the archetype surviving the clone is what keeps a live traveler
	// preachy/musical/surgical — same refactor risk as Payload above.
	if snap.VisitorState.Archetype != "peddler" {
		t.Errorf("snapshot Archetype = %q; want it carried through — perception derives the vocation line from it", snap.VisitorState.Archetype)
	}
}

// TestSnapshotActorCarriesPayloadSharedWith is the LLM-545 sibling of the test
// above: the shared-word memory must survive the real published-snapshot builder
// (snapshotActor -> cloneVisitorState) — perception's roadWordSharedWith
// reads it off ActorSnapshot.VisitorState — and must be INDEPENDENT of the live
// actor, so a world-side stamp can't appear mid-publish nor a snapshot mutation
// bleed back.
func TestSnapshotActorCarriesPayloadSharedWith(t *testing.T) {
	a := &Actor{
		ID:          "vstr-0000abcd",
		DisplayName: "Elias Drum the peddler",
		Kind:        KindNPCShared,
		Needs:       seedVisitorNeeds(),
		Inventory:   map[ItemKind]int{},
		VisitorState: &VisitorState{
			Archetype: "peddler", Phase: VisitorPhasePresent,
			Payload:           "Ezekiel Crane turned out a plow for the Hale farm",
			PayloadSharedWith: []ActorID{"hannah"},
		},
	}
	snap := snapshotActor(a, 0, false)
	if snap.VisitorState == nil {
		t.Fatal("snapshot dropped VisitorState")
	}
	got := snap.VisitorState.PayloadSharedWith
	if len(got) != 1 || got[0] != "hannah" {
		t.Fatalf("snapshot PayloadSharedWith = %v; want [hannah]", got)
	}
	// Independence: an element write on the live actor must not show through the
	// already-published snapshot (an append can reallocate and mask aliasing, so
	// the probe is an in-place write).
	a.VisitorState.PayloadSharedWith[0] = "overwritten"
	if snap.VisitorState.PayloadSharedWith[0] != "hannah" {
		t.Error("world-side write reached the published snapshot — the slice is aliased, not deep-copied")
	}
}
