package sim

import (
	"testing"
	"time"
)

// merchant_capital_test.go — the conserve determination (LLM-294/298) and its
// ware-scoping (LLM-462).
//
// The case table below is deliberately MIRRORED, case for case, by
// TestMerchantConserve_WareScopingCases in engine/sim/perception. actorConserving
// (here, over the live World) and merchantConserve (there, over a Snapshot) are
// hand-written twins that MUST agree — a warrant whose cue declines to render is a
// wake loop, which is the failure LLM-298 was filed for. Neither is reachable from
// the other's package, so the tables standing side by side is what catches drift:
// change one, and the other's identical case fails. Keep them in sync.

// conservePolicy is a RestockPolicy from bare entries, for readability in the table.
func conservePolicy(entries ...RestockEntry) *RestockPolicy {
	return &RestockPolicy{Restock: entries}
}

// TestActorConserving_WareScopingCases pins which holdings count as merchandise for
// the overstock half of the conserve verdict. The recurring shape: 19-20 units of
// something, a purse under the floor, and the question is only whether that something
// is a WARE (conserving) or the actor's own RAW MATERIAL (not conserving).
func TestActorConserving_WareScopingCases(t *testing.T) {
	porridgeFromWater := map[ItemKind]*ItemRecipe{
		"porridge": {OutputItem: "porridge", OutputQty: 4, RateQty: 4, RatePerHours: 1,
			Inputs: []RecipeInput{{Item: "water", Qty: 5}}},
	}
	cases := []struct {
		name      string
		inventory map[ItemKind]int
		policy    *RestockPolicy
		recipes   map[ItemKind]*ItemRecipe
		want      bool
		why       string
	}{
		{
			name:      "required_input_only",
			inventory: map[ItemKind]int{"water": 19, "porridge": 0},
			policy: conservePolicy(
				RestockEntry{Item: "porridge", Source: RestockSourceProduce, Max: 30},
				RestockEntry{Item: "water", Source: RestockSourceBuy, Max: 10},
			),
			recipes: porridgeFromWater,
			want:    false,
			why:     "the only pile is raw material and the ware shelf is bare — the empty-shelf exception",
		},
		{
			name:      "plain_ware",
			inventory: map[ItemKind]int{"water": 19},
			policy:    conservePolicy(RestockEntry{Item: "water", Source: RestockSourceBuy, Max: 10}),
			recipes:   map[ItemKind]*ItemRecipe{},
			want:      true,
			why:       "water feeds no recipe of hers, so 19 unsold is merchandise sitting still",
		},
		{
			name:      "dual_role_produced_and_consumed",
			inventory: map[ItemKind]int{"water": 40, "porridge": 0},
			policy: conservePolicy(
				RestockEntry{Item: "porridge", Source: RestockSourceProduce, Max: 30},
				RestockEntry{Item: "water", Source: RestockSourceProduce, Max: 30},
			),
			recipes: porridgeFromWater,
			want:    false,
			why:     "required-input-always-wins: a good the actor both sells and cooks with stays raw material (see the invariant in merchant_capital.go)",
		},
		{
			name:      "input_plus_separate_overstocked_ware",
			inventory: map[ItemKind]int{"water": 19, "ale": 20},
			policy: conservePolicy(
				RestockEntry{Item: "porridge", Source: RestockSourceProduce, Max: 30},
				RestockEntry{Item: "water", Source: RestockSourceBuy, Max: 10},
				RestockEntry{Item: "ale", Source: RestockSourceBuy, Max: 24},
			),
			recipes: porridgeFromWater,
			want:    true,
			why:     "excluding the water pile must not excuse the 20 unsold ale beside it — this is the John Ellis shape",
		},
		{
			name:      "nil_recipes",
			inventory: map[ItemKind]int{"water": 19},
			policy: conservePolicy(
				RestockEntry{Item: "porridge", Source: RestockSourceProduce, Max: 30},
				RestockEntry{Item: "water", Source: RestockSourceBuy, Max: 10},
			),
			recipes: nil,
			want:    true,
			why:     "no catalog means nothing is known to be an input — everything held is a ware, the pre-LLM-462 behavior",
		},
		{
			name:      "elective_boost_input_is_a_ware",
			inventory: map[ItemKind]int{"salt": 20, "porridge": 0},
			policy: conservePolicy(
				RestockEntry{Item: "porridge", Source: RestockSourceProduce, Max: 30},
				RestockEntry{Item: "salt", Source: RestockSourceBuy, Max: 6},
			),
			recipes: map[ItemKind]*ItemRecipe{
				"porridge": {OutputItem: "porridge", OutputQty: 4, RateQty: 4, RatePerHours: 1,
					Inputs:      []RecipeInput{{Item: "water", Qty: 5}},
					BoostInputs: []BoostInput{{Item: "salt", Qty: 1, BonusQty: 3}}},
			},
			want: true,
			why:  "an elective booster never stalls the line, so a hoard of it is merchandise (ReorderFloors counts required Inputs only)",
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			a := &Actor{
				ID:            "keeper",
				Kind:          KindNPCStateful,
				LLMAgent:      "keeper-agent",
				Coins:         2, // below the floor in every case — the coin half is not what's under test
				Inventory:     c.inventory,
				RestockPolicy: c.policy,
			}
			w := restockWorld(a)
			w.Settings.MerchantCoinFloor = 10
			w.Recipes = c.recipes
			if got := actorConserving(w, a, time.Now().UTC()); got != c.want {
				t.Errorf("actorConserving = %v, want %v — %s", got, c.want, c.why)
			}
		})
	}
}

// TestActorConserving_HealthyPurseNeverConserves pins the coin half: the ware scoping
// only ever matters below the floor, and an explicit floor of 0 is the operator
// off-switch. Guards against the LLM-462 scoping change being read as the only gate.
func TestActorConserving_HealthyPurseNeverConserves(t *testing.T) {
	a := &Actor{
		ID:            "keeper",
		Kind:          KindNPCStateful,
		LLMAgent:      "keeper-agent",
		Inventory:     map[ItemKind]int{"ale": 20}, // plainly overstocked
		RestockPolicy: conservePolicy(RestockEntry{Item: "ale", Source: RestockSourceBuy, Max: 24}),
	}
	w := restockWorld(a)
	w.Settings.MerchantCoinFloor = 10

	a.Coins = 10 // at the floor, not below it
	if actorConserving(w, a, time.Now().UTC()) {
		t.Error("conserving at exactly the floor; the gate is coins < floor")
	}
	a.Coins = 2
	if !actorConserving(w, a, time.Now().UTC()) {
		t.Fatal("not conserving at 2 coins with 20 unsold ale — the fixture is vacuous")
	}
	w.Settings.MerchantCoinFloor = 0 // the operator off-switch
	if actorConserving(w, a, time.Now().UTC()) {
		t.Error("conserving with the floor disabled; an explicit 0 must stick")
	}
}
