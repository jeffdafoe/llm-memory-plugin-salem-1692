package sim

import "testing"

// Internal tests for the ZBBS-WORK-412 discovery mint helpers (unexported).

func TestNormalizeDiscoveredKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a pinch of Dried Chamomile", "dried_chamomile"},
		{"Dried Chamomile", "dried_chamomile"},
		{"  Lavender sprigs ", "lavender_sprigs"},
		{"a bowl of stew", "stew"},
		{"the Moonbeam", "moonbeam"},
		{"some   spaced   thing", "spaced_thing"},
		{"chamomile", "chamomile"},
		{"!?!", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := normalizeDiscoveredKey(c.in); got != c.want {
			t.Errorf("normalizeDiscoveredKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMintDiscoveredKind(t *testing.T) {
	w := &World{ItemKinds: map[ItemKind]*ItemKindDef{
		"stew": {Name: "stew", DisplayLabel: "Stew", Category: ItemCategoryFood},
	}}
	orig := w.ItemKinds // capture for the copy-on-write check

	kind, ok := mintDiscoveredKind(w, "a pinch of dried chamomile")
	if !ok || kind != "dried_chamomile" {
		t.Fatalf("mint = (%q,%v), want (dried_chamomile,true)", kind, ok)
	}
	def := w.ItemKinds["dried_chamomile"]
	if def == nil || def.Category != ItemCategoryUnknown || def.DisplayLabel != "dried chamomile" {
		t.Errorf("minted def = %+v, want category unknown / label 'dried chamomile'", def)
	}
	if len(def.Satisfies) != 0 || len(def.Capabilities) != 0 {
		t.Errorf("minted def must carry no satisfies/capabilities: %+v", def)
	}

	// Copy-on-write: the original map is untouched (an already-published
	// snapshot aliasing it stays immutable); w.ItemKinds points at a new map.
	if _, leaked := orig["dried_chamomile"]; leaked {
		t.Error("mint mutated the original ItemKinds map in place (violates IMMUTABILITY CONTRACT)")
	}
	if len(orig) != 1 {
		t.Errorf("original map grew to %d, want 1 (copy-on-write)", len(orig))
	}

	// Dedup: re-minting the same normalized key returns the existing kind, and
	// "a bowl of stew" collapses onto the real "stew" without overwriting it.
	if k, _ := mintDiscoveredKind(w, "Dried Chamomile"); k != "dried_chamomile" {
		t.Errorf("re-mint = %q, want dried_chamomile (dedup)", k)
	}
	if k, ok := mintDiscoveredKind(w, "a bowl of stew"); !ok || k != "stew" {
		t.Errorf("mint 'a bowl of stew' = (%q,%v), want (stew,true)", k, ok)
	}
	if w.ItemKinds["stew"].Category != ItemCategoryFood {
		t.Error("dedup onto 'stew' must not overwrite the real food kind")
	}

	// All-filler / punctuation normalizes to empty → no mint.
	if k, ok := mintDiscoveredKind(w, "!?!"); ok || k != "" {
		t.Errorf("mint '!?!' = (%q,%v), want ('',false)", k, ok)
	}
}
