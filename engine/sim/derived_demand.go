package sim

import "math"

// derived_demand.go — LLM-260. Derives input-procurement demand from produce
// recipes, so an NPC that produces a good no longer needs a hand-authored `buy`
// restock entry mirroring every recipe input it does not self-source.
//
// Before this, `produce X` and `buy X's-inputs` were independent, hand-authored
// intents: nothing walked produce entry → recipe.Inputs, and when an input ran
// short the produce tick silently skipped (produce_tick.go, executions <= 0 —
// no mint, no signal), so a mis-wired producer looked fine and simply never made
// anything (the live Hannah Boggs porridge stall: a `porridge: produce` entry
// with no source at all for its milk + water inputs).
//
// The derivation is computed AT READ TIME, never persisted: the policy an
// operator authors stays exactly what they wrote, and a recipe hot-reload
// (SetRecipe) or policy edit is picked up on the next read with no
// re-materialization step. Explicit entries are OVERRIDES:
//   - a `produce`/`forage` entry for an input means "I self-source this"
//     (John Ellis produces his stew's water → no derived buy for water);
//   - an explicit `buy` entry for an input wins over the derived one, so an
//     operator can still tune the cap by hand (redundant but harmless).
//
// A no-input recipe (water, milk — conjured) contributes no demand; that falls
// out of the empty Inputs walk. Boost inputs (LLM-248) are deliberately NOT
// derived: production never stalls on an elective booster, so a booster is a
// hand-authored buy concern only.
//
// Consumers — the three buy-side restock-demand surfaces, plus the trade-good
// demotion:
//   - the restock warrant producer (restock_tick.go firstActionableLowEntry)
//   - the "## Restocking" cue (perception/restock.go buildRestocking)
//   - the "## Keeping up production" cue (perception/production_inputs.go)
//   - the LLM-134 own-merchandise demotion (ManagesEffective, via perception's
//     own-stock TradeStock flag)
//
// The wares-worth cue (perception/trade_value.go) deliberately stays on the
// EXPLICIT BuyEntries: it values goods the actor RESELLS, and a derived input
// is a production material, not a ware.

// DefaultDerivedDemandBatches is how many recipe executions a derived buy
// entry's cap covers when the produce entry it serves has no cap of its own to
// size against. Two batches keeps a buffer so the producer is not sent shopping
// after every single execution.
const DefaultDerivedDemandBatches = 2

// derivedInputCap sizes a derived buy entry's carry cap: enough of the input to
// run the batches that fill the produce entry's own output cap once —
// inputQty × ceil(outputCap / OutputQty) — floored at one batch. When the
// produce entry carries no cap there is no shelf size to fill, so it falls back
// to DefaultDerivedDemandBatches worth. int64 arithmetic + clamp guards a
// corrupt/imported catalog with huge caps or quantities from overflowing int —
// the same posture buildProductionInputs takes on the runway math. (Decision on
// the heuristic: LLM-260.)
func derivedInputCap(inputQty int, produceEntry RestockEntry, recipe *ItemRecipe) int {
	if inputQty <= 0 {
		return 0
	}
	batches := int64(DefaultDerivedDemandBatches)
	if outputCap := produceEntry.Cap(); outputCap > 0 && recipe.OutputQty > 0 {
		batches = (int64(outputCap) + int64(recipe.OutputQty) - 1) / int64(recipe.OutputQty)
		if batches < 1 {
			batches = 1
		}
	}
	v := int64(inputQty) * batches
	if v > int64(math.MaxInt32) {
		v = int64(math.MaxInt32)
	}
	return int(v)
}

// EffectiveBuyEntries returns the policy's buy-side demand: the explicit `buy`
// entries (policy order, verbatim) followed by the DERIVED entries — one per
// recipe input of the policy's produce entries that the actor neither
// self-sources (ProducesOrForages) nor already buys explicitly. Derived order
// follows produce-entry order then recipe-input order, so the result is
// deterministic without a sort. An input feeding two of the actor's recipes
// derives once, at the larger of the two caps (the bigger need governs).
//
// Nil-safe on both arguments: a nil policy yields nil, and a nil/incomplete
// recipe catalog simply derives nothing (the explicit entries still return).
// Callers on the world goroutine pass w.Recipes; perception passes
// snap.Recipes — same catalog type, so the warrant and the cues share this one
// definition and cannot disagree on what the actor's buy demand IS.
func EffectiveBuyEntries(recipes map[ItemKind]*ItemRecipe, p *RestockPolicy) []RestockEntry {
	if p == nil {
		return nil
	}
	out := p.BuyEntries()
	explicit := make(map[ItemKind]bool, len(out))
	for _, e := range out {
		explicit[e.Item] = true
	}
	derivedAt := map[ItemKind]int{}
	for _, pe := range p.ProduceEntries() {
		recipe := recipes[pe.Item]
		if recipe == nil {
			continue
		}
		for _, in := range recipe.Inputs {
			if in.Item == "" || in.Qty <= 0 {
				continue
			}
			if explicit[in.Item] || p.ProducesOrForages(in.Item) {
				continue // explicit buy wins; self-sourced inputs generate no buy demand
			}
			cap := derivedInputCap(in.Qty, pe, recipe)
			if i, ok := derivedAt[in.Item]; ok {
				if cap > out[i].Max {
					out[i].Max = cap
				}
				continue
			}
			out = append(out, RestockEntry{Item: in.Item, Source: RestockSourceBuy, Max: cap})
			derivedAt[in.Item] = len(out) - 1
		}
	}
	return out
}

// RestockInputBatchBuffer is how many whole recipe batches of a produce input a
// producer keeps on hand before the reorder threshold trips (LLM-279). Two means
// the reorder fires while one full batch still remains to feed production during
// the multi-minute supplier trip, so the input shelf never stalls mid-trip — the
// fix for the "dead production window before every rebuy" mode. One batch would
// only fire once the producer already cannot cover a batch (production already
// halted), which cures the permanent deadlock but not the per-cycle gap.
const RestockInputBatchBuffer = 2

// ReorderFloors returns, per item that is a REQUIRED input of one of the policy's
// produce recipes, the on-hand quantity below which that item must be reordered
// regardless of its cap fraction: RestockInputBatchBuffer × the largest per-batch
// draw across the recipes it feeds (an input feeding two recipes floors at the
// bigger draw — the tighter need governs). This is the produce-input floor
// LLM-279 hands RestockReorderThresholdMet so a chunky batch consumer reorders on
// "can I still cover a batch after this one" rather than a fraction of cap a batch
// draw skips clean over.
//
// Only required Inputs count. Elective BoostInputs never stall production, so they
// keep the plain cap-fraction rule (absent here → floor 0), as do pure-resale
// goods and foraged/gathered stock (they feed no produce recipe). A self-sourced
// input still lands in the map but is never a buy entry, so no buy-side gate ever
// reads its floor. Nil-safe on both arguments like EffectiveBuyEntries, and read
// by the warrant producer and both buy-side cues so they can't disagree on the
// floor. Returns nil when the actor produces nothing with inputs (a nil map indexes
// to 0, which callers pass straight through as "no floor").
func ReorderFloors(recipes map[ItemKind]*ItemRecipe, p *RestockPolicy) map[ItemKind]int {
	if p == nil {
		return nil
	}
	var floors map[ItemKind]int
	for _, pe := range p.ProduceEntries() {
		recipe := recipes[pe.Item]
		if recipe == nil {
			continue
		}
		for _, in := range recipe.Inputs {
			if in.Item == "" || in.Qty <= 0 {
				continue
			}
			// int64 + clamp guards a corrupt/imported catalog with a huge input qty
			// from overflowing int, the same posture derivedInputCap takes.
			v := int64(RestockInputBatchBuffer) * int64(in.Qty)
			if v > int64(math.MaxInt32) {
				v = int64(math.MaxInt32)
			}
			floor := int(v)
			if floors == nil {
				floors = make(map[ItemKind]int)
			}
			if floor > floors[in.Item] {
				floors[in.Item] = floor
			}
		}
	}
	return floors
}

// ProductionInputKinds returns the set of items the policy's own produce recipes
// REQUIRE and that the actor does not self-source — the goods it must procure from
// someone else to keep producing. This is exactly the DERIVED half of
// EffectiveBuyEntries, deliberately without the explicit `buy` rows.
//
// The distinction matters only to the LLM-477 wholesale grant, which is the one
// caller: an operator-authored buy entry for an unrelated good is trade stock or
// larder, not a production input, and must not widen a transformer's permission to
// buy straight from a wholesale source. Live case that forced the split — Ellis Farm
// (wholesaler-tagged) produces cheese/milk/meat and carries an explicit `buy: sage`
// row; sage feeds none of its recipes, so granting wholesale access to it would let
// one farm buy direct from another the moment a wholesaler happened to sell sage,
// which is the farms-eat-each-other loop the tier exists to prevent.
//
// Elective BoostInputs are excluded for the same reason ReorderFloors excludes them:
// production never stalls on a booster, so a booster is not a required input.
//
// Nil-safe on both arguments; returns nil when the actor produces nothing with
// inputs (a nil map indexes to false, which callers read as "not a production input").
func ProductionInputKinds(recipes map[ItemKind]*ItemRecipe, p *RestockPolicy) map[ItemKind]bool {
	if p == nil {
		return nil
	}
	var kinds map[ItemKind]bool
	for _, pe := range p.ProduceEntries() {
		recipe := recipes[pe.Item]
		if recipe == nil {
			continue
		}
		for _, in := range recipe.Inputs {
			if in.Item == "" || in.Qty <= 0 {
				continue
			}
			if p.ProducesOrForages(in.Item) {
				continue // self-sourced: never procured, so never granted
			}
			if kinds == nil {
				kinds = make(map[ItemKind]bool)
			}
			kinds[in.Item] = true
		}
	}
	return kinds
}

// ManagesEffective reports whether kind is one of the actor's trade goods under
// the EFFECTIVE policy — an explicit restock entry of any source (Manages), or
// a derived buy input of something it produces. The derived-aware sibling of
// RestockPolicy.Manages for the LLM-134 own-merchandise demotion: a producer's
// production materials should stay out of its casual "consume to eat" cue
// whether the buy entry was hand-authored (John's stew carrots) or derived
// (Hannah's porridge milk). Nil-safe like Manages.
func ManagesEffective(recipes map[ItemKind]*ItemRecipe, p *RestockPolicy, kind ItemKind) bool {
	if p.Manages(kind) {
		return true
	}
	for _, e := range EffectiveBuyEntries(recipes, p) {
		if e.Item == kind {
			return true
		}
	}
	return false
}
