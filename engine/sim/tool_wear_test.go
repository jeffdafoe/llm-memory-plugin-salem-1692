package sim_test

import (
	"math"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// tool_wear_test.go — LLM-330. Per-use durability for tool-kind recipe inputs:
// a produce start wears the tool 1 use instead of consuming it; at 0 uses the
// unit is spent (inventory -1) and the next execution takes up a fresh one.
// The buildCookWorld seed gives the skillet durability 3 and the mallet
// durability 1 (the consumed-every-use degenerate case).

// skilletStewRecipes: stew needs 2 sage (consumed) and a skillet on hand
// (worn, durability 3 per the buildCookWorld item-kind seed).
func skilletStewRecipes() map[sim.ItemKind]*sim.ItemRecipe {
	return map[sim.ItemKind]*sim.ItemRecipe{
		"stew": {OutputItem: "stew", OutputQty: 1, RateQty: 1, RatePerHours: 1,
			Inputs: []sim.RecipeInput{{Item: "sage", Qty: 2}, {Item: "skillet", Qty: 1}}, WholesalePrice: 3, RetailPrice: 5},
	}
}

func skilletStewRestock() []sim.RestockEntry {
	return []sim.RestockEntry{{Item: "stew", Source: sim.RestockSourceProduce, Max: 30}}
}

// toolWearOf reads the actor's live ToolWear entry for kind (0 = no entry).
func toolWearOf(t *testing.T, w *sim.World, actorID sim.ActorID, kind sim.ItemKind) int {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors[actorID].ToolWear[kind], nil
	}})
	if err != nil {
		t.Fatalf("toolWearOf: %v", err)
	}
	return res.(int)
}

// clearProductionWindow closes the actor's in-flight cycle so a test can start
// the next execution without simulating the full cycle duration.
func clearProductionWindow(t *testing.T, w *sim.World, actorID sim.ActorID) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors[actorID].ProductionActivity = nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("clearProductionWindow: %v", err)
	}
}

// TestProduceWearsToolInsteadOfConsuming — the core LLM-330 semantics: a
// durable-tool input stays in inventory at start; the wear counter takes up a
// fresh unit and decrements 1, the consumed-inputs clause omits the tool, and
// the wear clause reports the remaining uses.
func TestProduceWearsToolInsteadOfConsuming(t *testing.T) {
	w, cancel := buildCookWorld(t, skilletStewRecipes(), skilletStewRestock(),
		map[sim.ItemKind]int{"sage": 4, "skillet": 2})
	defer cancel()

	res, err := w.Send(sim.StartProductionCycle("cook", "stew", "", false))
	if err != nil {
		t.Fatalf("StartProductionCycle: %v", err)
	}
	r := res.(sim.ProductionStartResult)
	if !strings.Contains(r.InputsUsed, "2 sage") {
		t.Errorf("InputsUsed = %q, want the spent sage named", r.InputsUsed)
	}
	if strings.Contains(r.InputsUsed, "skillet") {
		t.Errorf("InputsUsed = %q — the tool must not read as consumed", r.InputsUsed)
	}
	if !strings.Contains(r.ToolWear, "skillet") || !strings.Contains(r.ToolWear, "2 more uses") {
		t.Errorf("ToolWear = %q, want the skillet's 2 remaining uses named", r.ToolWear)
	}
	if got := inventoryOf(t, w, "cook", "skillet"); got != 2 {
		t.Errorf("skillet = %d after start, want 2 (worn, not consumed)", got)
	}
	if got := inventoryOf(t, w, "cook", "sage"); got != 2 {
		t.Errorf("sage = %d after start, want 2 (consumed as before)", got)
	}
	if got := toolWearOf(t, w, "cook", "skillet"); got != 2 {
		t.Errorf("ToolWear[skillet] = %d, want 2 (fresh take-up at 3, minus this use)", got)
	}
}

// TestProduceSpendsToolAtLastUse — wear reaching 0 spends the in-use unit:
// inventory decrements, the wear entry clears (next execution takes up a fresh
// unit), and the result phrases the give-out with the spare status.
func TestProduceSpendsToolAtLastUse(t *testing.T) {
	w, cancel := buildCookWorld(t, skilletStewRecipes(), skilletStewRestock(),
		map[sim.ItemKind]int{"sage": 8, "skillet": 2})
	defer cancel()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["cook"].ToolWear = map[sim.ItemKind]int{"skillet": 1}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed wear: %v", err)
	}

	res, err := w.Send(sim.StartProductionCycle("cook", "stew", "", false))
	if err != nil {
		t.Fatalf("StartProductionCycle: %v", err)
	}
	r := res.(sim.ProductionStartResult)
	if !strings.Contains(r.ToolWear, "gives out") || !strings.Contains(r.ToolWear, "spare") {
		t.Errorf("ToolWear = %q, want the give-out with the spare named", r.ToolWear)
	}
	if got := inventoryOf(t, w, "cook", "skillet"); got != 1 {
		t.Errorf("skillet = %d, want 1 (the spent unit consumed)", got)
	}
	if got := toolWearOf(t, w, "cook", "skillet"); got != 0 {
		t.Errorf("ToolWear[skillet] = %d, want no entry (fresh take-up next use)", got)
	}

	// The next execution takes up the spare at full durability.
	clearProductionWindow(t, w, "cook")
	if _, err := w.Send(sim.StartProductionCycle("cook", "stew", "", false)); err != nil {
		t.Fatalf("second start: %v", err)
	}
	if got := toolWearOf(t, w, "cook", "skillet"); got != 2 {
		t.Errorf("ToolWear[skillet] = %d after fresh take-up, want 2", got)
	}
}

// TestProduceSpendsLastToolNamesIt — the give-out on the LAST unit says so,
// and the next start is rejected short of a skillet (the rebuy pressure).
func TestProduceSpendsLastToolNamesIt(t *testing.T) {
	w, cancel := buildCookWorld(t, skilletStewRecipes(), skilletStewRestock(),
		map[sim.ItemKind]int{"sage": 8, "skillet": 1})
	defer cancel()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["cook"].ToolWear = map[sim.ItemKind]int{"skillet": 1}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed wear: %v", err)
	}

	res, err := w.Send(sim.StartProductionCycle("cook", "stew", "", false))
	if err != nil {
		t.Fatalf("StartProductionCycle: %v", err)
	}
	r := res.(sim.ProductionStartResult)
	if !strings.Contains(r.ToolWear, "last one") {
		t.Errorf("ToolWear = %q, want the last-one give-out named", r.ToolWear)
	}
	if got := inventoryOf(t, w, "cook", "skillet"); got != 0 {
		t.Errorf("skillet = %d, want 0 (spent)", got)
	}

	// Skillet-less, the next batch bounces on the inputs gate.
	clearProductionWindow(t, w, "cook")
	_, err = w.Send(sim.StartProductionCycle("cook", "stew", "", false))
	if err == nil {
		t.Fatalf("accepted a skillet-less stew; want rejection")
	}
	if !strings.Contains(err.Error(), "skillet") {
		t.Errorf("rejection = %q, want the missing skillet named", err.Error())
	}
}

// TestProduceDurabilityOneConsumesEveryUse — the degenerate case: durability 1
// spends a unit per execution, exactly the old consumed-input behavior.
func TestProduceDurabilityOneConsumesEveryUse(t *testing.T) {
	recipes := map[sim.ItemKind]*sim.ItemRecipe{
		"pie": {OutputItem: "pie", OutputQty: 1, RateQty: 1, RatePerHours: 1,
			Inputs: []sim.RecipeInput{{Item: "mallet", Qty: 1}}},
	}
	restock := []sim.RestockEntry{{Item: "pie", Source: sim.RestockSourceProduce, Max: 30}}
	w, cancel := buildCookWorld(t, recipes, restock, map[sim.ItemKind]int{"mallet": 2})
	defer cancel()

	res, err := w.Send(sim.StartProductionCycle("cook", "pie", "", false))
	if err != nil {
		t.Fatalf("StartProductionCycle: %v", err)
	}
	if r := res.(sim.ProductionStartResult); !strings.Contains(r.ToolWear, "gives out") {
		t.Errorf("ToolWear = %q, want the immediate give-out", r.ToolWear)
	}
	if got := inventoryOf(t, w, "cook", "mallet"); got != 1 {
		t.Errorf("mallet = %d, want 1 (durability 1 spends per use)", got)
	}
	if got := toolWearOf(t, w, "cook", "mallet"); got != 0 {
		t.Errorf("ToolWear[mallet] = %d, want no entry", got)
	}
}

// TestProduceClampsRetunedWear — an operator retuning durability DOWN below an
// actor's stored wear must not grant the old lifetime: the entry clamps to the
// new durability at next use.
func TestProduceClampsRetunedWear(t *testing.T) {
	w, cancel := buildCookWorld(t, skilletStewRecipes(), skilletStewRestock(),
		map[sim.ItemKind]int{"sage": 4, "skillet": 1})
	defer cancel()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["cook"].ToolWear = map[sim.ItemKind]int{"skillet": 99}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed wear: %v", err)
	}

	if _, err := w.Send(sim.StartProductionCycle("cook", "stew", "", false)); err != nil {
		t.Fatalf("StartProductionCycle: %v", err)
	}
	if got := toolWearOf(t, w, "cook", "skillet"); got != 2 {
		t.Errorf("ToolWear[skillet] = %d, want 2 (clamped to durability 3, minus this use)", got)
	}
}

// TestToolRunwayUses pins the cue-side runway math: spares at full durability
// plus the wear left on the in-use unit; missing/oversized wear reads fresh.
// int64 so huge corrupt-catalog values can't overflow before the caller's
// clamp (code_review).
func TestToolRunwayUses(t *testing.T) {
	cases := []struct {
		name                     string
		onHand, wear, durability int
		want                     int64
	}{
		{"two fresh", 2, 0, 20, 40},
		{"in-use worn", 2, 5, 20, 25},
		{"last unit nearly out", 1, 1, 20, 1},
		{"oversized wear clamps", 1, 99, 20, 20},
		{"nothing on hand", 0, 5, 20, 0},
		{"not a tool", 3, 0, 0, 0},
		{"huge values do not overflow", math.MaxInt32, 0, math.MaxInt32, (int64(math.MaxInt32)-1)*int64(math.MaxInt32) + int64(math.MaxInt32)},
	}
	for _, c := range cases {
		if got := sim.ToolRunwayUses(c.onHand, c.wear, c.durability); got != c.want {
			t.Errorf("%s: ToolRunwayUses(%d, %d, %d) = %d, want %d", c.name, c.onHand, c.wear, c.durability, got, c.want)
		}
	}
}

// TestProduceMultiToolInputWearsQtyUses — a recipe listing Qty > 1 of a tool
// draws that many USES per execution (code_review: wear and the on-hand
// requirement stay in the same currency). Durability 3, Qty 2: the first
// execution takes up a unit and wears it to 1; the second crosses a unit
// boundary — spends the in-use one mid-execution and finishes on a fresh one.
func TestProduceMultiToolInputWearsQtyUses(t *testing.T) {
	recipes := map[sim.ItemKind]*sim.ItemRecipe{
		"pie": {OutputItem: "pie", OutputQty: 1, RateQty: 1, RatePerHours: 1,
			Inputs: []sim.RecipeInput{{Item: "skillet", Qty: 2}}},
	}
	restock := []sim.RestockEntry{{Item: "pie", Source: sim.RestockSourceProduce, Max: 30}}
	w, cancel := buildCookWorld(t, recipes, restock, map[sim.ItemKind]int{"skillet": 2})
	defer cancel()

	if _, err := w.Send(sim.StartProductionCycle("cook", "pie", "", false)); err != nil {
		t.Fatalf("first start: %v", err)
	}
	if got := toolWearOf(t, w, "cook", "skillet"); got != 1 {
		t.Errorf("ToolWear[skillet] = %d after 2 uses of a fresh 3, want 1", got)
	}
	if got := inventoryOf(t, w, "cook", "skillet"); got != 2 {
		t.Errorf("skillet = %d, want 2 (no unit spent yet)", got)
	}

	clearProductionWindow(t, w, "cook")
	res, err := w.Send(sim.StartProductionCycle("cook", "pie", "", false))
	if err != nil {
		t.Fatalf("second start: %v", err)
	}
	r := res.(sim.ProductionStartResult)
	if !strings.Contains(r.ToolWear, "gives out") {
		t.Errorf("ToolWear = %q, want the mid-execution give-out named", r.ToolWear)
	}
	if got := inventoryOf(t, w, "cook", "skillet"); got != 1 {
		t.Errorf("skillet = %d, want 1 (the worn unit spent crossing the boundary)", got)
	}
	if got := toolWearOf(t, w, "cook", "skillet"); got != 2 {
		t.Errorf("ToolWear[skillet] = %d, want 2 (fresh unit, one boundary use taken)", got)
	}
}
