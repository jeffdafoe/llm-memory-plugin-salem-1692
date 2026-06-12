package sim

import (
	"testing"
	"time"
)

// social_test.go — ZBBS-WORK-279 slice 4b, tick-driver producer #4 (social).
// Covers the boundary math (mostRecentWindowBoundary, incl. wrap-midnight), the
// tag/nearest helpers, and the pure socialMove decision (subject filters, the
// enter/leave guards, idempotency via the boundary stamp). The actual MoveActor
// walk dispatch is exercised by MoveActor's own tests — socialMove returns the
// target without dispatching, so these tests need no terrain (same split as
// shiftDutyTarget vs ShiftTick).

func socialTestWorld(actors ...*Actor) *World {
	m := make(map[ActorID]*Actor, len(actors))
	for _, a := range actors {
		m[a.ID] = a
	}
	return &World{
		Actors:         m,
		Structures:     map[StructureID]*Structure{},
		VillageObjects: map[VillageObjectID]*VillageObject{},
		Settings:       WorldSettings{Location: time.UTC},
	}
}

// addTaggedStructure registers a structure (shared identity: same id in
// Structures and VillageObjects) at world coords (x,y) carrying tags.
func addTaggedStructure(w *World, id string, x, y float64, tags ...string) {
	w.Structures[StructureID(id)] = &Structure{ID: StructureID(id)}
	w.VillageObjects[VillageObjectID(id)] = &VillageObject{ID: VillageObjectID(id), Pos: WorldPos{X: x, Y: y}, Tags: tags}
}

// socialNPC builds a decorative NPC with a fully-set social window.
func socialNPC(id ActorID, tag string, start, end int, home, inside StructureID) *Actor {
	s, e := start, end
	return &Actor{
		ID:                id,
		Kind:              KindDecorative,
		SocialTag:         tag,
		SocialStartMin:    &s,
		SocialEndMin:      &e,
		HomeStructureID:   home,
		InsideStructureID: inside,
	}
}

// at is a brief helper for a UTC instant on a fixed test day.
func at(hour, min int) time.Time {
	return time.Date(2026, 5, 22, hour, min, 0, 0, time.UTC)
}

func TestMostRecentSocialBoundary(t *testing.T) {
	w := socialTestWorld()
	const start, end = 1080, 1320 // 18:00–22:00

	cases := []struct {
		name          string
		now           time.Time
		wantEnter     bool
		wantHour      int // expected boundary hour (UTC); -1 = expect today, see day check
		wantYesterday bool
	}{
		{"just after enter", at(18, 30), true, 18, false},
		{"just after leave", at(22, 30), false, 22, false},
		{"before today's enter -> yesterday's leave", at(9, 0), false, 22, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, isEnter, ok := mostRecentWindowBoundary(w, start, end, c.now)
			if !ok {
				t.Fatal("ok=false, want a boundary")
			}
			if isEnter != c.wantEnter {
				t.Errorf("isEnter = %v, want %v", isEnter, c.wantEnter)
			}
			if b.Hour() != c.wantHour {
				t.Errorf("boundary hour = %d, want %d (boundary=%v)", b.Hour(), c.wantHour, b)
			}
			if b.After(c.now) {
				t.Errorf("boundary %v is after now %v — must be at-or-before", b, c.now)
			}
			isYesterday := b.Day() != c.now.Day()
			if isYesterday != c.wantYesterday {
				t.Errorf("boundary yesterday = %v, want %v (boundary=%v now=%v)", isYesterday, c.wantYesterday, b, c.now)
			}
		})
	}
}

func TestMostRecentSocialBoundary_WrapMidnight(t *testing.T) {
	w := socialTestWorld()
	const start, end = 1320, 120 // 22:00–02:00, wraps midnight

	// At 00:30, the most recent boundary is YESTERDAY's 22:00 enter (today's
	// 22:00 enter and 02:00 leave are both still in the future).
	b, isEnter, ok := mostRecentWindowBoundary(w, start, end, at(0, 30))
	if !ok || !isEnter {
		t.Fatalf("got (enter=%v, ok=%v), want enter=true ok=true", isEnter, ok)
	}
	if b.Hour() != 22 || b.Day() == 22 {
		t.Errorf("boundary = %v, want yesterday 22:00", b)
	}

	// At 02:30, the most recent boundary is today's 02:00 leave.
	b, isEnter, ok = mostRecentWindowBoundary(w, start, end, at(2, 30))
	if !ok || isEnter {
		t.Fatalf("got (enter=%v, ok=%v), want enter=false ok=true", isEnter, ok)
	}
	if b.Hour() != 2 || b.Day() != 22 {
		t.Errorf("boundary = %v, want today 02:00", b)
	}
}

func TestMostRecentSocialBoundary_EqualEndpointsEmpty(t *testing.T) {
	w := socialTestWorld()
	if _, _, ok := mostRecentWindowBoundary(w, 600, 600, at(12, 0)); ok {
		t.Error("start==end should be an empty window (ok=false)")
	}
}

func TestStructureHasTag(t *testing.T) {
	w := socialTestWorld()
	addTaggedStructure(w, "tavern", 100, 100, "social", "vendor")
	addTaggedStructure(w, "shop", 200, 200, "vendor")

	if !structureHasTag(w, "tavern", "social") {
		t.Error("tavern should have 'social'")
	}
	if structureHasTag(w, "shop", "social") {
		t.Error("shop should NOT have 'social'")
	}
	if structureHasTag(w, "", "social") {
		t.Error("empty structureID should be false")
	}
	if structureHasTag(w, "nonexistent", "social") {
		t.Error("unknown structureID should be false")
	}
}

func TestFindNearestSocialStructure(t *testing.T) {
	w := socialTestWorld()
	// Two tagged structures; "near" is closer to the actor at (0,0).
	addTaggedStructure(w, "near", 30, 40, "social") // dist 50
	addTaggedStructure(w, "far", 300, 400, "social")
	// A tagged village_object that is NOT a structure — must be ignored.
	w.VillageObjects["bare"] = &VillageObject{ID: "bare", Pos: WorldPos{X: 1, Y: 1}, Tags: []string{"social"}}
	// An untagged structure — must be ignored.
	addTaggedStructure(w, "untagged", 2, 2)

	a := &Actor{ID: "d", Pos: TilePos{X: 0, Y: 0}}
	got, ok := findNearestSocialStructure(w, a, "social")
	if !ok || got != "near" {
		t.Errorf("got (%q, %v), want (near, true)", got, ok)
	}

	// No structure carries the tag → ok=false.
	if _, ok := findNearestSocialStructure(w, a, "nope"); ok {
		t.Error("want ok=false when no tagged structure exists")
	}
}

func TestSocialMove_EnterWalksToNearest(t *testing.T) {
	w := socialTestWorld()
	addTaggedStructure(w, "tavern", 100, 100, "social")
	a := socialNPC("d", "social", 1080, 1320, "home", "") // outdoors, not at tavern
	w.Actors["d"] = a

	walkTo, _, ok := socialMove(w, a, at(18, 30)) // just after enter
	if !ok || walkTo != "tavern" {
		t.Errorf("got (%q, ok=%v), want (tavern, true)", walkTo, ok)
	}
}

func TestSocialMove_EnterNoOpWhenAlreadyInsideTagged(t *testing.T) {
	w := socialTestWorld()
	addTaggedStructure(w, "tavern", 100, 100, "social")
	a := socialNPC("d", "social", 1080, 1320, "home", "tavern") // already at the tavern
	w.Actors["d"] = a

	walkTo, _, ok := socialMove(w, a, at(18, 30))
	if !ok {
		t.Fatal("ok=false, want true (boundary still stamps)")
	}
	if walkTo != "" {
		t.Errorf("walkTo = %q, want empty (already inside a tagged structure)", walkTo)
	}
}

func TestSocialMove_EnterNoOpWhenNoTaggedStructure(t *testing.T) {
	w := socialTestWorld() // no tagged structures at all
	a := socialNPC("d", "social", 1080, 1320, "home", "")
	w.Actors["d"] = a

	walkTo, _, ok := socialMove(w, a, at(18, 30))
	if !ok || walkTo != "" {
		t.Errorf("got (%q, ok=%v), want (\"\", true) — stamp-only no-op", walkTo, ok)
	}
}

func TestSocialMove_LeaveWalksHomeOnlyIfInsideTagged(t *testing.T) {
	w := socialTestWorld()
	addTaggedStructure(w, "tavern", 100, 100, "social")

	// Inside the tagged structure at the leave boundary → walk home.
	inside := socialNPC("a", "social", 1080, 1320, "home", "tavern")
	w.Actors["a"] = inside
	walkTo, _, ok := socialMove(w, inside, at(22, 30))
	if !ok || walkTo != "home" {
		t.Errorf("inside-tagged leave: got (%q, ok=%v), want (home, true)", walkTo, ok)
	}

	// NOT inside a tagged structure at the leave boundary → stamp only, no
	// walk home (the load-bearing guard — don't yank from a shop).
	elsewhere := socialNPC("b", "social", 1080, 1320, "home", "shop")
	w.Actors["b"] = elsewhere
	walkTo, _, ok = socialMove(w, elsewhere, at(22, 30))
	if !ok {
		t.Fatal("ok=false, want true (boundary stamps)")
	}
	if walkTo != "" {
		t.Errorf("elsewhere leave: walkTo = %q, want empty (guard suppresses the walk-home)", walkTo)
	}
}

func TestSocialMove_Idempotent(t *testing.T) {
	w := socialTestWorld()
	addTaggedStructure(w, "tavern", 100, 100, "social")
	a := socialNPC("d", "social", 1080, 1320, "home", "")
	w.Actors["d"] = a

	now := at(18, 30)
	_, boundary, ok := socialMove(w, a, now)
	if !ok {
		t.Fatal("first eval: ok=false, want true")
	}
	// Simulate the stamp the SocialTick caller would apply.
	a.SocialLastBoundaryAt = &boundary

	if _, _, ok := socialMove(w, a, now); ok {
		t.Error("second eval at same boundary: ok=true, want false (idempotent via stamp)")
	}
	// A bit later, still same window, still the same most-recent boundary → no re-fire.
	if _, _, ok := socialMove(w, a, at(19, 0)); ok {
		t.Error("eval later in same window: ok=true, want false")
	}
}

func TestSocialMove_SubjectExclusions(t *testing.T) {
	w := socialTestWorld()
	addTaggedStructure(w, "tavern", 100, 100, "social")
	now := at(18, 30)

	// Agent NPC (not decorative) — excluded.
	agent := socialNPC("agent", "social", 1080, 1320, "home", "")
	agent.Kind = KindNPCStateful
	if _, _, ok := socialMove(w, agent, now); ok {
		t.Error("agent NPC should be excluded")
	}

	// PC — excluded.
	pc := socialNPC("pc", "social", 1080, 1320, "home", "")
	pc.Kind = KindPC
	if _, _, ok := socialMove(w, pc, now); ok {
		t.Error("PC should be excluded")
	}

	// No home — excluded (nowhere to return at leave).
	noHome := socialNPC("nohome", "social", 1080, 1320, "", "")
	if _, _, ok := socialMove(w, noHome, now); ok {
		t.Error("NPC without HomeStructureID should be excluded")
	}

	// Incomplete config (tag without window) — excluded.
	incomplete := &Actor{ID: "inc", Kind: KindDecorative, SocialTag: "social", HomeStructureID: "home"}
	if _, _, ok := socialMove(w, incomplete, now); ok {
		t.Error("incomplete social config should be excluded")
	}
}

// TestSocialTick_FailedWalkDoesNotStamp — when an intended walk fails (no
// terrain to pathfind in this minimal world, so MoveActor errors),
// SocialLastBoundaryAt must stay unstamped so the next tick retries the
// boundary. Stamping a failed walk would silently skip the boundary for the
// rest of the window, since the stamp is the sole edge re-fire guard
// (code_review, 2026-05-22).
func TestSocialTick_FailedWalkDoesNotStamp(t *testing.T) {
	w := socialTestWorld()
	addTaggedStructure(w, "tavern", 100, 100, "social")
	a := socialNPC("d", "social", 1080, 1320, "home", "") // outdoors → enter walk to tavern
	w.Actors["d"] = a

	if _, err := SocialTick(at(18, 30)).Fn(w); err != nil {
		t.Fatalf("SocialTick: %v", err)
	}
	if a.SocialLastBoundaryAt != nil {
		t.Errorf("SocialLastBoundaryAt = %v after a FAILED walk; want nil so the boundary retries", a.SocialLastBoundaryAt)
	}
}

// TestSocialTick_StampsNoOpBoundary verifies the full Command stamps
// SocialLastBoundaryAt on a stamp-only (no-walk) boundary and is idempotent on
// the next tick — exercised via the no-tagged-structure case so no MoveActor /
// terrain is needed.
func TestSocialTick_StampsNoOpBoundary(t *testing.T) {
	w := socialTestWorld() // no tagged structures → enter is a stamp-only no-op
	a := socialNPC("d", "social", 1080, 1320, "home", "")
	w.Actors["d"] = a

	if _, err := SocialTick(at(18, 30)).Fn(w); err != nil {
		t.Fatalf("SocialTick: %v", err)
	}
	if a.SocialLastBoundaryAt == nil {
		t.Fatal("SocialLastBoundaryAt not stamped after the enter boundary")
	}
	if a.SocialLastBoundaryAt.Hour() != 18 {
		t.Errorf("stamped boundary hour = %d, want 18", a.SocialLastBoundaryAt.Hour())
	}
	first := *a.SocialLastBoundaryAt

	// Second tick in the same window must not change the stamp.
	if _, err := SocialTick(at(19, 0)).Fn(w); err != nil {
		t.Fatalf("SocialTick (2nd): %v", err)
	}
	if !a.SocialLastBoundaryAt.Equal(first) {
		t.Errorf("stamp moved on idempotent re-tick: %v != %v", *a.SocialLastBoundaryAt, first)
	}
}
