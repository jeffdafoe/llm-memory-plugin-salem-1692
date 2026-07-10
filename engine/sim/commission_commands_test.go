package sim_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// commission_commands_test.go — LLM-338. A buyer prepays for a craft good the
// seller MAKES but doesn't yet hold; the accept mints a deferred Ready Order
// (a "commission") the keeper fulfils via deliver_order once forged, instead of
// rejecting for lack of stock. Coverage:
//   - a stockless accept of a produced good mints a deferred, lead-time-sized,
//     undelivered Order (not a deliver-at-accept take-home);
//   - a stockless accept of a good the seller does NOT produce still rejects;
//   - a barter (goods-paid) stockless offer is NOT a commission (coin-only rule);
//   - a producer WITH stock still delivers at accept (commission is shortfall-only);
//   - the full forge → deliver_order lifecycle; and
//   - refund-on-expiry returns the buyer's coins if the commission is never made.

// commissionRecipes are the recipes buildCommissionWorld seeds. "nail" is a
// makeable produced material (1 per 1h, no inputs) — the take-home craft good a
// commission is minted for. "widget" is likewise makeable, but seeded so a test
// can hand it to a seller that does NOT produce it.
func commissionRecipes() map[sim.ItemKind]*sim.ItemRecipe {
	return map[sim.ItemKind]*sim.ItemRecipe{
		"nail":   {OutputItem: "nail", OutputQty: 1, RateQty: 1, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 2},
		"widget": {OutputItem: "widget", OutputQty: 1, RateQty: 1, RatePerHours: 1, WholesalePrice: 3, RetailPrice: 6},
	}
}

// buildCommissionWorld seeds a producer (smith) and a buyer (alice) co-present in
// one huddle + scene, plus a small craft catalog with recipes. smith's produce
// manifest and inventory are caller-supplied so a test can make it a producer of
// the good under test (or not) and stocked or dry. Modeled on
// buildPayWithItemWorld + buildProducerWorld.
func buildCommissionWorld(t *testing.T, smithRestock []sim.RestockEntry, smithInv map[sim.ItemKind]int, aliceCoins int, aliceInv map[sim.ItemKind]int) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		// Craft materials — non-consumable, so take-home (not eat-here).
		"nail":   {Name: "nail", DisplayLabel: "Nail", DisplayLabelSingular: "nail", DisplayLabelPlural: "nails", Category: sim.ItemCategoryMaterial, SortOrder: 310},
		"widget": {Name: "widget", DisplayLabel: "Widget", DisplayLabelSingular: "widget", DisplayLabelPlural: "widgets", Category: sim.ItemCategoryMaterial, SortOrder: 320},
		"pebble": {Name: "pebble", DisplayLabel: "Pebble", DisplayLabelSingular: "pebble", DisplayLabelPlural: "pebbles", Category: sim.ItemCategoryMaterial, SortOrder: 330},
	})
	handles.Recipes.Seed(commissionRecipes())

	now := time.Now().UTC()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"smith": {
			ID:              "smith",
			DisplayName:     "Smith",
			Kind:            sim.KindNPCStateful,
			State:           sim.StateIdle,
			CurrentHuddleID: "h1",
			WorkStructureID: "forge",
			Inventory:       smithInv,
			RestockPolicy:   &sim.RestockPolicy{Restock: smithRestock},
			RecentActions:   sim.NewRingBuffer[sim.Action](4),
		},
		"alice": {
			ID:              "alice",
			DisplayName:     "Alice",
			Kind:            sim.KindNPCShared,
			State:           sim.StateIdle,
			CurrentHuddleID: "h1",
			Coins:           aliceCoins,
			Inventory:       aliceInv,
			RecentActions:   sim.NewRingBuffer[sim.Action](4),
		},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Huddles["h1"] = &sim.Huddle{
			ID:        "h1",
			Members:   map[sim.ActorID]struct{}{"smith": {}, "alice": {}},
			StartedAt: now,
		}
		world.Scenes["sc1"] = &sim.Scene{
			ID:       "sc1",
			OriginAt: now,
			Bound:    sim.NewUnboundedBound(),
			Huddles:  map[sim.HuddleID]struct{}{"h1": {}},
		}
		sim.RebuildIndicesForTest(world)
		return nil, nil
	}}); err != nil {
		cancel()
		<-done
		t.Fatalf("seed scene+huddle: %v", err)
	}
	return w, func() { cancel(); <-done }
}

// readOrders returns a copy of World.Orders taken on the world goroutine.
func readOrders(t *testing.T, w *sim.World) map[sim.OrderID]sim.Order {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		out := make(map[sim.OrderID]sim.Order, len(world.Orders))
		for id, o := range world.Orders {
			if o == nil {
				continue
			}
			out[id] = *o
		}
		return out, nil
	}})
	if err != nil {
		t.Fatalf("readOrders: %v", err)
	}
	return res.(map[sim.OrderID]sim.Order)
}

func produceNail() []sim.RestockEntry {
	return []sim.RestockEntry{{Item: "nail", Source: sim.RestockSourceProduce, Max: 20}}
}

// commissionOffer seeds a coin-only, take-home (consume_now=false) pending offer
// from alice to smith for `qty` of `item` at `amount` coins.
func commissionOffer(t *testing.T, w *sim.World, id sim.LedgerID, item sim.ItemKind, qty, amount int, at time.Time) {
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: id, BuyerID: "alice", SellerID: "smith",
		ItemKind: item, Qty: qty, Amount: amount, ConsumeNow: false,
		State:     sim.PayLedgerStatePending,
		CreatedAt: at, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
}

// TestAcceptPay_Commission_MintsDeferredOrder: smith produces nail but holds 0.
// Accepting alice's take-home offer mints a Ready Order (not a deliver-at-accept
// take-home), moves no goods, takes the coins, and sizes the Order's window from
// the forge lead time rather than the 10-minute takeaway TTL.
func TestAcceptPay_Commission_MintsDeferredOrder(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	at := time.Now().UTC()
	commissionOffer(t, w, 1, "nail", 1, 6, at)

	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}

	// Ledger accepted, coins transferred, but no goods handed over.
	ledger := readPayLedger(t, w)
	if got := ledger[1].State; got != sim.PayLedgerStateAccepted {
		t.Fatalf("ledger.State = %q, want accepted", got)
	}
	alice := readHoldings(t, w, "alice")
	smith := readHoldings(t, w, "smith")
	if alice.Coins != 44 {
		t.Errorf("alice.Coins = %d, want 44 (prepaid 6)", alice.Coins)
	}
	if smith.Coins != 6 {
		t.Errorf("smith.Coins = %d, want 6", smith.Coins)
	}
	if alice.inv["nail"] != 0 {
		t.Errorf("alice.nail = %d, want 0 (nothing forged yet — not delivered at accept)", alice.inv["nail"])
	}
	if smith.inv["nail"] != 0 {
		t.Errorf("smith.nail = %d, want 0", smith.inv["nail"])
	}

	// A Ready commission Order exists, undelivered, with the lead-time window.
	orders := readOrders(t, w)
	o, ok := orders[1]
	if !ok {
		t.Fatalf("no Order minted for the commission (orders=%+v)", orders)
	}
	if o.State != sim.OrderStateReady {
		t.Errorf("order.State = %q, want ready", o.State)
	}
	if o.Item != "nail" || o.Qty != 1 || o.BuyerID != "alice" || o.SellerID != "smith" {
		t.Errorf("order shape = %+v, want nail×1 alice→smith", o)
	}
	// nail: 1 per 1h → 3600s forge; window = 3600s×Slack + Grace.
	wantExpiry := at.Add(time.Duration(sim.CycleDurationSeconds(commissionRecipes()["nail"]))*time.Second*time.Duration(sim.CommissionOrderSlack) + sim.CommissionOrderGrace)
	if !o.ExpiresAt.Equal(wantExpiry) {
		t.Errorf("order.ExpiresAt = %v, want %v (lead-time-sized commission window)", o.ExpiresAt, wantExpiry)
	}
	if !o.ExpiresAt.After(at.Add(sim.OrderTTLDefault)) {
		t.Errorf("commission window %v must outlast the 10-minute takeaway TTL", o.ExpiresAt.Sub(at))
	}
}

// TestAcceptPay_Commission_NonProducedGoodRejects: a stockless accept of a good
// the seller does NOT produce is a plain out-of-stock reject, not a commission —
// only a good the keeper can actually forge earns the deferral.
func TestAcceptPay_Commission_NonProducedGoodRejects(t *testing.T) {
	// smith produces nail, but the offer is for widget (which smith does not make).
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	at := time.Now().UTC()
	commissionOffer(t, w, 1, "widget", 1, 6, at)

	_, err := w.Send(sim.AcceptPay("smith", 1, at))
	if err == nil || !strings.Contains(err.Error(), "widget") {
		t.Fatalf("AcceptPay: want widget stock-shortfall reject, got %v", err)
	}
	if !strings.Contains(err.Error(), "decline_pay") || !strings.Contains(err.Error(), "counter_pay") {
		t.Errorf("reject should name decline_pay / counter_pay, got %q", err)
	}
	// Offer stays pending; no order minted; no coins moved.
	if ledger := readPayLedger(t, w); ledger[1].State != sim.PayLedgerStatePending {
		t.Errorf("ledger.State = %q, want pending (tool error leaves it open)", ledger[1].State)
	}
	if orders := readOrders(t, w); len(orders) != 0 {
		t.Errorf("orders = %+v, want none minted", orders)
	}
	if alice := readHoldings(t, w, "alice"); alice.Coins != 50 {
		t.Errorf("alice.Coins = %d, want 50 (no transfer on reject)", alice.Coins)
	}
}

// TestAcceptPay_Commission_BarterFallsThrough: a stockless offer that pays with
// GOODS is not a commission — a commission that expires refunds coins, and a
// barter leg couldn't be reversed. So it falls through to the normal stock
// reject (coin-only rule, mirroring advance-lodging).
func TestAcceptPay_Commission_BarterFallsThrough(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 0, map[sim.ItemKind]int{"pebble": 2})
	defer stop()
	at := time.Now().UTC()
	// Goods-paid offer for a nail smith doesn't hold.
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "smith",
		ItemKind: "nail", Qty: 1, Amount: 0, ConsumeNow: false,
		PayItems:  []sim.ItemKindQty{{Kind: "pebble", Qty: 1}},
		State:     sim.PayLedgerStatePending,
		CreatedAt: at, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})

	_, err := w.Send(sim.AcceptPay("smith", 1, at))
	if err == nil || !strings.Contains(err.Error(), "nail") {
		t.Fatalf("AcceptPay: want nail stock-shortfall reject for the barter offer, got %v", err)
	}
	if orders := readOrders(t, w); len(orders) != 0 {
		t.Errorf("orders = %+v, want none (barter is not a commission)", orders)
	}
	if ledger := readPayLedger(t, w); ledger[1].State != sim.PayLedgerStatePending {
		t.Errorf("ledger.State = %q, want pending", ledger[1].State)
	}
}

// TestAcceptPay_Commission_ProducerWithStockDeliversNow: a producer who HOLDS
// enough still delivers at accept — the commission deferral is shortfall-only.
func TestAcceptPay_Commission_ProducerWithStockDeliversNow(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), map[sim.ItemKind]int{"nail": 1}, 50, nil)
	defer stop()
	at := time.Now().UTC()
	commissionOffer(t, w, 1, "nail", 1, 6, at)

	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	alice := readHoldings(t, w, "alice")
	smith := readHoldings(t, w, "smith")
	if alice.inv["nail"] != 1 {
		t.Errorf("alice.nail = %d, want 1 (delivered at accept — seller held stock)", alice.inv["nail"])
	}
	if smith.inv["nail"] != 0 {
		t.Errorf("smith.nail = %d, want 0 (handed over)", smith.inv["nail"])
	}
	// Delivered-at-accept prunes the Order (no lingering Ready commission).
	for _, o := range readOrders(t, w) {
		if o.State == sim.OrderStateReady {
			t.Errorf("unexpected Ready order for an in-stock take-home sale: %+v", o)
		}
	}
}

// TestCommission_ForgeThenDeliver: the full lifecycle — commission a nail smith
// doesn't hold, then (simulating the forged batch landing) stock the nail and
// deliver_order it. Gate 5 (stock) is the readiness gate, so delivery only
// succeeds once the good exists.
func TestCommission_ForgeThenDeliver(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	at := time.Now().UTC()
	commissionOffer(t, w, 1, "nail", 1, 6, at)
	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}

	// Before forging, deliver_order can't fulfil — nothing to hand over.
	if _, err := w.Send(sim.DeliverOrder("smith", 1, at)); err == nil {
		t.Fatalf("DeliverOrder before forge: want stock-gate reject, got nil")
	}

	// The forged batch lands: smith now holds the nail.
	mustSend(t, w, func(world *sim.World) {
		world.Actors["smith"].Inventory = map[sim.ItemKind]int{"nail": 1}
	})

	if _, err := w.Send(sim.DeliverOrder("smith", 1, at.Add(time.Hour))); err != nil {
		t.Fatalf("DeliverOrder after forge: %v", err)
	}
	alice := readHoldings(t, w, "alice")
	if alice.inv["nail"] != 1 {
		t.Errorf("alice.nail = %d, want 1 (delivered after forge)", alice.inv["nail"])
	}
	if o, ok := readOrders(t, w)[1]; ok && o.State == sim.OrderStateReady {
		t.Errorf("order still Ready after delivery: %+v", o)
	}
}

// TestCommission_ExpiryRefundsBuyer: a commission never forged expires and
// refunds the buyer's coins (ZBBS-HOME-403 refund-on-expiry covers "never made").
func TestCommission_ExpiryRefundsBuyer(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	at := time.Now().UTC()
	commissionOffer(t, w, 1, "nail", 1, 6, at)
	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	if alice := readHoldings(t, w, "alice"); alice.Coins != 44 {
		t.Fatalf("alice.Coins after prepay = %d, want 44", alice.Coins)
	}

	// Sweep past the commission window: the order expires and refunds.
	past := at.Add(time.Duration(sim.CycleDurationSeconds(commissionRecipes()["nail"]))*time.Second*time.Duration(sim.CommissionOrderSlack) + sim.CommissionOrderGrace + time.Minute)
	if _, err := w.Send(sim.EvaluateOrderSweep(past)); err != nil {
		t.Fatalf("EvaluateOrderSweep: %v", err)
	}
	alice := readHoldings(t, w, "alice")
	if alice.Coins != 50 {
		t.Errorf("alice.Coins = %d, want 50 (refunded on expiry)", alice.Coins)
	}
	if o, ok := readOrders(t, w)[1]; ok && o.State == sim.OrderStateReady {
		t.Errorf("order still Ready after expiry sweep: %+v", o)
	}
}

// TestAcceptPay_Commission_ReservedStockMintsCommission: smith produces nail and
// holds exactly 1, but it is already reserved by an existing Ready order. A new
// offer for a nail must NOT deliver the spoken-for unit — the commission stock
// check subtracts outstandingReadyOrderQty, so available is 0 and a deferred
// commission is minted instead. Locks the reservation-accounting edge.
func TestAcceptPay_Commission_ReservedStockMintsCommission(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), map[sim.ItemKind]int{"nail": 1}, 50, nil)
	defer stop()
	at := time.Now().UTC()
	// Existing Ready order (id 2) reserves smith's only nail (buyer carol is a
	// reservation placeholder — outstandingReadyOrderQty reads the order, not the
	// actor).
	mustSend(t, w, func(world *sim.World) {
		world.Orders[2] = &sim.Order{
			ID: 2, State: sim.OrderStateReady,
			SellerID: "smith", BuyerID: "carol",
			Item: "nail", Qty: 1, ConsumerIDs: []sim.ActorID{"carol"},
			CreatedAt: at, ExpiresAt: at.Add(time.Hour),
		}
	})
	commissionOffer(t, w, 1, "nail", 1, 6, at)

	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	alice := readHoldings(t, w, "alice")
	smith := readHoldings(t, w, "smith")
	if alice.inv["nail"] != 0 {
		t.Errorf("alice.nail = %d, want 0 (the on-hand nail is reserved — not delivered)", alice.inv["nail"])
	}
	if smith.inv["nail"] != 1 {
		t.Errorf("smith.nail = %d, want 1 (still held, reserved for the other order)", smith.inv["nail"])
	}
	if alice.Coins != 44 {
		t.Errorf("alice.Coins = %d, want 44 (prepaid the commission)", alice.Coins)
	}
	o, ok := readOrders(t, w)[1]
	if !ok || o.State != sim.OrderStateReady {
		t.Errorf("commission order 1 = %+v (ok=%v), want a Ready deferred commission", o, ok)
	}
}

// commissionOfferWithDeposit seeds a coin-only, take-home pending offer from
// alice to smith for `qty` of `item` at `amount` coins total, with `deposit`
// payable now and the balance (amount - deposit) collected at deliver_order —
// the LLM-357 partial-payment shape.
func commissionOfferWithDeposit(t *testing.T, w *sim.World, id sim.LedgerID, item sim.ItemKind, qty, amount, deposit int, at time.Time) {
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: id, BuyerID: "alice", SellerID: "smith",
		ItemKind: item, Qty: qty, Amount: amount, Deposit: deposit, ConsumeNow: false,
		State:     sim.PayLedgerStatePending,
		CreatedAt: at, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
}

// commissionExpiry returns a time past a nail commission's Ready window, for
// driving the expiry sweep.
func commissionExpiry(at time.Time) time.Time {
	return at.Add(time.Duration(sim.CycleDurationSeconds(commissionRecipes()["nail"]))*time.Second*time.Duration(sim.CommissionOrderSlack) + sim.CommissionOrderGrace + time.Minute)
}

// TestCommission_PartialPayment_AcceptChargesDepositOnly: an offer carrying a
// deposit (5 of 15) charges only the deposit at accept — not the full price —
// and mints a Ready commission recording both the total and the deposit, so the
// balance owed at delivery is Amount - Deposit. LLM-357.
func TestCommission_PartialPayment_AcceptChargesDepositOnly(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	at := time.Now().UTC()
	commissionOfferWithDeposit(t, w, 1, "nail", 1, 15, 5, at)

	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	alice := readHoldings(t, w, "alice")
	smith := readHoldings(t, w, "smith")
	if alice.Coins != 45 {
		t.Errorf("alice.Coins = %d, want 45 (only the 5-coin deposit charged, not the full 15)", alice.Coins)
	}
	if smith.Coins != 5 {
		t.Errorf("smith.Coins = %d, want 5 (deposit received)", smith.Coins)
	}
	o, ok := readOrders(t, w)[1]
	if !ok || o.State != sim.OrderStateReady {
		t.Fatalf("order 1 = %+v (ok=%v), want a Ready commission", o, ok)
	}
	if o.Amount != 15 || o.Deposit != 5 {
		t.Errorf("order Amount/Deposit = %d/%d, want 15/5 (full price + deposit recorded)", o.Amount, o.Deposit)
	}
	if got := sim.OrderBalanceDue(&o); got != 10 {
		t.Errorf("OrderBalanceDue = %d, want 10 (balance owed at delivery)", got)
	}
}

// TestCommission_PartialPayment_DeliverCollectsBalance: the full deposit
// lifecycle — deposit at accept, forge, then deliver_order collects the balance
// as an atomic coin↔goods swap. The buyer pays deposit + balance = the full
// price; the seller receives all of it; the good is handed over. LLM-357.
func TestCommission_PartialPayment_DeliverCollectsBalance(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	at := time.Now().UTC()
	commissionOfferWithDeposit(t, w, 1, "nail", 1, 15, 5, at)
	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	// The forged nail lands.
	mustSend(t, w, func(world *sim.World) {
		world.Actors["smith"].Inventory = map[sim.ItemKind]int{"nail": 1}
	})
	if _, err := w.Send(sim.DeliverOrder("smith", 1, at.Add(time.Hour))); err != nil {
		t.Fatalf("DeliverOrder: %v", err)
	}
	alice := readHoldings(t, w, "alice")
	smith := readHoldings(t, w, "smith")
	if alice.inv["nail"] != 1 {
		t.Errorf("alice.nail = %d, want 1 (delivered)", alice.inv["nail"])
	}
	if alice.Coins != 35 {
		t.Errorf("alice.Coins = %d, want 35 (50 - 5 deposit - 10 balance)", alice.Coins)
	}
	if smith.Coins != 15 {
		t.Errorf("smith.Coins = %d, want 15 (5 deposit + 10 balance collected at delivery)", smith.Coins)
	}
	if o, ok := readOrders(t, w)[1]; ok && o.State == sim.OrderStateReady {
		t.Errorf("order still Ready after delivery: %+v", o)
	}
}

// TestCommission_PartialPayment_ShortBalanceBouncesDelivery: a buyer who put a
// deposit down but can't cover the balance bounces the delivery (before any
// goods move) — the order stays Ready to ride to expiry. LLM-357.
func TestCommission_PartialPayment_ShortBalanceBouncesDelivery(t *testing.T) {
	// alice can afford the 5 deposit but not the 10 balance.
	w, stop := buildCommissionWorld(t, produceNail(), nil, 5, nil)
	defer stop()
	at := time.Now().UTC()
	commissionOfferWithDeposit(t, w, 1, "nail", 1, 15, 5, at)
	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	mustSend(t, w, func(world *sim.World) {
		world.Actors["smith"].Inventory = map[sim.ItemKind]int{"nail": 1}
	})
	_, err := w.Send(sim.DeliverOrder("smith", 1, at.Add(time.Hour)))
	if err == nil || !strings.Contains(err.Error(), "balance") {
		t.Fatalf("DeliverOrder with a broke buyer: want a balance-gate reject, got %v", err)
	}
	alice := readHoldings(t, w, "alice")
	if alice.inv["nail"] != 0 {
		t.Errorf("alice.nail = %d, want 0 (delivery bounced, no goods moved)", alice.inv["nail"])
	}
	if alice.Coins != 0 {
		t.Errorf("alice.Coins = %d, want 0 (only the deposit was ever charged)", alice.Coins)
	}
	if o, ok := readOrders(t, w)[1]; !ok || o.State != sim.OrderStateReady {
		t.Errorf("order 1 = %+v (ok=%v), want still Ready (bounced delivery rides to expiry)", o, ok)
	}
}

// TestCommission_PartialPayment_SellerFaultRefundsDeposit: a partial-payment
// commission the seller NEVER forges expires seller-fault — the buyer's deposit
// is refunded (they're made whole), the seller debited what they received. Only
// the deposit moves, since only the deposit was ever paid. LLM-357.
func TestCommission_PartialPayment_SellerFaultRefundsDeposit(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	at := time.Now().UTC()
	commissionOfferWithDeposit(t, w, 1, "nail", 1, 15, 5, at)
	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	if alice := readHoldings(t, w, "alice"); alice.Coins != 45 {
		t.Fatalf("alice.Coins after deposit = %d, want 45", alice.Coins)
	}
	// smith never forges the nail; the window lapses.
	if _, err := w.Send(sim.EvaluateOrderSweep(commissionExpiry(at))); err != nil {
		t.Fatalf("EvaluateOrderSweep: %v", err)
	}
	alice := readHoldings(t, w, "alice")
	smith := readHoldings(t, w, "smith")
	if alice.Coins != 50 {
		t.Errorf("alice.Coins = %d, want 50 (seller-fault: deposit refunded in full)", alice.Coins)
	}
	if smith.Coins != 0 {
		t.Errorf("smith.Coins = %d, want 0 (deposit reversed on refund)", smith.Coins)
	}
	// alice (shared-VA buyer) durably remembers the refund — smith never made it.
	if joined := strings.Join(readSalientFacts(t, w, "alice", "smith"), " | "); !strings.Contains(joined, "deposit came back") {
		t.Errorf("alice's memory of smith should record the refunded deposit, got %q", joined)
	}
}

// TestCommission_PartialPayment_BuyerFaultForfeitsDeposit: a partial-payment
// commission the seller DID forge but the buyer never collected expires
// buyer-fault — the seller keeps the deposit (no refund) and the forged good
// returns to sellable stock (the terminal order stops reserving it). LLM-357.
func TestCommission_PartialPayment_BuyerFaultForfeitsDeposit(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), nil, 50, nil)
	defer stop()
	at := time.Now().UTC()
	commissionOfferWithDeposit(t, w, 1, "nail", 1, 15, 5, at)
	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	// smith holds enough stock at expiry (forged for this order, or acquired any
	// other way — buyer-fault is a current-stock / "was deliverable" test, not
	// "forged for this order") but the buyer never returns to collect.
	mustSend(t, w, func(world *sim.World) {
		world.Actors["smith"].Inventory = map[sim.ItemKind]int{"nail": 1}
	})
	if _, err := w.Send(sim.EvaluateOrderSweep(commissionExpiry(at))); err != nil {
		t.Fatalf("EvaluateOrderSweep: %v", err)
	}
	alice := readHoldings(t, w, "alice")
	smith := readHoldings(t, w, "smith")
	if alice.Coins != 45 {
		t.Errorf("alice.Coins = %d, want 45 (buyer-fault: deposit forfeited, no refund)", alice.Coins)
	}
	if smith.Coins != 5 {
		t.Errorf("smith.Coins = %d, want 5 (seller keeps the deposit)", smith.Coins)
	}
	if smith.inv["nail"] != 1 {
		t.Errorf("smith.nail = %d, want 1 (forged good stays with the seller, sellable again)", smith.inv["nail"])
	}
	if o, ok := readOrders(t, w)[1]; ok && o.State == sim.OrderStateReady {
		t.Errorf("order still Ready after buyer-fault expiry: %+v", o)
	}
	// The reputation seed: alice (shared-VA buyer) durably remembers forfeiting
	// the deposit. smith is a stateful NPC (own memory system), so his side is
	// carried through the dream path, not salient_facts — the same gate the
	// delivery fact uses.
	if joined := strings.Join(readSalientFacts(t, w, "alice", "smith"), " | "); !strings.Contains(joined, "forfeited") {
		t.Errorf("alice's memory of smith should record the forfeited deposit, got %q", joined)
	}
}

// TestCommission_PartialPayment_InStockIgnoresDeposit: a deposit is honored only
// for a genuine commission (the seller is out of stock and must forge). When the
// seller HOLDS the good, the offer delivers at accept for the FULL price — the
// deposit is ignored (depositChargeForEntry re-checks isCommissionOrder) and no
// Ready order is left carrying a phantom balance. LLM-357.
func TestCommission_PartialPayment_InStockIgnoresDeposit(t *testing.T) {
	w, stop := buildCommissionWorld(t, produceNail(), map[sim.ItemKind]int{"nail": 1}, 50, nil)
	defer stop()
	at := time.Now().UTC()
	commissionOfferWithDeposit(t, w, 1, "nail", 1, 15, 5, at)

	if _, err := w.Send(sim.AcceptPay("smith", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	alice := readHoldings(t, w, "alice")
	smith := readHoldings(t, w, "smith")
	if alice.inv["nail"] != 1 {
		t.Errorf("alice.nail = %d, want 1 (in-stock sale delivers at accept)", alice.inv["nail"])
	}
	if alice.Coins != 35 {
		t.Errorf("alice.Coins = %d, want 35 (full 15 charged — deposit ignored for an in-stock sale)", alice.Coins)
	}
	if smith.Coins != 15 {
		t.Errorf("smith.Coins = %d, want 15 (full price)", smith.Coins)
	}
	for _, o := range readOrders(t, w) {
		if o.State == sim.OrderStateReady {
			t.Errorf("unexpected Ready order for an in-stock sale (a deposit must not defer it): %+v", o)
		}
	}
}
