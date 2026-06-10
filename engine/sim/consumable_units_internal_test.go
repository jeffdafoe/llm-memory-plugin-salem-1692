package sim

import "testing"

// consumable_units_internal_test.go — unit matrix for consumableUnits
// (ZBBS-WORK-391), the shared needs-clamp behind both the Consume command
// and commitPayTransfer's consume_now branch. Behavior-level coverage
// (inventory routing, events, results) lives in item_commands_test.go and
// pay_with_item_commands_test.go; this pins the arithmetic edges directly.
func TestConsumableUnits(t *testing.T) {
	bread := &ItemKindDef{
		Name:      "bread",
		Satisfies: []ItemSatisfaction{{Attribute: "hunger", Immediate: 8}},
	}
	ale := &ItemKindDef{
		Name: "ale",
		Satisfies: []ItemSatisfaction{
			{Attribute: "thirst", Immediate: 4},
			{Attribute: "hunger", Immediate: 2},
		},
	}

	cases := []struct {
		name   string
		needs  map[NeedKey]int
		def    *ItemKindDef
		maxQty int
		want   int
	}{
		{"exact fit", map[NeedKey]int{"hunger": 16}, bread, 10, 2},
		{"ceil rounds up", map[NeedKey]int{"hunger": 9}, bread, 10, 2},
		{"one unit covers", map[NeedKey]int{"hunger": 5}, bread, 10, 1},
		{"clamped to maxQty", map[NeedKey]int{"hunger": 24}, bread, 2, 2},
		{"zero need floors at one", map[NeedKey]int{"hunger": 0}, bread, 10, 1},
		{"nil needs floors at one", nil, bread, 10, 1},
		{"multi-attribute takes the max", map[NeedKey]int{"thirst": 4, "hunger": 8}, ale, 10, 4},
		{"maxQty one short-circuits", map[NeedKey]int{"hunger": 24}, bread, 1, 1},
		{"nil def passes maxQty through", map[NeedKey]int{"hunger": 24}, nil, 7, 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			actor := &Actor{ID: "a", Needs: tc.needs}
			if got := consumableUnits(actor, tc.def, tc.maxQty); got != tc.want {
				t.Errorf("consumableUnits = %d, want %d", got, tc.want)
			}
		})
	}

	t.Run("nil actor passes maxQty through", func(t *testing.T) {
		if got := consumableUnits(nil, bread, 5); got != 5 {
			t.Errorf("consumableUnits(nil actor) = %d, want 5", got)
		}
	})
}
