package sim

import "testing"

// move_to_shown_test.go — ZBBS-HOME-389, OBJECT half. A free source the tick's
// perception surfaced (passed in `shown`) resolves by NAME at any distance, while
// a far source NOT shown still misses — objects stay discovered (a wild bush is
// not common knowledge the way a building is). The structure half is gone:
// village geography is common knowledge, so every structure resolves by name
// (LLM-142, see move_to_byname_test.go).

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
