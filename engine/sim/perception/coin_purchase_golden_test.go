package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// coin_purchase_golden_test.go — LLM-612. A distributor reading his own week of
// stock-buying, in the scene it was read in.
//
// The situation, live on 2026-08-06 at 17:38 UTC: Josiah Thorne, who keeps the
// General Store, stands inside the Ellis Farm haggling with Elizabeth Ellis over
// cheese. His prompt told him:
//
//	Elizabeth Ellis has paid you 4 coins across 2 payments, and you have paid back
//	168 coins across 24 payments.
//
// He had not paid back anything. Every one of those 24 payments was a ledger-settled
// purchase with a goods leg against it — cheese, milk and meat off her farm, which he
// then sold on in the village to third parties whose coin never appears in a pairwise
// line at all. Rendered as repayment, the ordinary shape of a distributor's trade
// reads as a man who has been over-settling 42 to 1.
//
// The fixture is deliberately built on keeperOffPostBuying, the LLM-611 scene, rather
// than on a scene of its own: both cues were in the SAME live prompt, and a keeper
// away from his post buying stock is exactly the situation in which a lopsided
// purchase history is put in front of him. The one variable this arm adds is the coin
// record.
//
// What it pins is the register, not the arithmetic. The counts were always right.

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "distributor_buying_stock_from_a_supplier",
			summary: "LLM-612 (the live 2026-08-06 17:38 Josiah prompt): the same off-post buying scene as " +
				"keeper_off_post_buying_in_company, plus the week of coin behind it — 168 coins out to Elizabeth Ellis " +
				"across 24 ledger-settled purchases against 4 coins in. The golden pins '## Coin between you and those " +
				"here' voicing both directions as trade rather than repayment. Before this the section said he had " +
				"'paid back' 168 coins, which casts a distributor's ordinary stock-buying as the discharge of a debt " +
				"and makes a 42-to-1 outflow read as chronic over-settlement.",
			build: distributorBuyingStockFromASupplier,
		},
	)
}

func distributorBuyingStockFromASupplier() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		josiahID    = sim.ActorID("josiah")
		elizabethID = sim.ActorID("elizabeth")
	)
	snap, subjectID, warrants := keeperOffPostBuying(true)

	// The live week, at the amounts the durable log holds: 23 settled purchases
	// running 07-30 to 08-06 plus one more to reach the 24 the prompt stated, and
	// two payments the other way. Varied amounts on purpose — a run of single coins
	// renders as "a coin 24 times", which is the town-rate shape and not this one.
	//
	// Every one is ForGoods because every one was a pay-with-item ledger entry
	// settling ACCEPTED. That is not a judgment about what Josiah meant by them; it
	// is which settlement path the engine ran, which is the only source of a
	// classification this record accepts.
	paidAmounts := []int{3, 3, 4, 4, 6, 12, 6, 27, 1, 9, 5, 5, 1, 4, 6, 26, 16, 4, 6, 4, 4, 4, 4, 4}
	paid := make([]sim.CoinPayment, 0, len(paidAmounts))
	for i, amount := range paidAmounts {
		paid = append(paid, sim.CoinPayment{
			// Spread across the six days before the scene, oldest first, all inside
			// the one-week recall window.
			At:     snap.PublishedAt.Add(-time.Duration(len(paidAmounts)-i) * 6 * time.Hour),
			Amount: amount,
			Kind:   sim.CoinPaymentForGoods,
		})
	}
	received := []sim.CoinPayment{
		{At: snap.PublishedAt.Add(-52 * time.Hour), Amount: 2, Kind: sim.CoinPaymentForGoods},
		{At: snap.PublishedAt.Add(-9 * time.Hour), Amount: 2, Kind: sim.CoinPaymentForGoods},
	}

	// Both directions on both actors' records — RecordCoinPaid writes the ordered
	// pair twice, so a fixture that seeded only one side would render a pair
	// production never produces.
	snap.CoinRecord = map[sim.ActorID]map[sim.ActorID]*sim.CoinPairRecord{
		josiahID:    {elizabethID: {Paid: paid, Received: received}},
		elizabethID: {josiahID: {Paid: received, Received: paid}},
	}
	snap.CoinRecordWindow = sim.DefaultCoinRecordWindow
	return snap, subjectID, warrants
}

// TestDistributorReadsHisBuyingAsTrade is the assertion behind the golden. The
// scenario is worthless if it renders the section and gets the register wrong, and a
// golden diff alone would let the old wording through as "intended".
func TestDistributorReadsHisBuyingAsTrade(t *testing.T) {
	got := renderScenario(perceptionScenario{
		name:  "distributor_buying_stock_from_a_supplier",
		build: distributorBuyingStockFromASupplier,
	})
	const want = "Elizabeth Ellis has paid you 4 coins across 2 payments for goods, and you have paid Elizabeth Ellis 168 coins across 24 payments for goods."
	if !strings.Contains(got, want) {
		t.Errorf("the distributor's week of stock-buying must read as trade.\nwant line: %s\n--- got ---\n%s", want, got)
	}
	// The figures themselves were never in dispute and must survive the reframing —
	// this section exists to be checkable against a claim.
	for _, fact := range []string{"168 coins across 24 payments", "4 coins across 2 payments"} {
		if !strings.Contains(got, fact) {
			t.Errorf("the record must still state %q:\n%s", fact, got)
		}
	}
}

// TestGoldensNeverCallAPaymentARepayment is a cross-scenario invariant, and the one
// that would have caught this at the source.
//
// The property: NO scene, in any situation, describes a payment as having been paid
// back. "Back" asserts that the coin discharged something owed — a purpose — and this
// record deliberately carries no purposes. It was not merely the wrong register for a
// purchase; it was a claim the record had no basis for about a wage, a gift or a tip
// either. The joined two-way form is the only place it ever appeared, so a future
// edit that reaches for the shorter phrasing to avoid naming the peer twice trips
// this for every affected fixture.
func TestGoldensNeverCallAPaymentARepayment(t *testing.T) {
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			if strings.Contains(renderScenario(sc), "paid back") {
				t.Errorf("a scene describes a payment as repayment, which this record cannot know:\n%s", renderScenario(sc))
			}
		})
	}
}

// TestGoldensNeverDenyThatGoodsCameBack is the second invariant, and the sharper
// half of the ticket.
//
// The property: where a direction's coin bought goods, no scene may also say nothing
// came back the other way. That pairing is not an odd register — it is false. Meat
// came back. It is the same shape the due invariant polices from the other side
// (TestGoldensNeverTellAKeeperASettledRateIsOwedBack), and both exist because the
// tail is a blanket denial that only holds when the record genuinely cannot say what
// the coin did.
//
// Runs over the whole matrix rather than the one scenario, because the failure can
// appear in any scene where a purchase sits on the record — which, in this village,
// is roughly seven payments in eight.
func TestGoldensNeverDenyThatGoodsCameBack(t *testing.T) {
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, warrants := sc.build()
			if !scenarioHoldsKind(snap, actorID, sim.CoinPaymentForGoods) {
				return
			}
			out := combinedPrompt(Render(Build(snap, actorID, warrants), DefaultRenderConfig()))
			for _, banned := range []string{
				"nothing has come back the other way",
				"nothing has gone back the other way",
			} {
				if strings.Contains(out, banned) {
					t.Errorf("goods were bought on this pair's record, yet the scene says %q:\n%s", banned, out)
				}
			}
		})
	}
}
