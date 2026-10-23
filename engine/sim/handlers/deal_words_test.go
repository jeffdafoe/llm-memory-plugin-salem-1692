package handlers

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/perception"
)

// TestCanOfferLotMatchesSellGate pins perception.CanOfferLot — which decides
// whether the offer cue may point at sell — to gateTools' own sell decision, for
// every condition that strips sell. A drift here would have the cue name a tool
// the seller does not have.
func TestCanOfferLotMatchesSellGate(t *testing.T) {
	r := laborSpeakOnlyRegistry(t)
	for _, tc := range []struct {
		name    string
		payload perception.Payload
	}{
		{"in company", perception.Payload{ActorID: "hannah", Surroundings: speakAudience()}},
		{"no huddle peer", perception.Payload{ActorID: "hannah"}},
		{"held hired hand at work", perception.Payload{ActorID: "hannah", Surroundings: speakAudience(),
			Laboring: &perception.LaboringView{Employer: "john", Until: time.Now().Add(time.Hour)}}},
		{"free hired hand", perception.Payload{ActorID: "hannah", Surroundings: speakAudience(),
			Laboring: &perception.LaboringView{Employer: "john", Free: true, Until: time.Now().Add(time.Hour)}}},
		{"visitor off his rounds' commerce places", perception.Payload{ActorID: "hannah", Surroundings: speakAudience(),
			VisitorCommerceStripped: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sellGated := specNameSet(gateTools(r, tc.payload, nil))["sell"] == 1
			if got := perception.CanOfferLot(tc.payload); got != sellGated {
				t.Errorf("CanOfferLot = %v, but gateTools advertises sell = %v", got, sellGated)
			}
		})
	}
}

// TestDealWordsNote_RidesEachResult: when the words named a good the deal does
// not carry, the offer, the counter and the accept results all say so; when they
// agree, each result reads exactly as before.
func TestDealWordsNote_RidesEachResult(t *testing.T) {
	water := sim.DealWords{Named: []string{"water"}, Deal: "1 fried meat"}
	const note = " Mind: you spoke of water too, but this deal is for 1 fried meat alone."

	offer := &ValidatedCall{Name: "pay_with_item", DecodedArgs: PayWithItemArgs{
		Seller: "Hannah Boggs", Item: "fried meat", Qty: 1, Amount: 5, Say: "a fried meat and a flask of water"}}
	counter := &ValidatedCall{Name: "counter_pay", DecodedArgs: CounterPayArgs{LedgerID: 1, Amount: 8, Say: "the meat and the water for eight"}}
	accept := &ValidatedCall{Name: "accept_pay", DecodedArgs: AcceptPayArgs{LedgerID: 1, Say: "and the water besides?"}}

	for _, tc := range []struct {
		name   string
		vc     *ValidatedCall
		result func(sim.DealWords) any
	}{
		{"placed offer", offer, func(dw sim.DealWords) any {
			return sim.PayWithItemResult{LedgerID: 1, State: sim.PayLedgerStatePending, Announced: true, DealWords: dw}
		}},
		{"counter stands", counter, func(dw sim.DealWords) any {
			return payResponseResult{State: sim.PayLedgerStateCountered, Announced: true, DealWords: dw}
		}},
		{"counter settles at the buyer's price", counter, func(dw sim.DealWords) any {
			return payResponseResult{State: sim.PayLedgerStateAccepted, Announced: true, DealWords: dw}
		}},
		{"accept", accept, func(dw sim.DealWords) any {
			return payResponseResult{State: sim.PayLedgerStateAccepted, Announced: true, DealWords: dw}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plain := commitResultContent(tc.vc, tc.result(sim.DealWords{}))
			if strings.Contains(plain, "Mind: you spoke of") {
				t.Fatalf("agreeing words still carry the note: %q", plain)
			}
			got := commitResultContent(tc.vc, tc.result(water))
			if got != plain+note {
				t.Errorf("result = %q, want the plain result plus the note:\n%q", got, plain+note)
			}
		})
	}
}
