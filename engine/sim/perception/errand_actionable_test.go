package perception

import (
	"strings"
	"testing"
)

// LLM-620. The duty-steer suppression is level-triggered on an errand cue's
// PRESENCE, and both of these cues render on a stock threshold rather than on
// there being anything to do about it — so each can name a want the actor cannot
// take one step toward, and hold shift duty suppressed for as long as the want
// lasts. Worse, the same signal flips the at-post stabilizer to a leave-permission,
// so both ends of the shift come unpinned together and the actor oscillates between
// the only two places the prose names. Live: Moses James (52 wheat bushes, none
// ripe) ran James Farm<->James Residence in 6-to-17-second laps.
//
// These pin the predicates that narrow "the cue rendered" to "the cue is
// actionable". The golden pair covers the rendered consequence; this covers the
// branches goldens cannot reach cheaply — conserve, all-blocked, and the wild-source
// arm.

func TestForageViewActionable(t *testing.T) {
	if (*ForageView)(nil).Actionable() {
		t.Error("a nil forage view is not an errand")
	}
	if (&ForageView{}).Actionable() {
		t.Error("an empty forage view is not an errand")
	}
	// The live Moses shape: bushes he owns and remembers, nothing ripe on any of
	// them. The cue still renders ("none ripe yet — they will regrow"), but there is
	// no step to take, so it must not hold shift duty off.
	unripe := &ForageView{Items: []ForageItemView{
		{ItemLabel: "Wheat", BushCount: 52, RipeUnits: 0},
		{ItemLabel: "Carrots", BushCount: 4, RipeUnits: 0},
	}}
	if unripe.Actionable() {
		t.Error("bushes with nothing ripe are a standing want, not an errand in progress")
	}
	if !(&ForageView{Items: []ForageItemView{
		{ItemLabel: "Wheat", BushCount: 52, RipeUnits: 0},
		{ItemLabel: "Carrots", BushCount: 4, RipeUnits: 3},
	}}).Actionable() {
		t.Error("one ripe item among several is still an errand — he can go pick it")
	}
	// RipeUnits counts a bush the grower already stands on (buildForage accumulates
	// before the at-pin skip), so the LLM-617 nowhere-to-walk case stays an errand:
	// gather is right there even though move_to has no target.
	if !(&ForageView{Items: []ForageItemView{
		{ItemLabel: "Wheat", BushCount: 1, RipeUnits: 9, AtRipeBush: true},
	}}).Actionable() {
		t.Error("standing on the only ripe bush is an errand in progress — gather is callable here")
	}
	// The ranged-forager arm (LLM-253) counts too: a wild source in range is a walk
	// he can make even with no owned bush ripe.
	if !(&ForageView{
		Items:       []ForageItemView{{ItemLabel: "Wheat", BushCount: 52, RipeUnits: 0}},
		WildSources: []WildForageItemView{{ItemLabel: "Sage", RipeUnits: 10}},
	}).Actionable() {
		t.Error("a ripe wild source in range is an errand even with no owned bush ripe")
	}
}

func TestRestockingViewActionable(t *testing.T) {
	if (*RestockingView)(nil).Actionable() {
		t.Error("a nil restocking view is not an errand")
	}
	if (&RestockingView{}).Actionable() {
		t.Error("an empty restocking view is not an errand")
	}
	walkTo := func() *RestockingView {
		return &RestockingView{Items: []RestockItemView{
			{ItemLabel: "Wheat", Vendors: []RestockVendor{{StructureLabel: "James Farm", StructureID: "james_farm"}}},
		}}
	}
	if !walkTo().Actionable() {
		t.Error("a walk-to supplier is an errand — that walk is what the suppression protects")
	}
	// Conserve replaces the buy directory with a hold-off-buying steer (LLM-294):
	// render emits no imperative and no destination, so there is nothing in progress
	// to protect. Left counting, a conserving keeper was held off his post by a cue
	// telling him not to buy.
	conserving := walkTo()
	conserving.Conserve = true
	if conserving.Actionable() {
		t.Error("a hold-off-buying steer is the opposite of an errand in progress")
	}
	// All-blocked renders reasons with deliberately no destination ids (LLM-406) —
	// the keeper is told what he needs and why he cannot have it. Also not an errand.
	blocked := &RestockingView{Items: []RestockItemView{
		{ItemLabel: "Wheat", Blocked: []RestockBlockedSupplier{{}}},
	}}
	if blocked.Actionable() {
		t.Error("an item nobody will sell him is a want with no step to take")
	}
	// A seller standing with him names no walk, but the transaction IS the errand
	// and it is happening here — yanking him to his post would abandon it mid-deal.
	if !(&RestockingView{Items: []RestockItemView{
		{ItemLabel: "sage", CoPresentSeller: "Elizabeth Ellis"},
	}}).Actionable() {
		t.Error("a co-present seller is an errand in progress even with no walk to make")
	}
}

// TestGoldensAwayFromPostStatusCarriesNoDestination is the cross-scenario invariant
// for the LLM-620 status arm: it states a fact and must never become the yank its
// caller just chose to defer. The structure_id is the token the weak model echoes
// into move_to (HOME-349), so a destination on this line would restore the pull the
// suppression exists to remove — silently, since the wording would still read as a
// statement.
//
// Scoped to the steer's own line rather than the whole prompt: the anchors line
// legitimately carries both ids one paragraph above, and every errand cue carries
// its own destination. The to-work arm is told apart by its imperative, and is
// EXPECTED to carry an id.
func TestGoldensAwayFromPostStatusCarriesNoDestination(t *testing.T) {
	const yankVerb = "make your way to"
	seen := 0
	for _, sc := range perceptionScenarios {
		for _, line := range strings.Split(renderScenario(sc), "\n") {
			if !strings.Contains(line, dutySteerToPostMarker) || strings.Contains(line, yankVerb) {
				continue
			}
			seen++
			if strings.Contains(line, "(destination:") {
				t.Errorf("scenario %q: the away-from-post STATUS line carries a move_to destination, which makes it a to-work yank by the back door (LLM-620):\n\t%s", sc.name, line)
			}
		}
	}
	if seen == 0 {
		t.Fatalf("no scenario renders the away-from-post status line, so this invariant matches nothing — the arm or its wording changed")
	}
}
