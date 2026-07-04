package sim

import "testing"

// derived_demand_test.go — LLM-260. Covers EffectiveBuyEntries (explicit-wins,
// self-source exclusion, the cap heuristic, cross-recipe dedupe, no-input
// recipes, nil safety) and ManagesEffective.

func TestEffectiveBuyEntries_DerivesUnsourcedInputs(t *testing.T) {
	// Hannah: produces porridge (milk 3 + water 5 per batch of 4), sources
	// neither input. Porridge cap 12 → ceil(12/4) = 3 batches.
	p := &RestockPolicy{Restock: []RestockEntry{
		{Item: "porridge", Source: RestockSourceProduce, Max: 12},
	}}
	recipes := map[ItemKind]*ItemRecipe{
		"porridge": {OutputItem: "porridge", OutputQty: 4,
			Inputs: []RecipeInput{{Item: "milk", Qty: 3}, {Item: "water", Qty: 5}}},
	}
	got := EffectiveBuyEntries(recipes, p)
	if len(got) != 2 {
		t.Fatalf("effective entries = %d, want 2 derived: %+v", len(got), got)
	}
	if got[0].Item != "milk" || got[0].Source != RestockSourceBuy || got[0].Max != 9 {
		t.Errorf("derived milk = %+v, want {milk buy 9} (3 qty × 3 batches)", got[0])
	}
	if got[1].Item != "water" || got[1].Max != 15 {
		t.Errorf("derived water = %+v, want {water buy 15} (5 qty × 3 batches)", got[1])
	}
}

func TestEffectiveBuyEntries_SelfSourcedAndExplicitWin(t *testing.T) {
	// John: produces stew (water 10 + meat 10 per batch of 5) AND produces his
	// own water; buys meat explicitly at a hand-tuned cap. Neither input derives:
	// water is self-sourced, meat's explicit entry wins (cap override intact).
	p := &RestockPolicy{Restock: []RestockEntry{
		{Item: "stew", Source: RestockSourceProduce, Max: 10},
		{Item: "water", Source: RestockSourceProduce, Max: 20},
		{Item: "meat", Source: RestockSourceBuy, Max: 40},
	}}
	recipes := map[ItemKind]*ItemRecipe{
		"stew": {OutputItem: "stew", OutputQty: 5,
			Inputs: []RecipeInput{{Item: "water", Qty: 10}, {Item: "meat", Qty: 10}}},
		"water": {OutputItem: "water", OutputQty: 10},
	}
	got := EffectiveBuyEntries(recipes, p)
	if len(got) != 1 {
		t.Fatalf("effective entries = %+v, want just the explicit meat entry", got)
	}
	if got[0].Item != "meat" || got[0].Max != 40 {
		t.Errorf("explicit meat = %+v, want the hand-authored cap 40 kept", got[0])
	}
}

func TestEffectiveBuyEntries_SharedInputDerivesOnceAtLargerCap(t *testing.T) {
	// One input feeding two recipes derives a single entry at the larger of the
	// two computed caps (the bigger need governs).
	p := &RestockPolicy{Restock: []RestockEntry{
		{Item: "stew", Source: RestockSourceProduce, Max: 5},    // 1 batch → sage 2
		{Item: "remedy", Source: RestockSourceProduce, Max: 12}, // 3 batches → sage 9
	}}
	recipes := map[ItemKind]*ItemRecipe{
		"stew":   {OutputItem: "stew", OutputQty: 5, Inputs: []RecipeInput{{Item: "sage", Qty: 2}}},
		"remedy": {OutputItem: "remedy", OutputQty: 4, Inputs: []RecipeInput{{Item: "sage", Qty: 3}}},
	}
	got := EffectiveBuyEntries(recipes, p)
	if len(got) != 1 {
		t.Fatalf("effective entries = %+v, want one deduped sage entry", got)
	}
	if got[0].Item != "sage" || got[0].Max != 9 {
		t.Errorf("derived sage = %+v, want cap 9 (the larger of 2 and 9)", got[0])
	}
}

func TestEffectiveBuyEntries_CapFallbackAndNoInputRecipe(t *testing.T) {
	// A produce entry with no cap sizes derived demand at
	// DefaultDerivedDemandBatches; a no-input recipe (water — conjured)
	// contributes nothing.
	p := &RestockPolicy{Restock: []RestockEntry{
		{Item: "bread", Source: RestockSourceProduce}, // no cap
		{Item: "water", Source: RestockSourceProduce, Max: 20},
	}}
	recipes := map[ItemKind]*ItemRecipe{
		"bread": {OutputItem: "bread", OutputQty: 2, Inputs: []RecipeInput{{Item: "flour", Qty: 4}}},
		"water": {OutputItem: "water", OutputQty: 10},
	}
	got := EffectiveBuyEntries(recipes, p)
	if len(got) != 1 {
		t.Fatalf("effective entries = %+v, want just derived flour", got)
	}
	if got[0].Item != "flour" || got[0].Max != 4*DefaultDerivedDemandBatches {
		t.Errorf("derived flour = %+v, want cap %d (qty 4 × default %d batches)",
			got[0], 4*DefaultDerivedDemandBatches, DefaultDerivedDemandBatches)
	}
}

func TestEffectiveBuyEntries_NilSafety(t *testing.T) {
	if got := EffectiveBuyEntries(nil, nil); got != nil {
		t.Errorf("nil policy should yield nil, got %+v", got)
	}
	// A nil catalog derives nothing but still returns the explicit entries.
	p := &RestockPolicy{Restock: []RestockEntry{
		{Item: "stew", Source: RestockSourceProduce, Max: 10},
		{Item: "salt", Source: RestockSourceBuy, Max: 5},
	}}
	got := EffectiveBuyEntries(nil, p)
	if len(got) != 1 || got[0].Item != "salt" {
		t.Errorf("nil catalog: got %+v, want just the explicit salt entry", got)
	}
}

func TestManagesEffective(t *testing.T) {
	p := &RestockPolicy{Restock: []RestockEntry{
		{Item: "porridge", Source: RestockSourceProduce, Max: 12},
	}}
	recipes := map[ItemKind]*ItemRecipe{
		"porridge": {OutputItem: "porridge", OutputQty: 4, Inputs: []RecipeInput{{Item: "milk", Qty: 3}}},
	}
	if !ManagesEffective(recipes, p, "porridge") {
		t.Error("an explicit produce entry is managed (Manages passthrough)")
	}
	if !ManagesEffective(recipes, p, "milk") {
		t.Error("a derived buy input is trade stock (LLM-134 demotion applies)")
	}
	if ManagesEffective(recipes, p, "ale") {
		t.Error("an unrelated item is not managed")
	}
	if ManagesEffective(recipes, nil, "milk") {
		t.Error("nil policy manages nothing")
	}
}
