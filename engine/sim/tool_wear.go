package sim

// tool_wear.go — LLM-330. Per-use durability for tool-kind recipe inputs,
// replacing the LLM-83 stopgap that modeled tools as inputs consumed once per
// batch (absurd for a single-serving recipe: fried_meat at output_qty 1 would
// have worn a whole skillet per meal).
//
// A kind whose catalog entry carries DurabilityUses > 0 is a durable tool. A
// recipe input of a durable kind is required ON HAND at produce start (the
// unchanged HasProduceInputs gate) but not consumed; instead the actor's
// per-kind wear counter decrements 1 per execution — a skillet lasts N cooks
// whether those cooks are one-at-a-time or batched. At 0 the in-use unit is
// spent: inventory decrements by 1 (delete-on-zero), and the next execution
// takes up a fresh unit at full durability. The spent unit re-enters the
// economy through the untouched restock loop — the count drop trips the
// reorder floor and the keeper rebuys from the smith.
//
// Wear is per KIND, not per instance — inventory has no item instances
// (map[ItemKind]int). Diegetically: the keeper cooks on their worn skillet and
// the spares on the shelf stay fresh. Selling stock never moves wear; the one
// corner (selling every unit while wear remains, then rebuying) carries the
// old wear onto the replacement and self-corrects within one tool's life.
// A wear entry above the kind's current durability (an operator retuned N
// down live) clamps at next use.

// DurableToolUses resolves kind's per-unit durability from the catalog:
// > 0 means the kind is a durable tool lasting that many produce executions.
// 0 for a plain consumable input, a kind absent from the catalog, or a nil
// map. Exported so perception (over snap.ItemKinds) and the produce command
// (over w.ItemKinds) share one definition of "is this input a tool".
func DurableToolUses(kinds map[ItemKind]*ItemKindDef, kind ItemKind) int {
	if def := kinds[kind]; def != nil && def.DurabilityUses > 0 {
		return def.DurabilityUses
	}
	return 0
}

// toolWearResult reports one execution's wear on one tool kind, for the
// produce tool-result narration (mechanical numbers belong in the result,
// not the deliberation scene).
type toolWearResult struct {
	Item     ItemKind
	UsesLeft int  // uses remaining on the in-use unit after this execution
	Spent    bool // this execution used the unit up (inventory already decremented)
	OnHand   int  // units still in inventory after any spend (includes the in-use one)
}

// applyToolWear wears the actor's in-use unit of a durable tool kind by one
// produce execution: a missing/zero wear entry means a fresh unit is taken up
// at full durability first (an entry above durability clamps — the live-retune
// case). At 0 the unit is spent — inventory decrements with the delete-on-zero
// invariant and the wear entry clears so the next execution starts a fresh
// unit. The caller (StartProductionCycle) has already verified the tool is on
// hand via HasProduceInputs; durability must be > 0 (the caller gates on
// DurableToolUses).
func applyToolWear(a *Actor, kind ItemKind, durability int) toolWearResult {
	if a.ToolWear == nil {
		a.ToolWear = make(map[ItemKind]int)
	}
	wear := a.ToolWear[kind]
	if wear <= 0 || wear > durability {
		wear = durability
	}
	wear--
	if wear <= 0 {
		delete(a.ToolWear, kind)
		a.Inventory[kind]--
		if a.Inventory[kind] <= 0 {
			delete(a.Inventory, kind)
		}
		return toolWearResult{Item: kind, UsesLeft: 0, Spent: true, OnHand: a.Inventory[kind]}
	}
	a.ToolWear[kind] = wear
	return toolWearResult{Item: kind, UsesLeft: wear, Spent: false, OnHand: a.Inventory[kind]}
}

// ToolRunwayUses is how many more produce executions the actor's stock of a
// durable tool kind can back: full durability for every spare unit plus the
// wear remaining on the in-use one (a missing/zero wear entry means no unit
// has been taken up — all units are fresh; an entry above durability clamps,
// matching applyToolWear). 0 when nothing is on hand. Shared by the
// "## Keeping up production" cue's tool runway so perception and the wear
// mechanics can't drift on what the stock is worth.
func ToolRunwayUses(onHand, wearRemaining, durability int) int {
	if onHand <= 0 || durability <= 0 {
		return 0
	}
	if wearRemaining <= 0 || wearRemaining > durability {
		wearRemaining = durability
	}
	return (onHand-1)*durability + wearRemaining
}
