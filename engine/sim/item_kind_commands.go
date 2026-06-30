package sim

// item_kind_commands.go — live item-DEFINITION editing support (LLM-200). The
// third leg of the catalog-edit triad alongside recipe_commands.go (the produce
// rate / inputs / price) and satisfies_commands.go (the per-need satiation): this
// one covers the item_kind row itself — label, category, sort order,
// capabilities, counting nouns, dwell narration. Same reference-data posture as
// its siblings: the in-memory catalog (World.ItemKinds) rebuilds from item_kind +
// item_satisfies at load / SIGHUP, the durable write to item_kind lives in the pg
// repo (UpsertItemKind, no checkpoint path), and this is the in-memory half the
// umbilical /item/set route applies after the durable write succeeds.
//
// Unlike recipe/set and item/set-satisfies, item/set can CREATE a kind that does
// not exist yet — that is the whole point, the all-live new-good flow — so there
// is no Resolve precondition: the name IS the new catalog key.

// SetItemKind installs an item-kind definition into the live in-memory catalog
// (World.ItemKinds) and returns the stored def. It is the in-memory half of an
// /item/set edit; the durable item_kind write (the item-kinds repo) is the source
// of truth the catalog rebuilds from on restart and runs first.
//
// On an UPDATE (the key already exists) the existing entry's Satisfies slice is
// PRESERVED: item/set carries only the definitional columns, the per-need
// satiation lives in the separate item_satisfies table (edited via
// /item/set-satisfies) and the durable item_kind upsert leaves it untouched — so
// the live catalog must not drop it either, or editing a stew's label would
// silently zero its hunger value until the next reload. On an INSERT there is no
// prior entry, so Satisfies starts empty.
//
// ItemKindDef is documented read-only once it lands in World.ItemKinds, so this
// stores a fresh copy with its own Satisfies slice and swaps the map pointer (the
// SetRecipe / SetItemSatisfaction store-a-clone posture) rather than mutating a
// def a concurrent snapshot reader may be holding.
func SetItemKind(def ItemKindDef) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if w.ItemKinds == nil {
				w.ItemKinds = make(map[ItemKind]*ItemKindDef)
			}
			// Satiation is never carried on item/set: preserve it from the
			// existing entry on an update, empty on a fresh insert. Cloned either
			// way so the stored def owns its slice (no alias into the caller's def
			// or the prior live entry).
			base := def.Satisfies
			if existing := w.ItemKinds[def.Name]; existing != nil {
				base = existing.Satisfies
			}
			stored := def
			stored.Satisfies = append([]ItemSatisfaction(nil), base...)
			w.ItemKinds[def.Name] = &stored
			return stored, nil
		},
	}
}
