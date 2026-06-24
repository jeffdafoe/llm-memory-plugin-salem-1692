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
// A nil view (or empty Items) means render omits the section.
type ProductionInputsView struct {
	Items []ProductionInputView
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
func buildProductionInputs(snap *sim.Snapshot, actorSnap *sim.ActorSnapshot) *ProductionInputsView {
	if snap == nil || actorSnap == nil || actorSnap.RestockPolicy == nil || snap.Recipes == nil {
		return nil
	}
	pct := snap.RestockReorderPct
	if pct <= 0 {
		return nil // producer/feature disabled
	}
	// The items the actor restocks by buying, and their caps — the gate for which
	// inputs are a buy-restock concern (a self-produced input has no buy entry and
	// is excluded). Mirrors the set "## Restocking" works from.
	buyCaps := map[sim.ItemKind]int{}
	for _, e := range actorSnap.RestockPolicy.BuyEntries() {
		buyCaps[e.Item] = e.Cap()
	}
	if len(buyCaps) == 0 {
		return nil
	}
	var items []ProductionInputView
	for _, pe := range actorSnap.RestockPolicy.ProduceEntries() {
		recipe := snap.Recipes[pe.Item]
		if recipe == nil || recipe.OutputQty <= 0 {
			continue
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
			if !sim.RestockReorderThresholdMet(current, cap, pct) {
				continue // not low yet — the same gate Restocking uses
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
	if len(items) == 0 {
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
	return &ProductionInputsView{Items: items}
}

// renderProductionInputs writes the "## Keeping up production" section. Content-
// gated: a nil/empty view writes nothing. Each line states the production
// dependency, the on-hand input count (count-led like "## Restocking", so no
// item-noun pluralization), the runway in output units, and the running-low flag.
// It deliberately carries no supplier, structure_id, or pay_with_item — the
// adjacent "## Restocking" section carries the buy (LLM-64 split).
func renderProductionInputs(b *strings.Builder, v *ProductionInputsView) {
	if v == nil || len(v.Items) == 0 {
		return
	}
	b.WriteString("## Keeping up production\n")
	for _, it := range v.Items {
		fmt.Fprintf(b, "- You use %s to make %s — %d on hand, enough for about %d more, and you're running low.\n",
			sanitizeInline(it.InputLabel), sanitizeInline(it.OutputLabel), it.CurrentQty, it.RunwayUnits)
	}
	b.WriteString("\n")
}
