package sim

import "testing"

// LLM-113: an item kind resolves from any of its four naming forms — key,
// display label, singular phrase, plural phrase — case-insensitively and with a
// leading article tolerated. Covers the count-noun case (raspberry/raspberries)
// and the mass-noun measure-phrase case (tankard of ale).
func TestResolveItemKind_AllForms(t *testing.T) {
	w := &World{
		ItemKinds: map[ItemKind]*ItemKindDef{
			"raspberries": {
				Name:                 "raspberries",
				DisplayLabel:         "Raspberries",
				DisplayLabelSingular: "raspberry",
				DisplayLabelPlural:   "raspberries",
				Category:             ItemCategoryFood,
			},
			"ale": {
				Name:                 "ale",
				DisplayLabel:         "Ale",
				DisplayLabelSingular: "tankard of ale",
				DisplayLabelPlural:   "tankards of ale",
				Category:             ItemCategoryDrink,
			},
		},
	}

	cases := []struct {
		in   string
		want ItemKind
		ok   bool
	}{
		{"raspberries", "raspberries", true},  // key
		{"Raspberries", "raspberries", true},  // display label (case-insensitive)
		{"raspberry", "raspberries", true},    // singular phrase
		{"a raspberry", "raspberries", true},  // singular + leading article
		{"RASPBERRY", "raspberries", true},    // singular, upper
		{"ale", "ale", true},                  // key
		{"Ale", "ale", true},                  // display label
		{"tankard of ale", "ale", true},       // singular measure phrase
		{"a tankard of ale", "ale", true},     // singular phrase + article
		{"tankards of ale", "ale", true},      // plural measure phrase
		{"  ale  ", "ale", true},              // surrounding whitespace
		{"moonbeam", "", false},               // genuine miss
		{"", "", false},                       // empty
	}
	for _, tc := range cases {
		got, ok := resolveItemKind(w, tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("resolveItemKind(%q) = (%q, %v); want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
