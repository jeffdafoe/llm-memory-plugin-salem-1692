package sim

// WithDefiniteArticle returns name with a lowercase definite article ("the ")
// prepended, unless it already leads with an article ("the"/"a"/"an") — so a
// common-noun place reads "the Blueberry Bush" while an already-articled or
// indefinite name ("the Village Well", "a shiny rock") is left untouched.
// Mid-clause form; a sentence-start caller capitalizes. Empty in, empty out.
// Detection reuses stripLeadingArticle so naming a place (input resolution) and
// reading one (display) agree on what counts as an article — including the
// word-boundary rule that leaves "Theater"/"Anvil" alone.
func WithDefiniteArticle(name string) string {
	if name == "" {
		return ""
	}
	// Already led by the/a/an — leave it (covers baked-in "the Village Well",
	// proper "The Prancing Pony", and indefinite "a shiny rock").
	if stripLeadingArticle(name) != name {
		return name
	}
	return "the " + name
}
