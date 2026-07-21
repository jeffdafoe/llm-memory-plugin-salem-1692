package sim

import "testing"

// TestVisitorMerchantLabel_ProvisionerForFood pins the LLM-503 persona split:
// a buy errand bound to a FOOD good (Satisfies carries a hunger entry) labels
// the traveler "provisioner" — he is stocking for the road, not journeying to
// fetch a snack — while a non-food good keeps the grounded "<good>-buyer"
// label and a sell errand keeps "factor".
func TestVisitorMerchantLabel_ProvisionerForFood(t *testing.T) {
	w := &World{ItemKinds: map[ItemKind]*ItemKindDef{
		"journeycake": {
			Name:      "journeycake",
			Satisfies: []ItemSatisfaction{{Attribute: "hunger", Immediate: 3}},
		},
		"skillet": {
			Name:         "skillet",
			DisplayLabel: "skillet",
		},
	}}

	cases := []struct {
		name  string
		trade *TradeErrand
		want  string
	}{
		{"food buy → provisioner", &TradeErrand{Direction: TradeDirectionBuy, Good: "journeycake"}, ProvisionerArchetype},
		{"non-food buy → good-buyer", &TradeErrand{Direction: TradeDirectionBuy, Good: "skillet"}, "skillet-buyer"},
		{"sell → factor", &TradeErrand{Direction: TradeDirectionSell, Good: "iron"}, FactorArchetype},
		{"nil errand → empty", nil, ""},
	}
	for _, tc := range cases {
		if got := visitorMerchantLabel(w, tc.trade); got != tc.want {
			t.Errorf("%s: visitorMerchantLabel = %q, want %q", tc.name, got, tc.want)
		}
	}
}
