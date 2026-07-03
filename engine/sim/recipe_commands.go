package sim

import "fmt"

// recipe_commands.go — live recipe-catalog editing support (LLM-97).
//
// Recipes are reference data: World.Recipes is the in-memory catalog the produce
// tick reads, rebuilt from the item_recipe table at load / SIGHUP. The durable
// write to item_recipe lives in the pg repo (reference data has no checkpoint
// path). These two helpers cover the sim-side halves the umbilical recipe-edit
// route needs: catalog-aware reference validation (ResolveRecipe) and the
// in-memory catalog update (SetRecipe).

// ResolveRecipe validates a recipe's output and input item references against
// World.ItemKinds and returns a canonicalized copy — each ItemKind resolved to
// its catalog key, so "Cheese" and "cheese" both land on the canonical key
// (resolveItemKind). The umbilical recipe-edit route runs this on the world
// goroutine BEFORE the durable item_recipe write, so a recipe can only ever
// reference items that already exist (no new-item creation via this path).
// Returns a wrapped ErrUnknownItemKind naming the offending output/input.
//
// Numeric validation (positive qty/rate/output, non-negative prices) is the
// caller's concern — this is purely the catalog-reference check.
func ResolveRecipe(w *World, r ItemRecipe) (ItemRecipe, error) {
	out, ok := resolveItemKind(w, string(r.OutputItem))
	if !ok {
		return ItemRecipe{}, fmt.Errorf("%w: output %q", ErrUnknownItemKind, r.OutputItem)
	}
	r.OutputItem = out
	canonical := make([]RecipeInput, 0, len(r.Inputs))
	for _, in := range r.Inputs {
		k, ok := resolveItemKind(w, string(in.Item))
		if !ok {
			return ItemRecipe{}, fmt.Errorf("%w: input %q", ErrUnknownItemKind, in.Item)
		}
		canonical = append(canonical, RecipeInput{Item: k, Qty: in.Qty})
	}
	r.Inputs = canonical
	// Booster references (LLM-248): same catalog check, plus the overlap guard —
	// an item can't be both required and optional for the same recipe (the
	// produce tick would double-consume it with ambiguous semantics).
	required := make(map[ItemKind]bool, len(canonical))
	for _, in := range canonical {
		required[in.Item] = true
	}
	canonicalBoosts := make([]BoostInput, 0, len(r.BoostInputs))
	for _, bi := range r.BoostInputs {
		k, ok := resolveItemKind(w, string(bi.Item))
		if !ok {
			return ItemRecipe{}, fmt.Errorf("%w: boost input %q", ErrUnknownItemKind, bi.Item)
		}
		if required[k] {
			return ItemRecipe{}, fmt.Errorf("boost input %q is already a required input", k)
		}
		canonicalBoosts = append(canonicalBoosts, BoostInput{Item: k, Qty: bi.Qty, BonusQty: bi.BonusQty})
	}
	r.BoostInputs = canonicalBoosts
	return r, nil
}

// SetRecipe installs a recipe into the live in-memory catalog (World.Recipes) —
// the map the produce tick reads — and returns the stored recipe. It is the
// in-memory half of an edit; the durable item_recipe write happens separately
// (the recipe repo) and is the source of truth the catalog rebuilds from on
// restart. A copy is stored so the caller can't mutate the live entry through
// its pointer.
func SetRecipe(r ItemRecipe) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if w.Recipes == nil {
				w.Recipes = make(map[ItemKind]*ItemRecipe)
			}
			stored := r
			w.Recipes[r.OutputItem] = &stored
			// Refresh the memoized reverse index (LLM-166) — the cache is already
			// warm by the time a live edit lands, so ensureRecipeUses' lazy build
			// won't pick up this in-place change on its own.
			w.recipeUses = buildRecipeUses(w.Recipes)
			return r, nil
		},
	}
}
