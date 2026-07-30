package sim

import "testing"

// town_rate_fact_internal_test.go — LLM-572. The wording a settled town rate leaves
// in a relationship's fact trail.
//
// This text is read back by buildConsolidationPrompt under "What the ledger records
// of your dealings", where LLM-499 tells the model the ledger is the true record and
// to trust it over what was said. The generic pay text put a payment with a stated
// purpose and no delivery against it into that group — the shape of an order placed
// and never filled — and Moses James duly concluded the constable "takes my coin ...
// and I get nothing", then collected a five-coin refund on the strength of it.

func TestTownRatePaidFactText(t *testing.T) {
	store := &VillageObject{DisplayName: "James Farm"}
	cases := []struct {
		name                  string
		subject, verb, object string
		amount, settled       int
		business              *VillageObject
		want                  string
	}{
		{
			name:    "the live case, payer's side",
			subject: "I", verb: "paid", object: "Constable Gideon Marsh",
			amount: 1, settled: 1, business: store,
			want: "I paid Constable Gideon Marsh the day's rate on the James Farm, 1 coin — the town's due, owed and now settled. No goods were bought and none are owed in return.",
		},
		{
			name:    "the live case, collector's side",
			subject: "Moses James", verb: "paid", object: "me",
			amount: 1, settled: 1, business: store,
			want: "Moses James paid me the day's rate on the James Farm, 1 coin — the town's due, owed and now settled. No goods were bought and none are owed in return.",
		},
		{
			name:    "several days of arrears cleared at once",
			subject: "I", verb: "paid", object: "Constable Gideon Marsh",
			amount: 3, settled: 3, business: store,
			want: "I paid Constable Gideon Marsh the town rate on the James Farm, 3 coins — the town's due, owed and now settled. No goods were bought and none are owed in return.",
		},
		{
			// The over-broad settlement policy (see settleTownRate) means a gift or a
			// debt repayment can discharge the levy. Naming the whole sum a rate would
			// be a lie, so the two parts are stated separately.
			name:    "a larger payment that also cleared arrears",
			subject: "I", verb: "paid", object: "Constable Gideon Marsh",
			amount: 5, settled: 1, business: store,
			want: "I paid Constable Gideon Marsh 5 coins; 1 coin of it was the day's rate owing on the James Farm — the town's due, owed and now settled. No goods were bought and none are owed in return.",
		},
		{
			// A business with no usable name drops the "on the X" clause rather than
			// rendering "on the ".
			name:    "nil business drops the place clause",
			subject: "I", verb: "paid", object: "Constable Gideon Marsh",
			amount: 1, settled: 1, business: nil,
			want: "I paid Constable Gideon Marsh the day's rate, 1 coin — the town's due, owed and now settled. No goods were bought and none are owed in return.",
		},
		{
			name:    "blank business name drops the place clause",
			subject: "I", verb: "paid", object: "Constable Gideon Marsh",
			amount: 1, settled: 1, business: &VillageObject{DisplayName: "   "},
			want: "I paid Constable Gideon Marsh the day's rate, 1 coin — the town's due, owed and now settled. No goods were bought and none are owed in return.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := townRatePaidFactText(tc.subject, tc.verb, tc.object, tc.amount, tc.settled, tc.business)
			if got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

// The fact has to survive the write-time truncation intact: NewSalientFact caps at
// MaxSalientFactTextLen runes, and a bare prefix here is a memory that ENDS
// MID-SENTENCE — which is exactly how the closing "none are owed in return" clause,
// the load-bearing half, would be lost.
func TestTownRatePaidFactTextFitsTheSalientFactCap(t *testing.T) {
	// Deliberately long, well past any real village name or display name.
	business := &VillageObject{DisplayName: "The Old Meeting House Farm and Orchard at the Crossing"}
	text := townRatePaidFactText("Constable Gideon Marsh", "paid", "me", 999, 3, business)
	if n := len([]rune(text)); n > MaxSalientFactTextLen {
		t.Errorf("fact text is %d runes, over the %d-rune cap — the closing clause would be truncated away:\n%s",
			n, MaxSalientFactTextLen, text)
	}
}

// settleTownRate reports what it settled and on which business, which is what lets
// the pay path describe the payment. Silent before LLM-572, which is why nothing
// downstream could tell a tax from a purchase.
func TestSettleTownRateReportsWhatItSettled(t *testing.T) {
	newWorld := func(owed int) (*World, *Actor, *Actor) {
		w := &World{
			Actors: map[ActorID]*Actor{
				"josiah": {ID: "josiah", DisplayName: "Josiah Thorne"},
				"gideon": {ID: "gideon", DisplayName: "Constable Gideon Marsh", Attributes: map[string][]byte{AttrConstable: nil}},
			},
			VillageObjects: map[VillageObjectID]*VillageObject{
				"store": {ID: "store", DisplayName: "General Store", OwnerActorID: "josiah", Tags: []string{TagBusiness}, RateOwed: owed},
			},
		}
		return w, w.Actors["josiah"], w.Actors["gideon"]
	}

	t.Run("full settlement reports the arrears, not the payment", func(t *testing.T) {
		w, payer, payee := newWorld(2)
		settled, business := settleTownRate(w, payer, payee, 7)
		if settled != 2 {
			t.Errorf("settled = %d, want 2 (the debt, not the 7 handed over)", settled)
		}
		if business == nil || business.ID != "store" {
			t.Errorf("business = %+v, want the General Store", business)
		}
	})
	t.Run("partial settlement reports the whole payment", func(t *testing.T) {
		w, payer, payee := newWorld(5)
		settled, business := settleTownRate(w, payer, payee, 2)
		if settled != 2 || business == nil {
			t.Errorf("settled = %d, business = %+v, want 2 on the store", settled, business)
		}
	})
	t.Run("nothing owed reports nothing", func(t *testing.T) {
		w, payer, payee := newWorld(0)
		if settled, business := settleTownRate(w, payer, payee, 5); settled != 0 || business != nil {
			t.Errorf("settled = %d, business = %+v, want 0/nil", settled, business)
		}
	})
	t.Run("payee is not a constable", func(t *testing.T) {
		w, payer, _ := newWorld(3)
		other := &Actor{ID: "hannah", DisplayName: "Hannah Boggs"}
		if settled, business := settleTownRate(w, payer, other, 3); settled != 0 || business != nil {
			t.Errorf("settled = %d, business = %+v, want 0/nil", settled, business)
		}
	})
}
