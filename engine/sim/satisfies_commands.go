package sim

import "fmt"

// satisfies_commands.go — live item-satiation (item_satisfies) editing support
// (LLM-119). The sister of recipe_commands.go for the OTHER half of the item
// catalog: how much consuming one unit of an item eases a need
// (ItemKindDef.Satisfies). Same reference-data posture as recipes — the
// in-memory catalog is rebuilt from item_kind + item_satisfies at load / SIGHUP,
// the durable write to item_satisfies lives in the pg repo (no checkpoint path),
// and these two helpers cover the sim-side halves the umbilical
// /item/set-satisfies route needs: catalog-reference validation
// (ResolveSatisfaction) and the in-memory catalog update (SetItemSatisfaction).

// ResolveSatisfaction validates an item reference against World.ItemKinds and
// returns its canonical catalog key, so "Coca Tea", "coca tea", and "coca_tea"
// all land on the canonical key (resolveItemKind). The umbilical
// /item/set-satisfies route runs this on the world goroutine BEFORE the durable
// item_satisfies write, so a satiation row can only ever reference an item that
// already exists (no new-item creation via this path). Returns a wrapped
// ErrUnknownItemKind naming the offending item.
//
// Numeric validation (positive amount) and need-key validation (FindNeed) are
// the caller's concern — this is purely the catalog-reference check.
func ResolveSatisfaction(w *World, item string) (ItemKind, error) {
	kind, ok := resolveItemKind(w, item)
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownItemKind, item)
	}
	return kind, nil
}

// SetItemSatisfaction upserts one immediate need-ease entry into the live
// in-memory catalog (the ItemKindDef.Satisfies slice perception + consume read)
// and returns the applied entry. It is the in-memory half of an edit; the
// durable item_satisfies write happens separately (the item-kinds repo) and is
// the source of truth the catalog rebuilds from on restart.
//
// Keyed on (kind, attribute): an existing attribute entry has only its Immediate
// magnitude replaced — the dwell triple (DwellAmount / DwellPeriodMinutes /
// DwellTotalTicks) is PRESERVED, mirroring the DB upsert that touches only the
// amount column — while a new attribute appends a fresh immediate-only entry.
//
// ItemKindDef is documented read-only once it lands in World.ItemKinds, so this
// installs a CLONE with its own Satisfies slice and swaps the map pointer (the
// SetRecipe store-a-fresh-copy posture) rather than mutating a def a concurrent
// snapshot reader may be holding. kind must already exist (ResolveSatisfaction
// guarantees this on the umbilical path); a missing kind is a wrapped
// ErrUnknownItemKind.
func SetItemSatisfaction(kind ItemKind, attr NeedKey, amount int) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			def, ok := w.ItemKinds[kind]
			if !ok {
				return ItemSatisfaction{}, fmt.Errorf("%w: %q", ErrUnknownItemKind, kind)
			}
			clone := *def
			clone.Satisfies = make([]ItemSatisfaction, len(def.Satisfies))
			copy(clone.Satisfies, def.Satisfies)

			var applied ItemSatisfaction
			updated := false
			for i := range clone.Satisfies {
				if clone.Satisfies[i].Attribute == attr {
					clone.Satisfies[i].Immediate = amount
					applied = clone.Satisfies[i]
					updated = true
					break
				}
			}
			if !updated {
				applied = ItemSatisfaction{Attribute: attr, Immediate: amount}
				clone.Satisfies = append(clone.Satisfies, applied)
			}
			w.ItemKinds[kind] = &clone
			return applied, nil
		},
	}
}
