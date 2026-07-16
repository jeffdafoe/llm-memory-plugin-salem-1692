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

	// A pack of only eat-here food + a service token is NO means to barter —
	// the live porridge-as-currency shape, now correctly a payment dead-end.
	if HoldsBarterableGoodsExcept(kinds, map[ItemKind]int{"stew": 8, "nights_stay": 1}, "") {
		t.Error("eat-here food + service token counted as barterable goods")
	}
	// One portable good flips it.
	if !HoldsBarterableGoodsExcept(kinds, map[ItemKind]int{"stew": 8, "bread": 1}, "") {
		t.Error("portable good not counted as barterable")
	}
	// except still excludes the item being bought.
	if HoldsBarterableGoodsExcept(kinds, map[ItemKind]int{"bread": 1}, "bread") {
		t.Error("the bought item itself counted as payment for itself")
	}
	// Zero-qty rows never count.
	if HoldsBarterableGoodsExcept(kinds, map[ItemKind]int{"bread": 0}, "") {
		t.Error("zero-qty holding counted as barterable")
	}
}
