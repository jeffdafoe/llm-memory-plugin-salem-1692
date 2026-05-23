package sim

import "testing"

// TestItemHasCapability_NilSafe — the world-level capability helper must
// tolerate a kind that is absent from the catalog (returns false, no
// nil-pointer panic). ZBBS-HOME-296.
func TestItemHasCapability_NilSafe(t *testing.T) {
	w := &World{ItemKinds: map[ItemKind]*ItemKindDef{
		"nights_stay": {Name: "nights_stay", Capabilities: []string{"service", "lodging"}},
		"stew":        {Name: "stew"},
	}}

	if !itemHasCapability(w, "nights_stay", "service") {
		t.Error("nights_stay should carry the service capability")
	}
	if !itemHasCapability(w, "nights_stay", "lodging") {
		t.Error("nights_stay should carry the lodging capability")
	}
	if itemHasCapability(w, "stew", "service") {
		t.Error("stew should not carry the service capability")
	}
	if itemHasCapability(w, "ghost_item", "service") {
		t.Error("a kind absent from the catalog must report no capability (and not panic)")
	}
}
