package sim

import "testing"

// move_to_byname_test.go — ZBBS-HOME-356 resolveStructureByPerceivableName.
// White-box (package sim) so it can drive the unexported resolver directly with
// controlled positions. World (0,0) is tile (PadX,PadY); a placement at world
// X = n*TileSize sits n tiles east of it, so the actor at the pad origin is
// exactly n tiles from that structure.

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
	bnPlace(w, "tavern", "The Tavern", 2) // within radius 3
	got, ok := resolveStructureByPerceivableName(w, bnActor("", ""), "the tavern", nil)
	if !ok || got != "tavern" {
		t.Fatalf("case-insensitive in-range match: got %q ok=%v, want tavern", got, ok)
	}
}

// The live ZBBS-WORK-417 case: the structure's canonical name has NO article
// ("Tavern") and the model emits "the tavern". The existing InRangeMatch test
// only covers article-on-both-sides ("The Tavern" <- "the tavern"), which the
// old exact case-insensitive match already passed; this is the asymmetric case
// that used to bounce.
func TestResolveStructureByName_LeadingArticleOnQuery(t *testing.T) {
	w := bnWorld(3)
	bnPlace(w, "tavern", "Tavern", 2) // canonical name has no leading article
	got, ok := resolveStructureByPerceivableName(w, bnActor("", ""), "the tavern", nil)
	if !ok || got != "tavern" {
		t.Fatalf("a leading article on the query must still resolve, got %q ok=%v, want tavern", got, ok)
	}
}

func TestResolveStructureByName_OutOfRangeMiss(t *testing.T) {
	w := bnWorld(3)
	bnPlace(w, "tavern", "The Tavern", 9) // beyond radius 3, NOT an anchor
	if got, ok := resolveStructureByPerceivableName(w, bnActor("", ""), "the tavern", nil); ok {
		t.Fatalf("a far non-anchor structure must not resolve by name, got %q", got)
	}
}

func TestResolveStructureByName_AnchorAnyDistance(t *testing.T) {
	w := bnWorld(3)
	bnPlace(w, "tavern", "The Tavern", 50) // far away...
	// ...but it's the actor's work anchor → always perceivable by name.
	got, ok := resolveStructureByPerceivableName(w, bnActor("", "tavern"), "the tavern", nil)
	if !ok || got != "tavern" {
		t.Fatalf("a far anchor must resolve by name, got %q ok=%v", got, ok)
	}
}

func TestResolveStructureByName_NearestWinsOnDuplicate(t *testing.T) {
	w := bnWorld(5)
	bnPlace(w, "well_far", "The Well", 4)
	bnPlace(w, "well_near", "The Well", 1)
	got, ok := resolveStructureByPerceivableName(w, bnActor("", ""), "the well", nil)
	if !ok || got != "well_near" {
		t.Fatalf("duplicate names must resolve to the NEAREST, got %q ok=%v", got, ok)
	}
}

func TestResolveStructureByName_NoMatch(t *testing.T) {
	w := bnWorld(5)
	bnPlace(w, "tavern", "The Tavern", 1)
	if got, ok := resolveStructureByPerceivableName(w, bnActor("", ""), "the smithy", nil); ok {
		t.Fatalf("a name no structure has must not resolve, got %q", got)
	}
}

func TestResolveStructureByName_TieBreaksByID(t *testing.T) {
	w := bnWorld(5)
	bnPlace(w, "well_bbb", "The Well", 2)
	bnPlace(w, "well_aaa", "The Well", 2) // same distance
	for i := 0; i < 25; i++ {
		got, ok := resolveStructureByPerceivableName(w, bnActor("", ""), "the well", nil)
		if !ok || got != "well_aaa" {
			t.Fatalf("equal-distance tie must break to the lowest id, got %q", got)
		}
	}
}
