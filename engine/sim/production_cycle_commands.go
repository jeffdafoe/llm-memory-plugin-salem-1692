package sim

import (
	"fmt"
	"strings"
	"time"
)

// production_cycle_commands.go — the world side of the `produce` tool
// (LLM-116, redesigned in LLM-319). One `produce` call starts exactly ONE
// production cycle: it validates the choice, consumes the recipe inputs up
// front (the milk goes in the pot when the pot goes on the fire — the
// StartRepair nails posture, no refund), and opens the actor's single
// ProductionActivity window. The batch lands when ApplyProduceTick has
// credited the full cycle's work; making more takes another decision.
//
// The continuous ProductionFocus ("keep making it until you choose again") is
// retired with the auto-produce tick it steered.

// ProductionStartResult is the command reply: what was begun, sized and
// phrased world-side (where the item catalog is in hand) so the harness can
// narrate the confirmation without re-plumbing the catalog. InputsUsed is the
// pre-phrased consumed-inputs clause ("3 milk and 5 water"); empty for an
// origin producer whose recipe has no inputs. ToolWear is the pre-phrased
// durable-tool wear clause (LLM-330, "Your skillet has about 12 more uses in
// it"); empty when the recipe uses no durable tool.
type ProductionStartResult struct {
	ID              ActorID
	Item            ItemKind
	Noun            string
	BatchQty        int
	DurationSeconds int64
	InputsUsed      string
	ToolWear        string
}

// StartProductionCycle validates and begins one production cycle of itemName
// for the actor. The item must resolve in the catalog, be one the actor
// carries a `produce` restock entry for, be makeable (recipe with positive
// rate), and be craftable right now (below cap, inputs on hand — with the
// LLM-300 steer toward named alternatives when it isn't). Inputs are consumed
// here, at start; the mint lands at cycle completion (landProductionCycle).
// Bad calls return a ModelFacingError the acting NPC can learn from within its
// tick budget. PCs are rejected (ErrActorNotFound); production is an NPC
// concept.
func StartProductionCycle(id ActorID, itemName string) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			a, err := editableNPC(w, id)
			if err != nil {
				return nil, err
			}
			// One cycle at a time — and the check precedes item resolution so a
			// mid-cycle produce call gets the "already making" steer whatever it
			// named. The tool isn't offered while a cycle runs; the substrate
			// stays authoritative for a call that arrives anyway.
			if act := a.ProductionActivity; act != nil {
				return nil, ModelFacingError{Msg: fmt.Sprintf(
					"you are already making %s — about %s of work left at your post. It finishes on its own; you can't start another batch until it does.",
					ProducePluralNoun(w, act.Item), HumanizeWorkDuration(act.RemainingSeconds),
				)}
			}
			if a.MoveIntent != nil {
				return nil, ModelFacingError{Msg: "you are walking — arrive at your workplace before starting a batch."}
			}
			if a.WorkStructureID == "" || a.InsideStructureID != a.WorkStructureID {
				return nil, ModelFacingError{Msg: "you can only produce at your own workplace — go there first."}
			}
			// LLM-304: a degraded business is shut for refill — a batch can't start
			// until the owner mends it (the produce tick would pause it anyway;
			// rejecting at start keeps the inputs out of a stalled pot).
			if ownerStallDegraded(w, id) {
				return nil, ModelFacingError{Msg: "your business is too worn to make anything — mend it first."}
			}
			kind, ok := resolveItemKind(w, itemName)
			if !ok {
				return nil, ModelFacingError{Msg: fmt.Sprintf("you don't know how to make %q", itemName)}
			}
			chosen, ok := produceEntry(a, kind)
			if !ok {
				return nil, ModelFacingError{Msg: fmt.Sprintf("you don't make %s at your workplace — produce only what your trade makes", kind)}
			}
			// Must be MAKEABLE (recipe with positive rate), not just on the produce
			// policy. Same gate the trade cue and the wake producer use.
			if !makeableRecipe(w, kind) {
				return nil, ModelFacingError{Msg: fmt.Sprintf("you can't make %s right now", kind)}
			}
			recipe := w.Recipes[kind]
			// Craftable-now guard (LLM-257/LLM-300): the batch must be startable
			// RIGHT NOW — below cap AND its inputs on hand. Unlike the old focus
			// (where an uncraftable pick could be allowed as a standing intent),
			// one-shot has no intent to record: an uncraftable start is always a
			// rejection, steered toward the goods the actor CAN make when any exist.
			if !craftableNow(w, a, chosen) {
				steer := ""
				if alternatives := otherCraftableNouns(a, w, kind); len(alternatives) > 0 {
					// Name the exact good when there's only one ("call produce with
					// nails"); use "one of those" for a multi-good list. Either way the
					// tail is a copyable produce argument, not a bare "make something else".
					tail := "one of those"
					if len(alternatives) == 1 {
						tail = alternatives[0]
					}
					steer = fmt.Sprintf(" You can make %s now — call produce with %s.", joinWithOr(alternatives), tail)
				}
				// Branch the message on the ACTUAL failing predicate rather than
				// inferring "not at cap ⇒ input-short": name the missing inputs only
				// when inputs are truly the problem; otherwise makeableRecipe passed
				// and the inputs are in hand, so the block is the carry cap.
				if !HasProduceInputs(recipe, a.Inventory) {
					return nil, ModelFacingError{Msg: fmt.Sprintf("you can't make %s right now — you're %s.%s", kind, describeMissingInputs(recipe, a.Inventory), steer)}
				}
				// Inputs are in hand, so the block is headroom: a whole batch must
				// fit under the carry cap (craftableNow's batchFitsCap).
				return nil, ModelFacingError{Msg: fmt.Sprintf("your stores of %s are too full to fit another batch.%s", kind, steer)}
			}
			duration := CycleDurationSeconds(recipe)
			if duration <= 0 {
				// makeableRecipe guarantees positive rates, so this is a defensive
				// impossibility, not a model-facing state.
				return nil, fmt.Errorf("StartProductionCycle: zero cycle duration for makeable %q", kind)
			}
			// Consume the inputs up front (delete-on-zero inventory invariant).
			// craftableNow already confirmed they're all on hand. The narration
			// phrase resolves catalog counting nouns ("3 pails of milk"), not raw
			// kind keys — it is model-facing via the tool result (code_review).
			// A durable-tool input (catalog DurabilityUses > 0, LLM-330) is not
			// consumed: it wears 1 use per execution instead, and its clause
			// rides the result separately so the model reads wear, not spend.
			var used []string
			var wornTools []string
			for _, in := range recipe.Inputs {
				if in.Qty <= 0 {
					continue
				}
				if durability := DurableToolUses(w.ItemKinds, in.Item); durability > 0 {
					wornTools = append(wornTools, toolWearPhrase(w, applyToolWear(a, in.Item, durability, in.Qty)))
					continue
				}
				a.Inventory[in.Item] -= in.Qty
				if a.Inventory[in.Item] <= 0 {
					delete(a.Inventory, in.Item)
				}
				used = append(used, inputCountPhrase(w, in.Item, in.Qty))
			}
			batchQty := recipeBatchQty(recipe)
			now := time.Now().UTC()
			a.ProductionActivity = &ProductionActivity{
				Item:             kind,
				BatchQty:         batchQty,
				RemainingSeconds: duration,
				LastProgressAt:   now,
			}
			w.emit(&ProductionCycleStarted{
				ActorID:         id,
				Item:            kind,
				BatchQty:        batchQty,
				DurationSeconds: duration,
				At:              now,
			})
			return ProductionStartResult{
				ID:              id,
				Item:            kind,
				Noun:            ProducePluralNoun(w, kind),
				BatchQty:        batchQty,
				DurationSeconds: duration,
				InputsUsed:      joinWithAnd(used),
				ToolWear:        strings.Join(wornTools, " "),
			}, nil
		},
	}
}

// produceEntry returns the actor's produce RestockEntry for kind, and whether one
// exists (`ok` false = the actor doesn't produce it). StartProductionCycle uses
// `ok` as its "do you make this?" gate, so the entry lookup and the existence
// check are one pass.
func produceEntry(a *Actor, kind ItemKind) (RestockEntry, bool) {
	if a.RestockPolicy == nil {
		return RestockEntry{}, false
	}
	for _, e := range a.RestockPolicy.ProduceEntries() {
		if e.Item == kind {
			return e, true
		}
	}
	return RestockEntry{}, false
}

// otherCraftableNouns returns the catalog plural display nouns of the actor's
// produce goods OTHER than `kind` that are craftable right now (makeable, below
// cap, inputs on hand), in produce-entry order. StartProductionCycle uses it to
// steer a rejected start onto named legal alternatives (LLM-300); an empty
// result means nothing else is craftable either.
func otherCraftableNouns(a *Actor, w *World, kind ItemKind) []string {
	if a.RestockPolicy == nil {
		return nil
	}
	var nouns []string
	for _, e := range a.RestockPolicy.ProduceEntries() {
		if e.Item == kind {
			continue
		}
		if craftableNow(w, a, e) {
			nouns = append(nouns, ProducePluralNoun(w, e.Item))
		}
	}
	return nouns
}

// ProducePluralNoun resolves kind's catalog plural display phrase ("nails",
// "loaves of bread"), falling back to the raw kind key for a discovery-minted kind
// that carries no phrase. Shared by the ProductionStartResult confirmation, the
// LLM-300 alternatives steer, and the completion narration so every surface
// names a good the same way.
func ProducePluralNoun(w *World, kind ItemKind) string {
	if def := w.ItemKinds[kind]; def != nil {
		if p := def.Plural(); p != "" {
			return p
		}
	}
	return string(kind)
}

// describeMissingInputs renders the recipe inputs the actor is short of as a
// model-facing phrase for a produce-tool rejection, e.g. "short of sage (need 2,
// have 0), meat (need 10, have 4)". Only shortfalls are listed; an input held in
// full is omitted. The caller gates on !HasProduceInputs, so there is normally at
// least one shortfall.
func describeMissingInputs(recipe *ItemRecipe, inventory map[ItemKind]int) string {
	if recipe == nil {
		return "missing inputs"
	}
	var parts []string
	for _, in := range recipe.Inputs {
		if in.Qty <= 0 {
			continue
		}
		if have := inventory[in.Item]; have < in.Qty {
			parts = append(parts, fmt.Sprintf("%s (need %d, have %d)", in.Item, in.Qty, have))
		}
	}
	if len(parts) == 0 {
		return "missing inputs"
	}
	return "short of " + strings.Join(parts, ", ")
}

// toolWearPhrase renders one execution's durable-tool wear (LLM-330) as a
// model-facing result clause. The mechanical numbers belong here, in the
// post-decision result, not the deliberation scene: "Your skillet has about
// 12 more uses in it." — or, when the unit gave out with this use — "Your
// skillet gives out with this batch — you have a spare." / "…— that was your
// last one." The kind's singular counting noun keeps the exact item name a
// rebuy decision needs.
func toolWearPhrase(w *World, wear toolWearResult) string {
	noun := string(wear.Item)
	if def := w.ItemKinds[wear.Item]; def != nil {
		if s := def.Singular(); s != "" {
			noun = s
		}
	}
	if wear.Spent {
		if wear.OnHand > 0 {
			return fmt.Sprintf("Your %s gives out with this batch — you have a spare.", noun)
		}
		return fmt.Sprintf("Your %s gives out with this batch — that was your last one.", noun)
	}
	return fmt.Sprintf("Your %s has about %d more uses in it.", noun, wear.UsesLeft)
}

// inputCountPhrase renders one consumed input as a counted catalog phrase —
// "3 pails of milk", "1 sage" — via ItemKindDef.CountNoun, falling back to the
// raw kind key for a discovery-minted kind with no catalog entry.
func inputCountPhrase(w *World, kind ItemKind, qty int) string {
	if def := w.ItemKinds[kind]; def != nil {
		if noun := def.CountNoun(qty); noun != "" {
			return fmt.Sprintf("%d %s", qty, noun)
		}
	}
	return fmt.Sprintf("%d %s", qty, kind)
}

// joinWithAnd renders a slice as an "and"-terminated list: "a", "a and b",
// "a, b, and c". The consumed-inputs sibling of joinWithOr.
func joinWithAnd(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + ", and " + parts[len(parts)-1]
	}
}

// HumanizeWorkDuration phrases base-rate work seconds for model-facing text —
// "about 20 minutes", "about an hour", "about an hour and a half", "about 2
// hours". Coarse on purpose: hired help (LLM-224) shortens the real wall time,
// so precision would be false anyway.
func HumanizeWorkDuration(seconds int64) string {
	minutes := (seconds + 30) / 60
	if minutes < 1 {
		minutes = 1
	}
	if minutes < 60 {
		if minutes == 1 {
			return "a minute"
		}
		return fmt.Sprintf("%d minutes", minutes)
	}
	// Round to the nearest quarter hour and phrase it the way a person would.
	quarters := (minutes + 7) / 15
	hours := quarters / 4
	lead := fmt.Sprintf("%d hours", hours)
	if hours == 1 {
		lead = "an hour"
	}
	switch quarters % 4 {
	case 1:
		return lead + " and a quarter"
	case 2:
		return lead + " and a half"
	case 3:
		return lead + " and three quarters"
	default:
		return lead
	}
}
