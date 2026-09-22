package perception

import (
	"strconv"
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
// It also names the floor and says he never asks (LLM-665). The inverse of the refund
// shape: two minutes after his ring showed a real collection at the Mill, he stood
// at a farm whose owner sat under the floor — where the engine took nothing — and
// demanded "four-and-twenty coins this quarter". The keeper has the pay tool and no
// cue, so she handed it over, into HIS purse, and he did it twice more that round:
// 64 coin gone from three purses, none of it in the chest. The line was silent on
// why nothing happened at that door, and a collector who has just seen coin taken
// fills the silence himself.
//
// CO-LOCATION-GATED: rendered when a rateable keeper (a resident who owns a business,
// sim.RateableBusinessOf) shares the constable's huddle or shop scope. Off-scene the
// line is a lecture with no audience; both conversations it guards against happen
// face to face.

// EstateRateCollectorView is the "## Town rate" section's payload: present only for
// a constable with a rateable keeper co-present.
type EstateRateCollectorView struct {
	// Floor is the coin a keeper keeps untouched — Snapshot.EstateRateFloor, the
	// same figure collectEstateRateAtStop assesses against.
	Floor int
}

// buildEstateRateCollector returns the section payload when the subject is a
// constable with at least one rateable keeper co-present, nil otherwise. Pure over
// the snapshot.
func buildEstateRateCollector(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot) *EstateRateCollectorView {
	if snap == nil || actorSnap == nil || !isConstableSnapshot(actorSnap) {
		return nil
	}
	sc := buyerCoPresenceScope(snap, actorSnap)
	if sc.huddle == "" && sc.scope == "" {
		return nil
	}
	for id, other := range snap.Actors {
		if other == nil || id == actorID || other.DisplayName == "" {
			continue
		}
		if !sc.sellerCoPresent(other) {
			continue
		}
		if sim.RateableBusinessOf(snap.VillageObjects, id) != nil {
			return &EstateRateCollectorView{Floor: snap.EstateRateFloor}
		}
	}
	return nil
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

// renderEstateRateCollector writes the "## Town rate" section. Content-gated: nil
// writes nothing. The body states standing facts about the levy, not the state of
// any one purse — the constable's own action ring ("You collected 16 coins from
// Prudence Ward for the rate on their estate") carries what was taken from whom on
// this visit; the one figure here is the floor, so a door where nothing was taken
// reads as a keeper under it, not as a rate left uncollected.
func renderEstateRateCollector(b *strings.Builder, v *EstateRateCollectorView) {
	if v == nil {
		return
	}
	b.WriteString("## Town rate\n")
	b.WriteString("The town pays your wage out of the estate rate. When you call at a shop on your rounds and the keeper is there, the rate on their estate is taken into the town chest as you arrive — none of it passes through your purse. ")
	b.WriteString("A keeper holding ")
	b.WriteString(strconv.Itoa(v.Floor))
	b.WriteString(" coins or fewer owes nothing today. You never ask for the rate or name a sum: what is due is taken without a word from you, and what is taken is the town's, not yours to hand back.\n")
}
