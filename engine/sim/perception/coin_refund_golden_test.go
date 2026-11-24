package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// coin_refund_golden_test.go — LLM-1015. A buyer whose order lapsed, in the
// smith's company the turn she is told about it.
//
// Before LLM-1015 the refund never reached the coin record, so the same prompt
// carried the LLM-993 lapse line ("the 20 coins you paid came back to you") and a
// "## Coin between you" line saying she had paid the smith 20 coins for goods and
// nothing had come back. Two engine-authored statements in one prompt, and they
// disagreed. Now the refund is on the record as coin going back, seller to buyer.

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "buyer_order_lapsed_refund_in_company",
			summary: "LLM-1015: Elizabeth Ellis paid Ezekiel Crane 20 coins for 5 shovels he never made; the order " +
				"lapsed and the 20 came back. She is in his huddle on the turn the lapse beat reaches her. The golden " +
				"pins '## Coin between you and those here' carrying the refund as coin going back ('returned when an " +
				"order lapsed') beside her 20 for goods, so the record agrees with the lapse line in the same prompt.",
			build: buyerOrderLapsedRefundInCompany,
		},
	)
}

func buyerOrderLapsedRefundInCompany() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		ezekielID   = sim.ActorID("ezekiel")
		elizabethID = sim.ActorID("elizabeth")
		forge       = sim.StructureID("forge")
	)
	snap, subjectID, _ := orderLapsedBeatSnapshot(false, false, 20)
	// Together at the forge, in one huddle, so the coin section renders.
	for _, id := range []sim.ActorID{ezekielID, elizabethID} {
		a := snap.Actors[id]
		a.InsideStructureID = forge
		a.CurrentHuddleID = "h1"
		a.Pos = sim.TilePos{X: 10, Y: 10}
	}
	snap.Huddles = map[sim.HuddleID]*sim.Huddle{
		"h1": {ID: "h1", Members: map[sim.ActorID]struct{}{ezekielID: {}, elizabethID: {}}},
	}
	// The full prepay at accept, two days back, and the refund at the lapse this
	// morning. Both actors' records, as RecordCoinPaid writes them.
	paid := []sim.CoinPayment{{At: snap.PublishedAt.Add(-48 * time.Hour), Amount: 20, Kind: sim.CoinPaymentForGoods}}
	refund := []sim.CoinPayment{{At: snap.PublishedAt.Add(-2 * time.Hour), Amount: 20, Kind: sim.CoinPaymentRefund}}
	snap.CoinRecord = map[sim.ActorID]map[sim.ActorID]*sim.CoinPairRecord{
		elizabethID: {ezekielID: {Paid: paid, Received: refund}},
		ezekielID:   {elizabethID: {Paid: refund, Received: paid}},
	}
	snap.CoinRecordWindow = sim.DefaultCoinRecordWindow
	// The lapse beat as a full prepay (not a deposit) of 20.
	warrants := []sim.WarrantMeta{{
		TriggerActorID: ezekielID,
		Reason: sim.OrderLapsedWarrantReason{
			OrderID:       7,
			Counterparty:  ezekielID,
			NarrationText: sim.OrderLapsedBuyerNarration("Ezekiel Crane", "5 shovels", 20, 20, false, false),
		},
		SourceEventID: 1,
	}}
	return snap, subjectID, warrants
}

// TestRefundOnTheRecordAgreesWithTheLapseLine is the assertion behind the golden.
func TestRefundOnTheRecordAgreesWithTheLapseLine(t *testing.T) {
	got := renderScenario(perceptionScenario{
		name:  "buyer_order_lapsed_refund_in_company",
		build: buyerOrderLapsedRefundInCompany,
	})
	const lapse = "Ezekiel Crane never handed over the 5 shovels you paid for in time, so your 20 coins came back to you."
	const record = "Ezekiel Crane has paid you 20 coins, returned when an order lapsed, and you have paid Ezekiel Crane 20 coins for goods."
	for _, want := range []string{lapse, record} {
		if !strings.Contains(got, want) {
			t.Errorf("want line %q\n--- got ---\n%s", want, got)
		}
	}
	if strings.Contains(got, "nothing has come back") {
		t.Errorf("the refund came back; the record must not say nothing did:\n%s", got)
	}
}

// The refund portion's three shapes: the whole direction, part of it, and a
// direction whose single payment is the refund.
func TestCoinRefundPortion(t *testing.T) {
	cases := []struct {
		count, refundCount, refundTotal int
		want                            string
	}{
		{1, 1, 20, "returned when an order lapsed"},
		{2, 2, 26, "all of it returned when an order lapsed"},
		{3, 1, 6, "6 coins of it returned when an order lapsed"},
		{3, 0, 0, ""},
	}
	for _, c := range cases {
		if got := coinRefundPortion(c.refundCount, c.refundTotal, c.count); got != c.want {
			t.Errorf("coinRefundPortion(%d, %d, %d) = %q, want %q", c.refundCount, c.refundTotal, c.count, got, c.want)
		}
	}
	// Beside goods in one direction: both portions, comma form.
	clause := coinDirectionClause(coinDirection{count: 3, goodsCount: 2, goodsTotal: 9, refundCount: 1, refundTotal: 6})
	if clause != ", 9 coins of it for goods and 6 coins of it returned when an order lapsed" {
		t.Errorf("mixed clause = %q", clause)
	}
}
