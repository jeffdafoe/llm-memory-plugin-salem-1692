package perception

import (
	"fmt"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// stall_repair.go — LLM-118 owner audience, generalized to all businesses in
// LLM-247. The "## Your business" section: a standing reminder, shown whenever a
// business owner stands at their own worn premises (tavern, farm, shop, smithy,
// stall), that it needs mending and how. A nil view renders nothing (the common
// case — not at the business, or it isn't worn yet). The SAME non-nil view gates
// the `repair` tool's advertisement (handlers/tool_gating.go), so the cue and the
// tool appear together. This is the standing-fact surface that keeps reminding
// the owner after the one-shot repair warrant (renderWarrantLine) is consumed.

// StallRepairView is the owner's at-the-business repair cue. Non-nil only when
// the actor owns the business they are standing at AND it has worn to the repair
// threshold.
type StallRepairView struct {
	Degraded       bool   // worn past the degrade threshold: closed for trade until mended
	NailsNeeded    int    // nails one repair consumes
	NailsHeld      int    // nails the owner currently carries
	HasEnoughNails bool   // NailsHeld >= NailsNeeded
	Name           string // the business's display name (structure/object); "" → generic noun
}

// buildStallRepair returns the owner's at-the-business repair cue, or nil. Pure
// over the snapshot. Gated on: the actor owns a wearable business, is standing at
// its loiter, and it has worn to the repair threshold.
func buildStallRepair(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot) *StallRepairView {
	if snap == nil || actorSnap == nil {
		return nil
	}
	stall := sim.OwnedWearableStall(snap.VillageObjects, actorID)
	if stall == nil {
		return nil
	}
	if actorSnap.Pos.Chebyshev(objectLoiterPin(stall)) > sim.LoiterAttributionTiles {
		return nil // not standing at the stall
	}
	if !sim.StallRepairable(stall, snap.StallWearRepairThreshold, snap.StallWearDegradeThreshold) {
		return nil // not worn enough to bother (degraded counts — a bad threshold config can't hide the cue)
	}
	needed := snap.StallNailsPerRepair
	held := actorSnap.Inventory[sim.NailItemKind]
	return &StallRepairView{
		Degraded:       sim.StallDegraded(stall, snap.StallWearDegradeThreshold),
		NailsNeeded:    needed,
		NailsHeld:      held,
		HasEnoughNails: held >= needed,
		Name:           resolveDwellPinLabel(snap, stall.ID),
	}
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
	b.WriteString("## Your business\n")
	if v.Degraded {
		fmt.Fprintf(b, "Your %s is too worn to trade — it stays shut until you mend it. ", name)
	} else {
		fmt.Fprintf(b, "Your %s is showing hard use and needs mending. ", name)
	}
	if v.HasEnoughNails {
		fmt.Fprintf(b, "You carry enough nails (%d) to mend it — repair it now (it takes a short while, hammer in hand, on site).\n", v.NailsHeld)
	} else {
		fmt.Fprintf(b, "Mending takes %d nails and you have %d — buy more from the smith, then repair it.\n", v.NailsNeeded, v.NailsHeld)
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
	for _, obj := range snap.VillageObjects {
		if !sim.IsWearableStall(obj) || obj.OwnerActorID == actorID {
			continue // non-business/unowned, or my own (## Your business covers me)
		}
		if actorSnap.Pos.Chebyshev(objectLoiterPin(obj)) > sim.LoiterAttributionTiles {
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
