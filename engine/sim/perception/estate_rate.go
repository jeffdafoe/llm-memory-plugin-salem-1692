package perception

import (
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// estate_rate.go — the constable's side of the estate rate (LLM-652/653; LLM-655
// retired the LLM-557 day's rate this section used to carry). The "## Town rate"
// section, rendered to the constable only, and only when a keeper stands with him.
//
// The keeper has no cue. The engine takes the rate as the constable arrives at the
// business (sim.collectEstateRateAtStop) and writes the payer's ring line — "You paid
// Constable Gideon Marsh 16 coins for the rate on your estate" — and relationship
// fact in the same command, so the payer's scene already says what happened, and
// there is nothing for him to do about it: no tool, no coin to hand over.
//
// The constable does need a line, and this is the only one he has. He is a stateful
// NPC, so buildRelationships is gated off and he holds no stored view of any
// villager; when a keeper presses him for coin back — the LLM-607 shape, eight coins
// of rate refunded one at a time — this sentence is what he answers with. It says
// where his wage comes from, that the rate is taken as he arrives without passing
// through his purse, and that it is not his to return. No imperative and no tool: he
// has nothing to call, and an imperative here would risk instructing a second
// terminal verb alongside speak.
//
// CO-LOCATION-GATED: rendered when a rateable keeper (a resident who owns a business,
// sim.RateableBusinessOf) shares the constable's huddle or shop scope. Off-scene the
// line is a lecture with no audience; the refund conversation it guards against
// happens face to face.

// estateRateCollectorLine is the whole section body. Static on purpose: it states a
// standing fact about the levy, not the state of any one purse — the constable's own
// action ring ("You collected 16 coins from Prudence Ward for the rate on their
// estate") carries what was taken from whom on this visit.
const estateRateCollectorLine = "The town pays your wage out of the estate rate. When you call at a shop on your rounds and the keeper is there, the rate on their estate is taken into the town chest as you arrive — none of it passes through your purse. What is taken is the town's, not yours to hand back."

// buildEstateRateCollector reports whether the subject is a constable with at least
// one rateable keeper co-present. Pure over the snapshot.
func buildEstateRateCollector(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot) bool {
	if snap == nil || actorSnap == nil || !isConstableSnapshot(actorSnap) {
		return false
	}
	sc := buyerCoPresenceScope(snap, actorSnap)
	if sc.huddle == "" && sc.scope == "" {
		return false
	}
	for id, other := range snap.Actors {
		if other == nil || id == actorID || other.DisplayName == "" {
			continue
		}
		if !sc.sellerCoPresent(other) {
			continue
		}
		if sim.RateableBusinessOf(snap.VillageObjects, id) != nil {
			return true
		}
	}
	return false
}

// isConstableSnapshot reports whether an actor snapshot carries the constable
// attribute. AttributeSlugs is the sorted projection of the live Actor.Attributes
// keys, and the engine keys behaviour on presence only — the same test
// sim.ActorIsConstable makes against the live Actor.
func isConstableSnapshot(a *sim.ActorSnapshot) bool {
	if a == nil {
		return false
	}
	for _, slug := range a.AttributeSlugs {
		if slug == sim.AttrConstable {
			return true
		}
	}
	return false
}

// renderEstateRateCollector writes the "## Town rate" section. Content-gated: false
// writes nothing.
func renderEstateRateCollector(b *strings.Builder, collector bool) {
	if !collector {
		return
	}
	b.WriteString("## Town rate\n")
	b.WriteString(estateRateCollectorLine)
	b.WriteString("\n")
}
