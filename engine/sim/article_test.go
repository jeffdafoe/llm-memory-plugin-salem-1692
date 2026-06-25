package sim

import "testing"

func TestWithDefiniteArticle(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bare common noun", "Blueberry Bush", "the Blueberry Bush"},
		{"bare common noun, two words", "General Store", "the General Store"},
		{"already definite, lowercase", "the Village Well", "the Village Well"},
		{"already definite, capitalized proper name", "The Prancing Pony", "The Prancing Pony"},
		{"indefinite a left alone", "a shiny rock", "a shiny rock"},
		{"indefinite an left alone", "an apple cart", "an apple cart"},
		{"the-prefixed word is not an article", "Theater", "the Theater"},
		{"an-prefixed word is not an article", "Anvil", "the Anvil"},
		{"a-prefixed word is not an article", "Apple", "the Apple"},
		{"singular possessive proper name", "Hannah's Inn", "Hannah's Inn"},
		{"singular possessive, another", "John's Tavern", "John's Tavern"},
		{"possessive, apostrophe in owner name", "O'Brien's Inn", "O'Brien's Inn"},
		{"plural possessive, bare apostrophe", "Travelers' Rest", "Travelers' Rest"},
		{"singular name ending in s, bare-apostrophe possessive", "James' Place", "James' Place"},
		{"plural possessive, typographic apostrophe", "Farmers’ Market", "Farmers’ Market"},
		{"singular possessive, typographic apostrophe", "Maria’s Bakery", "Maria’s Bakery"},
		{"non-possessive proper name still gets the", "Thorne Residence", "the Thorne Residence"},
		{"apostrophe but not possessive still gets the", "O'Malley Hall", "the O'Malley Hall"},
		{"trailing s without apostrophe still gets the", "Oats Market", "the Oats Market"},
		{"empty stays empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WithDefiniteArticle(tc.in); got != tc.want {
				t.Errorf("WithDefiniteArticle(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
