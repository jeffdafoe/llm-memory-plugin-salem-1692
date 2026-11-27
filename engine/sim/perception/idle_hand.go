package perception

import (
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// idle_hand.go — the "no batch a hand could speed up" scene line.
//
// A hired hand at the keeper's post adds LaborProduceBoostPct of the keeper's
// base rate to a RUNNING batch (sim.produceRateScalePct). With no batch in the
// works and none able to start — every good the keeper makes either has no room
// under its carry cap for a whole batch, or lacks its makings — the boost has
// nothing to act on, and the wage buys no output. The hire cues never said so:
// the live miller, his flour at 18 of 20 (a batch is 5), hired five hands across
// two days who ground nothing, about ten coins a day out of a purse that took
// in thirteen.
//
// The line states the production fact as a scene and leaves the hire decision
// to the model — offer_work and accept_work are untouched. It speaks only of
// production ("no batch a hand could speed up"): a hand may still stoke a hearth
// or mind the shop, and the line must not read as a verdict on the hire.
//
// It and the "## Your trade" menu (buildForgeChoice) are mutually exclusive, not
// exact complements: both read produceBatchReady, the menu lists the goods a
// batch could start for, and this line shows only when there are none. Neither
// may show (a worn stall suppresses this line; the menu's own gates — away from
// the post, a full produce block — drop the menu).

// IdleHandView is the content-gated idle-production view. nil means a hand
// could hurry something (or the subject makes nothing) and render writes nothing.
type IdleHandView struct {
	// Full names the goods whose stores have no room for another whole batch,
	// in policy order. Labels as shortageGoodLabel gives them ("flour",
	// "shovels").
	Full []string
	// Short lists the goods that lack their makings, in policy order.
	Short []IdleHandShort
}

// IdleHandShort is one good the keeper cannot start a batch of for want of its
// inputs.
type IdleHandShort struct {
	Good    string   // the good's label ("flour")
	Missing []string // the missing inputs' labels ("wheat"), as missingProduceInputs gives them
}

// buildIdleHand returns the view when the subject makes goods, has no batch in
// the works, and could start a batch of none of them; else nil. Pure over the
// snapshot.
//
// Not gated on location: offer_work brings the hand to the employer's post
// wherever the deal is struck, and the stores are the stores.
//
// Suppressed while the subject's own business is worn to the repair threshold:
// a hired hand mends the employer's stall (sim.WearableStallToMend), so there is
// real work for one, and a line saying otherwise would steer a keeper off the
// help that reopens a degraded shop. The same suppression covers the full
// production block (pct 0), which only happens while degraded.
func buildIdleHand(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot) *IdleHandView {
	if snap == nil || actorSnap == nil || actorSnap.RestockPolicy == nil || snap.Recipes == nil {
		return nil
	}
	if actorSnap.ProductionItem != "" {
		return nil // a batch is running — the boost hurries it
	}
	if stall := sim.OwnedWearableStall(snap.VillageObjects, actorID); stall != nil &&
		sim.StallRepairable(stall, snap.StallWearRepairThreshold, snap.StallWearDegradeThreshold) {
		return nil // a hand has a stall to mend
	}
	view := &IdleHandView{}
	makes := false
	for _, e := range actorSnap.RestockPolicy.ProduceEntries() {
		recipe := snap.Recipes[e.Item]
		if recipe == nil || recipe.RateQty <= 0 || recipe.RatePerHours <= 0 {
			continue // not makeable — the same validity test as buildForgeChoice
		}
		makes = true
		// The same readiness test the "## Your trade" menu filters on. A good that
		// passes both axes is a batch a hand could hurry, so no line.
		_, fitsCap, inputsReady := produceBatchReady(actorSnap, e, recipe)
		if fitsCap && inputsReady {
			return nil
		}
		label := shortageGoodLabel(snap.ItemKinds[e.Item], string(e.Item))
		// No room first: a good with no room for a whole batch is idle whatever
		// its makings, and buying the makings would not change it.
		if !fitsCap {
			view.Full = append(view.Full, label)
			continue
		}
		view.Short = append(view.Short, IdleHandShort{Good: label, Missing: missingProduceInputs(snap, actorSnap, e.Item)})
	}
	if !makes {
		return nil // not a producer — a hand's worth to them is not output
	}
	return view
}

// renderIdleHand writes the one scene line. Content-gated: a nil view writes
// nothing. The full and short clauses join into one sentence that closes on the
// same beat, so the line reads as one fact about the work, not a list.
func renderIdleHand(b *strings.Builder, v *IdleHandView) {
	if v == nil || (len(v.Full) == 0 && len(v.Short) == 0) {
		return
	}
	var clauses []string
	// "No room for a whole batch", not "full until some is sold": 18 of 20 is not
	// full, and a batch larger than the whole cap fits no matter what is sold.
	if len(v.Full) > 0 {
		clauses = append(clauses, "your stores have no room for a whole batch of "+joinGoods(v.Full))
	}
	for _, s := range v.Short {
		if len(s.Missing) == 0 {
			clauses = append(clauses, "you have not the makings for another batch of "+sanitizeInline(s.Good))
			continue
		}
		// Lower-cased: catalog display labels are capitalized ("Wheat") for menus,
		// and this line names no tool argument that needs the exact label.
		clauses = append(clauses, "you have not the "+strings.ToLower(missingInputsPhrase(s.Missing))+" for another batch of "+sanitizeInline(s.Good))
	}
	line := strings.Join(clauses, ", and ")
	b.WriteString(strings.ToUpper(line[:1]) + line[1:] + " — there is no batch a hand could speed up just now.\n")
}

// idleHandInOffers reports whether the work-offers section will carry the idle
// line: it renders there once when any pending offer is a hand offering to work
// for the subject. The offer_work cue then omits it, so a prompt with both a
// pending offer and another hireable hand prints the line once, by the decision.
func idleHandInOffers(idle *IdleHandView, offers []LaborOfferView) bool {
	if idle == nil {
		return false
	}
	for _, o := range offers {
		if !o.SubjectIsWorker() {
			return true
		}
	}
	return false
}

// joinGoods joins sanitized good labels for prose: "flour", "nails and
// shovels", "nails, shovels and skillets".
func joinGoods(labels []string) string {
	clean := make([]string, len(labels))
	for i, l := range labels {
		clean[i] = sanitizeInline(l)
	}
	switch len(clean) {
	case 0:
		return ""
	case 1:
		return clean[0]
	}
	return strings.Join(clean[:len(clean)-1], ", ") + " and " + clean[len(clean)-1]
}
