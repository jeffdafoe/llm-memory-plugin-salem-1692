package sim

import (
	"reflect"
	"testing"
)

// recipe_uses_test.go — LLM-166. The reverse recipe index + the "used to produce
// X" clause shared by perception and the consume rejection.

func TestBuildRecipeUses_ReversesAndSorts(t *testing.T) {
	recipes := map[ItemKind]*ItemRecipe{
		"stew":  {OutputItem: "stew", Inputs: []RecipeInput{{Item: "meat", Qty: 10}, {Item: "milk", Qty: 10}}},
		"bread": {OutputItem: "bread", Inputs: []RecipeInput{{Item: "flour", Qty: 2}, {Item: "milk", Qty: 1}}},
		"nail":  {OutputItem: "nail"}, // no inputs -> contributes nothing
	}

	got := buildRecipeUses(recipes)
	want := map[ItemKind][]ItemKind{
		"meat":  {"stew"},
		"flour": {"bread"},
		"milk":  {"bread", "stew"}, // sorted by kind so goldens are stable
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildRecipeUses = %v, want %v", got, want)
	}
}

// A recipe that lists the same input twice (or an empty input/output from a
// malformed recipe/set edit) must not produce a duplicate output — else the prose
// reads "used to produce stew or stew". (code_review)
func TestBuildRecipeUses_DedupesAndSkipsEmpty(t *testing.T) {
	recipes := map[ItemKind]*ItemRecipe{
		"stew": {OutputItem: "stew", Inputs: []RecipeInput{
			{Item: "meat", Qty: 5},
			{Item: "meat", Qty: 5}, // duplicate input -> output deduped
			{Item: "", Qty: 1},     // empty input -> skipped
		}},
		"": {OutputItem: "", Inputs: []RecipeInput{{Item: "meat", Qty: 1}}}, // empty output -> skipped
	}
	got := buildRecipeUses(recipes)
	if want := []ItemKind{"stew"}; !reflect.DeepEqual(got["meat"], want) {
		t.Errorf("meat uses = %v, want %v (deduped, empty output skipped)", got["meat"], want)
	}
}

func TestBuildRecipeUses_NilAndEmpty(t *testing.T) {
	if got := buildRecipeUses(nil); len(got) != 0 || got == nil {
		t.Errorf("nil catalog should yield a non-nil empty map, got %v", got)
	}
	// A nil recipe entry is skipped without panicking.
	got := buildRecipeUses(map[ItemKind]*ItemRecipe{"x": nil})
	if len(got) != 0 {
		t.Errorf("nil recipe entry should contribute nothing, got %v", got)
	}
}

func TestRecipeUseClause(t *testing.T) {
	cases := []struct {
		name   string
		labels []string
		want   string
	}{
		{"empty", nil, ""},
		{"all-blank", []string{"", "  "}, ""},
		{"one", []string{"Stew"}, "used to produce stew"},
		{"two", []string{"Stew", "Porridge"}, "used to produce stew or porridge"},
		{"three", []string{"Stew", "Porridge", "Pie"}, "used to produce stew, porridge, or pie"},
		{"four-collapses-tail", []string{"Stew", "Porridge", "Pie", "Cake"}, "used to produce stew, porridge, or other things"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := RecipeUseClause(c.labels); got != c.want {
				t.Errorf("RecipeUseClause(%v) = %q, want %q", c.labels, got, c.want)
			}
		})
	}
}

func TestEnsureRecipeUses_MemoizesAndBuilds(t *testing.T) {
	w := &World{Recipes: map[ItemKind]*ItemRecipe{
		"stew": {OutputItem: "stew", Inputs: []RecipeInput{{Item: "meat", Qty: 1}}},
	}}
	uses := w.ensureRecipeUses()
	if !reflect.DeepEqual(uses["meat"], []ItemKind{"stew"}) {
		t.Fatalf("ensureRecipeUses meat = %v, want [stew]", uses["meat"])
	}
	// The build populated the cache field; a second call serves it back.
	if w.recipeUses == nil {
		t.Error("ensureRecipeUses should memoize onto World.recipeUses")
	}
	if again := w.ensureRecipeUses(); !reflect.DeepEqual(again, uses) {
		t.Errorf("second ensureRecipeUses = %v, want %v", again, uses)
	}
}
