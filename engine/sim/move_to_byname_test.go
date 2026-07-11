package sim

import (
	"reflect"
	"testing"
	"time"
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

// bnBusiness seeds a business structure + its placement (tagged TagBusiness) at
// world X = tilesEast*TileSize with the given entry policy, and — when keeper is
// true — an awake worker standing inside it so keeperPresentAt reports it as
// currently tended. This is the LLM-336 open-business gate namedVillageDestinations
// filters on: an owner-only shop must still be listed while its keeper is in.
func bnBusiness(w *World, id StructureID, name string, tilesEast int, entry EntryPolicy, keeper bool) {
	bnPlace(w, id, name, tilesEast)
	obj := w.VillageObjects[VillageObjectID(id)]
	obj.Tags = []string{TagBusiness}
	obj.EntryPolicy = entry
	if keeper {
		workerID := ActorID("keeper_" + string(id))
		w.Actors[workerID] = &Actor{ID: workerID, WorkStructureID: id, InsideStructureID: id, State: StateIdle}
	}
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
// names the model can pick, nearest-first, capped. The set is every business — an
// object tagged TagBusiness, INCLUDING owner-only shops (blacksmith, store,
// apothecary) and regardless of whether a keeper tends it right now (LLM-341, which
// reverted the LLM-336 keeperPresentAt gate). A residence and a placement-less or
// blank-named structure are not. Cooper Shop here is a keeperless business and
// still lists. This pins the predicate, the skips, nearest-first order,
// dedupe-by-name, and the cap + "more existed" signal in one controlled world so
// the produced error string is stable.
func TestNamedVillageDestinations_BusinessesNearestFirstCapped(t *testing.T) {
	w := bnWorld(5)
	// Businesses at increasing distance. Blacksmith / General Store / PW Apothecary
	// are owner-only (public to walk TO, LLM-336); Tavern / Inn / Mill are
	// open-policy. All keeper-tended except Cooper Shop below.
	bnBusiness(w, "store", "General Store", 1, EntryPolicyOwner, true)
	bnBusiness(w, "tavern", "The Tavern", 2, EntryPolicyOpen, true)
	bnBusiness(w, "smithy", "Blacksmith", 3, EntryPolicyOwner, true)
	bnBusiness(w, "inn", "Inn", 4, EntryPolicyOpen, true)
	bnBusiness(w, "apothecary", "PW Apothecary", 5, EntryPolicyOwner, true)
	bnBusiness(w, "mill", "Mill", 6, EntryPolicyOpen, true)
	// A second "General Store" farther out — must dedupe to the nearest instance.
	bnBusiness(w, "store_far", "General Store", 7, EntryPolicyOwner, true)
	// A keeperless business still lists after LLM-341 — placed farthest out so it
	// appends to the full list without disturbing the nearest-cap assertion.
	bnBusiness(w, "cooper", "Cooper Shop", 8, EntryPolicyOwner, false)
	// Excluded: a residence (not a business), a structure with no placement, and
	// one with a blank name.
	bnPlace(w, "home", "Thorne Residence", 1)
	w.VillageObjects["home"].EntryPolicy = EntryPolicyOwner
	w.Structures["ghost"] = &Structure{ID: "ghost", DisplayName: "Ghost Hall"}
	bnBusiness(w, "blank", "", 1, EntryPolicyOwner, true)

	names, more := namedVillageDestinations(w, bnActor("", ""), moveToDestinationNameCap, time.Now())
	want := []string{"General Store", "The Tavern", "Blacksmith", "Inn", "PW Apothecary"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v (businesses, nearest-first, deduped, capped at %d)", names, want, moveToDestinationNameCap)
	}
	if !more {
		t.Errorf("more = false, want true (Mill and Cooper Shop are beyond the cap of %d)", moveToDestinationNameCap)
	}

	// With a limit above the distinct count, the full deduped list returns (the
	// keeperless Cooper Shop last, farthest out) and more is false.
	all, moreAll := namedVillageDestinations(w, bnActor("", ""), 10, time.Now())
	wantAll := append(append([]string{}, want...), "Mill", "Cooper Shop")
	if !reflect.DeepEqual(all, wantAll) {
		t.Errorf("full list = %v, want %v", all, wantAll)
	}
	if moreAll {
		t.Errorf("more = true, want false (limit 10 exceeds the 7 distinct businesses)")
	}
}

// LLM-341: keeper presence does NOT gate the recovery hint. An owner-only business
// lists whether or not a keeper tends it right now — move_to walks an NPC there at
// any hour (open/closed is learned on arrival, LLM-142), so hiding an untended shop
// from the hint only left a lost NPC (the live forge / "wrong name" case) guessing
// at a name it was never shown. This reverts the LLM-336 keeperPresentAt gate.
func TestNamedVillageDestinations_ListsUntendedBusiness(t *testing.T) {
	w := bnWorld(5)
	bnBusiness(w, "smithy", "Blacksmith", 2, EntryPolicyOwner, true) // owner-only, keeper in

	names, _ := namedVillageDestinations(w, bnActor("", ""), moveToDestinationNameCap, time.Now())
	if !reflect.DeepEqual(names, []string{"Blacksmith"}) {
		t.Fatalf("keeper present: names = %v, want [Blacksmith]", names)
	}

	// Keeper leaves the forge — no longer inside it, nor loitering at its pin. The
	// blacksmith is still a business, so it still lists.
	w.Actors["keeper_smithy"].InsideStructureID = ""
	w.Actors["keeper_smithy"].Pos = WorldPos{X: 999 * TileSize, Y: 0}.Tile()

	names, more := namedVillageDestinations(w, bnActor("", ""), moveToDestinationNameCap, time.Now())
	if !reflect.DeepEqual(names, []string{"Blacksmith"}) || more {
		t.Fatalf("keeper gone: names = %v more = %v, want [Blacksmith] + false (untended business still lists)", names, more)
	}
}

// The business tag is read off the PLACED OBJECT (vobj.HasTag), not Structure.Tags,
// which is stale in the live world (the Inn's structure row even omits "business").
// A structure whose ROW carries TagBusiness but whose placed object is untagged is
// not eligible — pinning the placed-object-is-authoritative data-source choice
// (LLM-336 code_review).
func TestNamedVillageDestinations_StructureTagsNotConsulted(t *testing.T) {
	w := bnWorld(5)
	bnPlace(w, "smithy", "Blacksmith", 2)               // placed object left UNtagged
	w.Structures["smithy"].Tags = []string{TagBusiness} // stale structure-level tag

	names, more := namedVillageDestinations(w, bnActor("", ""), moveToDestinationNameCap, time.Now())
	if len(names) != 0 || more {
		t.Errorf("names = %v more = %v, want empty + false (Structure.Tags must not make an untagged placed object eligible)", names, more)
	}
}

// A world with no business to name — only a residence — yields an empty list, so
// MoveToStructureByName falls through to the generic hint. An untended shop DOES
// list now (LLM-341), so "no business at all" is the only empty case left.
func TestNamedVillageDestinations_NoBusinesses(t *testing.T) {
	w := bnWorld(5)
	bnPlace(w, "home", "Thorne Residence", 1)
	w.VillageObjects["home"].EntryPolicy = EntryPolicyOwner

	names, more := namedVillageDestinations(w, bnActor("", ""), moveToDestinationNameCap, time.Now())
	if len(names) != 0 || more {
		t.Errorf("names = %v more = %v, want empty + false (no business to name)", names, more)
	}
}

// A non-positive cap names nothing (no negative-slice panic) but still reports
// that businesses existed.
func TestNamedVillageDestinations_NonPositiveLimit(t *testing.T) {
	w := bnWorld(5)
	bnBusiness(w, "store", "General Store", 1, EntryPolicyOwner, true)
	bnBusiness(w, "tavern", "The Tavern", 2, EntryPolicyOpen, true)

	for _, limit := range []int{0, -1} {
		names, more := namedVillageDestinations(w, bnActor("", ""), limit, time.Now())
		if len(names) != 0 {
			t.Errorf("limit %d: names = %v, want empty", limit, names)
		}
		if !more {
			t.Errorf("limit %d: more = false, want true (businesses exist)", limit)
		}
	}
}

// LLM-366: the suggestion list drops a business the actor recently found shut (an
// active ObservedClosed within its TTL), so a lost, workless NPC isn't pointed
// back at the very shop it just walked to and found closed — the seam that let
// Silence Walker re-pick the closed General Store every idle cycle. A stale
// (decayed) memory does NOT drop — the NPC should retry once the TTL lapses.
func TestNamedVillageDestinations_DropsRememberedShut(t *testing.T) {
	w := bnWorld(5)
	bnBusiness(w, "store", "General Store", 1, EntryPolicyOwner, true)
	bnBusiness(w, "tavern", "The Tavern", 2, EntryPolicyOpen, true)
	bnBusiness(w, "inn", "Inn", 3, EntryPolicyOpen, true)

	now := time.Unix(1_700_000_000, 0)
	a := bnActor("", "")
	// Found the nearest business (the store) shut an hour ago — within the 4h TTL.
	a.Observed = NewObservedStates(map[ObservedStateKey]time.Time{
		{StructureID: "store", Condition: ObservedClosed}: now.Add(-time.Hour),
	})
	names, _ := namedVillageDestinations(w, a, moveToDestinationNameCap, now)
	if want := []string{"The Tavern", "Inn"}; !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v (the remembered-shut General Store is dropped)", names, want)
	}

	// The same memory, now beyond the TTL, has decayed — the store lists again.
	a.Observed = NewObservedStates(map[ObservedStateKey]time.Time{
		{StructureID: "store", Condition: ObservedClosed}: now.Add(-ClosedBusinessMemoryTTL - time.Minute),
	})
	names, _ = namedVillageDestinations(w, a, moveToDestinationNameCap, now)
	if want := []string{"General Store", "The Tavern", "Inn"}; !reflect.DeepEqual(names, want) {
		t.Errorf("stale memory: names = %v, want %v (decayed shut memory does not drop)", names, want)
	}

	// A FUTURE-stamped shut observation (clock skew) is rejected by Observed.Active's
	// age >= 0 guard, so the business is NOT dropped — the filter shares that decay
	// funnel rather than a looser "is it present" check.
	a.Observed = NewObservedStates(map[ObservedStateKey]time.Time{
		{StructureID: "store", Condition: ObservedClosed}: now.Add(time.Hour),
	})
	names, _ = namedVillageDestinations(w, a, moveToDestinationNameCap, now)
	if want := []string{"General Store", "The Tavern", "Inn"}; !reflect.DeepEqual(names, want) {
		t.Errorf("future-stamped memory: names = %v, want %v (future observation must not drop)", names, want)
	}
}

// LLM-366: when EVERY nearby business is remembered-shut, the list must not go
// empty — an empty hint is worse than naming a shop that may have reopened, and
// MoveToStructureByName would otherwise fall through to the generic no-place hint.
func TestNamedVillageDestinations_AllShutFallsBackToFullList(t *testing.T) {
	w := bnWorld(5)
	bnBusiness(w, "store", "General Store", 1, EntryPolicyOwner, true)
	bnBusiness(w, "tavern", "The Tavern", 2, EntryPolicyOpen, true)

	now := time.Unix(1_700_000_000, 0)
	a := bnActor("", "")
	a.Observed = NewObservedStates(map[ObservedStateKey]time.Time{
		{StructureID: "store", Condition: ObservedClosed}:  now.Add(-time.Hour),
		{StructureID: "tavern", Condition: ObservedClosed}: now.Add(-time.Hour),
	})
	names, _ := namedVillageDestinations(w, a, moveToDestinationNameCap, now)
	if want := []string{"General Store", "The Tavern"}; !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v (all shut → fall back to the full list, never empty)", names, want)
	}
}
