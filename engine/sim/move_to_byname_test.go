package sim

import (
	"reflect"
	"testing"
)

// move_to_byname_test.go — resolveStructureByVillageName (LLM-142). White-box
// (package sim) so it can drive the unexported resolver directly with controlled
// positions. World (0,0) is tile (PadX,PadY); a placement at world X = n*TileSize
// sits n tiles east of it, so the actor at the pad origin is exactly n tiles from
// that structure. Village geography is common knowledge: every named structure
// resolves by name at any distance, anchor or not; distance only breaks ties.

func bnWorld(radius int) *World {
	return &World{
		Actors:         make(map[ActorID]*Actor),
		Structures:     make(map[StructureID]*Structure),
		VillageObjects: make(map[VillageObjectID]*VillageObject),
		Assets:         map[AssetID]*Asset{"a": {ID: "a"}},
		Settings:       WorldSettings{DefaultOutdoorSceneRadius: radius},
	}
}

// bnPlace seeds a structure + its placement at world X = tilesEast*TileSize.
func bnPlace(w *World, id StructureID, name string, tilesEast int) {
	w.Structures[id] = &Structure{ID: id, DisplayName: name}
	w.VillageObjects[VillageObjectID(id)] = &VillageObject{
		ID: VillageObjectID(id), AssetID: "a",
		Pos: WorldPos{X: float64(tilesEast) * TileSize, Y: 0},
	}
}

func bnActor(home, work StructureID) *Actor {
	// At the pad origin = world (0,0) = tile (PadX, PadY).
	return &Actor{ID: "a1", Pos: WorldPos{X: 0, Y: 0}.Tile(), HomeStructureID: home, WorkStructureID: work}
}

func TestResolveStructureByName_InRangeMatch(t *testing.T) {
	w := bnWorld(3)
	bnPlace(w, "tavern", "The Tavern", 2)
	got, ok := resolveStructureByVillageName(w, bnActor("", ""), "the tavern")
	if !ok || got != "tavern" {
		t.Fatalf("case-insensitive match: got %q ok=%v, want tavern", got, ok)
	}
}

// The live ZBBS-WORK-417 case: the structure's canonical name has NO article
// ("Tavern") and the model emits "the tavern". The asymmetric case that used to
// bounce before placeNameMatches tolerated a one-sided article.
func TestResolveStructureByName_LeadingArticleOnQuery(t *testing.T) {
	w := bnWorld(3)
	bnPlace(w, "tavern", "Tavern", 2) // canonical name has no leading article
	got, ok := resolveStructureByVillageName(w, bnActor("", ""), "the tavern")
	if !ok || got != "tavern" {
		t.Fatalf("a leading article on the query must still resolve, got %q ok=%v, want tavern", got, ok)
	}
}

// The LLM-142 fix: a far, never-visited, non-anchor structure resolves by name —
// village geography is common knowledge, not earned. This is the exact scene
// 019f094d case (a Walker at home naming "the Tavern" across town) as a unit
// invariant. Before the fix this was a deliberate MISS (out-of-radius reject).
func TestResolveStructureByName_FarStructureResolvesAsCommonKnowledge(t *testing.T) {
	w := bnWorld(3)
	bnPlace(w, "tavern", "The Tavern", 9) // far beyond any scene radius, NOT an anchor
	got, ok := resolveStructureByVillageName(w, bnActor("walker_home", ""), "the tavern")
	if !ok || got != "tavern" {
		t.Fatalf("a far non-anchor structure must resolve as common knowledge, got %q ok=%v", got, ok)
	}
}

func TestResolveStructureByName_AnchorAnyDistance(t *testing.T) {
	w := bnWorld(3)
	bnPlace(w, "tavern", "The Tavern", 50) // far away, and it's the actor's work anchor
	got, ok := resolveStructureByVillageName(w, bnActor("", "tavern"), "the tavern")
	if !ok || got != "tavern" {
		t.Fatalf("a far anchor must resolve by name, got %q ok=%v", got, ok)
	}
}

func TestResolveStructureByName_NearestWinsOnDuplicate(t *testing.T) {
	w := bnWorld(5)
	bnPlace(w, "well_far", "The Well", 4)
	bnPlace(w, "well_near", "The Well", 1)
	got, ok := resolveStructureByVillageName(w, bnActor("", ""), "the well")
	if !ok || got != "well_near" {
		t.Fatalf("duplicate names must resolve to the NEAREST, got %q ok=%v", got, ok)
	}
}

func TestResolveStructureByName_NoMatch(t *testing.T) {
	w := bnWorld(5)
	bnPlace(w, "tavern", "The Tavern", 1)
	if got, ok := resolveStructureByVillageName(w, bnActor("", ""), "the smithy"); ok {
		t.Fatalf("a name no structure has must not resolve, got %q", got)
	}
}

func TestResolveStructureByName_TieBreaksByID(t *testing.T) {
	w := bnWorld(5)
	bnPlace(w, "well_bbb", "The Well", 2)
	bnPlace(w, "well_aaa", "The Well", 2) // same distance
	for i := 0; i < 25; i++ {
		got, ok := resolveStructureByVillageName(w, bnActor("", ""), "the well")
		if !ok || got != "well_aaa" {
			t.Fatalf("equal-distance tie must break to the lowest id, got %q", got)
		}
	}
}

// namedVillageDestinations backs the LLM-306 unknown-place error: it lists real
// PUBLIC structure names the model can pick, nearest-first, capped. This pins the
// public filter (owner-only/closed excluded), the placement + blank-name skips,
// nearest-first order, dedupe-by-name, and the cap + "more existed" signal — all
// in one controlled world so the produced error string is byte-stable.
func TestNamedVillageDestinations_PublicNearestFirstCapped(t *testing.T) {
	w := bnWorld(5)
	// Public destinations at increasing distance (bnPlace leaves EntryPolicy at
	// the default "", which counts as public). Six distinct public names.
	bnPlace(w, "store", "General Store", 1)
	bnPlace(w, "tavern", "The Tavern", 2)
	bnPlace(w, "smithy", "Blacksmith", 3)
	bnPlace(w, "meeting", "Meeting House", 4)
	bnPlace(w, "church", "Church", 5)
	bnPlace(w, "mill", "Mill", 6)
	// A second "General Store" farther out — must dedupe to the nearest instance.
	bnPlace(w, "store_far", "General Store", 7)
	// Excluded: a private home (owner-only) and a closed prop (a well), both near.
	bnPlace(w, "home", "Thorne Residence", 1)
	w.VillageObjects["home"].EntryPolicy = EntryPolicyOwner
	bnPlace(w, "well", "The Well", 1)
	w.VillageObjects["well"].EntryPolicy = EntryPolicyClosed
	// Excluded: a structure with no placement (can't walk there), and one with a
	// blank display name (nothing to name).
	w.Structures["ghost"] = &Structure{ID: "ghost", DisplayName: "Ghost Hall"}
	bnPlace(w, "blank", "", 1)

	names, more := namedVillageDestinations(w, bnActor("", ""), moveToDestinationNameCap)
	want := []string{"General Store", "The Tavern", "Blacksmith", "Meeting House", "Church"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v (public only, nearest-first, deduped, capped at %d)", names, want, moveToDestinationNameCap)
	}
	if !more {
		t.Errorf("more = false, want true (Mill is a 6th public destination beyond the cap of %d)", moveToDestinationNameCap)
	}

	// With a limit above the distinct count, the full deduped list returns and
	// more is false.
	all, moreAll := namedVillageDestinations(w, bnActor("", ""), 10)
	wantAll := append(append([]string{}, want...), "Mill")
	if !reflect.DeepEqual(all, wantAll) {
		t.Errorf("full list = %v, want %v", all, wantAll)
	}
	if moreAll {
		t.Errorf("more = true, want false (limit 10 exceeds the 6 distinct public names)")
	}
}

// A world with no public destination to name (only a private home and a closed
// well) yields an empty list, so the caller falls back to the generic hint.
func TestNamedVillageDestinations_NoPublicStructures(t *testing.T) {
	w := bnWorld(5)
	bnPlace(w, "home", "Thorne Residence", 1)
	w.VillageObjects["home"].EntryPolicy = EntryPolicyOwner
	bnPlace(w, "well", "The Well", 2)
	w.VillageObjects["well"].EntryPolicy = EntryPolicyClosed

	names, more := namedVillageDestinations(w, bnActor("", ""), moveToDestinationNameCap)
	if len(names) != 0 || more {
		t.Errorf("names = %v more = %v, want empty + false (no public destinations)", names, more)
	}
}

// A non-positive cap names nothing (no negative-slice panic) but still reports
// that public destinations existed.
func TestNamedVillageDestinations_NonPositiveLimit(t *testing.T) {
	w := bnWorld(5)
	bnPlace(w, "store", "General Store", 1)
	bnPlace(w, "tavern", "The Tavern", 2)

	for _, limit := range []int{0, -1} {
		names, more := namedVillageDestinations(w, bnActor("", ""), limit)
		if len(names) != 0 {
			t.Errorf("limit %d: names = %v, want empty", limit, names)
		}
		if !more {
			t.Errorf("limit %d: more = false, want true (public destinations exist)", limit)
		}
	}
}
