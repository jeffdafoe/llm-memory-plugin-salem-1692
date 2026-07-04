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

// Possessive returns name in the possessive case for a mid-sentence label —
// "Ezekiel Crane" → "Ezekiel Crane's", "John Ellis" → "John Ellis'". A name
// ending in "s" takes a bare apostrophe, the form the rest of the world uses
// (WithDefiniteArticle already reads "James' Place"/"Travelers' Rest" as
// possessives), so forming and reading a possessive agree; any other name takes
// "'s". A name already in the possessive (a trailing apostrophe, straight or
// typographic, optionally followed by "s") is returned unchanged so it never
// doubles. Empty in, empty out.
func Possessive(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	// Already possessive — leave it (the same suffix set firstWordIsPossessive
	// matches, applied to the whole label rather than only the first word).
	for _, suffix := range []string{"'s", "'", "’s", "’"} {
		if strings.HasSuffix(name, suffix) {
			return name
		}
	}
	if strings.HasSuffix(name, "s") || strings.HasSuffix(name, "S") {
		return name + "'"
	}
	return name + "'s"
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
