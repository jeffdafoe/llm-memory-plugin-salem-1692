package perception

import (
	"fmt"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// stall_repair.go — LLM-118 owner audience, generalized to all businesses in
// LLM-247, and to hired workers in LLM-271. The repair section: a standing
// reminder, shown whenever the actor stands at a worn business they are
// responsible for — its owner at their own premises ("## Your business"), or a
// worker Working a hired job there ("## The business you're working at") — that it
// needs mending and how. A nil view renders nothing (the common case — not at such
// a business, or it isn't worn yet). The SAME non-nil view gates the `repair`
// tool's advertisement (handlers/tool_gating.go), so the cue and the tool appear
// together. This is the standing-fact surface that keeps reminding after the
// one-shot repair warrant (renderWarrantLine) is consumed.

// StallRepairView is the at-the-business repair cue. Non-nil only when the actor
// is responsible for a wearable business they are standing at AND it has worn to
// the repair threshold — either as its owner, or (LLM-271) as a worker actively
// Working a hired job there. Hired flips the render from "your business" to "the
// business you're working at" so the cue states the true relationship.
type StallRepairView struct {
	Hired          bool            // resolved through a hire (Working for the owner), not ownership (LLM-271)
	Degraded       bool            // worn past the degrade threshold: closed for trade until mended
	NailsNeeded    int             // nails one repair consumes
	NailsHeld      int             // nails the actor currently carries
	HasEnoughNails bool            // NailsHeld >= NailsNeeded
	Name           string          // the business's display name (structure/object); "" → generic noun
	NailVendors    []RestockVendor // owner's buy-nails destinations (LLM-274); populated only when short of nails and NOT hired
}

// buildStallRepair returns the at-the-business repair cue, or nil. Pure over the
// snapshot. Gated on: the actor is responsible for a wearable business (owns it,
// or is Working a hired job there — LLM-271), is standing at/inside it, and it has
// worn to the repair threshold.
func buildStallRepair(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot) *StallRepairView {
	if snap == nil || actorSnap == nil {
		return nil
	}
	stall, hired := sim.WearableStallToMend(snap.VillageObjects, snap.LaborLedger, actorID)
	if stall == nil {
		return nil
	}
	if !sim.AtBusiness(actorSnap.Pos, actorSnap.InsideStructureID, stall.ID, objectLoiterPin(stall), true) {
		return nil // not standing at or inside the business
	}
	if !sim.StallRepairable(stall, snap.StallWearRepairThreshold, snap.StallWearDegradeThreshold) {
		return nil // not worn enough to bother (degraded counts — a bad threshold config can't hide the cue)
	}
	needed := snap.StallNailsPerRepair
	held := actorSnap.Inventory[sim.NailItemKind]
	view := &StallRepairView{
		Hired:          hired,
		Degraded:       sim.StallDegraded(stall, snap.StallWearDegradeThreshold),
		NailsNeeded:    needed,
		NailsHeld:      held,
		HasEnoughNails: held >= needed,
		Name:           resolveDwellPinLabel(snap, stall.ID),
	}
	// LLM-274: when the owner is short of nails, resolve the nail supplier(s) so the
	// cue can name a concrete move_to destination instead of the dead-end "the smith".
	// findItemVendors inherits the restock directory's filtering (supplier-of-record,
	// remembered-shut drop, affordability drop, workplace dedupe). Skipped for a hired
	// worker — they can't leave the job to shop, so their cue only names the shortfall
	// (renderHiredStallRepair).
	if !view.HasEnoughNails && !hired {
		view.NailVendors = findItemVendors(snap, actorID, actorSnap, sim.NailItemKind)
	}
	return view
}

// renderStallRepair writes the "## Your business" section. Content-gated: a nil view
// writes nothing. Symmetrical awareness — it states the problem AND the way out
// (the smith sells the nails) in one place, and tells the owner whether they can
// mend now or must buy nails first (the two-step buy->repair, mirroring
// gather->consume).
func renderStallRepair(b *strings.Builder, v *StallRepairView) {
	if v == nil {
		return
	}
	name := v.Name
	if name == "" {
		name = "place of business"
	}
	if v.Hired {
		renderHiredStallRepair(b, v, name)
		return
	}
	b.WriteString("## Your business\n")
	if v.Degraded {
		fmt.Fprintf(b, "Your %s is too worn to trade — it stays shut until you mend it. ", name)
	} else {
		fmt.Fprintf(b, "Your %s is showing hard use and needs mending. ", name)
	}
	if v.HasEnoughNails {
		fmt.Fprintf(b, "You carry enough nails (%d) to mend it — repair it now (it takes a short while, hammer in hand, on site).\n", v.NailsHeld)
	} else if len(v.NailVendors) > 0 {
		// LLM-274: name the actual nail supplier(s) — workplace + structure_id, resolved
		// via findItemVendors — in the model-proven Restocking format, plus the repair's
		// second hop (come back and mend). The old destination-less "buy from the smith"
		// left llama-3.3-70b narrating the errand ("I must visit the smith") and never
		// issuing move_to.
		fmt.Fprintf(b, "Mending takes %d nails and you have %d — buy them, then come back here and repair. Use move_to to reach a supplier, then pay_with_item once you arrive:\n", v.NailsNeeded, v.NailsHeld)
		renderWalkToVendors(b, v.NailVendors)
	} else {
		// No reachable, open, affordable nail supplier on record — keep the generic
		// sentence rather than a dead-end target (mirrors the Restocking actionability
		// posture, LLM-216); the cue self-heals when a supplier opens or the purse covers one.
		fmt.Fprintf(b, "Mending takes %d nails and you have %d — buy more from the smith, then repair it.\n", v.NailsNeeded, v.NailsHeld)
	}
}

// renderHiredStallRepair writes the repair cue for a worker hired to labor at the
// business (LLM-271): the same actionable mend, framed as the premises they were
// taken on to help with rather than their own ("## The business you're working
// at"), so the engine states the situation truthfully instead of calling a hired
// hand's workplace "your" shop. It still tells them whether they can mend now or
// are short of nails — the buy-then-mend steer is softened (they can't leave a job
// to shop), so it just names the shortfall.
func renderHiredStallRepair(b *strings.Builder, v *StallRepairView, name string) {
	b.WriteString("## The business you're working at\n")
	if v.Degraded {
		fmt.Fprintf(b, "The %s you're working at is too worn to trade — it stays shut until it's mended. ", name)
	} else {
		fmt.Fprintf(b, "The %s you're working at is showing hard use and needs mending. ", name)
	}
	if v.HasEnoughNails {
		fmt.Fprintf(b, "You carry enough nails (%d) to mend it — repair it now (it takes a short while, hammer in hand, on site).\n", v.NailsHeld)
	} else {
		fmt.Fprintf(b, "Mending takes %d nails and you have %d — you'd need more from the smith first.\n", v.NailsNeeded, v.NailsHeld)
	}
}

// StallConditionView is the co-present world-fact line for an actor — NOT the
// owner — standing at a worn business. Social texture a passerby can remark
// on (and the perception twin of the worn sprite, which the art pass defers).
// nil when the actor isn't at a worn business, or when they ARE its owner (they
// get the richer "## Your business" cue instead).
type StallConditionView struct {
	Degraded bool
	Name     string // the business's display name (structure/object); "" → generic noun
}

// buildStallCondition returns the co-present worn-business line for a non-owner,
// or nil. Pure over the snapshot. Surfaces the FIRST worn business the actor
// stands at that isn't their own.
func buildStallCondition(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot) *StallConditionView {
	if snap == nil || actorSnap == nil {
		return nil
	}
	// The business the actor is responsible for mending — their own, or (LLM-271)
	// the one they are Working a hired job at — gets the richer repair cue instead,
	// so skip it here: a mender shouldn't ALSO be handed the bystander "looks worn"
	// texture line for the very stall they've been cued to fix.
	responsible, _ := sim.WearableStallToMend(snap.VillageObjects, snap.LaborLedger, actorID)
	for _, obj := range snap.VillageObjects {
		if !sim.IsWearableStall(obj) || obj.OwnerActorID == actorID {
			continue // non-business/unowned, or my own (## Your business covers me)
		}
		if responsible != nil && obj.ID == responsible.ID {
			continue // I'm hired to mend this one — "## The business you're working at" covers me
		}
		if !sim.AtBusiness(actorSnap.Pos, actorSnap.InsideStructureID, obj.ID, objectLoiterPin(obj), true) {
			continue
		}
		if !sim.StallNeedsRepair(obj, snap.StallWearRepairThreshold) {
			continue
		}
		return &StallConditionView{
			Degraded: sim.StallDegraded(obj, snap.StallWearDegradeThreshold),
			Name:     resolveDwellPinLabel(snap, obj.ID),
		}
	}
	return nil
}

// renderStallCondition writes the co-present worn-business atmosphere line. A nil
// view writes nothing. A bare standing fact (no header) — it's environmental
// texture, not an actionable cue for this actor.
func renderStallCondition(b *strings.Builder, v *StallConditionView) {
	if v == nil {
		return
	}
	name := v.Name
	if name == "" {
		name = "business"
	}
	if v.Degraded {
		fmt.Fprintf(b, "The %s here is battered and clearly unfit for trade.\n", name)
	} else {
		fmt.Fprintf(b, "The %s here looks worn and run-down from hard use.\n", name)
	}
}
