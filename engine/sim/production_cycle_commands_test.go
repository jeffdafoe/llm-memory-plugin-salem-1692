package sim_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// production_cycle_commands_test.go — LLM-319. StartProductionCycle's
// validation matrix and start semantics: one call opens one batch, inputs are
// consumed at start, and every uncraftable start is a rejection (steered
// toward named craftable alternatives — the LLM-300 posture, preserved from
// the retired SetProductionFocus).

// buildCookWorld seeds a producer ("cook") at its workplace with the given
// recipes + produce restock, starting inventory. Item kinds are seeded for the
// palette these tests draw from. No tickers run (only w.Run) so commands apply
// deterministically.
func buildCookWorld(t *testing.T, recipes map[sim.ItemKind]*sim.ItemRecipe, restock []sim.RestockEntry, inv map[sim.ItemKind]int) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		"stew":      {Name: "stew", DisplayLabel: "Stew", DisplayLabelSingular: "bowl of stew", DisplayLabelPlural: "stew", Category: sim.ItemCategoryFood, SortOrder: 140},
		"water":     {Name: "water", DisplayLabel: "Water", DisplayLabelSingular: "pail of water", DisplayLabelPlural: "water", Category: sim.ItemCategoryDrink, SortOrder: 20},
		"sage":      {Name: "sage", DisplayLabel: "Sage", DisplayLabelSingular: "sage", DisplayLabelPlural: "sage", Category: sim.ItemCategoryMaterial, SortOrder: 240},
		"pie":       {Name: "pie", DisplayLabel: "Pie", DisplayLabelSingular: "pie", DisplayLabelPlural: "pies", Category: sim.ItemCategoryFood, SortOrder: 150},
		"flour":     {Name: "flour", DisplayLabel: "Flour", Category: sim.ItemCategoryMaterial, SortOrder: 250},
		"porridge":  {Name: "porridge", DisplayLabel: "Porridge", Category: sim.ItemCategoryFood, SortOrder: 130},
		"horseshoe": {Name: "horseshoe", DisplayLabel: "Horseshoe", Category: sim.ItemCategoryMaterial, SortOrder: 320}, // item kind, NO recipe seeded
		// Durable tools (LLM-330): a skillet lasts 3 produce executions; the
		// mallet's durability 1 pins the consumed-every-use degenerate case.
		"skillet": {Name: "skillet", DisplayLabel: "Skillet", DisplayLabelSingular: "skillet", DisplayLabelPlural: "skillets", Category: "tool", SortOrder: 400, DurabilityUses: 3},
		"mallet":  {Name: "mallet", DisplayLabel: "Mallet", DisplayLabelSingular: "mallet", DisplayLabelPlural: "mallets", Category: "tool", SortOrder: 410, DurabilityUses: 1},
	})
	handles.Recipes.Seed(recipes)
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"cook": {
			ID:                "cook",
			LLMAgent:          "cook-agent",
			Kind:              sim.KindNPCStateful,
			InsideStructureID: "tavern",
			WorkStructureID:   "tavern",
			Inventory:         inv,
			RestockPolicy:     &sim.RestockPolicy{Restock: restock},
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

// stewWaterRecipes/stewWaterRestock: stew needs 2 sage per batch; water is an
// origin good (no inputs, batch of 12/h). The minimal two-good shape behind
// the LLM-257/LLM-300 steering tests.
func stewWaterRecipes() map[sim.ItemKind]*sim.ItemRecipe {
	return map[sim.ItemKind]*sim.ItemRecipe{
		"stew":  {OutputItem: "stew", OutputQty: 1, RateQty: 1, RatePerHours: 1, Inputs: []sim.RecipeInput{{Item: "sage", Qty: 2}}, WholesalePrice: 3, RetailPrice: 5},
		"water": {OutputItem: "water", OutputQty: 12, RateQty: 12, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 1},
	}
}

func stewWaterRestock() []sim.RestockEntry {
	return []sim.RestockEntry{
		{Item: "stew", Source: sim.RestockSourceProduce, Max: 30},
		{Item: "water", Source: sim.RestockSourceProduce, Max: 30},
	}
}

// TestStartProductionCycleOpensOneBatch — the happy path: the call consumes the
// inputs AT START (the pot is on the fire), opens the window with the recipe's
// cycle duration, and reports batch size + duration + the spent inputs for the
// tool-result narration. Nothing is minted yet.
func TestStartProductionCycleOpensOneBatch(t *testing.T) {
	w, cancel := buildCookWorld(t, stewWaterRecipes(), stewWaterRestock(), map[sim.ItemKind]int{"sage": 3})
	defer cancel()

	res, err := w.Send(sim.StartProductionCycle("cook", "stew"))
	if err != nil {
		t.Fatalf("StartProductionCycle: %v", err)
	}
	r := res.(sim.ProductionStartResult)
	if r.Item != "stew" || r.BatchQty != 1 {
		t.Errorf("result = %+v, want item stew batch 1", r)
	}
	if r.DurationSeconds != 3600 {
		t.Errorf("DurationSeconds = %d, want 3600 (1 unit at 1/h)", r.DurationSeconds)
	}
	if !strings.Contains(r.InputsUsed, "2 sage") {
		t.Errorf("InputsUsed = %q, want the spent sage named", r.InputsUsed)
	}

	state, _ := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["cook"]
		var item sim.ItemKind
		var remaining int64
		if a.ProductionActivity != nil {
			item = a.ProductionActivity.Item
			remaining = a.ProductionActivity.RemainingSeconds
		}
		return []any{item, remaining, a.Inventory["sage"], a.Inventory["stew"]}, nil
	}})
	got := state.([]any)
	if got[0].(sim.ItemKind) != "stew" || got[1].(int64) != 3600 {
		t.Errorf("activity = %v/%v, want stew/3600", got[0], got[1])
	}
	if got[2].(int) != 1 {
		t.Errorf("sage = %d after start, want 1 (2 consumed at start)", got[2])
	}
	if got[3].(int) != 0 {
		t.Errorf("stew = %d at start, want 0 (the mint lands at completion)", got[3])
	}
}

// TestStartProductionCycleRejectsSecondWhileInFlight — one cycle at a time; a
// mid-cycle produce (any item) bounces with the "already making" steer naming
// what's cooking.
func TestStartProductionCycleRejectsSecondWhileInFlight(t *testing.T) {
	w, cancel := buildCookWorld(t, stewWaterRecipes(), stewWaterRestock(), map[sim.ItemKind]int{"sage": 3})
	defer cancel()

	if _, err := w.Send(sim.StartProductionCycle("cook", "water")); err != nil {
		t.Fatalf("first start: %v", err)
	}
	_, err := w.Send(sim.StartProductionCycle("cook", "stew"))
	if err == nil {
		t.Fatalf("accepted a second cycle mid-flight; want rejection")
	}
	if !strings.Contains(err.Error(), "already making") {
		t.Errorf("rejection = %q, want the 'already making' steer", err.Error())
	}
}

// TestStartProductionCycleRejectsAwayFromWorkplace — production starts only at
// the actor's own post.
func TestStartProductionCycleRejectsAwayFromWorkplace(t *testing.T) {
	w, cancel := buildCookWorld(t, stewWaterRecipes(), stewWaterRestock(), map[sim.ItemKind]int{"sage": 3})
	defer cancel()

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.SetActorInsideStructure(world, world.Actors["cook"], "inn")
		return nil, nil
	}}); err != nil {
		t.Fatalf("move: %v", err)
	}
	if _, err := w.Send(sim.StartProductionCycle("cook", "water")); err == nil {
		t.Fatalf("accepted a start away from the workplace; want rejection")
	}
}

// TestStartProductionCycleRejectsUnknownAndForeignItems — an unresolvable name
// and a catalog good the actor doesn't produce both bounce with model-facing
// errors.
func TestStartProductionCycleRejectsUnknownAndForeignItems(t *testing.T) {
	w, cancel := buildCookWorld(t, stewWaterRecipes(), stewWaterRestock(), map[sim.ItemKind]int{"sage": 3})
	defer cancel()

	if _, err := w.Send(sim.StartProductionCycle("cook", "dragon scale")); err == nil {
		t.Fatalf("accepted an unknown item; want rejection")
	}
	if _, err := w.Send(sim.StartProductionCycle("cook", "porridge")); err == nil {
		t.Fatalf("accepted a good the cook doesn't make; want rejection")
	}
}

// TestStartProductionCycleRejectsRecipelessEntry — a produce entry with no
// recipe (or a rate-less one) isn't makeable.
func TestStartProductionCycleRejectsRecipelessEntry(t *testing.T) {
	restock := append(stewWaterRestock(), sim.RestockEntry{Item: "horseshoe", Source: sim.RestockSourceProduce, Max: 5})
	w, cancel := buildCookWorld(t, stewWaterRecipes(), restock, map[sim.ItemKind]int{"sage": 3})
	defer cancel()

	if _, err := w.Send(sim.StartProductionCycle("cook", "horseshoe")); err == nil {
		t.Fatalf("accepted a recipe-less good; want rejection")
	}
}

// TestStartProductionCycleRejectsInputStarvedWithSteer — LLM-257/LLM-300: a
// sage-less stew bounces naming the shortfall AND the craftable alternative
// with a copyable produce argument.
func TestStartProductionCycleRejectsInputStarvedWithSteer(t *testing.T) {
	w, cancel := buildCookWorld(t, stewWaterRecipes(), stewWaterRestock(), map[sim.ItemKind]int{})
	defer cancel()

	_, err := w.Send(sim.StartProductionCycle("cook", "stew"))
	if err == nil {
		t.Fatalf("accepted a sage-less stew; want rejection")
	}
	msg := err.Error()
	if !strings.Contains(msg, "sage") {
		t.Errorf("rejection should name the missing input; got %q", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "call produce with water") {
		t.Errorf("rejection should steer to the craftable alternative (call produce with water); got %q", msg)
	}
	// The steered-to good starts fine.
	if _, err := w.Send(sim.StartProductionCycle("cook", "water")); err != nil {
		t.Fatalf("rejected water (no inputs, makeable): %v", err)
	}
}

// TestStartProductionCycleRejectsAtCapWithSteer — the cap-side rejection keeps
// the LLM-300 alternative steer too.
func TestStartProductionCycleRejectsAtCapWithSteer(t *testing.T) {
	w, cancel := buildCookWorld(t, stewWaterRecipes(), stewWaterRestock(), map[sim.ItemKind]int{"water": 30, "sage": 3})
	defer cancel()

	_, err := w.Send(sim.StartProductionCycle("cook", "water"))
	if err == nil {
		t.Fatalf("accepted an at-cap water batch; want rejection")
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "too full to fit another batch") {
		t.Errorf("rejection should state the headroom block; got %q", msg)
	}
	if !strings.Contains(msg, "call produce with stew") {
		t.Errorf("rejection should steer to stew (craftable — sage in hand); got %q", msg)
	}
}

// TestStartProductionCycleRejectsWithoutWholeBatchHeadroom — code_review: a
// start from one-below-cap must NOT overshoot. Water batches are 12; at 25 of
// 30 a whole batch doesn't fit, so the start is rejected BEFORE any inputs are
// spent, matching the old continuous clamp's never-exceed-cap invariant.
func TestStartProductionCycleRejectsWithoutWholeBatchHeadroom(t *testing.T) {
	w, cancel := buildCookWorld(t, stewWaterRecipes(), stewWaterRestock(), map[sim.ItemKind]int{"water": 25})
	defer cancel()

	_, err := w.Send(sim.StartProductionCycle("cook", "water"))
	if err == nil {
		t.Fatalf("accepted a batch that would overshoot the cap (25+12 > 30); want rejection")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "too full to fit another batch") {
		t.Errorf("rejection = %q, want the headroom block named", err.Error())
	}
	if got := inventoryOf(t, w, "cook", "water"); got != 25 {
		t.Errorf("water = %d, want untouched 25", got)
	}
}

// TestStartProductionCycleRejectsWhenNothingCraftable — with every good
// blocked there is no steer, just the rejection: one-shot production has no
// standing intent to record, so the old "all-starved escape" (allow the stuck
// focus) is retired with the focus itself.
func TestStartProductionCycleRejectsWhenNothingCraftable(t *testing.T) {
	recipes := map[sim.ItemKind]*sim.ItemRecipe{
		"stew": {OutputItem: "stew", OutputQty: 1, RateQty: 1, RatePerHours: 1, Inputs: []sim.RecipeInput{{Item: "sage", Qty: 2}}},
		"pie":  {OutputItem: "pie", OutputQty: 1, RateQty: 1, RatePerHours: 1, Inputs: []sim.RecipeInput{{Item: "flour", Qty: 2}}},
	}
	restock := []sim.RestockEntry{
		{Item: "stew", Source: sim.RestockSourceProduce, Max: 30},
		{Item: "pie", Source: sim.RestockSourceProduce, Max: 30},
	}
	w, cancel := buildCookWorld(t, recipes, restock, map[sim.ItemKind]int{})
	defer cancel()

	_, err := w.Send(sim.StartProductionCycle("cook", "stew"))
	if err == nil {
		t.Fatalf("accepted an uncraftable start with nothing else craftable; want rejection")
	}
	if strings.Contains(strings.ToLower(err.Error()), "call produce with") {
		t.Errorf("no alternative exists, so no steer should render; got %q", err.Error())
	}
	act, _ := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors["cook"].ProductionActivity == nil, nil
	}})
	if !act.(bool) {
		t.Errorf("a rejected start must not open a window")
	}
}

// TestStartProductionCycleRejectsAtDegradedBusiness — LLM-304: degrade shuts
// refill, production included. A batch can't start until the owner mends the
// business (rejecting at start keeps the inputs out of a stalled pot).
func TestStartProductionCycleRejectsAtDegradedBusiness(t *testing.T) {
	w, cancel := buildCookWorld(t, stewWaterRecipes(), stewWaterRestock(), map[sim.ItemKind]int{"sage": 3})
	defer cancel()

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.StallWearDegradeThreshold = 600
		if world.VillageObjects == nil {
			world.VillageObjects = map[sim.VillageObjectID]*sim.VillageObject{}
		}
		world.VillageObjects["tavern"] = &sim.VillageObject{
			ID: "tavern", OwnerActorID: "cook", Tags: []string{sim.TagBusiness}, Wear: 650,
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	_, err := w.Send(sim.StartProductionCycle("cook", "water"))
	if err == nil {
		t.Fatalf("accepted a start at a degraded business; want rejection")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "mend") {
		t.Errorf("rejection should steer to mending; got %q", err.Error())
	}
}

// TestCycleDurationSeconds pins the duration formula: OutputQty ×
// (RatePerHours×3600 / RateQty), the porridge 75-minute case included.
func TestCycleDurationSeconds(t *testing.T) {
	cases := []struct {
		name   string
		recipe *sim.ItemRecipe
		want   int64
	}{
		{"porridge 10 at 8/h", &sim.ItemRecipe{OutputQty: 10, RateQty: 8, RatePerHours: 1}, 4500},
		{"single unit at 1/h", &sim.ItemRecipe{OutputQty: 1, RateQty: 1, RatePerHours: 1}, 3600},
		{"batch matches rate", &sim.ItemRecipe{OutputQty: 12, RateQty: 12, RatePerHours: 1}, 3600},
		{"slow good 1 per 3h", &sim.ItemRecipe{OutputQty: 1, RateQty: 1, RatePerHours: 3}, 10800},
		{"rate-less", &sim.ItemRecipe{OutputQty: 1}, 0},
		{"nil", nil, 0},
	}
	for _, c := range cases {
		if got := sim.CycleDurationSeconds(c.recipe); got != c.want {
			t.Errorf("%s: CycleDurationSeconds = %d, want %d", c.name, got, c.want)
		}
	}
}

// TestHumanizeWorkDuration pins the model-facing duration phrases.
func TestHumanizeWorkDuration(t *testing.T) {
	cases := []struct {
		seconds int64
		want    string
	}{
		{30, "a minute"},
		{1200, "20 minutes"},
		{3600, "an hour"},
		{4500, "an hour and a quarter"},
		{5400, "an hour and a half"},
		{7200, "2 hours"},
		{10800, "3 hours"},
	}
	for _, c := range cases {
		if got := sim.HumanizeWorkDuration(c.seconds); got != c.want {
			t.Errorf("HumanizeWorkDuration(%d) = %q, want %q", c.seconds, got, c.want)
		}
	}
}
