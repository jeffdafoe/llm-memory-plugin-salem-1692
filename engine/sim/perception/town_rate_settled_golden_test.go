package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// town_rate_settled_golden_test.go — LLM-607. The situation a keeper who has paid
// the town rate every day for a week stands with the constable who collected it.
//
// The companion to peer_disputes_a_payment_never_made, from the OTHER side of the
// same pair. That scenario pins what the constable is told; this one pins what the
// keeper is told, and the keeper is the one who asks for the money back.
//
// The live shape, at the James Farm on 2026-08-06. Moses James had paid Constable
// Gideon Marsh a single coin of town rate on each of eight days. Marsh returned
// eight of the nine coins he had collected, across three refunds, the last of them
// "the final coin owing on the eight day's rates — we are square now". Nine days of
// levy netted the town one coin.
//
// Nothing in Moses's scene was wrong. His arithmetic was exact and the durable log
// agreed with it to the coin — LLM-572 had already stopped the money being invented.
// What his scene did not contain was any statement that a rate paid is a rate
// settled. The town-rate cue was suppressed the moment he squared up, so the only
// thing left about Marsh was a record of nine single coins going one way with
// nothing coming back, which is the shape of nine orders placed and never filled.
//
// Two halves of the fixture are load-bearing:
//
//   - RateOwed is 0. Before this ticket buildTownRateKeeper returned nil on exactly
//     that, so the settled state — the state a daily payer is in nearly always — was
//     the one state the scene never described.
//   - The eight payments carry Due. Without it the coin line reads "you have paid
//     Gideon Marsh a coin 8 times, and nothing has come back the other way", which
//     is a true sentence that supports a false conclusion.

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "keeper_square_on_a_week_of_town_rate",
			summary: "LLM-607: Moses James, who has paid the town rate on eight straight days, stands at his farm with " +
				"Constable Gideon Marsh. The farm owes nothing. The golden pins the two lines that keep eight settled " +
				"levies from reading as eight unpaid debts: the town-rate cue saying the rate is paid and done with " +
				"(which used to vanish entirely once he was square), and the coin-dealings line naming the eight coins " +
				"as the town's due rather than as money that never came back. Live shape, 2026-08-06 — on the reading " +
				"this fixture reproduces, the constable refunded eight of the nine coins he had collected.",
			build: keeperSquareOnAWeekOfTownRate,
		},
	)
}

func keeperSquareOnAWeekOfTownRate() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		marshID = sim.ActorID("gideon")
		mosesID = sim.ActorID("moses")
		farm    = sim.StructureID("james_farm")
	)
	start, end := 480, 1080
	now := 732 // 12:12 — the hour the live refund conversation ran
	published := time.Date(2026, 8, 6, 12, 12, 0, 0, time.UTC)

	moses := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Moses James",
		Role:              "farmer",
		State:             sim.StateIdle,
		Pos:               sim.TilePos{X: 10, Y: 10},
		WorkStructureID:   farm,
		InsideStructureID: farm,
		CurrentHuddleID:   "h1",
		Coins:             12,
		Needs:             map[sim.NeedKey]int{},
		Acquaintances:     map[string]sim.Acquaintance{"Constable Gideon Marsh": {}},
	}
	marsh := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Constable Gideon Marsh",
		Role:              "constable",
		State:             sim.StateIdle,
		Pos:               sim.TilePos{X: 10, Y: 10},
		InsideStructureID: farm,
		CurrentHuddleID:   "h1",
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             9,
		Needs:             map[sim.NeedKey]int{},
		AttributeSlugs:    []string{sim.AttrConstable},
		Acquaintances:     map[string]sim.Acquaintance{"Moses James": {}},
	}

	// Eight single coins of rate, one a day, the last of them this morning. Spaced a
	// day apart so every one of them lands inside the one-week recall window and the
	// count the render states is the count the fixture describes.
	rates := make([]sim.CoinPayment, 0, 8)
	for i := 7; i >= 0; i-- {
		rates = append(rates, sim.CoinPayment{
			At:     published.Add(-time.Duration(i) * 24 * time.Hour),
			Amount: 1,
			Kind:   sim.CoinPaymentForDue,
		})
	}

	return &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			marshID: marsh, mosesID: moses,
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			"h1": {ID: "h1", Members: map[sim.ActorID]struct{}{marshID: {}, mosesID: {}}},
		},
		Structures: map[sim.StructureID]*sim.Structure{
			farm: plainStructure(farm, "James Farm"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(farm): {
				ID:           sim.VillageObjectID(farm),
				DisplayName:  "James Farm",
				OwnerActorID: mosesID,
				Tags:         []string{sim.TagBusiness},
				// Square. This is the branch that used to render nothing at all.
				RateOwed: 0,
			},
		},
		CoinRecord: map[sim.ActorID]map[sim.ActorID]*sim.CoinPairRecord{
			mosesID: {marshID: {Paid: rates}},
			marshID: {mosesID: {Received: rates}},
		},
		CoinRecordWindow: sim.DefaultCoinRecordWindow,
	}, mosesID, nil
}

// TestSettledRateReadsAsSettled pins the assertion behind the golden. A golden diff
// alone would let a wrong wording through as "intended", and the wording is the whole
// mechanism here — there is no tool call and no state change to check.
func TestSettledRateReadsAsSettled(t *testing.T) {
	got := renderScenario(perceptionScenario{
		name:  "keeper_square_on_a_week_of_town_rate",
		build: keeperSquareOnAWeekOfTownRate,
	})
	for _, want := range []string{
		// The town-rate cue, which used to be suppressed entirely once he squared up.
		"The rate on the James Farm stands settled — nothing is owing on it, and nothing is owed back.",
		// The coin record, saying what eight single coins one way actually were.
		"You have paid Constable Gideon Marsh a coin 8 times, all of it the town's due — settled as it was handed over, and no goods owed back.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("keeper's prompt is missing the settled reading.\nwant line: %s\n--- got ---\n%s", want, got)
		}
	}
	// The clause the due replaces. On a pure-due record it is a true sentence that
	// invites the false conclusion, so its ABSENCE is part of the fix rather than
	// incidental to it.
	if strings.Contains(got, "nothing has come back the other way") {
		t.Errorf("settled levies must not be described as money that never came back:\n%s", got)
	}
}

// The mixed case: one direction carrying dues AND ordinary purchases, the other
// carrying a plain purchase (code_review). This is the shape most likely to lose
// information, because the presence of a single due switches the WHOLE pair to the
// two-sentence form and suppresses the "nothing came back" tail for both
// directions.
//
// What the render must keep separable: the total flow, the due portion of it, and
// the fact that the peer paid in the other direction too. What it deliberately does
// not say is that nothing came back for the non-due remainder — the section is a
// record of coin, not a statement of what is owed, and the grievance channel is the
// relationship file. Naming a portion as a due while leaving the reader to infer a
// debt from the rest would recreate the defect at a smaller scale.
func TestMixedDueAndPurchaseRendersBothDirections(t *testing.T) {
	d := sim.CoinDealings{
		// Four payments out, five coins: three single coins of rate, one 2-coin buy.
		PaidCount: 4, PaidTotal: 5, PaidDueCount: 3, PaidDueTotal: 3,
		PaidGoodsCount: 1, PaidGoodsTotal: 2,
		// One purchase in, no due — the constable buying wheat off the farm.
		ReceivedCount: 1, ReceivedTotal: 4,
		ReceivedGoodsCount: 1, ReceivedGoodsTotal: 4,
	}
	got := coinDealingsSentence("Constable Gideon Marsh", d)
	for _, want := range []string{
		// The incoming direction carries no due, so it voices its own register
		// rather than inheriting the outgoing one's silence (LLM-612).
		"Constable Gideon Marsh has paid you 4 coins for goods.",
		// The outgoing direction is mixed. The DUE portion is named and the goods
		// portion is not: two portions in one sentence turns a recollection into an
		// itemized statement, and the due is the clause that carries a correctness
		// job rather than a register one.
		"You have paid Constable Gideon Marsh 5 coins across 4 payments, 3 coins of it the town's due — settled as it was handed over, and no goods owed back.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("mixed sentence is missing a half.\nwant: %s\n got: %s", want, got)
		}
	}
	// "paid back" would cast the levies as repayment of a debt to the collector.
	if strings.Contains(got, "paid back") {
		t.Errorf("a direction carrying a due is described as repayment: %s", got)
	}
}

// The clause boundaries, including the singular partial the review flagged. The
// difference between branches is one phrase in a line a weak model reads every
// tick, so each is pinned rather than left to the two golden fixtures.
func TestCoinDueClauseBoundaries(t *testing.T) {
	const closing = " — settled as it was handed over, and no goods owed back"
	cases := []struct {
		name                      string
		dueCount, dueTotal, count int
		want                      string
	}{
		{"no due at all", 0, 0, 5, ""},
		{"a single payment, and it was the due", 1, 1, 1, ", the town's due" + closing},
		{"every payment was a due", 8, 8, 8, ", all of it the town's due" + closing},
		{"one due coin among five payments", 1, 1, 5, ", 1 coin of it the town's due" + closing},
		{"two due coins among five payments", 1, 2, 5, ", 2 coins of it the town's due" + closing},
		// Defensive: a dueCount past count cannot arise from tallyCoinPayments (the
		// due subset is counted from the same loop), but the branch must not fall
		// through to a partial phrase naming more coin than the direction holds.
		{"more dues than payments", 6, 6, 5, ", all of it the town's due" + closing},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := coinDueClause(tc.dueCount, tc.dueTotal, tc.count); got != tc.want {
				t.Errorf("coinDueClause(%d, %d, %d) = %q, want %q", tc.dueCount, tc.dueTotal, tc.count, got, tc.want)
			}
		})
	}
}

// The goods clause boundaries (LLM-612), mirroring the due ones above. Shorter by
// design and carrying no closing: a levy needs explaining because coin that never
// comes back is counterintuitive, while goods for coin is the ordinary shape of a
// trade and a closing on it would be boilerplate re-read every tick on nearly every
// line in the village.
func TestCoinGoodsClauseBoundaries(t *testing.T) {
	cases := []struct {
		name                          string
		goodsCount, goodsTotal, count int
		want                          string
	}{
		{"nothing bought", 0, 0, 5, ""},
		{"a single payment, and it bought goods", 1, 3, 1, " for goods"},
		{"every payment bought goods", 24, 168, 24, " for goods"},
		{"one coin of five payments bought goods", 1, 1, 5, ", 1 coin of it for goods"},
		{"seven coins of five payments bought goods", 2, 7, 5, ", 7 coins of it for goods"},
		// Defensive, matching the due clause's own guard: a goodsCount past count
		// cannot arise from tallyCoinPayments, but the branch must not fall through
		// to a partial phrase naming a portion of a direction it exceeds.
		{"more goods than payments", 6, 9, 5, " for goods"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := coinGoodsClause(tc.goodsCount, tc.goodsTotal, tc.count); got != tc.want {
				t.Errorf("coinGoodsClause(%d, %d, %d) = %q, want %q", tc.goodsCount, tc.goodsTotal, tc.count, got, tc.want)
			}
		})
	}
}

// A due outranks goods within one direction. The two cannot co-occur today —
// settleTownRate is reachable only from the bare-coin Pay command, which mints no
// ledger entry — so this pins the tiebreak against a future path that produced both.
// The due clause is the one carrying a correctness job: it is what stops a levy
// reading as an order placed and never filled.
func TestCoinDirectionClausePrefersTheDue(t *testing.T) {
	got := coinDirectionClause(2, 2, 3, 9, 5)
	if !strings.Contains(got, "the town's due") {
		t.Errorf("a direction carrying a due must voice the due: %q", got)
	}
	if strings.Contains(got, "for goods") {
		t.Errorf("naming both portions turns a recollection into an itemized statement: %q", got)
	}
}

// TestGoldensNeverTellAKeeperASettledRateIsOwedBack is the cross-scenario invariant.
//
// The property: in no situation may a scene describe coin that discharged a due as
// coin that went out and never returned. That pairing is what a reader turns into a
// debt, and a debt against a constable is a refund — real coin moving back out of
// the levy, which is how the town collected nine coins in nine days and kept one.
//
// Runs over the whole matrix rather than the one scenario above, because the failure
// is a rendering that can appear in any scene where a due sits on the record. Any
// future edit that reintroduces the "nothing came back" tail beside a due — or that
// drops the due clause and leaves the tail — trips this for every affected fixture.
func TestGoldensNeverTellAKeeperASettledRateIsOwedBack(t *testing.T) {
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, warrants := sc.build()
			if !scenarioHoldsADue(snap, actorID) {
				return
			}
			out := combinedPrompt(Render(Build(snap, actorID, warrants), DefaultRenderConfig()))
			for _, banned := range []string{
				"nothing has come back the other way",
				"nothing has gone back the other way",
			} {
				if strings.Contains(out, banned) {
					t.Errorf("a due is on this pair's record, yet the scene says %q:\n%s", banned, out)
				}
			}
		})
	}
}

// scenarioHoldsADue reports whether the subject has a due on record with anyone —
// the condition under which the invariant above applies. Reads the fixture's own
// CoinRecord rather than the rendered text, so the check cannot be satisfied by the
// very wording it is policing.
func scenarioHoldsADue(snap *sim.Snapshot, actorID sim.ActorID) bool {
	return scenarioHoldsKind(snap, actorID, sim.CoinPaymentForDue)
}

// scenarioHoldsKind is the shared predicate behind both coin-record invariants.
func scenarioHoldsKind(snap *sim.Snapshot, actorID sim.ActorID, kind sim.CoinPaymentKind) bool {
	if snap == nil {
		return false
	}
	for _, rec := range snap.CoinRecord[actorID] {
		if rec == nil {
			continue
		}
		for _, trail := range [][]sim.CoinPayment{rec.Paid, rec.Received} {
			for _, p := range trail {
				if p.Kind == kind {
					return true
				}
			}
		}
	}
	return false
}
