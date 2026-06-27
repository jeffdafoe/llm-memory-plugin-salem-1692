package sim

import (
	"reflect"
	"testing"
)

// move_to_remembered_test.go — LLM-78 / LLM-142. White-box (package sim) coverage
// of the memory-backed OBJECT name-resolution fallback: CollectRememberedPlaces
// (object-only projection) and resolveObjectByRememberedName. Structures are
// common-knowledge geography (LLM-142) and resolve directly by name, so there is
// no remembered-structure resolver. Reuses bnWorld / bnPlace / bnActor
// (move_to_byname_test.go). The end-to-end walk-issuing + precedence wiring is in
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

func TestCollectRememberedPlaces_CollectsObjectsSortedDropsStructures(t *testing.T) {
	known := map[PlaceRef]*KnownPlace{
		"s2":    {Ref: "s2", Kind: PlaceKindStructure}, // structures dropped — geography is common knowledge (LLM-142)
		"s1":    {Ref: "s1", Kind: PlaceKindStructure},
		"o2":    {Ref: "o2", Kind: PlaceKindObject},
		"o1":    {Ref: "o1", Kind: PlaceKindObject},
		"bad":   {Ref: "bad", Kind: PlaceKind("???")}, // unrecognized kind → dropped
		"":      {Ref: "", Kind: PlaceKindObject},     // empty ref → dropped
		"nilkp": nil,                                  // nil entry → dropped
	}
	got := CollectRememberedPlaces(known)
	want := []VillageObjectID{"o1", "o2"}
	if !reflect.DeepEqual(got.ObjectIDs, want) {
		t.Errorf("ObjectIDs = %v, want %v (sorted, objects only, structures + junk dropped)", got.ObjectIDs, want)
	}
}

func TestCollectRememberedPlaces_EmptyAndNilYieldNilSlice(t *testing.T) {
	for _, in := range []map[PlaceRef]*KnownPlace{nil, {}} {
		got := CollectRememberedPlaces(in)
		if got.ObjectIDs != nil {
			t.Errorf("empty/nil map must yield a nil ObjectIDs slice, got %+v", got)
		}
	}
}

func TestCollectRememberedPlaces_StructureOnlyYieldsNil(t *testing.T) {
	known := map[PlaceRef]*KnownPlace{"s1": {Ref: "s1", Kind: PlaceKindStructure}}
	got := CollectRememberedPlaces(known)
	if got.ObjectIDs != nil {
		t.Errorf("a structure-only known set must yield nil ObjectIDs (structures resolve by village name), got %v", got.ObjectIDs)
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
