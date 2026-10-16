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
	return actions
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
