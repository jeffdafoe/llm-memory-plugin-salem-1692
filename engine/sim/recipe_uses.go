package sim

import (
	"sort"
	"strings"
)

// recipe_uses.go — LLM-166. The reverse "what is this item used for" index over
// the recipe catalog, plus the in-world prose that names an INEDIBLE item's
// purpose.
//
// A hungry weak model reads a food-named non-food off its inventory (raw "Meat",
// which eases no need until cooked) and burns turns trying to consume() it — the
// live Josiah-eats-raw-meat loop (22 rejected eats in one turn). Nothing in the
// neutral carry readout said meat is an ingredient, not a meal. This index lets
// perception annotate it ("used to produce stew") and lets the consume rejection
// steer the same way, so the model redirects instead of re-trying.
//
// The index is the recipe catalog read backwards: a forward recipe is
// output -> inputs; this is input -> the outputs it helps make. It is memoized on
// the World (ensureRecipeUses) and refreshed in place only when the catalog
// changes (SetRecipe), so it is never scanned per-tick or per-consume.

// buildRecipeUses computes the reverse index input-item -> the items it helps
// produce, from the forward recipe catalog (output -> inputs). Output kinds are
// deduped (a recipe can list the same input twice — recipe/set is an unvalidated
// edit path — and the same output must not surface as "stew or stew") and sorted
// so perception annotations and goldens are deterministic (Recipes is a map).
// Empty item/output keys are skipped. Returns a non-nil (possibly empty) map so
// callers need no nil check.
func buildRecipeUses(recipes map[ItemKind]*ItemRecipe) map[ItemKind][]ItemKind {
	sets := make(map[ItemKind]map[ItemKind]struct{})
	for _, r := range recipes {
		if r == nil {
			continue
		}
		for _, in := range r.Inputs {
			if in.Item == "" || r.OutputItem == "" {
				continue
			}
			if sets[in.Item] == nil {
				sets[in.Item] = make(map[ItemKind]struct{})
			}
			sets[in.Item][r.OutputItem] = struct{}{}
		}
	}
	uses := make(map[ItemKind][]ItemKind, len(sets))
	for in, outsSet := range sets {
		outs := make([]ItemKind, 0, len(outsSet))
		for out := range outsSet {
			outs = append(outs, out)
		}
		sort.Slice(outs, func(i, j int) bool { return outs[i] < outs[j] })
		uses[in] = outs
	}
	return uses
}

// ensureRecipeUses returns the memoized reverse index, building it from the
// current catalog on first use. Runs on the world goroutine (republish + tick
// command Fns), so the lazy populate is race-free. SetRecipe refreshes it
// explicitly because the cache is already warm by the time a live edit lands.
func (w *World) ensureRecipeUses() map[ItemKind][]ItemKind {
	if w.recipeUses == nil {
		w.recipeUses = buildRecipeUses(w.Recipes)
	}
	return w.recipeUses
}

// itemKindDisplayLabel resolves a kind's catalog display label, falling back to
// the raw key — the sim-side mirror of perception's itemDisplayLabel.
func itemKindDisplayLabel(w *World, kind ItemKind) string {
	if def := w.ItemKinds[kind]; def != nil && def.DisplayLabel != "" {
		return def.DisplayLabel
	}
	return string(kind)
}

// maxNamedRecipeUses caps how many output goods the use clause names before it
// collapses the tail to "other things" — a length guard for an ingredient that
// ever feeds many recipes. No non-consumable ingredient feeds more than one
// today (meat and skillet each feed only stew), so the cap never fires yet.
const maxNamedRecipeUses = 3

// RecipeUseClause builds the "used to produce X" fragment naming the goods an
// inedible item helps make, from their already-resolved display labels. Names are
// lowercased — goods are common nouns, matching the satiation cue's "buy meat"
// style — and joined with "or". Empty in -> "". Past maxNamedRecipeUses the tail
// collapses to "other things". Exported so the perception annotation and the
// consume rejection share one phrasing and can't drift.
func RecipeUseClause(labels []string) string {
	names := make([]string, 0, len(labels))
	for _, l := range labels {
		if l = strings.TrimSpace(l); l != "" {
			names = append(names, strings.ToLower(l))
		}
	}
	if len(names) == 0 {
		return ""
	}
	if len(names) > maxNamedRecipeUses {
		names = append(names[:maxNamedRecipeUses-1], "other things")
	}
	return "used to produce " + joinWithOr(names)
}

// joinWithOr renders a slice as an "or"-terminated list: "a", "a or b",
// "a, b, or c".
func joinWithOr(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + " or " + names[1]
	default:
		return strings.Join(names[:len(names)-1], ", ") + ", or " + names[len(names)-1]
	}
}
