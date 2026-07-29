package sim

import (
	"testing"
	"time"
)

// town_rate_internal_test.go — LLM-557. Unit coverage for the levy's three pure-ish
// pieces: the accrual rule, the daily assessment pass, and the settle that draws the
// debt down when a keeper actually pays.

func TestTownRateAccrual(t *testing.T) {
	tests := []struct {
		name                  string
		owed, perDay, maxOwed int
		want                  int
	}{
		{"first day", 0, 1, 3, 1},
		{"second day", 1, 1, 3, 2},
		{"clamped at the cap", 3, 1, 3, 3},
		{"cap reached from below in one step", 2, 5, 3, 3},
		{"uncapped when maxOwed is zero", 9, 1, 0, 10},
		// The off-switch FREEZES arrears rather than forgiving them: turning the levy
		// off mid-debt must not quietly wipe what a keeper already owes, so that
		// turning it back on resumes from where it stopped.
		{"disabled leaves arrears untouched", 2, 0, 3, 2},
		{"disabled leaves a zero balance at zero", 0, 0, 3, 0},
		{"negative perDay is treated as disabled", 2, -1, 3, 2},
		// Lowering the cap live clamps DOWN on the next assessment rather than
		// stranding a balance above the new ceiling.
		{"balance above a lowered cap clamps down", 8, 1, 3, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := TownRateAccrual(tc.owed, tc.perDay, tc.maxOwed); got != tc.want {
				t.Errorf("TownRateAccrual(%d, %d, %d) = %d, want %d",
					tc.owed, tc.perDay, tc.maxOwed, got, tc.want)
			}
		})
	}
}

// townRateWorld builds a world with one owned business, one unowned business, one
// non-business object, and (optionally) a constable — the scope matrix assessTownRate
// has to get right.
func townRateWorld(withConstable bool) *World {
	w := &World{
		Actors: map[ActorID]*Actor{
			"josiah": {ID: "josiah", DisplayName: "Josiah Thorne", Coins: 20},
		},
		VillageObjects: map[VillageObjectID]*VillageObject{
			"store": {
				ID: "store", DisplayName: "General Store",
				OwnerActorID: "josiah", Tags: []string{TagBusiness},
			},
			"derelict": {
				ID: "derelict", DisplayName: "Empty Shop",
				Tags: []string{TagBusiness}, // no owner — nobody to collect from
			},
			"well": {
				ID: "well", DisplayName: "Old Well",
				OwnerActorID: "josiah", // owned, but not a business
			},
		},
	}
	w.Settings.TownRateCoinsPerDay = 1
	w.Settings.TownRateMaxOwed = 3
	if withConstable {
		w.Actors["gideon"] = &Actor{
			ID: "gideon", DisplayName: "Constable Gideon Marsh",
			Attributes: map[string][]byte{AttrConstable: nil},
		}
	}
	return w
}

func TestAssessTownRate_ScopeAndAccrual(t *testing.T) {
	w := townRateWorld(true)

	assessTownRate(w, time.Now().UTC())
	if got := w.VillageObjects["store"].RateOwed; got != 1 {
		t.Errorf("owned business RateOwed = %d after one day, want 1", got)
	}
	if got := w.VillageObjects["derelict"].RateOwed; got != 0 {
		t.Errorf("UNOWNED business RateOwed = %d, want 0 — there is nobody to collect from", got)
	}
	if got := w.VillageObjects["well"].RateOwed; got != 0 {
		t.Errorf("non-business object RateOwed = %d, want 0 — the levy falls on shops", got)
	}

	// Four more days against a cap of three.
	for i := 0; i < 4; i++ {
		assessTownRate(w, time.Now().UTC())
	}
	if got := w.VillageObjects["store"].RateOwed; got != 3 {
		t.Errorf("RateOwed = %d after five days with cap 3, want 3 — arrears must plateau", got)
	}
}

// A village with no constable accrues nothing: the debt would be uncollectable and
// every keeper would carry a cue naming a man who does not exist.
func TestAssessTownRate_NoConstableNoAccrual(t *testing.T) {
	w := townRateWorld(false)
	assessTownRate(w, time.Now().UTC())
	if got := w.VillageObjects["store"].RateOwed; got != 0 {
		t.Errorf("RateOwed = %d with no constable in the village, want 0", got)
	}
}

// Turning the levy off stops accrual without forgiving what is already owed.
func TestAssessTownRate_DisabledFreezesArrears(t *testing.T) {
	w := townRateWorld(true)
	w.VillageObjects["store"].RateOwed = 2
	w.Settings.TownRateCoinsPerDay = 0

	assessTownRate(w, time.Now().UTC())
	if got := w.VillageObjects["store"].RateOwed; got != 2 {
		t.Errorf("RateOwed = %d with the levy disabled, want 2 — off must freeze, not forgive", got)
	}
}

func TestSettleTownRate(t *testing.T) {
	tests := []struct {
		name     string
		owed     int
		amount   int
		payerID  ActorID
		toPayee  string // "constable" or "ordinary"
		wantOwed int
	}{
		{"exact payment clears the debt", 3, 3, "josiah", "constable", 0},
		{"partial payment draws it down", 3, 1, "josiah", "constable", 2},
		// Overpaying settles the debt and no more — the surplus is a gift, which is
		// the keeper's to give, and must never drive the balance negative.
		{"overpayment floors at zero", 2, 10, "josiah", "constable", 0},
		// Paying anyone who is not a constable is an ordinary transaction.
		{"paying a non-constable is untouched", 3, 3, "josiah", "ordinary", 3},
		// Someone who keeps no shop has no rate to settle; the store's debt stands.
		{"payer who owns no business", 3, 3, "stranger", "constable", 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := townRateWorld(true)
			w.VillageObjects["store"].RateOwed = tc.owed
			w.Actors["stranger"] = &Actor{ID: "stranger", DisplayName: "A Traveller"}
			w.Actors["hannah"] = &Actor{ID: "hannah", DisplayName: "Hannah Boggs"}

			payee := w.Actors["gideon"]
			if tc.toPayee == "ordinary" {
				payee = w.Actors["hannah"]
			}
			settleTownRate(w, w.Actors[tc.payerID], payee, tc.amount)

			if got := w.VillageObjects["store"].RateOwed; got != tc.wantOwed {
				t.Errorf("RateOwed = %d, want %d", got, tc.wantOwed)
			}
		})
	}
}

// settleTownRate moves NO coin — the pay command has already done that by the time it
// runs. A settle that also touched purses would double-count the payment.
func TestSettleTownRate_MovesNoCoin(t *testing.T) {
	w := townRateWorld(true)
	w.VillageObjects["store"].RateOwed = 3
	keeperCoins := w.Actors["josiah"].Coins
	constableCoins := w.Actors["gideon"].Coins

	settleTownRate(w, w.Actors["josiah"], w.Actors["gideon"], 3)

	if w.Actors["josiah"].Coins != keeperCoins {
		t.Errorf("keeper coins moved: %d → %d", keeperCoins, w.Actors["josiah"].Coins)
	}
	if w.Actors["gideon"].Coins != constableCoins {
		t.Errorf("constable coins moved: %d → %d", constableCoins, w.Actors["gideon"].Coins)
	}
}

func TestSettleTownRate_NilSafe(t *testing.T) {
	w := townRateWorld(true)
	// None of these may panic — the pay path calls this on every coin payment in the
	// village, including ones with no business or constable anywhere near them.
	settleTownRate(nil, w.Actors["josiah"], w.Actors["gideon"], 1)
	settleTownRate(w, nil, w.Actors["gideon"], 1)
	settleTownRate(w, w.Actors["josiah"], nil, 1)
	settleTownRate(w, w.Actors["josiah"], w.Actors["gideon"], 0)
	settleTownRate(w, w.Actors["josiah"], w.Actors["gideon"], -5)
}

func TestIsRateableBusiness(t *testing.T) {
	owned := &VillageObject{OwnerActorID: "josiah", Tags: []string{TagBusiness}}
	unowned := &VillageObject{Tags: []string{TagBusiness}}
	notBusiness := &VillageObject{OwnerActorID: "josiah"}

	if !IsRateableBusiness(owned) {
		t.Error("an owned business should be rateable")
	}
	if IsRateableBusiness(unowned) {
		t.Error("an unowned business should not be rateable — nobody to collect from")
	}
	if IsRateableBusiness(notBusiness) {
		t.Error("a non-business object should not be rateable")
	}
	if IsRateableBusiness(nil) {
		t.Error("nil must not be rateable")
	}
}

func TestActorIsConstable(t *testing.T) {
	if !ActorIsConstable(&Actor{Attributes: map[string][]byte{AttrConstable: nil}}) {
		t.Error("an actor carrying the constable attribute should read as a constable")
	}
	if ActorIsConstable(&Actor{Attributes: map[string][]byte{"washerwoman": nil}}) {
		t.Error("another attribute must not read as constable")
	}
	if ActorIsConstable(&Actor{}) {
		t.Error("an attributeless actor must not read as constable")
	}
	if ActorIsConstable(nil) {
		t.Error("nil must not read as constable")
	}
}
