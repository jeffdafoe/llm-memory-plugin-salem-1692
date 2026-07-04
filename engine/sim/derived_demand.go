package sim

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
// to DefaultDerivedDemandBatches worth. (Decision on the heuristic: LLM-260.)
func derivedInputCap(inputQty int, produceEntry RestockEntry, recipe *ItemRecipe) int {
	batches := DefaultDerivedDemandBatches
	if outputCap := produceEntry.Cap(); outputCap > 0 && recipe.OutputQty > 0 {
		batches = (outputCap + recipe.OutputQty - 1) / recipe.OutputQty
		if batches < 1 {
			batches = 1
		}
	}
	return inputQty * batches
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
