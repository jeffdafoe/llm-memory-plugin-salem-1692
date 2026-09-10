package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// coin_dispute_golden_test.go — LLM-572. The situation a peer disputes a payment
// that was never made, from the side of the actor being asked to believe it.
//
// Reproduces the live shape rather than an abstraction of it. At the James Farm on
// 2026-07-30 at 19:00:22 UTC, Moses James told Constable Gideon Marsh he had paid
// him for help that never came. Marsh answered "once on the day you gave me your
// coin" and handed back five coins. What Moses had actually paid him, across the
// whole of agent_action_log — which is not pruned and runs to 2026-04-25 — was two
// single coins of town rate, on 07-29 at 14:34 and 07-30 at 12:18. Both timestamps
// are in the fixture.
//
// Two details of the fixture are load-bearing and easy to "clean up" wrongly:
//
//   - Marsh is KindNPCStateful. That is not incidental colour; buildRelationships
//     is gated to KindNPCShared, so the "## What you remember of those here"
//     section is empty for him and the coin line is the ONLY per-peer record in
//     his prompt. Flipping him to shared would hide the regression this pins.
//   - Moses is a KEEPER (the James Farm is his owned business). That is what makes
//     the constable's estate-rate line render here — the section that tells him the
//     rate runs toward the town, not toward him. LLM-572 first widened the old
//     town-rate cue to render on a paid-up keeper because, with the section
//     suppressed, the constable paid a town rate TO a tavern keeper three minutes
//     after the refund; LLM-655 keeps that lesson with one static line.

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "peer_disputes_a_payment_never_made",
			summary: "LLM-572 money-claim ground truth: Constable Gideon Marsh (STATEFUL, so his Relationships are nil " +
				"and this is his only per-peer record) stands at the James Farm with Moses James, who is about to claim " +
				"he paid for help never delivered. The golden pins '## Coin between you and those here' reporting the two " +
				"single coins of town rate Moses actually paid — the live record, 2026-07-29 14:34 and 2026-07-30 12:18, " +
				"dues from the retired LLM-557 levy that the coin record still carries — so a five-coin refund cannot " +
				"clear against it. It also pins the collector's estate-rate line (LLM-655): a keeper is in front of him, " +
				"so the constable is told the rate is the town's and none of it his to hand back — the line whose " +
				"absence had him paying a town rate to a tavern keeper.",
			build: peerDisputesAPaymentNeverMade,
		},
	)
}

func peerDisputesAPaymentNeverMade() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		marshID = sim.ActorID("gideon")
		mosesID = sim.ActorID("moses")
		farm    = sim.StructureID("james_farm")
	)
	start, end := 480, 1080 // 08:00-18:00, the constable's watch
	now := 1140             // 19:00 — the hour of the live scene
	published := time.Date(2026, 7, 30, 19, 0, 0, 0, time.UTC)

	marsh := &sim.ActorSnapshot{
		// STATEFUL. See the file header — this is what makes the coin line his only
		// per-peer record.
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Constable Gideon Marsh",
		Role:              "constable",
		State:             sim.StateIdle,
		Pos:               sim.TilePos{X: 10, Y: 10},
		InsideStructureID: farm,
		CurrentHuddleID:   "h1",
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             6,
		Needs:             map[sim.NeedKey]int{},
		AttributeSlugs:    []string{sim.AttrConstable},
		Acquaintances:     map[string]sim.Acquaintance{"Moses James": {}},
	}
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

	// The record, exactly as production holds it: two single coins, both from Moses
	// to Marsh, and nothing at all the other way. Held on BOTH actors' records
	// because RecordCoinPaid writes the ordered pair in both directions.
	//
	// Due (LLM-607) because both coins WERE the town rate of the day — the retired
	// LLM-557 levy's bare-coin settle classified them, and the durable rows still
	// seed the record that way. Leaving the kind Unstated here would pin a rendering
	// production does not produce for this pair.
	first := time.Date(2026, 7, 29, 14, 34, 15, 0, time.UTC)
	second := time.Date(2026, 7, 30, 12, 18, 58, 0, time.UTC)
	rates := []sim.CoinPayment{{At: first, Amount: 1, Kind: sim.CoinPaymentForDue}, {At: second, Amount: 1, Kind: sim.CoinPaymentForDue}}

	snap := &sim.Snapshot{
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
		// Owned + business, so the farm is rateable and Moses is a keeper — the gate
		// on the constable's estate-rate line.
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(farm): {
				ID:           sim.VillageObjectID(farm),
				DisplayName:  "James Farm",
				OwnerActorID: mosesID,
				Tags:         []string{sim.TagBusiness},
			},
		},
		CoinRecord: map[sim.ActorID]map[sim.ActorID]*sim.CoinPairRecord{
			marshID: {mosesID: {Received: rates}},
			mosesID: {marshID: {Paid: rates}},
		},
		CoinRecordWindow: sim.DefaultCoinRecordWindow,
	}
	return snap, marshID, nil
}

// TestCoinDisputeGoldenStatesTheRealAmount is the assertion behind the golden: the
// scenario is worthless if it renders the section but gets the figure wrong, and a
// golden diff alone would let a wrong figure through as "intended".
//
// Pins the two facts that refute the live claim — that Moses paid, and that he paid
// a coin twice rather than five coins once — and, since LLM-607, that both coins
// were a due, which is what makes a "return of your coin" beat unsupportable. The
// count alone never did: two coins in with nothing back reads as two debts owing,
// and over the following week Marsh refunded eight coins of rate on exactly that
// reading.
func TestCoinDisputeGoldenStatesTheRealAmount(t *testing.T) {
	got := renderScenario(perceptionScenario{
		name:  "peer_disputes_a_payment_never_made",
		build: peerDisputesAPaymentNeverMade,
	})
	const want = "Moses James has paid you a coin twice, all of it the town's due — settled as it was handed over, and no goods owed back."
	if !strings.Contains(got, want) {
		t.Errorf("constable's prompt must state what Moses actually paid.\nwant line: %s\n--- got ---\n%s", want, got)
	}
	// The five-coin claim must find no support anywhere in the prompt.
	if strings.Contains(got, "5 coins") {
		t.Errorf("prompt mentions 5 coins — nothing in the record supports it:\n%s", got)
	}
}

// TestCoinDisputeGoldenTellsTheConstableWhichWayTheRateRuns pins the third half of
// LLM-572 on the same fixture, in its LLM-655 shape. Before LLM-572 the constable's
// prompt said nothing about the levy whenever nobody owed — so he paid a "Town rate
// — Tavern day's fee" of one coin to John Ellis at 19:03:43, three minutes after the
// refund this scenario reproduces. A keeper in front of him is the whole gate now,
// and the line says the rate is the town's and none of it his to hand back.
func TestCoinDisputeGoldenTellsTheConstableWhichWayTheRateRuns(t *testing.T) {
	got := renderScenario(perceptionScenario{
		name:  "peer_disputes_a_payment_never_made",
		build: peerDisputesAPaymentNeverMade,
	})
	if !strings.Contains(got, "## Town rate\n"+estateRateCollectorLine) {
		t.Errorf("collector line must render with a keeper present:\n%s", got)
	}
}

// TestGoldensOnlyAConstableHearsTheRateSection is the cross-scenario invariant.
//
// The section is the constable's alone: the keeper's side of the estate rate is the
// engine debit and his own action ring, and the retired town-rate cue that used to
// tell a keeper "Settle it with pay (recipient: …)" is gone. Rendered to anyone
// else, the line would tell a villager the town pays HIS wage out of a rate he
// collects — the mirror image of the 19:03:43 defect. Runs over the whole matrix so
// the property holds for any situation, not just the scenarios built for it.
func TestGoldensOnlyAConstableHearsTheRateSection(t *testing.T) {
	seen := false
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, warrants := sc.build()
			a := snap.Actors[actorID]
			if a == nil {
				return
			}
			out := combinedPrompt(Render(Build(snap, actorID, warrants), DefaultRenderConfig()))
			has := strings.Contains(out, "## Town rate")
			if has && !isConstableSnapshot(a) {
				t.Errorf("%q is not a constable and is told the rate pays their wage:\n%s", a.DisplayName, out)
			}
			if has {
				seen = true
			}
		})
	}
	if !seen {
		t.Fatal("no scenario rendered the constable's rate section — the invariant is vacuous")
	}
}
