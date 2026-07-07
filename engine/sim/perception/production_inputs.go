package perception

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// production_inputs.go — LLM-82. The "## Keeping up production" section: the
// producer-side mirror of "## Restocking". Where Restocking tells a reseller
// WHERE to buy a low bought-in good, this tells a PRODUCER that a good it buys
// is also an INPUT it consumes to make something, how much runway it has left
// (current input stock expressed as units of the output it can still produce),
// and that it is running low — the WHY behind the trip.
//
// The split is deliberate (the LLM-64 contradiction guard): this section never
// names a supplier, a structure_id, or pay_with_item — it motivates, and the
// adjacent "## Restocking" section (which fires on the SAME reorder threshold for
// a buy-input) carries the where/how. Two cues both issuing a buy instruction is
// exactly the contradiction trap; keeping this one to "you make X with Y, here's
// your runway, you're low" keeps them complementary.
//
// Gating mirrors Restocking exactly: an input surfaces only when it is a `buy`
// entry on the actor's restock policy AND below the shared reorder threshold
// (sim.RestockReorderThresholdMet, the same gate the warrant and Restocking use).
// Self-produced inputs (a produce/forage entry, e.g. John's own water) carry no
// buy cap and never surface — the actor makes them itself. So this section only
// ever appears alongside a Restocking line for the same item, annotating it with
// the production dependency and runway.

// ProductionInputsView is the content-gated "## Keeping up production" section.
// A nil view (or empty Items+Boosts) means render omits the section.
type ProductionInputsView struct {
	Items  []ProductionInputView
	Boosts []ProductionBoostView // optional-booster lines (LLM-248)
}

// ProductionBoostView is one low OPTIONAL booster (LLM-248): an item the actor
// buys, that a recipe the actor produces consumes electively for bonus yield,
// currently below its reorder threshold. Unlike a required input there is no
// runway — production continues without it — so the line motivates the buy with
// the forgone bonus instead ("each batch made with it yields N extra").
type ProductionBoostView struct {
	BoostLabel string       // display label of the booster ("sage")
	BoostKind  sim.ItemKind // sort-key parity with the label
	CurrentQty int          // on-hand quantity of the booster

	OutputLabel string       // display label of the good it boosts ("milk")
	OutputKind  sim.ItemKind // tie-break sort key

	BonusQty int // extra output units per boosted execution
}

// ProductionInputView is one low production input: an item the actor buys, that
// is also consumed by a recipe the actor produces, and is currently below its
// reorder threshold. One view per (produced good, input) pair — an input feeding
// two of the actor's recipes surfaces once per recipe, since the runway is
// per-good.
type ProductionInputView struct {
	InputLabel string       // display label of the consumed input ("skillet")
	InputKind  sim.ItemKind // unexported sort key parity with the label
	CurrentQty int          // on-hand quantity of the input

	OutputLabel string       // display label of the good it helps make ("stew")
	OutputKind  sim.ItemKind // tie-break sort key

	// RunwayUnits is how many more of the output good the current input stock can
	// back: currentQty * outputQty / inputQtyPerBatch. For a tool consumed 1 per
	// batch (skillet: 1 per 30-stew batch) this is the exact wear runway (1 skillet
	// = 30 stews). For a bulk input consumed in step with the output (carrots: 30
	// per 30-stew batch) it is the effective per-unit rate. A soft signal, not a
	// guarantee — production is batch-atomic, so a sub-batch input stock can't
	// actually mint a partial batch; the number conveys relative urgency.
	RunwayUnits int
}

// buildProductionInputs builds the producer-input view for actorSnap, or nil when
// the actor produces nothing with a bought, below-threshold input, restock is
// disabled (RestockReorderPct == 0), or the recipe catalog/policy is absent. Pure
// over the snapshot.
//
// Every line is additionally gated on an ACTIONABLE buy path for the input
// (itemHasActionableBuyPath — the same LLM-216 item gate buildRestocking
// applies), so this section renders only alongside a "## Restocking" line for
// the same item, as the LLM-64 split intends. Without the gate, an
// unobtainable input (no vendor anywhere — the live Hannah Boggs water case)
// would get a motivate-line with no act-half, and the model improvises on the
// dead end (LLM-260).
func buildProductionInputs(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot) *ProductionInputsView {
	if snap == nil || actorSnap == nil || actorSnap.RestockPolicy == nil || snap.Recipes == nil {
		return nil
	}
	pct := snap.RestockReorderPct
	if pct <= 0 {
		return nil // producer/feature disabled
	}
	// LLM-298: this section is pure buy-motivation — keep an input stocked so you can
	// keep making your product. A conserving keeper (coin-poor + overstocked) is told to
	// hold off buying and sell down first, and the "## Restocking" cue for the same input
	// already carries that steer; a "you're running low on X" motivate-line here would
	// dangle a second want with no legal outlet — the live sage→stew case, where it fed
	// futile `produce Stew` retries. Suppress the whole section while conserving.
	if merchantConserve(snap, actorID, actorSnap).Active {
		return nil
	}
	// LLM-304: a degraded business is shut for production — no point motivating an
	// input buy while the shop can't produce until it's mended (the "## Your business"
	// cue carries that). Suppress the section so it doesn't dangle an outletless want.
	if ownerBusinessDegraded(snap, actorID) {
		return nil
	}
	// The items the actor restocks by buying, and their caps — the gate for which
	// inputs are a buy-restock concern (a self-produced input has no buy entry,
	// derived or otherwise, and is excluded). Mirrors the set "## Restocking"
	// works from: the EFFECTIVE demand (LLM-260), so an unsourced recipe input
	// gets its runway line without a hand-authored buy entry.
	buyCaps := map[sim.ItemKind]int{}
	for _, e := range sim.EffectiveBuyEntries(snap.Recipes, actorSnap.RestockPolicy) {
		buyCaps[e.Item] = e.Cap()
	}
	if len(buyCaps) == 0 {
		return nil
	}
	// Batch floors for required inputs (LLM-279) — the same floor the warrant and
	// "## Restocking" use, so this runway line surfaces on the identical trigger.
	// Elective boosters below keep the plain cap fraction (floor 0): a missing
	// booster never stalls production.
	floors := sim.ReorderFloors(snap.Recipes, actorSnap.RestockPolicy)
	var items []ProductionInputView
	var boosts []ProductionBoostView
	for _, pe := range actorSnap.RestockPolicy.ProduceEntries() {
		recipe := snap.Recipes[pe.Item]
		if recipe == nil || recipe.OutputQty <= 0 {
			continue
		}
		// Optional boosters (LLM-248): same buy-entry + below-threshold gate as
		// required inputs, but the line motivates with the forgone bonus rather
		// than a runway (production continues without a booster).
		for _, bi := range recipe.BoostInputs {
			if bi.Qty <= 0 || bi.BonusQty <= 0 {
				continue
			}
			cap, isBuy := buyCaps[bi.Item]
			if !isBuy {
				continue
			}
			current := actorSnap.Inventory[bi.Item]
			if current < 0 {
				current = 0
			}
			if !sim.RestockReorderThresholdMet(current, cap, pct, 0) {
				continue // elective booster — cap fraction only, no batch floor
			}
			if !itemHasActionableBuyPath(snap, actorID, actorSnap, bi.Item) {
				continue // no vendor — Restocking omits it (LLM-216), so the booster motivation stays silent too
			}
			boosts = append(boosts, ProductionBoostView{
				BoostLabel:  itemDisplayLabel(snap, bi.Item),
				BoostKind:   bi.Item,
				CurrentQty:  current,
				OutputLabel: itemDisplayLabel(snap, pe.Item),
				OutputKind:  pe.Item,
				BonusQty:    bi.BonusQty,
			})
		}
		for _, in := range recipe.Inputs {
			if in.Qty <= 0 {
				continue
			}
			cap, isBuy := buyCaps[in.Item]
			if !isBuy {
				continue // self-produced/foraged input — not a buy-restock concern
			}
			current := actorSnap.Inventory[in.Item]
			if current < 0 {
				current = 0 // a corrupt negative on-hand reads as "out", not a negative runway
			}
			if !sim.RestockReorderThresholdMet(current, cap, pct, floors[in.Item]) {
				continue // not low yet — the same batch-floored gate Restocking uses
			}
			if !itemHasActionableBuyPath(snap, actorID, actorSnap, in.Item) {
				continue // no vendor — Restocking omits it (LLM-216), so the runway line stays silent too (LLM-260)
			}
			// int64 multiply + clamp guards a corrupt/imported catalog with huge
			// quantities from overflowing int before the divide — the same posture
			// RestockReorderThresholdMet and buyerLastPaidAffordableQty take.
			runway := int64(current) * int64(recipe.OutputQty) / int64(in.Qty)
			if runway > int64(math.MaxInt32) {
				runway = int64(math.MaxInt32)
			}
			items = append(items, ProductionInputView{
				InputLabel:  itemDisplayLabel(snap, in.Item),
				InputKind:   in.Item,
				CurrentQty:  current,
				OutputLabel: itemDisplayLabel(snap, pe.Item),
				OutputKind:  pe.Item,
				RunwayUnits: int(runway),
			})
		}
	}
	if len(items) == 0 && len(boosts) == 0 {
		return nil
	}
	// Deterministic order: by output good, then input, then the underlying kinds as
	// tie-breaks so two labels colliding still order stably across snapshots.
	sort.Slice(items, func(i, j int) bool {
		if items[i].OutputLabel != items[j].OutputLabel {
			return items[i].OutputLabel < items[j].OutputLabel
		}
		if items[i].OutputKind != items[j].OutputKind {
			return items[i].OutputKind < items[j].OutputKind
		}
		if items[i].InputLabel != items[j].InputLabel {
			return items[i].InputLabel < items[j].InputLabel
		}
		return items[i].InputKind < items[j].InputKind
	})
	sort.Slice(boosts, func(i, j int) bool {
		if boosts[i].OutputLabel != boosts[j].OutputLabel {
			return boosts[i].OutputLabel < boosts[j].OutputLabel
		}
		if boosts[i].OutputKind != boosts[j].OutputKind {
			return boosts[i].OutputKind < boosts[j].OutputKind
		}
		if boosts[i].BoostLabel != boosts[j].BoostLabel {
			return boosts[i].BoostLabel < boosts[j].BoostLabel
		}
		return boosts[i].BoostKind < boosts[j].BoostKind
	})
	return &ProductionInputsView{Items: items, Boosts: boosts}
}

// renderProductionInputs writes the "## Keeping up production" section. Content-
// gated: a nil/empty view writes nothing. Each line states the production
// dependency, the on-hand input count (count-led like "## Restocking", so no
// item-noun pluralization), the runway in output units, and the running-low flag.
// It deliberately carries no supplier, structure_id, or pay_with_item — the
// adjacent "## Restocking" section carries the buy (LLM-64 split).
func renderProductionInputs(b *strings.Builder, v *ProductionInputsView) {
	if v == nil || (len(v.Items) == 0 && len(v.Boosts) == 0) {
		return
	}
	b.WriteString("## Keeping up production\n")
	for _, it := range v.Items {
		fmt.Fprintf(b, "- You use %s to make %s — %d on hand, enough for about %d more, and you're running low.\n",
			sanitizeInline(it.InputLabel), sanitizeInline(it.OutputLabel), it.CurrentQty, it.RunwayUnits)
	}
	// Booster lines (LLM-248): elective, so no runway — the motivation is the
	// forgone bonus. Same no-supplier / no-tool-name discipline (LLM-64 split);
	// the adjacent "## Restocking" line carries the where/how.
	for _, bo := range v.Boosts {
		fmt.Fprintf(b, "- A measure of %s in each batch of %s adds %d extra to the yield — %d on hand, and you're running low.\n",
			sanitizeInline(bo.BoostLabel), sanitizeInline(bo.OutputLabel), bo.BonusQty, bo.CurrentQty)
	}
	b.WriteString("\n")
}
