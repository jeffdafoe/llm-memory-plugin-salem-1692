package sim

import (
	"testing"
	"time"
)

// restock_tick_degraded_test.go — LLM-608, the warrant half. buildRestocking now
// keeps the inputs a degraded keeper's OWN production consumes, so the warrant
// producer has to wake him for exactly those and nothing else. A cue that renders
// on a wake that never comes is as silent as no cue at all; a wake whose cue
// declines to render is the loop LLM-260 removed. Both halves read
// ProductionInputKinds so they cannot drift apart.

// degradedMaker returns a maker of "stew" who buys "skillet" for it and resells
// "ale", holding `skillet` and `ale` at `onHand`, plus a world where his own
// business is worn past the degrade threshold and the shop LIMPS (the live
// StallDegradedProducePct 50, not the legacy 0 full block).
func degradedMaker(onHand int) (*Actor, *World) {
	a := &Actor{
		ID:        "smith",
		Kind:      KindNPCStateful,
		LLMAgent:  "smith-agent",
		Coins:     50,
		Inventory: map[ItemKind]int{"skillet": onHand, "ale": onHand},
		RestockPolicy: &RestockPolicy{Restock: []RestockEntry{
			{Item: "stew", Source: RestockSourceProduce, Max: 60},
			{Item: "skillet", Source: RestockSourceBuy, Max: 20},
			{Item: "ale", Source: RestockSourceBuy, Max: 20},
		}},
	}
	w := restockWorld(a)
	w.Recipes = map[ItemKind]*ItemRecipe{
		"stew": {
			OutputItem: "stew", OutputQty: 30, RateQty: 30, RatePerHours: 6,
			Inputs: []RecipeInput{{Item: "skillet", Qty: 1}},
		},
	}
	addSupplier(w, "potter", "skillet")
	addSupplier(w, "brewer", "ale")
	w.Settings.StallWearDegradeThreshold = 600
	w.Settings.StallDegradedProducePct = 50
	if w.VillageObjects == nil {
		w.VillageObjects = map[VillageObjectID]*VillageObject{}
	}
	w.VillageObjects["smiths_shop"] = &VillageObject{
		ID: "smiths_shop", OwnerActorID: "smith", Tags: []string{TagBusiness}, Wear: 650,
	}
	return a, w
}

// A degraded maker IS woken to buy the input his own production consumes — the
// wake that carries him to the water that makes the nail that mends the forge.
// The representative item must be that input, never the resale good.
func TestEvaluateRestock_DegradedWarrantsProductionInput(t *testing.T) {
	a, w := degradedMaker(3) // 3/20 = 15% < 25% on both items
	now := time.Now().UTC()

	res, err := EvaluateRestock(now).Fn(w)
	if err != nil {
		t.Fatalf("EvaluateRestock: %v", err)
	}
	if res.(int) != 1 {
		t.Fatalf("stamped = %d, want 1 (a degraded maker still reorders his own inputs)", res.(int))
	}
	if !hasWarrantKind(a, WarrantKindRestock) {
		t.Fatalf("expected a restock warrant; kinds = %v", warrantKinds(a))
	}
	for _, m := range a.Warrants {
		if r, ok := m.Reason.(RestockWarrantReason); ok && r.Item != "skillet" {
			t.Errorf("warrant item = %q, want the production input skillet", r.Item)
		}
	}
}

// With the input stocked and only the RESALE good low, there is nothing a
// degraded keeper may act on — LLM-304 stands, and he is not woken for shelves he
// cannot refill.
func TestEvaluateRestock_DegradedNoWarrantForResaleAlone(t *testing.T) {
	a, w := degradedMaker(3)
	a.Inventory["skillet"] = 20 // at cap — only `ale` is low now
	now := time.Now().UTC()

	res, err := EvaluateRestock(now).Fn(w)
	if err != nil {
		t.Fatalf("EvaluateRestock: %v", err)
	}
	if res.(int) != 0 {
		t.Errorf("stamped = %d, want 0 (resale stock waits on the mending)", res.(int))
	}
	if hasWarrantKind(a, WarrantKindRestock) {
		t.Fatalf("resale stock must not warrant while worn; kinds = %v", warrantKinds(a))
	}
}

// At StallDegradedProducePct 0 the legacy full block applies — production is
// refused outright, so its inputs warrant nothing either. Foil of
// TestEvaluateRestock_DegradedWarrantsProductionInput: identical but hard-blocked.
func TestEvaluateRestock_DegradedProduceBlockedNoWarrant(t *testing.T) {
	a, w := degradedMaker(3)
	w.Settings.StallDegradedProducePct = 0
	now := time.Now().UTC()

	res, err := EvaluateRestock(now).Fn(w)
	if err != nil {
		t.Fatalf("EvaluateRestock: %v", err)
	}
	if res.(int) != 0 {
		t.Errorf("stamped = %d, want 0 (production hard-blocked)", res.(int))
	}
	if hasWarrantKind(a, WarrantKindRestock) {
		t.Fatalf("a hard-blocked business must not get a restock warrant; kinds = %v", warrantKinds(a))
	}
}

// The forage side stays shut while worn, exactly as LLM-304 left it: harvesting
// one's own bushes replenishes the SHELVES, which is the thing the mending gates.
// Only the buy side was narrowed.
func TestEvaluateRestock_DegradedForageStillSuppressed(t *testing.T) {
	a, w := degradedMaker(3)
	a.Inventory["skillet"] = 20 // input at cap, so only the forage entry could fire
	a.RestockPolicy.Restock = append(a.RestockPolicy.Restock,
		RestockEntry{Item: "berries", Source: RestockSourceForage, Max: 20})
	a.Inventory["berries"] = 1
	if w.VillageObjects == nil {
		w.VillageObjects = map[VillageObjectID]*VillageObject{}
	}
	w.VillageObjects["bush"] = forageBushObj("smith", "berries", 10)
	rememberForageBush(a, "berries", "bush")
	now := time.Now().UTC()

	res, err := EvaluateRestock(now).Fn(w)
	if err != nil {
		t.Fatalf("EvaluateRestock: %v", err)
	}
	if res.(int) != 0 {
		t.Errorf("stamped = %d, want 0 (the forage side is still shut while worn)", res.(int))
	}
}
