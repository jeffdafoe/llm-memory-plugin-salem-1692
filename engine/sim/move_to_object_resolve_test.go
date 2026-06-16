package sim

import "testing"

// move_to_object_resolve_test.go — ZBBS-HOME-359 resolveObjectByPerceivableName
// + objectIsRefreshSource. White-box (package sim) so it can drive the
// unexported resolver directly with controlled positions, mirroring
// move_to_byname_test.go. Reuses bnWorld / bnActor from that file.

// bnObject seeds a BARE refresh-bearing village object (no Structure row) at
// world X = tilesEast*TileSize with the given need-refresh attribute/amount.
func bnObject(w *World, id VillageObjectID, name string, tilesEast int, attr NeedKey, amount int) {
	w.VillageObjects[id] = &VillageObject{
		ID: id, AssetID: "a", DisplayName: name,
		Pos:       WorldPos{X: float64(tilesEast) * TileSize, Y: 0},
		Refreshes: []*ObjectRefresh{{Attribute: attr, Amount: amount}},
	}
}

func TestResolveObjectByName_InRangeRefreshSource(t *testing.T) {
	w := bnWorld(3)
	bnObject(w, "well", "Village Well", 2, "thirst", -8) // within radius 3
	got, ok := resolveObjectByPerceivableName(w, bnActor("", ""), "village well", nil)
	if !ok || got != "well" {
		t.Fatalf("case-insensitive in-range refresh source: got %q ok=%v, want well", got, ok)
	}
}

// ZBBS-WORK-417: a free source whose canonical name has no article ("Well")
// resolves from "the well" — the asymmetric article case on the object path.
func TestResolveObjectByName_LeadingArticleOnQuery(t *testing.T) {
	w := bnWorld(3)
	bnObject(w, "well", "Well", 2, "thirst", -8) // canonical name has no leading article
	got, ok := resolveObjectByPerceivableName(w, bnActor("", ""), "the well", nil)
	if !ok || got != "well" {
		t.Fatalf("a leading article on the query must still resolve, got %q ok=%v, want well", got, ok)
	}
}

func TestResolveObjectByName_OutOfRangeMiss(t *testing.T) {
	w := bnWorld(3)
	bnObject(w, "well", "Village Well", 9, "thirst", -8) // beyond radius 3
	if got, ok := resolveObjectByPerceivableName(w, bnActor("", ""), "village well", nil); ok {
		t.Fatalf("a far free source must not resolve by name, got %q", got)
	}
}

// A bare object with no refresh row (décor — a lamp, a sign) is not a walkable
// destination: resolveObjectByPerceivableName must skip it so move_to never
// routes an NPC to a lamp.
func TestResolveObjectByName_SkipsNonRefreshDecor(t *testing.T) {
	w := bnWorld(5)
	w.VillageObjects["lamp"] = &VillageObject{
		ID: "lamp", AssetID: "a", DisplayName: "Lamp Post", Pos: WorldPos{X: TileSize, Y: 0},
	}
	if got, ok := resolveObjectByPerceivableName(w, bnActor("", ""), "lamp post", nil); ok {
		t.Fatalf("a non-refresh décor object must not resolve by name, got %q", got)
	}
}

// A name shared with a STRUCTURE resolves through the structure path, not here —
// so a structure-backed placement is excluded even when it carries a refresh.
func TestResolveObjectByName_ExcludesStructureBacked(t *testing.T) {
	w := bnWorld(5)
	bnPlace(w, "springhouse", "Spring", 2) // structure + placement
	// Give that placement a refresh row — it must STILL be excluded here.
	w.VillageObjects["springhouse"].Refreshes = []*ObjectRefresh{{Attribute: "thirst", Amount: -8}}
	if got, ok := resolveObjectByPerceivableName(w, bnActor("", ""), "spring", nil); ok {
		t.Fatalf("a structure-backed placement must resolve via the structure path, not as an object, got %q", got)
	}
}

func TestResolveObjectByName_NearestWinsOnDuplicate(t *testing.T) {
	w := bnWorld(5)
	bnObject(w, "well_far", "The Well", 4, "thirst", -8)
	bnObject(w, "well_near", "The Well", 1, "thirst", -8)
	got, ok := resolveObjectByPerceivableName(w, bnActor("", ""), "the well", nil)
	if !ok || got != "well_near" {
		t.Fatalf("duplicate names must resolve to the NEAREST, got %q ok=%v", got, ok)
	}
}

func TestResolveObjectByName_TieBreaksByID(t *testing.T) {
	w := bnWorld(5)
	bnObject(w, "well_bbb", "The Well", 2, "thirst", -8)
	bnObject(w, "well_aaa", "The Well", 2, "thirst", -8) // same distance
	for i := 0; i < 25; i++ {
		got, ok := resolveObjectByPerceivableName(w, bnActor("", ""), "the well", nil)
		if !ok || got != "well_aaa" {
			t.Fatalf("equal-distance tie must break to the lowest id, got %q", got)
		}
	}
}

func TestObjectIsRefreshSource(t *testing.T) {
	cases := []struct {
		name string
		obj  *VillageObject
		want bool
	}{
		{"nil object", nil, false},
		{"no refreshes", &VillageObject{ID: "x"}, false},
		{"thirst refresh", &VillageObject{ID: "x", Refreshes: []*ObjectRefresh{{Attribute: "thirst", Amount: -8}}}, true},
		{"worsening refresh", &VillageObject{ID: "x", Refreshes: []*ObjectRefresh{{Attribute: "thirst", Amount: 8}}}, false},
		{"depleted only", func() *VillageObject {
			zero := 0
			return &VillageObject{ID: "x", Refreshes: []*ObjectRefresh{{Attribute: "thirst", Amount: -8, AvailableQuantity: &zero}}}
		}(), false},
	}
	for _, tc := range cases {
		if got := objectIsRefreshSource(tc.obj); got != tc.want {
			t.Errorf("%s: objectIsRefreshSource = %v, want %v", tc.name, got, tc.want)
		}
	}
}
