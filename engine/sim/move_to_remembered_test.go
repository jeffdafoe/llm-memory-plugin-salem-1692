package sim

import (
	"reflect"
	"testing"
)

// move_to_remembered_test.go — LLM-78. White-box (package sim) coverage of the
// memory-backed name-resolution fallback: CollectRememberedPlaces (the by-kind
// split) and the resolveStructureByRememberedName / resolveObjectByRememberedName
// resolvers. Reuses bnWorld / bnPlace / bnActor (move_to_byname_test.go). The
// end-to-end walk-issuing + precedence wiring is covered in
// move_to_remembered_e2e_test.go; this drives the unexported pieces directly.

// bnBareObject seeds a bare object with NO refresh row — a gather patch or décor
// (the live object resolver skips these; the memory resolver must not).
func bnBareObject(w *World, id VillageObjectID, name string, tilesEast int) {
	w.VillageObjects[id] = &VillageObject{
		ID: id, AssetID: "a", DisplayName: name,
		Pos: WorldPos{X: float64(tilesEast) * TileSize, Y: 0},
	}
}

// --- CollectRememberedPlaces ------------------------------------------

func TestCollectRememberedPlaces_SplitsByKindSortedAndDeduped(t *testing.T) {
	known := map[PlaceRef]*KnownPlace{
		"s2":    {Ref: "s2", Kind: PlaceKindStructure},
		"s1":    {Ref: "s1", Kind: PlaceKindStructure},
		"o2":    {Ref: "o2", Kind: PlaceKindObject},
		"o1":    {Ref: "o1", Kind: PlaceKindObject},
		"bad":   {Ref: "bad", Kind: PlaceKind("???")}, // unrecognized kind → dropped
		"":      {Ref: "", Kind: PlaceKindObject},     // empty ref → dropped
		"nilkp": nil,                                  // nil entry → dropped
	}
	got := CollectRememberedPlaces(known)
	wantS := []StructureID{"s1", "s2"}
	wantO := []VillageObjectID{"o1", "o2"}
	if !reflect.DeepEqual(got.StructureIDs, wantS) {
		t.Errorf("StructureIDs = %v, want %v (sorted, kind-split, junk dropped)", got.StructureIDs, wantS)
	}
	if !reflect.DeepEqual(got.ObjectIDs, wantO) {
		t.Errorf("ObjectIDs = %v, want %v (sorted, kind-split, junk dropped)", got.ObjectIDs, wantO)
	}
}

func TestCollectRememberedPlaces_EmptyAndNilYieldNilSlices(t *testing.T) {
	for _, in := range []map[PlaceRef]*KnownPlace{nil, {}} {
		got := CollectRememberedPlaces(in)
		if got.StructureIDs != nil || got.ObjectIDs != nil {
			t.Errorf("empty/nil map must yield nil slices, got %+v", got)
		}
	}
}

func TestCollectRememberedPlaces_OneKindOnlyNilsTheOther(t *testing.T) {
	known := map[PlaceRef]*KnownPlace{"o1": {Ref: "o1", Kind: PlaceKindObject}}
	got := CollectRememberedPlaces(known)
	if got.StructureIDs != nil {
		t.Errorf("no structures → nil StructureIDs, got %v", got.StructureIDs)
	}
	if len(got.ObjectIDs) != 1 || got.ObjectIDs[0] != "o1" {
		t.Errorf("ObjectIDs = %v, want [o1]", got.ObjectIDs)
	}
}

// --- resolveStructureByRememberedName ---------------------------------

func TestResolveStructureByRememberedName_MatchAtAnyDistance(t *testing.T) {
	w := bnWorld(3)
	bnPlace(w, "tavern", "The Tavern", 50) // far beyond radius 3, NOT an anchor
	// The live resolver would miss it (not in scene radius, not an anchor); the
	// memory resolver finds it because it is in the remembered set, at any distance.
	got, ok := resolveStructureByRememberedName(w, bnActor("", ""), "the tavern", []StructureID{"tavern"})
	if !ok || got != "tavern" {
		t.Fatalf("a remembered structure must resolve at any distance, got %q ok=%v", got, ok)
	}
}

func TestResolveStructureByRememberedName_OnlyConsidersThreadedSet(t *testing.T) {
	w := bnWorld(3)
	bnPlace(w, "tavern", "The Tavern", 50) // present + named in the world...
	// ...but NOT in the remembered slice → must NOT resolve (no omniscience leak).
	if got, ok := resolveStructureByRememberedName(w, bnActor("", ""), "the tavern", nil); ok {
		t.Fatalf("a place not in the remembered set must not resolve, got %q", got)
	}
}

func TestResolveStructureByRememberedName_RemovedStructureSkipped(t *testing.T) {
	w := bnWorld(3)
	// The remembered id has no live structure (since removed) → liveness skip.
	if got, ok := resolveStructureByRememberedName(w, bnActor("", ""), "the tavern", []StructureID{"tavern"}); ok {
		t.Fatalf("a remembered-but-removed structure must not resolve, got %q", got)
	}
}

func TestResolveStructureByRememberedName_NoPlacementSkipped(t *testing.T) {
	w := bnWorld(3)
	w.Structures["ghost"] = &Structure{ID: "ghost", DisplayName: "The Tavern"} // no placement
	if got, ok := resolveStructureByRememberedName(w, bnActor("", ""), "the tavern", []StructureID{"ghost"}); ok {
		t.Fatalf("a remembered structure with no placement must not resolve, got %q", got)
	}
}

func TestResolveStructureByRememberedName_NearestWinsAndTieBreak(t *testing.T) {
	w := bnWorld(3)
	bnPlace(w, "tav_far", "The Tavern", 9)
	bnPlace(w, "tav_near", "The Tavern", 2)
	// Nearest wins regardless of the remembered slice's order.
	for _, order := range [][]StructureID{{"tav_far", "tav_near"}, {"tav_near", "tav_far"}} {
		got, ok := resolveStructureByRememberedName(w, bnActor("", ""), "the tavern", order)
		if !ok || got != "tav_near" {
			t.Fatalf("nearest must win regardless of order %v, got %q ok=%v", order, got, ok)
		}
	}
	// Equal distance → lowest id, stable across repeats.
	bnPlace(w, "inn_bbb", "Inn", 4)
	bnPlace(w, "inn_aaa", "Inn", 4)
	for i := 0; i < 25; i++ {
		got, ok := resolveStructureByRememberedName(w, bnActor("", ""), "inn", []StructureID{"inn_bbb", "inn_aaa"})
		if !ok || got != "inn_aaa" {
			t.Fatalf("equal-distance tie must break to the lowest id, got %q", got)
		}
	}
}

// --- resolveObjectByRememberedName ------------------------------------

func TestResolveObjectByRememberedName_GatherPatchResolves(t *testing.T) {
	w := bnWorld(3)
	// A bare gather patch with NO refresh row — NOT an objectIsRefreshSource. The
	// live object resolver would skip it; the memory resolver must NOT (the
	// affordance that earned the memory is the warrant; liveness is just "exists").
	bnBareObject(w, "berry_patch", "Raspberry Patch", 40)
	got, ok := resolveObjectByRememberedName(w, bnActor("", ""), "raspberry patch", []VillageObjectID{"berry_patch"})
	if !ok || got != "berry_patch" {
		t.Fatalf("a remembered non-refresh gather patch must resolve, got %q ok=%v", got, ok)
	}
}

func TestResolveObjectByRememberedName_RemovedObjectSkipped(t *testing.T) {
	w := bnWorld(3)
	if got, ok := resolveObjectByRememberedName(w, bnActor("", ""), "raspberry patch", []VillageObjectID{"berry_patch"}); ok {
		t.Fatalf("a remembered-but-removed object must not resolve, got %q", got)
	}
}

func TestResolveObjectByRememberedName_ExcludesStructureBacked(t *testing.T) {
	w := bnWorld(3)
	bnPlace(w, "springhouse", "Spring", 5)                 // structure + placement (structure-backed)
	w.VillageObjects["springhouse"].DisplayName = "Spring" // also name the placement, so the ONLY reason to skip is structure-backing
	if got, ok := resolveObjectByRememberedName(w, bnActor("", ""), "spring", []VillageObjectID{"springhouse"}); ok {
		t.Fatalf("a structure-backed id must route via the structure path, not resolve as an object, got %q", got)
	}
}

func TestResolveObjectByRememberedName_OnlyConsidersThreadedSet(t *testing.T) {
	w := bnWorld(3)
	bnBareObject(w, "berry_patch", "Raspberry Patch", 2)
	if got, ok := resolveObjectByRememberedName(w, bnActor("", ""), "raspberry patch", nil); ok {
		t.Fatalf("an object not in the remembered set must not resolve, got %q", got)
	}
}

func TestResolveObjectByRememberedName_NearestWinsAndTieBreak(t *testing.T) {
	w := bnWorld(3)
	bnBareObject(w, "patch_far", "Berry Patch", 9)
	bnBareObject(w, "patch_near", "Berry Patch", 2)
	for _, order := range [][]VillageObjectID{{"patch_far", "patch_near"}, {"patch_near", "patch_far"}} {
		got, ok := resolveObjectByRememberedName(w, bnActor("", ""), "berry patch", order)
		if !ok || got != "patch_near" {
			t.Fatalf("nearest must win regardless of order %v, got %q ok=%v", order, got, ok)
		}
	}
	bnBareObject(w, "grove_bbb", "Grove", 4)
	bnBareObject(w, "grove_aaa", "Grove", 4)
	for i := 0; i < 25; i++ {
		got, ok := resolveObjectByRememberedName(w, bnActor("", ""), "grove", []VillageObjectID{"grove_bbb", "grove_aaa"})
		if !ok || got != "grove_aaa" {
			t.Fatalf("equal-distance tie must break to the lowest id, got %q", got)
		}
	}
}
