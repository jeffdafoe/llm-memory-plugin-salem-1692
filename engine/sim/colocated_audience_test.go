package sim

import (
	"testing"
	"time"
)

// ZBBS-WORK-407. colocatedAudienceIDs is the non-mutating read mirror of the
// speak audience — who an UNHUDDLED speaker would reach if it spoke now.
// Perception renders it as the "## Around you" co-presence line, so it must
// track conversationalScopeStructure + colocatedConversationalActors (plus the
// active-huddle join EnsureColocatedHuddle performs) exactly, or that line and
// the speak "there is no one here to hear you" gate diverge.

func audienceTestWorld() *World {
	return &World{
		Actors:         make(map[ActorID]*Actor),
		Huddles:        make(map[HuddleID]*Huddle),
		VillageObjects: make(map[VillageObjectID]*VillageObject),
		Assets:         make(map[AssetID]*Asset),
		actorsByHuddle: make(map[HuddleID]map[ActorID]struct{}),
	}
}

func audienceNow() time.Time { return time.Unix(0, 0).UTC() }

func sameIDs(got []ActorID, want ...ActorID) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestColocatedAudienceIDs_AloneInside(t *testing.T) {
	w := audienceTestWorld()
	w.Actors["prudence"] = &Actor{ID: "prudence", Kind: KindNPCShared, InsideStructureID: "inn"}
	if got := colocatedAudienceIDs(w, w.Actors["prudence"], audienceNow()); got != nil {
		t.Errorf("alone inside: got %v, want nil", got)
	}
}

func TestColocatedAudienceIDs_OpenGroundIsAlone(t *testing.T) {
	w := audienceTestWorld()
	// No InsideStructureID and no nearby loiterable object → scope "" → no
	// audience, matching EnsureColocatedHuddle's outdoor bail. A second outdoor
	// actor is NOT reachable (open-ground speech is not formed here).
	w.Actors["prudence"] = &Actor{ID: "prudence", Kind: KindNPCShared}
	w.Actors["hannah"] = &Actor{ID: "hannah", Kind: KindNPCStateful}
	if got := colocatedAudienceIDs(w, w.Actors["prudence"], audienceNow()); got != nil {
		t.Errorf("open ground: got %v, want nil", got)
	}
}

func TestColocatedAudienceIDs_CoPresentUnhuddledSortedSelfExcluded(t *testing.T) {
	w := audienceTestWorld()
	w.Actors["prudence"] = &Actor{ID: "prudence", Kind: KindNPCShared, InsideStructureID: "inn"}
	w.Actors["hannah"] = &Actor{ID: "hannah", Kind: KindNPCStateful, InsideStructureID: "inn"}
	w.Actors["ezekiel"] = &Actor{ID: "ezekiel", Kind: KindNPCShared, InsideStructureID: "inn"}
	w.Actors["faraway"] = &Actor{ID: "faraway", Kind: KindNPCStateful, InsideStructureID: "tavern"}
	got := colocatedAudienceIDs(w, w.Actors["prudence"], audienceNow())
	if !sameIDs(got, "ezekiel", "hannah") {
		t.Errorf("got %v, want [ezekiel hannah] (sorted, self + other-structure excluded)", got)
	}
}

func TestColocatedAudienceIDs_ExcludesSleeperAndDecorative(t *testing.T) {
	w := audienceTestWorld()
	w.Actors["prudence"] = &Actor{ID: "prudence", Kind: KindNPCShared, InsideStructureID: "inn"}
	w.Actors["sleeper"] = &Actor{ID: "sleeper", Kind: KindNPCStateful, InsideStructureID: "inn", State: StateSleeping}
	w.Actors["statue"] = &Actor{ID: "statue", Kind: KindDecorative, InsideStructureID: "inn"}
	w.Actors["awake"] = &Actor{ID: "awake", Kind: KindNPCShared, InsideStructureID: "inn"}
	got := colocatedAudienceIDs(w, w.Actors["prudence"], audienceNow())
	if !sameIDs(got, "awake") {
		t.Errorf("got %v, want [awake] (sleeper + decorative excluded)", got)
	}
}

// ZBBS-WORK-426. colocatedSleeperIDs is the asleep counterpart to
// colocatedAudienceIDs: co-present SLEEPING conversational actors in the same
// scope, surfaced so perception can mark them "(asleep)" while they stay OUT of
// the audience above.

func TestColocatedSleeperIDs_SurfacesCoPresentSleepers(t *testing.T) {
	w := audienceTestWorld()
	w.Actors["prudence"] = &Actor{ID: "prudence", Kind: KindNPCShared, InsideStructureID: "inn"}
	w.Actors["sleeper"] = &Actor{ID: "sleeper", Kind: KindNPCStateful, InsideStructureID: "inn", State: StateSleeping}
	w.Actors["sleeper2"] = &Actor{ID: "sleeper2", Kind: KindNPCShared, InsideStructureID: "inn", State: StateSleeping}
	w.Actors["awake"] = &Actor{ID: "awake", Kind: KindNPCShared, InsideStructureID: "inn"}
	w.Actors["statue"] = &Actor{ID: "statue", Kind: KindDecorative, InsideStructureID: "inn", State: StateSleeping}
	w.Actors["faraway"] = &Actor{ID: "faraway", Kind: KindNPCStateful, InsideStructureID: "tavern", State: StateSleeping}
	got := colocatedSleeperIDs(w, w.Actors["prudence"], audienceNow())
	if !sameIDs(got, "sleeper", "sleeper2") {
		t.Errorf("got %v, want [sleeper sleeper2] (awake, decorative, other-structure, self excluded)", got)
	}
}

func TestColocatedSleeperIDs_OpenGroundIsEmpty(t *testing.T) {
	w := audienceTestWorld()
	w.Actors["prudence"] = &Actor{ID: "prudence", Kind: KindNPCShared} // no structure scope
	w.Actors["sleeper"] = &Actor{ID: "sleeper", Kind: KindNPCStateful, State: StateSleeping}
	if got := colocatedSleeperIDs(w, w.Actors["prudence"], audienceNow()); got != nil {
		t.Errorf("open ground: got %v, want nil", got)
	}
}

func TestColocatedSleeperIDs_ExcludesHuddledSleeper(t *testing.T) {
	// A sleeper has left its huddle on bedding (HOME-435); the already-huddled
	// skip is belt-and-suspenders, matching the audience scan.
	w := audienceTestWorld()
	w.Actors["prudence"] = &Actor{ID: "prudence", Kind: KindNPCShared, InsideStructureID: "inn"}
	w.Actors["sleeper"] = &Actor{ID: "sleeper", Kind: KindNPCStateful, InsideStructureID: "inn", State: StateSleeping, CurrentHuddleID: "h1"}
	if got := colocatedSleeperIDs(w, w.Actors["prudence"], audienceNow()); got != nil {
		t.Errorf("huddled sleeper: got %v, want nil", got)
	}
}

func TestColocatedAudienceIDs_JoinsActiveHuddleMembers(t *testing.T) {
	// Prudence (unhuddled) stands in the inn where John + Ezekiel are already
	// huddled. colocatedConversationalActors skips them (already huddled, never
	// leave-first-yanked), but a speak would join her into their huddle
	// (find-or-create) — so they ARE her audience. The live "walk into an ongoing
	// conversation" case the unhuddled scan alone would miss.
	w := audienceTestWorld()
	w.Actors["prudence"] = &Actor{ID: "prudence", Kind: KindNPCShared, InsideStructureID: "inn"}
	w.Actors["john"] = &Actor{ID: "john", Kind: KindNPCStateful, InsideStructureID: "inn", CurrentHuddleID: "h1"}
	w.Actors["ezekiel"] = &Actor{ID: "ezekiel", Kind: KindNPCShared, InsideStructureID: "inn", CurrentHuddleID: "h1"}
	w.Huddles["h1"] = &Huddle{ID: "h1", StructureID: "inn", Members: map[ActorID]struct{}{"john": {}, "ezekiel": {}}}
	w.actorsByHuddle["h1"] = map[ActorID]struct{}{"john": {}, "ezekiel": {}}
	got := colocatedAudienceIDs(w, w.Actors["prudence"], audienceNow())
	if !sameIDs(got, "ezekiel", "john") {
		t.Errorf("got %v, want [ezekiel john] (joins active-huddle members)", got)
	}
}
