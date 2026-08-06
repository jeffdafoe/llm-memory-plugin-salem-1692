package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// coin_wage_golden_test.go — LLM-613. An employer reading what he has paid the man
// who works for him.
//
// The situation, from the live durable log for the seven days to 2026-08-06: Josiah
// Thorne keeps the General Store and Lewis Walker does odd jobs for him. Nine coin
// movements passed between them in that week. Seven were `paid` rows and reached the
// coin record. TWO were `labored` rows — wages settled by the labor mechanism when a
// contract completed — and those reached nothing at all, because RecordCoinPaid was
// called from the `paid` subscribers and from nowhere else.
//
// So his prompt said he had paid Lewis 4 coins against 20 received. He had paid 12.
//
// The missing eight are the whole shape of the defect: a `labored` row only ever
// carries coin from employer to worker, so the omission could not average out. Every
// employer in the village under-read what he had paid, every worker under-read what
// he had earned, and the section built so that a villager could not deny money was
// the thing doing the denying.
//
// The fixture uses the real nine movements at their real amounts and registers,
// which is what makes the paid direction worth pinning: it holds all three of goods
// (one ledger-settled payment), work (two completed contracts) and coin the engine
// cannot account for (one bare pay). A wage is not a purchase and not a levy, and
// this line has to say so without claiming to know what the bare pay was.

// goldenCoinWageNow is the scene's clock — midday on the last day of the live window
// the fixture reproduces. The payment times below are the real ones, so they are
// stated absolutely rather than as offsets: an offset would drift against the
// one-week recall window if this constant ever moved.
var goldenCoinWageNow = time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

// goldenJosiahLewisWeek returns the pair's real week from JOSIAH's side — what he
// paid Lewis, and what Lewis paid him — at the amounts, times and registers the
// durable log holds.
//
// The registers are read off the settlement path each row attests to, never off any
// text: the five payments in and the 08-03 payment out carry a ledger_id, so they
// bought goods; the two `labored` rows are completed labor contracts, so they paid
// for work; the 08-05 bare pay carries no marker of either sort, so the engine
// cannot say what it was and the record does not guess.
func goldenJosiahLewisWeek() (paid, received []sim.CoinPayment) {
	at := func(day, hour, minute int) time.Time {
		return time.Date(2026, 8, day, hour, minute, 0, 0, time.UTC)
	}
	paid = []sim.CoinPayment{
		{At: at(1, 19, 30), Amount: 4, Kind: sim.CoinPaymentForWork},
		{At: at(3, 19, 23), Amount: 2, Kind: sim.CoinPaymentForGoods},
		{At: at(5, 15, 47), Amount: 2, Kind: sim.CoinPaymentUnstated},
		{At: at(5, 19, 51), Amount: 4, Kind: sim.CoinPaymentForWork},
	}
	received = []sim.CoinPayment{
		{At: time.Date(2026, 7, 31, 15, 31, 0, 0, time.UTC), Amount: 4, Kind: sim.CoinPaymentForGoods},
		{At: at(2, 20, 17), Amount: 2, Kind: sim.CoinPaymentForGoods},
		{At: at(3, 15, 32), Amount: 3, Kind: sim.CoinPaymentForGoods},
		{At: at(3, 19, 7), Amount: 1, Kind: sim.CoinPaymentForGoods},
		{At: at(5, 15, 48), Amount: 10, Kind: sim.CoinPaymentForGoods},
	}
	return paid, received
}

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "employer_reads_the_wages_he_paid",
			summary: "LLM-613 (the live Josiah Thorne / Lewis Walker week to 2026-08-06): a keeper at his post " +
				"with the man he hires, and the nine coin movements between them. The golden pins '## Coin between " +
				"you and those here' counting the two labor-settled wages — 8 of the 12 coins he paid — which " +
				"before this reached neither the live tally nor the boot seed. The section read 20 in against 4 " +
				"out, a five-to-one that denied two thirds of what the employer had actually handed over.",
			build: employerReadsTheWagesHePaid,
		},
	)
}

func employerReadsTheWagesHePaid() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		josiahID  = sim.ActorID("josiah")
		lewisID   = sim.ActorID("lewis")
		store     = sim.StructureID("general_store")
		home      = sim.StructureID("thorne_residence")
		lewisHome = sim.StructureID("walker_residence")
		huddle    = sim.HuddleID("h1")
	)
	start, end := 360, 1260 // 06:00–21:00
	now := 720              // midday, at his post and on shift
	published := goldenCoinWageNow
	josiah := &sim.ActorSnapshot{
		Kind:               sim.KindNPCStateful,
		DisplayName:        "Josiah Thorne",
		Role:               "merchant",
		State:              sim.StateIdle,
		WorkStructureID:    store,
		InsideStructureID:  store,
		HomeStructureID:    home,
		ScheduleStartMin:   &start,
		ScheduleEndMin:     &end,
		BusinessownerState: &sim.BusinessownerState{},
		Coins:              1,
		Needs:              map[sim.NeedKey]int{},
		Inventory:          map[sim.ItemKind]int{"flour": 12, "carrots": 4, "firewood": 2},
		Acquaintances:      map[string]sim.Acquaintance{"Lewis Walker": {}},
		CurrentHuddleID:    huddle,
	}
	lewis := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Lewis Walker",
		Role:              "laborer",
		State:             sim.StateIdle,
		InsideStructureID: store,
		HomeStructureID:   lewisHome,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             9,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{},
		CurrentHuddleID:   huddle,
	}
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{josiahID: josiah, lewisID: lewis},
		Structures: map[sim.StructureID]*sim.Structure{
			store:     plainStructure(store, "General Store"),
			home:      plainStructure(home, "Thorne Residence"),
			lewisHome: plainStructure(lewisHome, "Walker Residence"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddle: {ID: huddle, Members: map[sim.ActorID]struct{}{josiahID: {}, lewisID: {}}},
		},
	}

	paid, received := goldenJosiahLewisWeek()
	// Both directions on both actors' records — RecordCoinPaid writes the ordered
	// pair twice, so seeding one side would render a pair production never produces.
	snap.CoinRecord = map[sim.ActorID]map[sim.ActorID]*sim.CoinPairRecord{
		josiahID: {lewisID: {Paid: paid, Received: received}},
		lewisID:  {josiahID: {Paid: received, Received: paid}},
	}
	snap.CoinRecordWindow = sim.DefaultCoinRecordWindow
	return snap, josiahID, nil
}

// TestEmployerReadsTheWagesHePaid is the assertion behind the golden. A golden diff
// alone would let the old under-count through as "intended".
func TestEmployerReadsTheWagesHePaid(t *testing.T) {
	got := renderScenario(perceptionScenario{
		name:  "employer_reads_the_wages_he_paid",
		build: employerReadsTheWagesHePaid,
	})
	const want = "Lewis Walker has paid you 20 coins across 5 payments for goods, and you have paid Lewis Walker 12 coins across 4 payments, 2 coins of it for goods and 8 coins of it for work done."
	if !strings.Contains(got, want) {
		t.Errorf("the employer's wages must be on the record.\nwant line: %s\n--- got ---\n%s", want, got)
	}
	// The count is the defect, stated on its own so a later rewording cannot quietly
	// take the wages back out: 4 was what the section said before, and it is the one
	// number that must not reappear on this direction.
	if strings.Contains(got, "you have paid Lewis Walker 4 coins") {
		t.Errorf("the two labor-settled wages are missing from the record again:\n%s", got)
	}
}

// TestGoldensNeverDenyThatWorkWasDoneFor is the cross-scenario invariant, and the
// generalization of LLM-612's goods twin.
//
// The property: where a direction's coin paid for labor, no scene may also say
// nothing came back the other way. Labor is not goods, but it is emphatically not
// nothing — a man told he paid out 8 coins and nothing came back is being handed the
// exact inference this section exists to refute, and he is the one who hired the
// help. Runs over the whole matrix, because a wage can sit on any pair where one
// villager works for another.
func TestGoldensNeverDenyThatWorkWasDoneFor(t *testing.T) {
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, warrants := sc.build()
			if !scenarioHoldsKind(snap, actorID, sim.CoinPaymentForWork) {
				return
			}
			out := combinedPrompt(Render(Build(snap, actorID, warrants), DefaultRenderConfig()))
			for _, banned := range []string{
				"nothing has come back the other way",
				"nothing has gone back the other way",
			} {
				if strings.Contains(out, banned) {
					t.Errorf("work was done for this pair's coin, yet the scene says %q:\n%s", banned, out)
				}
			}
		})
	}
}

// TestWageDirectionNamesEveryPortion pins the property rather than the phrasing: a
// direction carrying three registers must account for all three.
//
// It is the same requirement code_review put on the due/goods pair in LLM-612, one
// register wider. Naming only the wages would leave the purchase unaccounted for and
// naming only the purchase would restore this ticket's own defect, so the test reads
// each portion's coin figure independently of the sentence that carries them.
func TestWageDirectionNamesEveryPortion(t *testing.T) {
	paid, received := goldenJosiahLewisWeek()
	got := coinDealingsSentence("Lewis Walker", (&sim.Snapshot{
		CoinRecord: map[sim.ActorID]map[sim.ActorID]*sim.CoinPairRecord{
			"josiah": {"lewis": {Paid: paid, Received: received}},
		},
		CoinRecordWindow: sim.DefaultCoinRecordWindow,
	}).CoinDealingsFor("josiah", "lewis", goldenCoinWageNow))
	for _, portion := range []string{
		"12 coins across 4 payments",  // the whole direction, wages included
		"2 coins of it for goods",     // the one ledger-settled purchase
		"8 coins of it for work done", // the two completed contracts
	} {
		if !strings.Contains(got, portion) {
			t.Errorf("the direction must state %q:\n%s", portion, got)
		}
	}
	// The bare pay is the fourth coin movement and the engine cannot account for it.
	// Nothing may claim it: the portions name 10 of the 12 coins, and the remaining 2
	// are unqualified on purpose.
	if strings.Contains(got, "10 coins of it for work done") || strings.Contains(got, "all of it") {
		t.Errorf("coin the engine cannot account for was folded into a portion:\n%s", got)
	}
}
