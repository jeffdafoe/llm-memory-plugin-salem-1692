package sim

import "testing"

// move_to_article_test.go — ZBBS-WORK-417 leading-article tolerance in move_to
// name resolution. placeNameMatches lets an NPC (or PC) say "the Tavern" and
// reach the structure named "Tavern" (and vice-versa), without a leading article
// turning into a silent resolution miss. The whole-word boundary keeps names that
// merely START with an article's letters ("Theater", "Anvil", "Apothecary")
// intact.
func TestPlaceNameMatches(t *testing.T) {
	cases := []struct {
		name    string
		display string
		query   string
		want    bool
	}{
		// The live bug: structure named "Tavern", model emits "the tavern".
		{"article only on query", "Tavern", "the tavern", true},
		// Symmetric: structure named "The Tavern", model emits "tavern".
		{"article only on display", "The Tavern", "tavern", true},
		{"article on both sides", "The Tavern", "the tavern", true},
		{"article on neither side", "Tavern", "tavern", true},
		{"still case-insensitive", "TAVERN", "the Tavern", true},
		{"a-article on query", "Well", "a well", true},
		{"an-article on query", "Orchard", "an orchard", true},
		{"surrounding whitespace trimmed", "  Tavern  ", " the tavern ", true},
		// A place literally named "The" keeps its name (never stripped to empty).
		{"bare 'the' is not stripped to empty", "The", "the", true},

		// Boundary safety: a name that merely STARTS with an article's letters is
		// not mistaken for "article + rest" — the article must be a whole word.
		{"Theater is not 'the' + ater", "Theater", "the ater", false},
		{"Anvil is not 'an' + vil", "Anvil", "an vil", false},
		{"Apothecary is not 'a' + pothecary", "Apothecary", "a pothecary", false},
		// A genuinely different name still misses.
		{"different names miss", "Tavern", "smithy", false},
	}
	for _, tc := range cases {
		if got := placeNameMatches(tc.display, tc.query); got != tc.want {
			t.Errorf("%s: placeNameMatches(%q, %q) = %v, want %v", tc.name, tc.display, tc.query, got, tc.want)
		}
	}
}
