package sim_test

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// wholesale_allowance_test.go — LLM-477. The buyer-side half of the Rule 1 wholesale
// gate: who may buy WHAT from a wholesaler-tagged seller. The distributor takes
// anything; a TRANSFORMER (itself stationed at a wholesaler-tagged structure) takes
// only the inputs its own recipes require; every other buyer takes nothing. All four
// enforcement points — the perception vendor scan, the co-present peer cue, the
// restock warrant's buy-path test, and the PayWithItem backstop — key off
// BuyerWholesaleAllowance, so this matrix pins the one shared definition.

// wholesaleTestRecipes mirrors the live catalog shape the gate reasons over: a
// transformer's recipe with a real input (flour ← wheat), a raw recipe with none
// (wheat, carrots, milk), and a recipe whose only input the producer self-sources
// (cheese ← milk, made by the same dairy that produces milk).
func wholesaleTestRecipes() map[sim.ItemKind]*sim.ItemRecipe {
	return map[sim.ItemKind]*sim.ItemRecipe{
		"flour":   {OutputItem: "flour", OutputQty: 5, Inputs: []sim.RecipeInput{{Item: "wheat", Qty: 5}}},
		"wheat":   {OutputItem: "wheat", OutputQty: 1},
		"carrots": {OutputItem: "carrots", OutputQty: 1},
		"milk":    {OutputItem: "milk", OutputQty: 1},
		"cheese":  {OutputItem: "cheese", OutputQty: 1, Inputs: []sim.RecipeInput{{Item: "milk", Qty: 3}}},
		// An elective booster must never grant a wholesale edge: production
		// never stalls on one, so it is not a required input (LLM-248 parity
		// with ReorderFloors).
		"bread": {OutputItem: "bread", OutputQty: 4,
			Inputs:      []sim.RecipeInput{{Item: "flour", Qty: 2}},
			BoostInputs: []sim.BoostInput{{Item: "milk", Qty: 1, BonusQty: 1}},
		},
	}
}

func TestProductionInputKinds(t *testing.T) {
	recipes := wholesaleTestRecipes()
	produce := func(items ...sim.ItemKind) *sim.RestockPolicy {
		p := &sim.RestockPolicy{}
		for _, it := range items {
			p.Restock = append(p.Restock, sim.RestockEntry{Item: it, Source: sim.RestockSourceProduce, Max: 20})
		}
		return p
	}

	cases := []struct {
		name   string
		policy *sim.RestockPolicy
		want   []sim.ItemKind // expected members; empty means the set is empty
	}{
		{"transformer requires its recipe input", produce("flour"), []sim.ItemKind{"wheat"}},
		{"raw producer requires nothing", produce("carrots", "wheat"), nil},
		{
			// Ellis Farm: cheese needs milk, but the same dairy produces milk, so it
			// procures nothing and the tier never reaches it.
			"self-sourced input is not procured", produce("cheese", "milk"), nil,
		},
		{
			// The decision this predicate exists for: an explicit `buy` row for a good
			// that feeds none of the actor's recipes is larder or trade stock, NOT a
			// production input, and must not earn a wholesale edge. Live shape —
			// Ellis Farm produces cheese/milk/meat and carries `buy: sage`.
			"explicit buy row for a non-input is excluded",
			&sim.RestockPolicy{Restock: []sim.RestockEntry{
				{Item: "cheese", Source: sim.RestockSourceProduce, Max: 15},
				{Item: "milk", Source: sim.RestockSourceProduce, Max: 30},
				{Item: "sage", Source: sim.RestockSourceBuy, Max: 3},
			}},
			nil,
		},
		{"elective booster is not a required input", produce("bread"), []sim.ItemKind{"flour"}},
		{"produces nothing", &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "sage", Source: sim.RestockSourceBuy, Max: 3},
		}}, nil},
		{"nil policy", nil, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sim.ProductionInputKinds(recipes, tc.policy)
			if len(got) != len(tc.want) {
				t.Fatalf("ProductionInputKinds = %v, want exactly %v", got, tc.want)
			}
			for _, kind := range tc.want {
				if !got[kind] {
					t.Errorf("ProductionInputKinds missing %q (got %v)", kind, got)
				}
			}
		})
	}
}

func TestBuyerWholesaleAllowance(t *testing.T) {
	objects := map[sim.VillageObjectID]*sim.VillageObject{
		"mill":          {ID: "mill", OwnerActorID: "joseph", Tags: []string{sim.TagWholesaler}},
		"james_farm":    {ID: "james_farm", OwnerActorID: "moses", Tags: []string{sim.TagFarm, sim.TagWholesaler}},
		"general_store": {ID: "general_store", OwnerActorID: "josiah", Tags: []string{sim.TagDistributor}},
		"the_inn":       {ID: "the_inn", OwnerActorID: "john"},
	}
	recipes := wholesaleTestRecipes()
	millPolicy := &sim.RestockPolicy{Restock: []sim.RestockEntry{
		{Item: "flour", Source: sim.RestockSourceProduce, Max: 20},
		{Item: "wheat", Source: sim.RestockSourceBuy, Max: 50},
		{Item: "firewood", Source: sim.RestockSourceForage, Max: 10},
	}}
	farmPolicy := &sim.RestockPolicy{Restock: []sim.RestockEntry{
		{Item: "carrots", Source: sim.RestockSourceProduce, Max: 30},
		{Item: "wheat", Source: sim.RestockSourceProduce, Max: 30},
	}}

	t.Run("distributor takes anything", func(t *testing.T) {
		a := sim.BuyerWholesaleAllowance(objects, recipes, "general_store", nil)
		if !a.All || !a.Any() {
			t.Fatalf("distributor allowance = %+v, want All", a)
		}
		for _, kind := range []sim.ItemKind{"wheat", "carrots", "cheese", "anything"} {
			if !a.Allows(kind) {
				t.Errorf("distributor should be allowed %q", kind)
			}
		}
	})

	t.Run("transformer takes its inputs only", func(t *testing.T) {
		a := sim.BuyerWholesaleAllowance(objects, recipes, "mill", millPolicy)
		if a.All {
			t.Fatalf("the mill is not the distributor; allowance = %+v", a)
		}
		if !a.Any() {
			t.Fatal("the mill should hold a non-empty allowance (wheat feeds its flour)")
		}
		if !a.Allows("wheat") {
			t.Error("the mill must be allowed to buy wheat wholesale — the whole point of LLM-477")
		}
		// Its own produce, its foraged good, and an unrelated good all stay gated:
		// the grant is scoped to what its recipes consume, nothing else.
		for _, kind := range []sim.ItemKind{"flour", "firewood", "carrots", "cheese"} {
			if a.Allows(kind) {
				t.Errorf("the mill must NOT be allowed to buy %q wholesale", kind)
			}
		}
	})

	t.Run("raw producer holds no allowance", func(t *testing.T) {
		// The farms-eat-each-other pin: a wholesaler that transforms nothing derives an
		// empty set, so one farm can never buy direct from another.
		a := sim.BuyerWholesaleAllowance(objects, recipes, "james_farm", farmPolicy)
		if a.Any() {
			t.Fatalf("a raw producer must hold no wholesale allowance; got %+v", a)
		}
		if a.Allows("cheese") || a.Allows("milk") {
			t.Error("one farm must not buy another farm's produce direct")
		}
	})

	t.Run("ordinary buyer holds no allowance", func(t *testing.T) {
		// An innkeeper transforms plenty (bread ← flour) but is NOT wholesaler-tagged,
		// so condition 1 fails and he stays routed through the distributor.
		innPolicy := &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "bread", Source: sim.RestockSourceProduce, Max: 20},
		}}
		a := sim.BuyerWholesaleAllowance(objects, recipes, "the_inn", innPolicy)
		if a.Any() || a.Allows("flour") {
			t.Fatalf("a non-wholesaler buyer must hold no allowance however much it transforms; got %+v", a)
		}
	})

	t.Run("zero value and unresolvable buyers fail closed", func(t *testing.T) {
		var zero sim.WholesaleAllowance
		if zero.Any() || zero.Allows("wheat") {
			t.Error("the zero allowance must deny everything")
		}
		if a := sim.BuyerWholesaleAllowance(objects, recipes, "", millPolicy); a.Any() {
			t.Error("no workplace must deny everything")
		}
		if a := sim.BuyerWholesaleAllowance(objects, recipes, "mill", nil); a.Any() {
			t.Error("nil policy must deny everything")
		}
		if a := sim.BuyerWholesaleAllowance(objects, nil, "mill", millPolicy); a.Any() {
			t.Error("an empty recipe catalog derives nothing, so it must deny everything")
		}
	})

	t.Run("scalar form agrees with the allowance", func(t *testing.T) {
		// BuyerMayBuyWholesale backs the two single-item call sites; it must never
		// disagree with the scan-side allowance.
		for _, kind := range []sim.ItemKind{"wheat", "flour", "carrots"} {
			want := sim.BuyerWholesaleAllowance(objects, recipes, "mill", millPolicy).Allows(kind)
			if got := sim.BuyerMayBuyWholesale(objects, recipes, "mill", millPolicy, kind); got != want {
				t.Errorf("BuyerMayBuyWholesale(%q) = %v, allowance says %v", kind, got, want)
			}
		}
	})
}
