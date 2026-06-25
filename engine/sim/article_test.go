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
