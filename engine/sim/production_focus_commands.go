package sim

import "fmt"

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
			if !producesItem(a, kind) {
				return nil, ModelFacingError{Msg: fmt.Sprintf("you don't make %s at your workplace — focus only on what you produce", kind)}
			}
			// Must be MAKEABLE (recipe with positive rate), not just on the produce
			// policy — else focus would set to a good produce_tick can never make.
			// Same gate the forge cue and the wake producer use. (Inputs are NOT
			// required — an origin producer like nail is makeable from nothing.)
			if !makeableRecipe(w, kind) {
				return nil, ModelFacingError{Msg: fmt.Sprintf("you can't make %s right now", kind)}
			}
			// At-cap guard: focusing a good you already hold to its cap produces
			// nothing (no headroom) and would just re-wake you next minute. If
			// another makeable good is still below its cap, reject and steer there.
			// When EVERY makeable good is at cap there's nothing better to pick, so
			// the focus is allowed (you'll make more once a sale frees headroom).
			if chosenAtCap, otherBelowCap := capStatusForFocus(a, w, kind); chosenAtCap && otherBelowCap {
				return nil, ModelFacingError{Msg: fmt.Sprintf("you already have all the %s you can hold — make something that's still needed", kind)}
			}
			a.ProductionFocus = kind
			noun := string(kind)
			if def := w.ItemKinds[kind]; def != nil {
				if p := def.Plural(); p != "" {
					noun = p
				}
			}
			return ProductionFocusResult{ID: id, Focus: kind, Noun: noun}, nil
		},
	}
}

// capStatusForFocus reports, for a chosen makeable good, whether the actor is
// already at that good's cap, and whether any OTHER makeable produce good is still
// below its cap. SetProductionFocus uses it to reject focusing a full good while
// something else still needs making (LLM-116). An uncapped entry (cap <= 0) is
// never "at cap".
func capStatusForFocus(a *Actor, w *World, kind ItemKind) (chosenAtCap, otherBelowCap bool) {
	if a.RestockPolicy == nil {
		return false, false
	}
	for _, e := range a.RestockPolicy.ProduceEntries() {
		if !makeableRecipe(w, e.Item) {
			continue
		}
		cap := e.Cap()
		belowCap := cap <= 0 || a.Inventory[e.Item] < cap
		if e.Item == kind {
			chosenAtCap = !belowCap
		} else if belowCap {
			otherBelowCap = true
		}
	}
	return chosenAtCap, otherBelowCap
}

// producesItem reports whether the actor carries a produce-source restock entry
// for kind.
func producesItem(a *Actor, kind ItemKind) bool {
	if a == nil || a.RestockPolicy == nil {
		return false
	}
	for _, e := range a.RestockPolicy.ProduceEntries() {
		if e.Item == kind {
			return true
		}
	}
	return false
}
