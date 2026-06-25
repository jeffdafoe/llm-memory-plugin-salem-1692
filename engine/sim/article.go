package sim

import "strings"

// WithDefiniteArticle returns name with a lowercase definite article ("the ")
// prepended, unless the name should carry no article — it already leads with one
// ("the"/"a"/"an"), or its first word is a possessive ("Hannah's Inn",
// "Travelers' Rest"). So a common-noun place reads "the Blueberry Bush" while
// "the Village Well", "a shiny rock", and "Hannah's Inn" are left untouched.
// Mid-clause form; a sentence-start caller capitalizes. Empty in, empty out.
// Leading-article detection reuses stripLeadingArticle so naming a place (input
// resolution) and reading one (display) agree on what counts as an article —
// including the word-boundary rule that leaves "Theater"/"Anvil" alone.
func WithDefiniteArticle(name string) string {
	if name == "" {
		return ""
	}
	// Already led by the/a/an — leave it.
	if stripLeadingArticle(name) != name {
		return name
	}
	// Possessive proper name ("Hannah's Inn", "Travelers' Rest") — the first
	// word is a possessive, so it takes no article. A common-noun place name
	// never opens with a possessive, so this only spares genuine proper names.
	if firstWordIsPossessive(name) {
		return name
	}
	return "the " + name
}

// firstWordIsPossessive reports whether name's first whitespace-delimited word
// ends in a possessive marker: a trailing apostrophe optionally followed by "s"
// — singular "Hannah's" or plural "Travelers'". Straight and typographic
// apostrophes both count.
func firstWordIsPossessive(name string) bool {
	first := name
	if i := strings.IndexByte(name, ' '); i >= 0 {
		first = name[:i]
	}
	for _, suffix := range []string{"'", "'s", "’", "’s"} {
		if strings.HasSuffix(first, suffix) {
			return true
		}
	}
	return false
}
