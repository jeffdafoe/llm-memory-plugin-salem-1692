package handlers

import (
	"reflect"
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

// TestDealWords_OnlyForWordsThatWentOut runs the real pay_with_item and
// counter_pay handlers with a line naming ale, which Bob sells but the bread
// deal does not carry. Addressed to Carol, who is not in the conversation, the
// vocative gate refuses the words while the offer or counter still stands — and
// a refused line spoke of nothing, so no DealWords. The same line without the
// address goes out and names the ale.
func TestDealWords_OnlyForWordsThatWentOut(t *testing.T) {
	ale := sim.DealWords{Named: []string{"ale"}, Deal: "1 bread"}
	setup := func(t *testing.T) *sim.World {
		w := buildPayResponseWorld(t)
		w.Actors["bob"].Inventory["ale"] = 3
		w.Actors["carol"] = &sim.Actor{ID: "carol", DisplayName: "Carol", Kind: sim.KindNPCShared,
			State: sim.StateIdle, RecentActions: sim.NewRingBuffer[sim.Action](4)}
		return w
	}
	for _, tc := range []struct {
		name string
		say  string
		want sim.DealWords
	}{
		{"refused words", "Carol, a loaf of bread and an ale for four.", sim.DealWords{}},
		{"words that went out", "A loaf of bread and an ale for four.", ale},
	} {
		t.Run("pay_with_item: "+tc.name, func(t *testing.T) {
			w := setup(t)
			cmd, err := HandlePayWithItem(HandlerInput{ActorID: "alice", AttemptID: "tk-1", HasNewNews: true,
				Args: PayWithItemArgs{Seller: "Bob", Item: "bread", Qty: 1, Amount: 4, Say: tc.say}})
			if err != nil {
				t.Fatalf("HandlePayWithItem: %v", err)
			}
			res, err := cmd.Fn(w)
			if err != nil {
				t.Fatalf("pay_with_item: %v", err)
			}
			placed := res.(sim.PayWithItemResult)
			if placed.State != sim.PayLedgerStatePending {
				t.Fatalf("State = %q, want pending — the offer must stand either way", placed.State)
			}
			if placed.Announced != (len(tc.want.Named) > 0) {
				t.Fatalf("Announced = %v (refused %q) — fixture did not drive the path under test", placed.Announced, placed.SayRefused)
			}
			if !reflect.DeepEqual(placed.DealWords, tc.want) {
				t.Errorf("DealWords = %+v, want %+v", placed.DealWords, tc.want)
			}
		})
		t.Run("counter_pay: "+tc.name, func(t *testing.T) {
			w := setup(t)
			ledgerID := placePendingBreadOffer(t, w, 2, time.Now().UTC())
			cmd, err := HandleCounterPay(HandlerInput{ActorID: "bob", AttemptID: "tk-2", HasNewNews: true,
				Args: CounterPayArgs{LedgerID: LenientID(ledgerID), Amount: 4, Say: tc.say}})
			if err != nil {
				t.Fatalf("HandleCounterPay: %v", err)
			}
			res, err := cmd.Fn(w)
			if err != nil {
				t.Fatalf("counter_pay: %v", err)
			}
			out := res.(payResponseResult)
			if out.State != sim.PayLedgerStateCountered {
				t.Fatalf("State = %q, want countered — the counter must stand either way", out.State)
			}
			if out.Announced != (len(tc.want.Named) > 0) {
				t.Fatalf("Announced = %v (refused %q) — fixture did not drive the path under test", out.Announced, out.SayRefused)
			}
			if !reflect.DeepEqual(out.DealWords, tc.want) {
				t.Errorf("DealWords = %+v, want %+v", out.DealWords, tc.want)
			}
		})
	}
}
