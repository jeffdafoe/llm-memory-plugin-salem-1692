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
// names the model can pick, nearest-first, capped. LLM-336 narrowed the set to
// currently-OPEN businesses — an object tagged TagBusiness with a keeper tending
// it (keeperPresentAt) — so an owner-only shop is now listed (the reported
// Blacksmith bug the old entry-policy filter dropped) while a shut business, a
// residence, and a placement-less/blank structure are not. This pins that
// predicate, the skips, nearest-first order, dedupe-by-name, and the cap + "more
// existed" signal in one controlled world so the produced error string is stable.
func TestNamedVillageDestinations_OpenBusinessesNearestFirstCapped(t *testing.T) {
	w := bnWorld(5)
	// Open businesses at increasing distance. Blacksmith / General Store / PW
	// Apothecary are owner-only AND open — the LLM-336 case the old entry-policy
	// filter wrongly dropped; Tavern / Inn / Mill are open-policy. Six names.
	bnBusiness(w, "store", "General Store", 1, EntryPolicyOwner, true)
	bnBusiness(w, "tavern", "The Tavern", 2, EntryPolicyOpen, true)
	bnBusiness(w, "smithy", "Blacksmith", 3, EntryPolicyOwner, true)
	bnBusiness(w, "inn", "Inn", 4, EntryPolicyOpen, true)
	bnBusiness(w, "apothecary", "PW Apothecary", 5, EntryPolicyOwner, true)
	bnBusiness(w, "mill", "Mill", 6, EntryPolicyOpen, true)
	// A second "General Store" farther out — must dedupe to the nearest instance.
	bnBusiness(w, "store_far", "General Store", 7, EntryPolicyOwner, true)
	// Excluded: a shut business (tagged, but no keeper present), a residence (not
	// a business), a structure with no placement, and one with a blank name.
	bnBusiness(w, "cooper", "Cooper Shop", 1, EntryPolicyOwner, false)
	bnPlace(w, "home", "Thorne Residence", 1)
	w.VillageObjects["home"].EntryPolicy = EntryPolicyOwner
	w.Structures["ghost"] = &Structure{ID: "ghost", DisplayName: "Ghost Hall"}
	bnBusiness(w, "blank", "", 1, EntryPolicyOwner, true)

	names, more := namedVillageDestinations(w, bnActor("", ""), moveToDestinationNameCap)
	want := []string{"General Store", "The Tavern", "Blacksmith", "Inn", "PW Apothecary"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v (open businesses, nearest-first, deduped, capped at %d)", names, want, moveToDestinationNameCap)
	}
	if !more {
		t.Errorf("more = false, want true (Mill is a 6th open business beyond the cap of %d)", moveToDestinationNameCap)
	}

	// With a limit above the distinct count, the full deduped list returns and
	// more is false.
	all, moreAll := namedVillageDestinations(w, bnActor("", ""), 10)
	wantAll := append(append([]string{}, want...), "Mill")
	if !reflect.DeepEqual(all, wantAll) {
		t.Errorf("full list = %v, want %v", all, wantAll)
	}
	if moreAll {
		t.Errorf("more = true, want false (limit 10 exceeds the 6 distinct open businesses)")
	}
}

// LLM-336: the open/closed test is ground-truth keeper presence, not entry policy
// or current_state. An owner-only business is listed while a keeper tends it and
// drops the instant the keeper is gone — the same keeperPresentAt signal the
// move_to shut-note and seek-work cue use. (A failed move_to is already a confused
// model, so the recovery hint reads true world state rather than earned memory.)
func TestNamedVillageDestinations_KeeperPresenceGatesBusiness(t *testing.T) {
	w := bnWorld(5)
	bnBusiness(w, "smithy", "Blacksmith", 2, EntryPolicyOwner, true) // owner-only, keeper in

	names, _ := namedVillageDestinations(w, bnActor("", ""), moveToDestinationNameCap)
	if !reflect.DeepEqual(names, []string{"Blacksmith"}) {
		t.Fatalf("keeper present: names = %v, want [Blacksmith] (owner-only business listed while tended)", names)
	}

	// Keeper leaves the forge — no longer inside it, nor loitering at its pin.
	w.Actors["keeper_smithy"].InsideStructureID = ""
	w.Actors["keeper_smithy"].Pos = WorldPos{X: 999 * TileSize, Y: 0}.Tile()

	names, more := namedVillageDestinations(w, bnActor("", ""), moveToDestinationNameCap)
	if len(names) != 0 || more {
		t.Fatalf("keeper gone: names = %v more = %v, want empty + false (shut business dropped)", names, more)
	}
}

// LLM-336: the business tag is read off the PLACED OBJECT (vobj.HasTag), not
// Structure.Tags, which is stale in the live world (the Inn's structure row even
// omits "business"). A structure whose ROW carries TagBusiness but whose placed
// object is untagged is not eligible even with a keeper present — pinning the
// placed-object-is-authoritative data-source choice (code_review).
func TestNamedVillageDestinations_StructureTagsNotConsulted(t *testing.T) {
	w := bnWorld(5)
	bnPlace(w, "smithy", "Blacksmith", 2)               // placed object left UNtagged
	w.Structures["smithy"].Tags = []string{TagBusiness} // stale structure-level tag
	// A keeper is present, so the untagged placed object is the only disqualifier.
	w.Actors["keeper_smithy"] = &Actor{ID: "keeper_smithy", WorkStructureID: "smithy", InsideStructureID: "smithy", State: StateIdle}

	names, more := namedVillageDestinations(w, bnActor("", ""), moveToDestinationNameCap)
	if len(names) != 0 || more {
		t.Errorf("names = %v more = %v, want empty + false (Structure.Tags must not make an untagged placed object eligible)", names, more)
	}
}

// A world with no OPEN business to name — only a residence and a shut shop (no
// keeper present) — yields an empty list, so MoveToStructureByName falls through
// to the generic hint. LLM-336.
func TestNamedVillageDestinations_NoOpenBusinesses(t *testing.T) {
	w := bnWorld(5)
	bnPlace(w, "home", "Thorne Residence", 1)
	w.VillageObjects["home"].EntryPolicy = EntryPolicyOwner
	bnBusiness(w, "smithy", "Blacksmith", 2, EntryPolicyOwner, false) // shut: no keeper

	names, more := namedVillageDestinations(w, bnActor("", ""), moveToDestinationNameCap)
	if len(names) != 0 || more {
		t.Errorf("names = %v more = %v, want empty + false (no open businesses)", names, more)
	}
}

// A non-positive cap names nothing (no negative-slice panic) but still reports
// that open businesses existed.
func TestNamedVillageDestinations_NonPositiveLimit(t *testing.T) {
	w := bnWorld(5)
	bnBusiness(w, "store", "General Store", 1, EntryPolicyOwner, true)
	bnBusiness(w, "tavern", "The Tavern", 2, EntryPolicyOpen, true)

	for _, limit := range []int{0, -1} {
		names, more := namedVillageDestinations(w, bnActor("", ""), limit)
		if len(names) != 0 {
			t.Errorf("limit %d: names = %v, want empty", limit, names)
		}
		if !more {
			t.Errorf("limit %d: more = false, want true (open businesses exist)", limit)
		}
	}
}
