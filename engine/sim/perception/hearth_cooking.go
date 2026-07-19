package perception

import (
	"fmt"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// hearth_cooking.go — LLM-474. The cook's stake in the fire.
//
// LLM-412 gave the hearth a consequence for COLD; this surface gives it one for
// the POT. A recipe carrying a `hearth_lit` BoostState mints extra output when
// the fire is in at landing, so a keeper who lets her fire die quietly loses
// yield she never knew she had. Before this line existed the engine kept that
// entirely to itself — and the models filled the silence by inventing a rule the
// engine does not have (Hannah Boggs, live 2026-07-18: "the hearth's gone cold
// on me... I'll have porridge for you then", said over a hearth that in fact
// gated nothing).
//
// DELIBERATELY NOT the stoke cue. HearthView (hearth.go) gates the `stoke` tool
// and is nil while a fire burns well; this view is nil-independent of it and
// gates NOTHING, so a cook can be told her fire is good without `stoke` being
// advertised for a fire that would bounce off StartStoke's worth-stoking gate
// (the LLM-435 class). It also carries no vendor/tool mechanics: when the fire
// is low the adjacent "## Your hearth" section already names the wood, the
// supplier and the tool. Same split as LLM-64 — stake here, where-and-how there.

// HearthCookingView is the fire-as-ingredient line for a keeper who cooks over
// a hearth. Non-nil only when the actor produces at least one hearth-boosted
// dish AND is standing inside the work structure whose hearth would boost it.
type HearthCookingView struct {
	Lit          bool     // the fire is burning at all
	NeedsStoking bool     // out, or down to the low-water mark — kept in lockstep with HearthView
	Dishes       []string // display labels of the boosted dishes made here
	Name         string   // the structure's display name; "" → generic noun
}

// buildHearthCooking returns the cook's fire line, or nil. Pure over the
// snapshot; PublishedAt is the fire clock, matching buildHearth and the
// sim-side landing check (recipeBoostStateMet).
func buildHearthCooking(snap *sim.Snapshot, actorSnap *sim.ActorSnapshot) *HearthCookingView {
	if snap == nil || actorSnap == nil || actorSnap.RestockPolicy == nil || snap.Recipes == nil {
		return nil
	}
	// The fire that matters is the WORK structure's — the same one
	// recipeBoostStateMet reads at landing. Standing elsewhere makes the line
	// noise rather than a cue, so it only renders in the kitchen.
	work := actorSnap.WorkStructureID
	if work == "" || actorSnap.InsideStructureID != work {
		return nil
	}
	hearth := sim.StructureHearth(snap.VillageObjects, work)
	if hearth == nil {
		return nil // no fireplace here — every non-hearth kitchen stays silent
	}
	var dishes []string
	for _, pe := range actorSnap.RestockPolicy.ProduceEntries() {
		recipe := snap.Recipes[pe.Item]
		if recipe == nil {
			continue
		}
		if !recipeHasHearthBoost(recipe) {
			continue
		}
		dishes = append(dishes, itemDisplayLabel(snap, pe.Item))
	}
	if len(dishes) == 0 {
		return nil // nothing they make here cares about the fire
	}
	now := snap.PublishedAt
	return &HearthCookingView{
		Lit:          sim.HearthLit(hearth, now),
		NeedsStoking: sim.HearthNeedsStoking(hearth, now, snap.HearthLowMinutes),
		Dishes:       dishes,
		Name:         resolveDwellPinLabel(snap, hearth.ID),
	}
}

// recipeHasHearthBoost reports whether the recipe earns anything from a live
// fire — a positive-bonus hearth_lit entry. A zero/negative bonus is treated as
// absent so a mis-authored row renders nothing rather than promising a bonus the
// produce tick skips.
func recipeHasHearthBoost(recipe *sim.ItemRecipe) bool {
	for _, bs := range recipe.BoostState {
		if bs.State == sim.BoostStateHearthLit && bs.BonusQty > 0 {
			return true
		}
	}
	return false
}

// renderHearthCooking writes the fire-as-ingredient scene. Three tiers, matching
// the escalation the hearth cues already use — and no imperative in any of them:
// the scene is the argument, the model draws the conclusion. No quantities, and
// no "stoke it" — the yield arithmetic belongs in the tool result and the remedy
// belongs to "## Your hearth".
func renderHearthCooking(b *strings.Builder, v *HearthCookingView) {
	if v == nil {
		return
	}
	name := v.Name
	if name == "" {
		name = "room"
	}
	dishes := joinDishLabels(v.Dishes)
	b.WriteString("## The fire and your cooking\n")
	// Pot-first framing throughout. "## Your hearth" (when it renders alongside)
	// opens on the fire and the cold room; opening on the fire here too would read
	// as the same sentence twice. The stake is the batch, so the batch leads.
	switch {
	case !v.Lit:
		fmt.Fprintf(b, "There is no fire under your pot at your %s, and a cold hearth makes a mean batch — what %s you put up will go nowhere near as far.\n", name, dishes)
	case v.NeedsStoking:
		fmt.Fprintf(b, "The next %s you put up will be the poorer if the fire at your %s is left to sink much further.\n", dishes, name)
	default:
		fmt.Fprintf(b, "%s put up over a good fire goes further, and the fire at your %s is burning well.\n", capitalizeFirst(dishes), name)
	}
}

// joinDishLabels renders the dish list in prose ("porridge", "bread and
// journeycake", "bread, journeycake and porridge") — the scene register, not a
// comma-separated field.
func joinDishLabels(dishes []string) string {
	switch len(dishes) {
	case 0:
		return "what you cook"
	case 1:
		return dishes[0]
	case 2:
		return dishes[0] + " and " + dishes[1]
	default:
		return strings.Join(dishes[:len(dishes)-1], ", ") + " and " + dishes[len(dishes)-1]
	}
}
