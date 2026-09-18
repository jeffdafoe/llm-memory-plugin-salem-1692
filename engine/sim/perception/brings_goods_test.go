package perception

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// The carter's second call at the smithy rendered "there was little left unsaid"
// beside "Offer it: call sell …", and he left with the iron. The contact brake
// stays off for the keeper a seller still carries goods for — and only for him.
func TestBringsGoodsTo(t *testing.T) {
	seller := func(mut func(*sim.ActorSnapshot)) *sim.ActorSnapshot {
		a := &sim.ActorSnapshot{
			Inventory: map[sim.ItemKind]int{"iron": 4},
			VisitorState: &sim.VisitorState{Trade: &sim.TradeErrand{
				Direction: sim.TradeDirectionSell, Carter: true, Good: "iron", Keeper: "ezekiel",
				Legs: []sim.CarterLeg{{Good: "iron", Qty: 4, Counterparty: "smithy", Keeper: "ezekiel", Unit: 2}},
			}},
		}
		if mut != nil {
			mut(a)
		}
		return a
	}
	for _, tc := range []struct {
		name   string
		subj   *sim.ActorSnapshot
		member sim.ActorID
		want   bool
	}{
		{"the keeper the goods are for", seller(nil), "ezekiel", true},
		{"a bystander at the same shop", seller(nil), "josiah", false},
		{"the pack is empty — sold already", seller(func(a *sim.ActorSnapshot) { a.Inventory["iron"] = 0 }), "ezekiel", false},
		{"the errand is settled", seller(func(a *sim.ActorSnapshot) { a.VisitorState.Trade.Settled = true }), "ezekiel", false},
		{"a carter on a buy leg has nothing to offer", seller(func(a *sim.ActorSnapshot) { a.VisitorState.Trade.Legs[0].Buy = true }), "ezekiel", false},
		{"a buyer's errand", seller(func(a *sim.ActorSnapshot) { a.VisitorState.Trade.Direction = sim.TradeDirectionBuy }), "ezekiel", false},
		{"a villager", &sim.ActorSnapshot{}, "ezekiel", false},
	} {
		if got := bringsGoodsTo(tc.subj, tc.member); got != tc.want {
			t.Errorf("%s: bringsGoodsTo = %v, want %v", tc.name, got, tc.want)
		}
	}
}
