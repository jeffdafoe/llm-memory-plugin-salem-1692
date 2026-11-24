package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/cascade"
)

// coin_record_orders_test.go — LLM-1015. The coin record must agree with the coin
// that actually moved on an order: the deposit at accept (not the full price), the
// balance at deliver_order, and the refund when the order lapses (coin going back,
// seller to buyer).

// withPaidLog registers the cascade action-log subscribers (the pay-resolved one
// credits the record at accept) and a capturing durable sink.
func withPaidLog(t *testing.T, w *sim.World) *lapseSink {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mustSend(t, w, func(world *sim.World) { cascade.RegisterActionLog(ctx, world) })
	return withLapseSink(t, w)
}

func readDealings(t *testing.T, w *sim.World, subject, peer sim.ActorID, now time.Time) sim.CoinDealings {
	t.Helper()
	var d sim.CoinDealings
	mustSend(t, w, func(world *sim.World) { d = world.CoinDealingsFor(subject, peer, now) })
	return d
}

func (s *lapseSink) byType(id sim.ActorID, kind sim.ActionType) []sim.DurableActionLogRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []sim.DurableActionLogRow
	for _, r := range s.rows {
		if r.ActorID == id && r.ActionType == kind {
			out = append(out, r)
		}
	}
	return out
}

// Deposit 5 of a 15-coin commission at accept, the 10 balance at delivery: the
// record holds 5 then 15 in two goods payments, and the durable rows carry the
// same amounts — the deposit row with the agreed price beside it, the balance row
// with the order_balance marker the boot seed classifies.
func TestCoinRecord_CommissionDepositThenBalance(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	sink := withPaidLog(t, w)
	at := time.Now().UTC()
	commissionOfferWithDeposit(t, w, 1, "nail", 1, 15, 5, at)
	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}

	d := readDealings(t, w, "alice", "smith", at.Add(time.Minute))
	if d.PaidCount != 1 || d.PaidTotal != 5 || d.PaidGoodsTotal != 5 {
		t.Fatalf("after accept: alice paid %d in %d (goods %d), want 5 in 1, all goods", d.PaidTotal, d.PaidCount, d.PaidGoodsTotal)
	}
	paid := sink.byType("alice", sim.ActionTypePaid)
	if len(paid) != 1 || paid[0].Payload["amount"] != 5 || paid[0].Payload["price"] != 15 {
		t.Fatalf("deposit row = %+v, want one paid row with amount 5 and price 15", paid)
	}

	mustSend(t, w, func(world *sim.World) {
		world.Actors["smith"].Inventory = map[sim.ItemKind]int{"nail": 1}
	})
	if _, err := w.Send(sim.DeliverOrder("smith", 1, at.Add(time.Hour))); err != nil {
		t.Fatalf("DeliverOrder: %v", err)
	}

	d = readDealings(t, w, "alice", "smith", at.Add(2*time.Hour))
	if d.PaidCount != 2 || d.PaidTotal != 15 || d.PaidGoodsTotal != 15 {
		t.Errorf("after delivery: alice paid %d in %d (goods %d), want 15 in 2, all goods", d.PaidTotal, d.PaidCount, d.PaidGoodsTotal)
	}
	if s := readDealings(t, w, "smith", "alice", at.Add(2*time.Hour)); s.ReceivedTotal != 15 {
		t.Errorf("smith's side received %d, want 15", s.ReceivedTotal)
	}
	paid = sink.byType("alice", sim.ActionTypePaid)
	if len(paid) != 2 {
		t.Fatalf("paid rows after delivery = %d, want 2", len(paid))
	}
	bal := paid[1].Payload
	if bal["amount"] != 10 || bal["order_balance"] != uint64(1) || bal["recipient_actor_id"] != "smith" {
		t.Errorf("balance row payload = %+v, want amount 10, order_balance 1, recipient smith", bal)
	}
	var ring int
	for _, e := range readActionLog(t, w) {
		if e.ActorID == "alice" && e.ActionType == sim.ActionTypePaid {
			ring++
		}
	}
	if ring != 2 {
		t.Errorf("alice paid ring entries = %d, want 2 (deposit + balance)", ring)
	}
}

// A full prepay of 6 that lapses undelivered: the 6 goes back, and the record says
// so as coin from the seller to the buyer, classified as a refund — accounted, so
// no bare-pay refund may hand it back again.
func TestCoinRecord_LapsedOrderRefundGoesBack(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	sink := withPaidLog(t, w)
	at := time.Now().UTC()
	commissionOffer(t, w, 1, "nail", 1, 6, at)
	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	expiry := commissionExpiry(at)
	if _, err := w.Send(sim.EvaluateOrderSweep(expiry)); err != nil {
		t.Fatalf("EvaluateOrderSweep: %v", err)
	}

	d := readDealings(t, w, "alice", "smith", expiry)
	if d.PaidTotal != 6 || d.PaidGoodsTotal != 6 {
		t.Errorf("alice paid %d (goods %d), want 6 for goods", d.PaidTotal, d.PaidGoodsTotal)
	}
	if d.ReceivedTotal != 6 || d.ReceivedRefundTotal != 6 || d.ReceivedRefundCount != 1 {
		t.Errorf("alice received %d (refund %d in %d), want the 6 back as one refund", d.ReceivedTotal, d.ReceivedRefundTotal, d.ReceivedRefundCount)
	}
	if got := d.ReceivedUnaccountedTotal(); got != 0 {
		t.Errorf("ReceivedUnaccountedTotal = %d, want 0 (a refund is not coin to hand back again)", got)
	}
	if s := readDealings(t, w, "smith", "alice", expiry); s.PaidRefundTotal != 6 || s.ReceivedGoodsTotal != 6 {
		t.Errorf("smith's side: paid refund %d, received goods %d, want 6 and 6", s.PaidRefundTotal, s.ReceivedGoodsTotal)
	}
	rows := sink.byType("smith", sim.ActionTypeRefunded)
	if len(rows) != 1 {
		t.Fatalf("refunded rows = %d, want 1", len(rows))
	}
	p := rows[0].Payload
	if p["amount"] != 6 || p["recipient_actor_id"] != "alice" || p["order_id"] != uint64(1) {
		t.Errorf("refunded payload = %+v, want amount 6 to alice for order 1", p)
	}

	// A second sweep finds the order terminal: no second refund on the record.
	if _, err := w.Send(sim.EvaluateOrderSweep(expiry.Add(time.Minute))); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if again := readDealings(t, w, "alice", "smith", expiry); again.ReceivedRefundCount != 1 {
		t.Errorf("refunds after a second sweep = %d, want still 1", again.ReceivedRefundCount)
	}
}

// The seller had spent all but 2 of the 6: only the 2 that came back are recorded
// (LLM-995 short refund), not the 6 that were owed.
func TestCoinRecord_ShortRefundRecordsWhatCameBack(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	sink := withPaidLog(t, w)
	at := time.Now().UTC()
	commissionOffer(t, w, 1, "nail", 1, 6, at)
	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	mustSend(t, w, func(world *sim.World) { world.Actors["smith"].Coins = 2 })
	expiry := commissionExpiry(at)
	if _, err := w.Send(sim.EvaluateOrderSweep(expiry)); err != nil {
		t.Fatalf("EvaluateOrderSweep: %v", err)
	}
	if d := readDealings(t, w, "alice", "smith", expiry); d.ReceivedRefundTotal != 2 {
		t.Errorf("refund on the record = %d, want 2", d.ReceivedRefundTotal)
	}
	if rows := sink.byType("smith", sim.ActionTypeRefunded); len(rows) != 1 || rows[0].Payload["amount"] != 2 {
		t.Errorf("refunded rows = %+v, want one for 2", rows)
	}
}

// A forfeit moves no coin back: nothing on the record, no refunded row.
func TestCoinRecord_ForfeitRecordsNoRefund(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	sink := withPaidLog(t, w)
	at := time.Now().UTC()
	commissionOfferWithDeposit(t, w, 1, "nail", 1, 15, 5, at)
	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	mustSend(t, w, func(world *sim.World) {
		world.Actors["smith"].Inventory = map[sim.ItemKind]int{"nail": 1} // forged; alice never comes
	})
	expiry := commissionExpiry(at)
	if _, err := w.Send(sim.EvaluateOrderSweep(expiry)); err != nil {
		t.Fatalf("EvaluateOrderSweep: %v", err)
	}
	if d := readDealings(t, w, "alice", "smith", expiry); d.ReceivedCount != 0 || d.PaidTotal != 5 {
		t.Errorf("after a forfeit: alice received %d payments, paid %d; want none back and 5 paid", d.ReceivedCount, d.PaidTotal)
	}
	if rows := sink.byType("smith", sim.ActionTypeRefunded); len(rows) != 0 {
		t.Errorf("refunded rows after a forfeit = %+v, want none", rows)
	}
}

// A deposit term on an offer the seller can fill from stock is not a commission:
// the engine charges the whole price at accept, and the record, the ring entry and
// the durable row all say so, with no separate price.
func TestCoinRecord_DepositTermWithStockChargesTheWholePrice(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), map[sim.ItemKind]int{"nail": 3}, 50, nil)
	defer stop()
	sink := withPaidLog(t, w)
	at := time.Now().UTC()
	commissionOfferWithDeposit(t, w, 1, "nail", 1, 15, 5, at)
	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	if alice := readHoldings(t, w, "alice"); alice.Coins != 35 {
		t.Fatalf("alice.Coins = %d, want 35 (the whole 15 charged)", alice.Coins)
	}
	if d := readDealings(t, w, "alice", "smith", at.Add(time.Minute)); d.PaidTotal != 15 {
		t.Errorf("record = %d, want 15", d.PaidTotal)
	}
	paid := sink.byType("alice", sim.ActionTypePaid)
	if len(paid) != 1 || paid[0].Payload["amount"] != 15 {
		t.Fatalf("paid rows = %+v, want one for 15", paid)
	}
	if _, ok := paid[0].Payload["price"]; ok {
		t.Errorf("price written on a whole-price row: %+v", paid[0].Payload)
	}
}

// A delivery that bounces on an unaffordable balance writes nothing; the retry
// after the buyer is funded writes exactly one balance row and one record entry.
func TestCoinRecord_BouncedDeliveryThenRetryRecordsTheBalanceOnce(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 5, nil)
	defer stop()
	sink := withPaidLog(t, w)
	at := time.Now().UTC()
	commissionOfferWithDeposit(t, w, 1, "nail", 1, 15, 5, at)
	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	mustSend(t, w, func(world *sim.World) {
		world.Actors["smith"].Inventory = map[sim.ItemKind]int{"nail": 1}
	})
	if _, err := w.Send(sim.DeliverOrder("smith", 1, at.Add(time.Hour))); err == nil {
		t.Fatal("DeliverOrder with an unaffordable balance succeeded")
	}
	if paid := sink.byType("alice", sim.ActionTypePaid); len(paid) != 1 {
		t.Fatalf("paid rows after a bounced delivery = %d, want 1 (the deposit)", len(paid))
	}
	mustSend(t, w, func(world *sim.World) { world.Actors["alice"].Coins = 10 })
	if _, err := w.Send(sim.DeliverOrder("smith", 1, at.Add(2*time.Hour))); err != nil {
		t.Fatalf("retry DeliverOrder: %v", err)
	}
	paid := sink.byType("alice", sim.ActionTypePaid)
	if len(paid) != 2 || paid[1].Payload["amount"] != 10 {
		t.Errorf("paid rows after the retry = %+v, want the deposit and one 10-coin balance", paid)
	}
	if d := readDealings(t, w, "alice", "smith", at.Add(3*time.Hour)); d.PaidCount != 2 || d.PaidTotal != 15 {
		t.Errorf("record = %d in %d, want 15 in 2", d.PaidTotal, d.PaidCount)
	}
}
