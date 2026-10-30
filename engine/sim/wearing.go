package sim

import (
	"errors"
	"slices"
	"time"
)

// wearing.go — item actions (LLM-800): what a player can DO with a good in the
// Your Pack list. /pc/me carries ItemActions per pack row and the client draws
// one button per action; POST /pc/item/use runs UseItem. A new action is a
// capability on the item kinds, a case in ItemActions, and a case in UseItem.
//
// The first two are Wear and Remove on a pendant (CapabilityWear). Wearing a
// duck pendant brings a duck familiar (familiar.go).

// CapabilityWear marks a good that can be put on and taken off from the pack —
// the pendants (migration LLM-800). Not a garment: clothes are dressed in the
// character creator, and their wear-and-tear is GarmentWear.
const CapabilityWear = "wear"

// The item-action slugs. The client knows only the button text for each.
const (
	ItemActionWear   = "wear"
	ItemActionRemove = "remove"
	// The player's tent (LLM-798, camp.go): pitched from the pack, struck from
	// the pitched row.
	ItemActionPitch  = "pitch"
	ItemActionStrike = "strike"
	// Food and drink (LLM-888): one unit from the pack, through Consume. A good
	// offers the one its ConsumeVerb names.
	ItemActionEat   = "eat"
	ItemActionDrink = "drink"
)

// ErrItemAction is the base of every refused item action. The wrapped message
// is player-facing: the pack shows it as it is.
var ErrItemAction = errors.New("item action refused")

type itemActionError struct{ msg string }

func (e *itemActionError) Error() string { return e.msg }
func (e *itemActionError) Unwrap() error { return ErrItemAction }

func refuseItemAction(msg string) error { return &itemActionError{msg: msg} }

// ItemActions lists the actions open on one pack row: the good's catalog
// entry and whether the actor has it on. Never nil, so the wire carries [].
func ItemActions(def *ItemKindDef, wearing bool) []string {
	actions := []string{}
	if def.HasCapability(CapabilityWear) {
		if wearing {
			actions = append(actions, ItemActionRemove)
		} else {
			actions = append(actions, ItemActionWear)
		}
	}
	// Only the tent itself pitches: useCampItem and PitchTent work the canonical
	// tent kind, so another shelter-capable good shows no button it cannot use.
	if def.Name == TentItemKind && def.HasCapability(CapabilityShelter) {
		actions = append(actions, ItemActionPitch)
	}
	if def.Consumable() {
		if def.IsDrink() {
			actions = append(actions, ItemActionDrink)
		} else {
			actions = append(actions, ItemActionEat)
		}
	}
	return actions
}

// eatFromPack consumes one unit of a pack good for a player (LLM-888). The
// refusals come first so Consume never sees a walking actor (its error is
// worded for an NPC's tool call) and a sated one never wastes a unit — a
// one-unit Consume eats it even at zero need.
func eatFromPack(w *World, a *Actor, def *ItemKindDef, kind ItemKind, now time.Time) error {
	verb := ConsumeVerb(def)
	if a.MoveIntent != nil {
		return refuseItemAction("Stop walking to " + verb + ".")
	}
	if !packGoodEasesANeed(a, def) {
		return refuseItemAction(notNeededLine(def))
	}
	if _, err := Consume(a.ID, string(kind), 1, now).Fn(w); err != nil {
		return err
	}
	// Push the new needs to the player's top bar at once, like eating in place
	// (applyObjectRefreshEffect).
	if a.Kind == KindPC {
		needs := make(map[NeedKey]int, len(a.Needs))
		for k, n := range a.Needs {
			needs[k] = n
		}
		w.emit(&PCNeedsChanged{ActorID: a.ID, Needs: needs})
	}
	return nil
}

// packGoodEasesANeed reports whether any need the good satisfies is above zero.
func packGoodEasesANeed(a *Actor, def *ItemKindDef) bool {
	for _, s := range def.Satisfies {
		if a.Needs[s.Attribute] > 0 {
			return true
		}
	}
	return false
}

// notNeededLine is the refusal when nothing the good eases is felt: a drink
// answers the Drink button with thirst even when it also fills the belly, food
// by the first need it eases.
func notNeededLine(def *ItemKindDef) string {
	if def.IsDrink() {
		return "You are not thirsty."
	}
	switch def.Satisfies[0].Attribute {
	case "hunger":
		return "You are not hungry."
	case "thirst":
		return "You are not thirsty."
	}
	return "You have no need of it now."
}

// actorWears reports whether a has kind on.
func actorWears(a *Actor, kind ItemKind) bool {
	return slices.Contains(a.Wearing, kind)
}

// UseItem runs one item action for an actor. A refusal is ErrItemAction with a
// message the pack can show. MUST be run on the world goroutine.
func UseItem(actorID ActorID, kind ItemKind, action string, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			a := w.Actors[actorID]
			if a == nil {
				return nil, ErrPCNotFound
			}
			if handled, err := useCampItem(w, a, kind, action, now); handled {
				return nil, err
			}
			def := w.ItemKinds[kind]
			if a.Inventory[kind] <= 0 || def == nil {
				return nil, refuseItemAction("You are not carrying that.")
			}
			if !slices.Contains(ItemActions(def, actorWears(a, kind)), action) {
				return nil, refuseItemAction("You cannot do that with it.")
			}
			switch action {
			case ItemActionEat, ItemActionDrink:
				return nil, eatFromPack(w, a, def, kind, now)
			case ItemActionWear:
				a.Wearing = append(a.Wearing, kind)
			case ItemActionRemove:
				a.Wearing = slices.DeleteFunc(a.Wearing, func(k ItemKind) bool { return k == kind })
				if len(a.Wearing) == 0 {
					a.Wearing = nil
				}
			}
			reconcileFamiliar(w, a, now, true)
			return nil, nil
		},
	}
}

// dropUnheldWearing takes off any kind a no longer holds — the pendant was
// sold, given away or spent. Run by the familiar ticker, so a sale needs no
// hook in the pay paths; the duck leaves on the next pass.
func dropUnheldWearing(a *Actor) {
	if len(a.Wearing) == 0 {
		return
	}
	a.Wearing = slices.DeleteFunc(a.Wearing, func(k ItemKind) bool { return a.Inventory[k] <= 0 })
	if len(a.Wearing) == 0 {
		a.Wearing = nil
	}
}
