package sim

import "testing"

// move_to_shown_test.go — ZBBS-HOME-389. A structure/object the tick's
// perception surfaced (passed in `shown`) resolves by NAME at any distance,
// while a far place NOT shown still misses — preserving HOME-356's
// no-omniscience guard (only what was shown this tick resolves).

func TestResolveStructureByName_ShownReachesFarButUnshownMisses(t *testing.T) {
	w := bnWorld(3)
	bnPlace(w, "store", "General Store", 20) // far, NOT an anchor, outside radius 3

	if _, ok := resolveStructureByPerceivableName(w, bnActor("", ""), "general store", nil); ok {
		t.Fatal("a far non-anchor structure must NOT resolve by name when not shown (no omniscience)")
	}
	got, ok := resolveStructureByPerceivableName(w, bnActor("", ""), "general store", []StructureID{"store"})
	if !ok || got != "store" {
		t.Fatalf("a far structure SHOWN this tick must resolve by name, got %q ok=%v", got, ok)
	}
}

func TestResolveStructureByName_ShownNearestWins(t *testing.T) {
	w := bnWorld(3)
	bnPlace(w, "store_far", "General Store", 30)
	bnPlace(w, "store_near", "General Store", 10) // both outside radius 3
	got, ok := resolveStructureByPerceivableName(w, bnActor("", ""), "general store",
		[]StructureID{"store_far", "store_near"})
	if !ok || got != "store_near" {
		t.Fatalf("shown duplicate names must resolve to the NEAREST, got %q ok=%v", got, ok)
	}
}

func TestResolveObjectByName_ShownReachesFarButUnshownMisses(t *testing.T) {
	w := bnWorld(3)
	bnObject(w, "well", "Village Well", 20, "thirst", -8) // far, outside radius 3

	if _, ok := resolveObjectByPerceivableName(w, bnActor("", ""), "village well", nil); ok {
		t.Fatal("a far free source must NOT resolve by name when not shown (no omniscience)")
	}
	got, ok := resolveObjectByPerceivableName(w, bnActor("", ""), "village well", []VillageObjectID{"well"})
	if !ok || got != "well" {
		t.Fatalf("a far free source SHOWN this tick must resolve by name, got %q ok=%v", got, ok)
	}
}
