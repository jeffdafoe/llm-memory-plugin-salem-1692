package handlers

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/perception"
)

// TestCanPayWithItemMatchesGate pins perception.CanPayWithItem, which every cue that
// says "call pay_with_item" is held to (perception's
// TestGoldensPayCueOnlyWhenPayOffered), to gateTools' own pay_with_item decision
// (LLM-1007). It also pins the two behavior changes: a seller a buy cue names in
// the same building opens the tool with no huddle yet, and walking closes it. sell
// stays huddle-only.
func TestCanPayWithItemMatchesGate(t *testing.T) {
	r := laborSpeakOnlyRegistry(t)
	walkingSnap := &sim.Snapshot{Actors: map[sim.ActorID]*sim.ActorSnapshot{
		"hannah": {MoveDestKind: sim.MoveDestinationStructureVisit, MoveDestStructureID: "store"},
	}}
	walking := perception.ActorView{InFlightMove: &perception.InFlightMoveView{}}
	restockSeller := &perception.RestockingView{Items: []perception.RestockItemView{
		{Kind: "whetstone", ItemLabel: "whetstone", Cap: 4, CoPresentSeller: "Ezekiel Crane", SellerStock: 2},
	}}
	upkeepSeller := &perception.FarmUpkeepView{ShovelsOwed: 2, ShovelsShort: 2, CoPresentSeller: "Ezekiel Crane", SellerStock: 2}

	for _, tc := range []struct {
		name     string
		payload  perception.Payload
		snap     *sim.Snapshot
		wantPay  bool
		wantSell bool
	}{
		{"huddle peer", perception.Payload{ActorID: "hannah", Surroundings: speakAudience()}, nil, true, true},
		{"alone, no cue", perception.Payload{ActorID: "hannah"}, nil, false, false},
		{"no huddle, restock names a seller in the building", perception.Payload{ActorID: "hannah", Restocking: restockSeller}, nil, true, false},
		{"no huddle, farm upkeep names a seller in the building", perception.Payload{ActorID: "hannah", FarmUpkeep: upkeepSeller}, nil, true, false},
		// A seller named only on the bide arm (her offer already stands) offers no
		// buy, so it must not reopen the no-huddle guard (code_review).
		{"no huddle, building seller only on a bide branch", perception.Payload{ActorID: "hannah",
			FarmUpkeep: &perception.FarmUpkeepView{ShovelsOwed: 2, ShovelsShort: 2, CoPresentSeller: "Ezekiel Crane", SellerStock: 2, PendingOffer: true}}, nil, false, false},
		{"walking with a huddle peer", perception.Payload{ActorID: "hannah", Surroundings: speakAudience(), Actor: walking}, walkingSnap, false, true},
		{"walking with a named seller", perception.Payload{ActorID: "hannah", Restocking: restockSeller, Actor: walking}, walkingSnap, false, false},
		{"visitor off his rounds' commerce places", perception.Payload{ActorID: "hannah", Surroundings: speakAudience(), VisitorCommerceStripped: true}, nil, false, false},
		{"baker mid the bake", bakingPayload(), nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			names := specNameSet(gateTools(r, tc.payload, tc.snap))
			advertised := names["pay_with_item"] == 1
			if got := perception.CanPayWithItem(tc.payload); got != advertised {
				t.Errorf("CanPayWithItem = %v, but gateTools advertises pay_with_item = %v", got, advertised)
			}
			if advertised != tc.wantPay {
				t.Errorf("pay_with_item advertised = %v, want %v", advertised, tc.wantPay)
			}
			if sell := names["sell"] == 1; sell != tc.wantSell {
				t.Errorf("sell advertised = %v, want %v (sell stays huddle-only)", sell, tc.wantSell)
			}
		})
	}
}
