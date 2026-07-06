package sim

import (
	"fmt"
	"strings"
)

// production_focus_commands.go — the crafter's "decide what to forge next"
// choice (LLM-116). A multi-output producer (e.g. the smith: skillet + nail) no
// longer auto-fills every produce entry in parallel; it sets a ProductionFocus
// and produce_tick fills only that item. This is the world side of the `craft`
// tool: it validates the chosen item is one the actor actually produces and
// records the focus. Single-output producers are unaffected — produce_tick
// ignores focus when there is only one produce entry.

// ProductionFocusResult is the command reply: the actor and the item now in
// focus, so tool dispatch can confirm the choice back to the model. Noun is the
// catalog plural display phrase for the focused good ("nails", "skillets"),
// resolved here on the world goroutine where the item catalog is in hand, so
// commitResultContent can name the choice without re-plumbing the catalog
// (LLM-120 craft steer). Falls back to the raw kind key for a discovery-minted
// kind that carries no phrase.
type ProductionFocusResult struct {
	ID    ActorID
	Focus ItemKind
	Noun  string
	// Switched is true when this call CHANGED the actor's active production item
	// (the previous focus differed from Focus). A no-switch produce — the model
	// re-issuing produce(currentItem), a "tend your post" no-op — is terminal-on-
	// success in the harness dispatch (LLM-201): the tick ends so the wasted second
	// agentic round, and the malformed tool output that rides it, never happen. A
	// genuine switch (Switched=true) stays non-terminal so the actor may speak/act/
	// done() after choosing.
	Switched bool
}

// SetProductionFocus records the item a crafter will forge next. The item must
// resolve in the catalog AND be one the actor carries a `produce` restock entry
// for — a crafter can only focus on something it knows how to make. A bad item
// returns a ModelFacingError so the acting NPC learns what went wrong within its
// tick budget. PCs are rejected (ErrActorNotFound); production is an NPC concept.
func SetProductionFocus(id ActorID, itemName string) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			a, err := editableNPC(w, id)
			if err != nil {
				return nil, err
			}
			kind, ok := resolveItemKind(w, itemName)
			if !ok {
				return nil, ModelFacingError{Msg: fmt.Sprintf("you don't know how to make %q", itemName)}
			}
			chosen, ok := produceEntry(a, kind)
			if !ok {
				return nil, ModelFacingError{Msg: fmt.Sprintf("you don't make %s at your workplace — focus only on what you produce", kind)}
			}
			// Must be MAKEABLE (recipe with positive rate), not just on the produce
			// policy — else focus would set to a good produce_tick can never make.
			// Same gate the forge cue and the wake producer use. (Inputs are NOT
			// required — an origin producer like nail is makeable from nothing.)
			if !makeableRecipe(w, kind) {
				return nil, ModelFacingError{Msg: fmt.Sprintf("you can't make %s right now", kind)}
			}
			// Worth-choosing guard (LLM-257): a focus worth setting must be
			// craftable RIGHT NOW — makeable, below cap, AND its inputs on hand
			// (craftableNow, the exact "worth choosing" test shouldChooseProduction
			// and the forge cue use, so the tool and the wake warrant can't disagree).
			// If the chosen good isn't craftable but ANOTHER good is, reject and steer
			// there: a focus that forges nothing (at cap, or input-short) just re-wakes
			// the crafter every minute and, for a multi-output crafter, starves the
			// goods it CAN make (John Ellis locked on a sage-less stew, unable to mint
			// even no-input water). When nothing else is craftable either, allow the
			// pick — there is nothing better (the all-at-cap / all-starved escape).
			if !craftableNow(w, a, chosen) {
				// Name the goods the crafter CAN make now (LLM-300) instead of a bare
				// "make something else" — a copyable next action for the weak model. An
				// empty list means nothing else is craftable either: fall through and
				// allow the pick (the all-at-cap / all-starved escape above).
				if alternatives := otherCraftableNouns(a, w, kind); len(alternatives) > 0 {
					// Name the exact good when there's only one ("call produce with
					// nails"); use "one of those" for a multi-good list. Either way the
					// tail is a copyable produce argument, not a bare "make something else".
					tail := "one of those"
					if len(alternatives) == 1 {
						tail = alternatives[0]
					}
					steer := fmt.Sprintf("you can make %s now — call produce with %s.", joinWithOr(alternatives), tail)
					// Branch the message on the ACTUAL failing predicate rather than
					// inferring "not at cap ⇒ input-short": name the missing inputs only
					// when inputs are truly the problem; otherwise makeableRecipe passed
					// and the inputs are in hand, so the block is the carry cap.
					if recipe := w.Recipes[kind]; !HasProduceInputs(recipe, a.Inventory) {
						return nil, ModelFacingError{Msg: fmt.Sprintf("you can't make %s right now — you're %s; %s", kind, describeMissingInputs(recipe, a.Inventory), steer)}
					}
					return nil, ModelFacingError{Msg: fmt.Sprintf("you already have all the %s you can hold — %s", kind, steer)}
				}
			}
			switched := a.ProductionFocus != kind
			a.ProductionFocus = kind
			return ProductionFocusResult{ID: id, Focus: kind, Noun: producePluralNoun(w, kind), Switched: switched}, nil
		},
	}
}

// produceEntry returns the actor's produce RestockEntry for kind, and whether one
// exists (`ok` false = the actor doesn't produce it). SetProductionFocus uses `ok`
// as its "do you make this?" gate, so the entry lookup and the existence check are
// one pass.
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
// cap, inputs on hand), in produce-entry order. SetProductionFocus uses it to steer
// a rejected stuck focus onto named legal alternatives (LLM-300); an empty result
// means nothing else is craftable either, so the stuck pick is allowed (the
// all-at-cap / all-starved escape). Supersedes the boolean anyOtherCraftable — the
// same craftableNow pass, keeping the names instead of reducing them to a flag.
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
			nouns = append(nouns, producePluralNoun(w, e.Item))
		}
	}
	return nouns
}

// producePluralNoun resolves kind's catalog plural display phrase ("nails",
// "loaves of bread"), falling back to the raw kind key for a discovery-minted kind
// that carries no phrase. Shared by the ProductionFocusResult confirmation and the
// LLM-300 alternatives steer so both name a good the same way.
func producePluralNoun(w *World, kind ItemKind) string {
	if def := w.ItemKinds[kind]; def != nil {
		if p := def.Plural(); p != "" {
			return p
		}
	}
	return string(kind)
}

// describeMissingInputs renders the recipe inputs the actor is short of as a
// model-facing phrase for a craft-tool rejection, e.g. "short of sage (need 2,
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
