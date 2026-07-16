package perception

import (
	"fmt"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// hearth.go — LLM-412. Two surfaces for the cold need and its superior relief:
//
//   - ColdSelfView: the subject's own felt cold, rendered as a situated line in
//     "## You" (the renderTiredness pattern — a scene, never "cold: 14/24"),
//     always carrying at least one FREE relief path (a roof; staying put by a
//     fire; the clearing sky) — the absorbing-state rule made visible.
//   - HearthView: the at-the-hearth stoke cue ("## Your hearth" / hired
//     framing), the exact shape of the stall-repair cue with firewood for
//     nails. The SAME non-nil view gates the `stoke` tool.

// ColdSelfView is the subject's felt cold plus the exposure context that
// decides which relief the line points at. Nil when cold is below the
// awareness floor (the common case — nothing renders).
type ColdSelfView struct {
	Tier     sim.NeedTier // Mild ("chilled") / Red ("cold") / Peak ("perished with cold")
	Storm    bool         // a storm is overhead
	Indoors  bool         // inside any structure
	Warm     bool         // inside a structure whose hearth is lit — recovering
	HomeID   sim.StructureID
	HomeName string // resolved display name; "" drops the home destination clause

	// HasWarmGarment: the subject is carrying a coat/cloak (CapabilityWarms), so the
	// storm's cold is being held off outdoors (LLM-410). Set only in the storm-
	// outdoors case; renders a confirming note instead of the buy nudge.
	HasWarmGarment bool
	// CoatVendors are the workplaces selling a warm garment — populated ONLY in the
	// storm-outdoors case when the subject carries none. Vendor-gated: an empty list
	// renders no nudge, so the cue never dangles before the clothing stock exists.
	// The PAID relief, always rendered AFTER the free-relief line, never instead of
	// it — a coat is an upgrade to stay outside, not a substitute for shelter.
	CoatVendors []RestockVendor
}

// buildColdSelf classifies the subject's cold for the self-state line, or nil
// when it is below the awareness floor. Pure over the snapshot; PublishedAt is
// the fire clock, mirroring the sim-side actorIsWarm.
func buildColdSelf(snap *sim.Snapshot, a *sim.ActorSnapshot) *ColdSelfView {
	if snap == nil || a == nil {
		return nil
	}
	value, ok := a.Needs[sim.ColdNeedKey]
	if !ok {
		return nil
	}
	need, ok := sim.FindNeed(sim.ColdNeedKey)
	if !ok {
		return nil
	}
	tier := need.Tier(value, snap.NeedThresholds.Get(sim.ColdNeedKey))
	if tier == sim.NeedSilent {
		return nil
	}
	view := &ColdSelfView{
		Tier:    tier,
		Storm:   snap.Environment.Weather == sim.WeatherStorm,
		Indoors: a.InsideStructureID != "",
		Warm:    sim.HearthLit(sim.StructureHearth(snap.VillageObjects, a.InsideStructureID), snap.PublishedAt),
	}
	// The concrete free-relief destination for an actor caught in the open: their
	// own home, rendered as a move_to target (the LLM-274 lesson — a steer with no
	// destination gets narrated, not walked). Only when they have one and aren't
	// already inside it; the prose "any roof" clause covers the homeless.
	if view.Storm && !view.Indoors && a.HomeStructureID != "" {
		if name, ok := resolveStructureLabel(snap, a.HomeStructureID); ok {
			view.HomeID = a.HomeStructureID
			view.HomeName = name
		}
	}
	// LLM-410 warm-garment relief. Only meaningful outdoors in a storm — that is the
	// case the coat "is your roof"; under a roof one already shelters, and by a fire
	// warmth beats a coat. Either the subject carries a garment (confirm it's holding
	// the chill off) or a seller has one (surface the vendor-gated buy nudge as a
	// PAID option alongside the free relief above).
	if view.Storm && !view.Indoors {
		if actorSnapHasWarmGarment(snap, a) {
			view.HasWarmGarment = true
		} else {
			view.CoatVendors = findWarmGarmentVendors(snap)
		}
	}
	return view
}

// actorSnapHasWarmGarment mirrors sim.actorHasWarmGarment over the snapshot: does
// the subject carry any CapabilityWarms good (coat or cloak)? The perception-side
// read behind the cold self-line's warm-garment branch (LLM-410). A kind absent
// from the catalog (or a nil ItemKinds map) simply doesn't match.
func actorSnapHasWarmGarment(snap *sim.Snapshot, a *sim.ActorSnapshot) bool {
	for kind, qty := range a.Inventory {
		if qty <= 0 {
			continue
		}
		if def := snap.ItemKinds[kind]; def != nil && def.HasCapability(sim.CapabilityWarms) {
			return true
		}
	}
	return false
}

// renderColdSelf writes the subject's situated cold line — tier phrase plus the
// relief the situation actually offers. Every branch names at least one FREE
// relief path (the cross-scenario invariant: a cold actor is never shown a
// dead end — shelter is always free). No branch is an imperative beyond
// pointing; the scene is the argument.
func renderColdSelf(b *strings.Builder, v *ColdSelfView) {
	if v == nil {
		return
	}
	var lead string
	switch v.Tier {
	case sim.NeedMild:
		lead = "You're chilled"
	case sim.NeedRed:
		lead = "You're cold through to your clothes"
	case sim.NeedPeak:
		lead = "You're perished with cold"
	default:
		return
	}
	switch {
	case v.Warm:
		fmt.Fprintf(b, "%s, but the fire here is working the chill out of you — stay by it and it will pass.\n", lead)
	case v.Storm && !v.Indoors:
		b.WriteString(lead)
		b.WriteString(" — the rain is soaking you where you stand. Any roof will stop the worst of it; get indoors.")
		if v.HomeName != "" {
			fmt.Fprintf(b, " Your home, %s (destination: %s), is shelter for free.", sanitizeInline(v.HomeName), v.HomeID)
		}
		b.WriteString("\n")
		renderColdGarment(b, v)
	case v.Storm && v.Indoors:
		fmt.Fprintf(b, "%s. The roof here keeps the rain off you — staying in is easing it — though only a lit fire would warm you through.\n", lead)
	default:
		// Clear sky, not by a fire: the chill is already fading on its own.
		fmt.Fprintf(b, "%s, but the sky has cleared and the chill is easing off you on its own; a fire would drive it out faster.\n", lead)
	}
}

// renderColdGarment writes the LLM-410 warm-garment tail of the storm-outdoors
// cold line: a confirming note when the subject already carries a coat/cloak, or —
// vendor-gated — the "buy one to keep working outside" nudge when a seller has one.
// Always AFTER the free-relief line, never instead of it: a coat is a PAID upgrade
// to stay out, not a substitute for going indoors (the free path is unconditional
// above, so the absorbing-state rule holds). Nothing renders when the subject
// carries none and none is for sale — no dangling steer before supply exists.
func renderColdGarment(b *strings.Builder, v *ColdSelfView) {
	if v.HasWarmGarment {
		b.WriteString("The warm clothes you carry are holding the worst of the cold off you — enough to keep working out here, though a fire indoors would drive the chill out entirely.\n")
		return
	}
	if len(v.CoatVendors) == 0 {
		return
	}
	b.WriteString("A warm coat or cloak would keep the worst of it off and let you keep working out here. Use move_to to reach a seller, then pay_with_item once you arrive:\n")
	renderWalkToVendors(b, v.CoatVendors)
}

// HearthView is the at-the-hearth stoke cue. Non-nil only when the actor is
// responsible for a hearth (owner, or Working a hired job for its owner —
// sim.HearthToStoke), is standing INSIDE its structure, and the fire is out or
// low (sim.HearthNeedsStoking). Hired flips the render to the truthful "the
// hearth where you're working" framing, mirroring StallRepairView.Hired.
type HearthView struct {
	Hired         bool
	Out           bool   // the fire is fully out (vs down to embers)
	Storm         bool   // a storm is overhead — the wording presses harder
	OccupantsCold bool   // someone co-present in the structure is feeling the cold — the red escalation
	Name          string // the structure's display name; "" → generic noun
	WoodNeeded    int    // firewood one stoke consumes
	WoodHeld      int    // firewood the actor carries
	HasEnoughWood bool
	WoodVendors   []RestockVendor // owner's buy-firewood destinations; only when short and NOT hired
	Conserve      bool            // the working-capital gate says hold off buying (owner only)
}

// buildHearth returns the at-the-hearth stoke cue, or nil. Pure over the
// snapshot; PublishedAt is the fire clock.
func buildHearth(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot) *HearthView {
	if snap == nil || actorSnap == nil {
		return nil
	}
	if actorMidSourceActivity(actorSnap) {
		return nil // mid a source-activity window — a fresh stoke bounces "already busy ... before tending the fire"
	}
	hearth, hired := sim.HearthToStoke(snap.VillageObjects, snap.LaborLedger, actorID)
	if hearth == nil {
		return nil
	}
	if string(actorSnap.InsideStructureID) != string(hearth.ID) {
		return nil // a fire is tended from inside its room
	}
	now := snap.PublishedAt
	if !sim.HearthNeedsStoking(hearth, now, snap.HearthLowMinutes) {
		return nil // burning well — nothing to say
	}
	needed := snap.StokeWoodPerStoke
	if needed <= 0 {
		needed = sim.DefaultStokeWoodPerStoke
	}
	held := actorSnap.Inventory[sim.FirewoodItemKind]
	view := &HearthView{
		Hired:         hired,
		Out:           !sim.HearthLit(hearth, now),
		Storm:         snap.Environment.Weather == sim.WeatherStorm,
		OccupantsCold: structureOccupantsCold(snap, actorID, actorSnap.InsideStructureID),
		Name:          resolveDwellPinLabel(snap, hearth.ID),
		WoodNeeded:    needed,
		WoodHeld:      held,
		HasEnoughWood: held >= needed,
	}
	// Short of wood: resolve the firewood supplier(s) so the cue names a concrete
	// destination (the LLM-274 lesson), owner only — a hired hand can't leave the
	// job to shop, same carve-out as the repair cue's NailVendors.
	if !view.HasEnoughWood && !hired {
		view.WoodVendors, _ = findItemVendors(snap, actorID, actorSnap, sim.FirewoodItemKind)
		view.Conserve = merchantConserve(snap, actorID, actorSnap).Active
	}
	return view
}

// structureOccupantsCold reports whether any OTHER actor standing inside the
// structure feels the cold (at or above its awareness floor) — the escalation
// that turns "the fire is low" into "and folk in the room feel it." The
// subject's own chill renders on its own self line, so it doesn't count here.
func structureOccupantsCold(snap *sim.Snapshot, subjectID sim.ActorID, structureID sim.StructureID) bool {
	if structureID == "" {
		return false
	}
	need, ok := sim.FindNeed(sim.ColdNeedKey)
	if !ok {
		return false
	}
	threshold := snap.NeedThresholds.Get(sim.ColdNeedKey)
	for id, a := range snap.Actors {
		if id == subjectID || a == nil || a.InsideStructureID != structureID {
			continue
		}
		if need.Tier(a.Needs[sim.ColdNeedKey], threshold) > sim.NeedSilent {
			return true
		}
	}
	return false
}

// renderHearth writes the "## Your hearth" section (or the hired framing).
// Content-gated: a nil view writes nothing. The scene escalates by tier —
// a quiet embers line under a calm sky, the wind pressing in under a storm,
// cold occupants as the red beat — and states the way to act (stoke now, or
// buy/gather wood first) without ever becoming a bare imperative.
func renderHearth(b *strings.Builder, v *HearthView) {
	if v == nil {
		return
	}
	name := v.Name
	if name == "" {
		name = "room"
	}
	if v.Hired {
		renderHiredHearth(b, v, name)
		return
	}
	b.WriteString("## Your hearth\n")
	writeHearthScene(b, v, fmt.Sprintf("your %s", name))
	writeHearthWoodSteer(b, v)
}

// renderHiredHearth is the hired-worker framing: the employer's fire, stated
// truthfully as the place the worker was taken on to help at. The buy steer is
// softened to a bare shortfall — a hired hand doesn't leave the job to shop.
func renderHiredHearth(b *strings.Builder, v *HearthView, name string) {
	b.WriteString("## The hearth where you're working\n")
	writeHearthScene(b, v, fmt.Sprintf("the %s you're working at", name))
	if v.HasEnoughWood {
		fmt.Fprintf(b, "You carry enough firewood (%d) to feed it — use the stoke tool now to build the fire back up (it takes a moment).\n", v.WoodHeld)
	} else {
		fmt.Fprintf(b, "Feeding it takes %d firewood and you have %d — you'd need more first.\n", v.WoodNeeded, v.WoodHeld)
	}
}

// writeHearthScene writes the fire-state sentence for the place phrase
// ("your Tavern" / "the Tavern you're working at"). Tiered: out vs embers,
// storm vs calm, plus the cold-occupants escalation.
func writeHearthScene(b *strings.Builder, v *HearthView, place string) {
	switch {
	case v.Out && v.Storm:
		fmt.Fprintf(b, "The hearth at %s sits cold and dark, and the storm outside is pressing its chill into the room. ", place)
	case v.Out:
		fmt.Fprintf(b, "The hearth at %s sits cold — no fire has been kept in it. ", place)
	case v.Storm:
		fmt.Fprintf(b, "The fire at %s has burned down to embers, and the wind is finding its way under the door. ", place)
	default:
		fmt.Fprintf(b, "The fire at %s has burned down to embers. ", place)
	}
	if v.OccupantsCold {
		b.WriteString("Folk in the room are feeling the cold. ")
	}
}

// writeHearthWoodSteer writes the owner's act-now / buy-first tail, the exact
// progression of the repair cue's nail steer: stoke in hand → conserve hold-off
// → named walk-to supplier(s) → destination-less shortfall (never a dead-end
// person-shaped errand — the LLM-216/301 posture).
func writeHearthWoodSteer(b *strings.Builder, v *HearthView) {
	if v.HasEnoughWood {
		fmt.Fprintf(b, "You carry enough firewood (%d) to feed it — use the stoke tool now to build the fire back up (it takes a moment).\n", v.WoodHeld)
		return
	}
	if v.Conserve {
		fmt.Fprintf(b, "Feeding it takes %d firewood and you have %d — but your purse can't take on wood just now. Sell what you can and let your coins recover first.\n", v.WoodNeeded, v.WoodHeld)
		return
	}
	if len(v.WoodVendors) > 0 {
		fmt.Fprintf(b, "Feeding it takes %d firewood and you have %d — buy more, then come back and stoke the fire. Use move_to to reach a supplier, then pay_with_item once you arrive:\n", v.WoodNeeded, v.WoodHeld)
		renderWalkToVendors(b, v.WoodVendors)
		return
	}
	fmt.Fprintf(b, "Feeding it takes %d firewood and you have %d — you'll need to buy or gather more before you can stoke it.\n", v.WoodNeeded, v.WoodHeld)
}
