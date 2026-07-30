package sim

// actor_at_home.go — LLM-571. The one "is this actor at home right now" test.
//
// Extracted from shift_duty.go's LLM-451 arrival stagger when a second caller
// (the seek-work backstop's at-ease arm) needed the same question answered. Kept
// as one function on purpose: salem has repeatedly grown several near-identical
// "is he at this place" predicates that drifted apart, and this one has a subtle
// arm — an actor with NO home is never "at home", which is what keeps homeless
// NPCs and lodgers out of both callers.
//
// "At home" is per-tick current position, not a claim about where the actor
// started the day: an actor who wanders back home mid-window becomes at-home
// again, which is correct for both callers.
func actorIsAtHome(a *Actor) bool {
	if a == nil {
		return false
	}
	return a.HomeStructureID != "" && a.InsideStructureID == a.HomeStructureID
}
