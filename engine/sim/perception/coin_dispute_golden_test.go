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
//   - The James Farm owes nothing. It is the paid-up branch of the collector cue
//     (LLM-572's third half), and it renders here because a keeper is in front of
//     him — before this ticket that whole section was suppressed whenever nobody
//     was in arrears, which is how the constable came to pay a town rate TO a
//     tavern keeper three minutes after the refund.

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "peer_disputes_a_payment_never_made",
			summary: "LLM-572 money-claim ground truth: Constable Gideon Marsh (STATEFUL, so his Relationships are nil " +
				"and this is his only per-peer record) stands at the James Farm with Moses James, who is about to claim " +
				"he paid for help never delivered. The golden pins '## Coin between you and those here' reporting the two " +
				"single coins of town rate Moses actually paid — the live record, 2026-07-29 14:34 and 2026-07-30 12:18 " +
				"— so a five-coin refund cannot clear against it. It also pins the paid-up arm of the collector cue: the " +
				"farm owes nothing, and the constable is still told the rate runs TOWARD him, which is the line whose " +
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
	// Due (LLM-607) because both coins WERE the town rate — settleTownRate cleared the
	// farm's arrears with them, which is why RateOwed is 0 below. Leaving it false
	// here would make the fixture describe a farm that paid its rate with coin the
	// engine did not treat as rate, and would pin a rendering production no longer
	// produces for this pair.
	first := time.Date(2026, 7, 29, 14, 34, 15, 0, time.UTC)
	second := time.Date(2026, 7, 30, 12, 18, 58, 0, time.UTC)
	rates := []sim.CoinPayment{{At: first, Amount: 1, Due: true}, {At: second, Amount: 1, Due: true}}

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
		// Owned + business, so the farm is rateable — and RateOwed 0, so it is the
		// PAID-UP arm of the collector cue.
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(farm): {
				ID:           sim.VillageObjectID(farm),
				DisplayName:  "James Farm",
				OwnerActorID: mosesID,
				Tags:         []string{sim.TagBusiness},
				RateOwed:     0,
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
// LLM-572 on the same fixture. The James Farm owes nothing, and before this ticket
// buildTownRateCollector returned nil in exactly that case — so the constable's
// prompt said nothing about the levy at all, and he paid a "Town rate — Tavern
// day's fee" of one coin to John Ellis at 19:03:43, three minutes after the refund
// this scenario reproduces.
func TestCoinDisputeGoldenTellsTheConstableWhichWayTheRateRuns(t *testing.T) {
	got := renderScenario(perceptionScenario{
		name:  "peer_disputes_a_payment_never_made",
		build: peerDisputesAPaymentNeverMade,
	})
	if !strings.Contains(got, "The town rate keeps you, and it falls to you to collect it.") {
		t.Errorf("collector cue must render even with nothing owing:\n%s", got)
	}
	if !strings.Contains(got, "The rate on the James Farm is paid up — nothing is owing you here.") {
		t.Errorf("paid-up arm must name the settled business:\n%s", got)
	}
}

// TestGoldensNeverTellAConstableTheyOweTheRate is the cross-scenario invariant.
//
// The keeper cue ends in "Settle it with pay (recipient: …)" — an instruction to
// hand coin to the constable. Rendered to a constable it is an instruction to pay
// the levy he collects, which is the shape of the live 19:03:43 defect. The
// Collector flag is what keeps the two branches apart, and a future edit that
// dispatches on len(Debtors) again (as renderTownRate did before this ticket) would
// silently reintroduce it for every settled scene. Runs over the whole matrix so
// the property is pinned for any situation, not just the one scenario above.
func TestGoldensNeverTellAConstableTheyOweTheRate(t *testing.T) {
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, warrants := sc.build()
			a := snap.Actors[actorID]
			if a == nil || !isConstableSnapshot(a) {
				return
			}
			out := combinedPrompt(Render(Build(snap, actorID, warrants), DefaultRenderConfig()))
			if strings.Contains(out, "Settle it with pay") {
				t.Errorf("constable %q is told to settle a rate he collects:\n%s", a.DisplayName, out)
			}
		})
	}
}
