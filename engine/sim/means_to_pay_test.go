package sim

import "testing"

// means_to_pay_test.go — LLM-445 coverage of the shared barterable-goods
// predicate: which held goods count as means to pay. The per-leg reject
// behavior (barter, counter, gift, labor wage) is pinned in the command tests;
// this pins the classification itself.

func TestKindBarterable(t *testing.T) {
	cases := []struct {
		name string
		def  *ItemKindDef
		want bool
	}{
		// nil def (a held kind absent from the catalog) degrades permissive —
		// the resolver, not the cue, backstops those.
		{"nil def", nil, true},
		{"material (inedible, no caps)", &ItemKindDef{Name: "iron"}, true},
		{"portable food", &ItemKindDef{Name: "bread",
			Capabilities: []string{"portable"},
			Satisfies:    []ItemSatisfaction{{Attribute: "hunger", Immediate: 8}}}, true},
		// consumable, neither service nor portable = EatHereOnly = not payment.
		{"eat-here food", &ItemKindDef{Name: "stew",
			Satisfies: []ItemSatisfaction{{Attribute: "hunger", Immediate: 4}}}, false},
		{"service", &ItemKindDef{Name: "nights_stay",
			Capabilities: []string{"service", "lodging"}}, false},
	}
	for _, c := range cases {
		if got := KindBarterable(c.def); got != c.want {
			t.Errorf("%s: KindBarterable = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestHoldsBarterableGoodsExcept_SkipsEatHereAndService(t *testing.T) {
	kinds := map[ItemKind]*ItemKindDef{
		"stew": {Name: "stew",
			Satisfies: []ItemSatisfaction{{Attribute: "hunger", Immediate: 4}}},
		"nights_stay": {Name: "nights_stay", Capabilities: []string{"service"}},
		"bread": {Name: "bread", Capabilities: []string{"portable"},
			Satisfies: []ItemSatisfaction{{Attribute: "hunger", Immediate: 8}}},
	}

	holder := func(inv map[ItemKind]int) BarterHolder { return BarterHolder{Inventory: inv} }

	// A pack of only eat-here food + a service token is NO means to barter —
	// the live porridge-as-currency shape, now correctly a payment dead-end.
	if HoldsBarterableGoodsExcept(kinds, nil, holder(map[ItemKind]int{"stew": 8, "nights_stay": 1}), "") {
		t.Error("eat-here food + service token counted as barterable goods")
	}
	// One portable good flips it.
	if !HoldsBarterableGoodsExcept(kinds, nil, holder(map[ItemKind]int{"stew": 8, "bread": 1}), "") {
		t.Error("portable good not counted as barterable")
	}
	// except still excludes the item being bought.
	if HoldsBarterableGoodsExcept(kinds, nil, holder(map[ItemKind]int{"bread": 1}), "bread") {
		t.Error("the bought item itself counted as payment for itself")
	}
	// Zero-qty rows never count.
	if HoldsBarterableGoodsExcept(kinds, nil, holder(map[ItemKind]int{"bread": 0}), "") {
		t.Error("zero-qty holding counted as barterable")
	}
}

// spokenForFixture is a maker of the LLM-636 live shape: porridge takes flour
// and water (a required-input floor of 2 batches each), thread feeds a service
// with no recipe (cap-reserved), homespun is a wearable garment. journeycake is
// its own produce; the pelt is in no policy line.
func spokenForFixture() (map[ItemKind]*ItemKindDef, map[ItemKind]*ItemRecipe, *RestockPolicy) {
	kinds := map[ItemKind]*ItemKindDef{
		"flour":       {Name: "flour"},
		"water":       {Name: "water"},
		"thread":      {Name: "thread"},
		"homespun":    {Name: "homespun", WearMinutes: 600},
		"journeycake": {Name: "journeycake", Capabilities: []string{"portable"}, Satisfies: []ItemSatisfaction{{Attribute: "hunger", Immediate: 4}}},
		"pelt":        {Name: "pelt"},
	}
	recipes := map[ItemKind]*ItemRecipe{
		"porridge": {Inputs: []RecipeInput{{Item: "flour", Qty: 2}, {Item: "water", Qty: 5}}},
	}
	policy := &RestockPolicy{Restock: []RestockEntry{
		{Item: "porridge", Source: RestockSourceProduce, Max: 30},
		{Item: "journeycake", Source: RestockSourceProduce, Max: 12},
		{Item: "flour", Source: RestockSourceBuy, Max: 6},
		{Item: "water", Source: RestockSourceBuy, Max: 10},
		{Item: "thread", Source: RestockSourceBuy, Max: 6},
	}}
	return kinds, recipes, policy
}

func TestSpokenFor_MakingsFloorCapAndGarment(t *testing.T) {
	kinds, recipes, policy := spokenForFixture()
	h := BarterHolder{Policy: policy, Inventory: map[ItemKind]int{
		"flour": 6, "water": 12, "thread": 2, "homespun": 1, "journeycake": 5, "pelt": 1,
	}}
	got := SpokenFor(kinds, recipes, h)
	want := map[ItemKind]SpokenForClaim{
		"flour":    {Qty: 4, Reason: SpokenForMakings},  // recipe input: LLM-609 floor 2×2, the 2 above it spare
		"water":    {Qty: 10, Reason: SpokenForMakings}, // recipe input: floor 2×5, 2 spare
		"thread":   {Qty: 2, Reason: SpokenForMakings},  // no recipe: cap 6 reserves everything held
		"homespun": {Qty: 1, Reason: SpokenForGarment},  // the unit on her back
	}
	if len(got) != len(want) {
		t.Fatalf("SpokenFor = %v, want %v", got, want)
	}
	for kind, c := range want {
		if got[kind] != c {
			t.Errorf("SpokenFor[%s] = %+v, want %+v", kind, got[kind], c)
		}
	}
	for kind, spare := range map[ItemKind]int{"flour": 2, "water": 2, "thread": 0, "homespun": 0, "journeycake": 5, "pelt": 1} {
		if s := SpareQty(h.Inventory, got, kind); s != spare {
			t.Errorf("SpareQty(%s) = %d, want %d", kind, s, spare)
		}
	}
}

func TestSpokenFor_StockholderReservesNothing(t *testing.T) {
	kinds, recipes, policy := spokenForFixture()
	h := BarterHolder{Policy: policy, Stockholder: true, Inventory: map[ItemKind]int{"thread": 2, "homespun": 1}}
	if got := SpokenFor(kinds, recipes, h); got != nil {
		t.Errorf("stockholder SpokenFor = %v, want nil", got)
	}
}

func TestSpokenFor_GarmentSpareUnitStaysTradeable(t *testing.T) {
	kinds, recipes, _ := spokenForFixture()
	h := BarterHolder{Inventory: map[ItemKind]int{"homespun": 2}}
	got := SpokenFor(kinds, recipes, h)
	if got["homespun"] != (SpokenForClaim{Qty: 1, Reason: SpokenForGarment}) {
		t.Errorf("SpokenFor[homespun] = %+v, want 1 garment unit", got["homespun"])
	}
	if s := SpareQty(h.Inventory, got, "homespun"); s != 1 {
		t.Errorf("SpareQty(homespun) = %d, want 1 (the fresh spare)", s)
	}
}

func TestHoldsBarterableGoodsExcept_HonorsSpokenFor(t *testing.T) {
	kinds, recipes, policy := spokenForFixture()
	// The live Hannah pack: only makings and the suit on her back — no means to
	// barter, so the buy cue lands on the no-means scene instead of the treadmill.
	h := BarterHolder{Policy: policy, Inventory: map[ItemKind]int{"thread": 2, "water": 6, "homespun": 1}}
	if HoldsBarterableGoodsExcept(kinds, recipes, h, "salt") {
		t.Error("a pack of makings + the worn garment counted as barterable")
	}
	// Her own produce is spare and flips it.
	h.Inventory["journeycake"] = 1
	if !HoldsBarterableGoodsExcept(kinds, recipes, h, "salt") {
		t.Error("own produce not counted as barterable")
	}
	// The distributor's identical pack is all wares (LLM-406 stays whole).
	d := BarterHolder{Policy: policy, Stockholder: true, Inventory: map[ItemKind]int{"thread": 2, "water": 6}}
	if !HoldsBarterableGoodsExcept(kinds, recipes, d, "salt") {
		t.Error("stockholder's buy-line stock not counted as barterable")
	}
}
