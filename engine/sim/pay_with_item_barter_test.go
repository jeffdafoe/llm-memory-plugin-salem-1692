package sim_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pay_with_item_barter_test.go — ZBBS-HOME-393 coverage for the barter
// extension to pay_with_item / counter_pay: paying WITH goods (pay_items),
// the two-way swap at accept, the goods-shortfall terminal, the
// must-offer-something rule (which also closes the free-goods hole), and
// the symmetric goods counter. Shares the fixtures in
// pay_with_item_commands_test.go (buildPayWithItemWorld, seedLedgerEntry,
// capturePayWithItemEvents, readPayLedger).

// readHoldings (coins + a copy of the inventory map, read on the world
// goroutine) is shared with holdings_commands_test.go.

// ---- mint -----------------------------------------------------------

// TestPayWithItem_Barter_PureGoodsMints — a coin-poor buyer offers goods
// only. The offer mints pending with PayItems recorded, and the
// PayOfferReceived event carries them for the seller's warrant.
func TestPayWithItem_Barter_PureGoodsMints(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 0, inventory: map[sim.ItemKind]int{"bread": 10}},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	events := capturePayWithItemEvents(t, w)
	at := time.Now().UTC()

	res, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 0, false, nil,
		[]sim.PayItemInput{{Item: "bread", Qty: 5}}, 0, 0, "", at))
	if err != nil {
		t.Fatalf("PayWithItem (pure barter): %v", err)
	}
	out := res.(sim.PayWithItemResult)
	if out.State != sim.PayLedgerStatePending {
		t.Fatalf("state = %q, want pending", out.State)
	}
	ledger := readPayLedger(t, w)
	entry := ledger[out.LedgerID]
	if len(entry.PayItems) != 1 || entry.PayItems[0].Kind != "bread" || entry.PayItems[0].Qty != 5 {
		t.Errorf("entry.PayItems = %+v, want [{bread 5}]", entry.PayItems)
	}
	if entry.Amount != 0 {
		t.Errorf("entry.Amount = %d, want 0 (pure barter)", entry.Amount)
	}
	if len(events.Offer) != 1 || len(events.Offer[0].PayItems) != 1 || events.Offer[0].PayItems[0].Kind != "bread" {
		t.Errorf("PayOfferReceived.PayItems = %+v, want [{bread 5}]", events.Offer)
	}
}

// TestPayWithItem_Barter_EmptyOfferRejects — amount 0 AND no pay_items is
// the free-goods hole (ZBBS-HOME-391). It must reject at mint.
func TestPayWithItem_Barter_EmptyOfferRejects(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"bread": 10}},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	_, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 0, false, nil, nil, 0, 0, "", time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "must include coins or goods") {
		t.Fatalf("want must-offer-something error, got %v", err)
	}
}

// TestPayWithItem_Barter_BuyerLacksGoodsRejects — the mint fast-fail
// rejects an offer whose pay_items the buyer doesn't currently hold.
func TestPayWithItem_Barter_BuyerLacksGoodsRejects(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 5, inventory: map[sim.ItemKind]int{"bread": 2}},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	_, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 0, false, nil,
		[]sim.PayItemInput{{Item: "bread", Qty: 5}}, 0, 0, "", time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "don't have 5 bread") {
		t.Fatalf("want goods-shortfall mint reject, got %v", err)
	}
}

// TestPayWithItem_Barter_UnknownAndDupGoodsReject — unknown item kinds and
// duplicate kinds in pay_items reject at mint.
func TestPayWithItem_Barter_UnknownAndDupGoodsReject(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"bread": 10}},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	at := time.Now().UTC()
	if _, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 0, false, nil,
		[]sim.PayItemInput{{Item: "fizzbuzz", Qty: 1}}, 0, 0, "", at)); err == nil || !strings.Contains(err.Error(), "unknown item kind") {
		t.Errorf("unknown pay_item: want reject, got %v", err)
	}
	if _, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 0, false, nil,
		[]sim.PayItemInput{{Item: "bread", Qty: 1}, {Item: "bread", Qty: 2}}, 0, 0, "", at)); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Errorf("dup pay_item: want reject, got %v", err)
	}
}

// TestPayWithItem_Barter_QuoteWithGoodsRejects — barter is slow-path only;
// pay_items alongside a quote_id rejects (a quote is a coin price).
func TestPayWithItem_Barter_QuoteWithGoodsRejects(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"bread": 10}},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	_, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 0, false, nil,
		[]sim.PayItemInput{{Item: "bread", Qty: 5}}, 7, 0, "", time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "can't pay a posted quote with goods") {
		t.Fatalf("want quote+goods reject, got %v", err)
	}
}

// ---- accept (the two-way swap) --------------------------------------

// TestAcceptPay_Barter_TwoWaySwap — accepting a mixed coins+goods offer
// moves coins AND each pay_item buyer→seller atomically. The bought good
// (takeaway) stays with the seller until deliver_order (S6), so the swap
// is exactly: buyer loses coins+bread, seller gains coins+bread.
func TestAcceptPay_Barter_TwoWaySwap(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 10, inventory: map[sim.ItemKind]int{"bread": 8}},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", coins: 0, inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	at := time.Now().UTC()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 2,
		PayItems:  []sim.ItemKindQty{{Kind: "bread", Qty: 5}},
		State:     sim.PayLedgerStatePending,
		CreatedAt: at, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})

	if _, err := w.Send(sim.AcceptPay("bob", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	if got := readPayLedger(t, w)[1].State; got != sim.PayLedgerStateAccepted {
		t.Fatalf("ledger.State = %q, want accepted", got)
	}

	a := readHoldings(t, w, "alice")
	b := readHoldings(t, w, "bob")
	if a.Coins != 8 {
		t.Errorf("alice.Coins = %d, want 8 (10-2)", a.Coins)
	}
	if a.inv["bread"] != 3 {
		t.Errorf("alice.bread = %d, want 3 (8-5)", a.inv["bread"])
	}
	if b.Coins != 2 {
		t.Errorf("bob.Coins = %d, want 2", b.Coins)
	}
	if b.inv["bread"] != 5 {
		t.Errorf("bob.bread = %d, want 5 (received)", b.inv["bread"])
	}
	if b.inv["stew"] != 5 {
		t.Errorf("bob.stew = %d, want 5 (takeaway: stays until deliver_order)", b.inv["stew"])
	}
}

// TestAcceptPay_Barter_DeleteOnZero — paying with the buyer's entire stock
// of a kind removes the map entry (delete-on-zero invariant), not a
// 0-count row.
func TestAcceptPay_Barter_DeleteOnZero(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 0, inventory: map[sim.ItemKind]int{"bread": 5}},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	at := time.Now().UTC()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 0,
		PayItems:  []sim.ItemKindQty{{Kind: "bread", Qty: 5}},
		State:     sim.PayLedgerStatePending,
		CreatedAt: at, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	if _, err := w.Send(sim.AcceptPay("bob", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	a := readHoldings(t, w, "alice")
	if _, present := a.inv["bread"]; present {
		t.Errorf("alice.bread should be deleted on zero, got map %+v", a.inv)
	}
}

// TestAcceptPay_Barter_InsufficientGoodsFlipsTerminal — if the buyer no
// longer holds the pay_items at accept time (drift between mint and
// accept), the offer flips to failed_insufficient_goods and nothing moves.
func TestAcceptPay_Barter_InsufficientGoodsFlipsTerminal(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 10, inventory: map[sim.ItemKind]int{"bread": 2}},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", coins: 0, inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	at := time.Now().UTC()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 2,
		PayItems:  []sim.ItemKindQty{{Kind: "bread", Qty: 5}}, // buyer only holds 2
		State:     sim.PayLedgerStatePending,
		CreatedAt: at, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	events := capturePayWithItemEvents(t, w)
	if _, err := w.Send(sim.AcceptPay("bob", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	if got := readPayLedger(t, w)[1].State; got != sim.PayLedgerStateFailedInsufficientGoods {
		t.Errorf("ledger.State = %q, want failed_insufficient_goods", got)
	}
	if len(events.Resolved) != 1 || events.Resolved[0].TerminalState != sim.PayTerminalStateFailedInsufficientGoods {
		t.Errorf("Resolved = %+v, want one failed_insufficient_goods", events.Resolved)
	}
	// Nothing moved.
	a := readHoldings(t, w, "alice")
	b := readHoldings(t, w, "bob")
	if a.Coins != 10 || a.inv["bread"] != 2 || b.Coins != 0 {
		t.Errorf("holdings changed on failed barter: alice %d/%dbread bob %d", a.Coins, a.inv["bread"], b.Coins)
	}
}

// TestAcceptPay_Barter_DuplicateKindsAggregate — resolvePayItems dedups
// canonical kinds at intake, but commitPayTransfer is the atomicity
// boundary and takes ledger data directly (seeded / future persisted
// entries). Duplicate kinds must AGGREGATE, not last-write-win: a seeded
// entry paying [{bread,3},{bread,4}] moves a total of 7 bread, leaving the
// buyer 3 and the seller 7 (not buyer 6 / seller 4).
func TestAcceptPay_Barter_DuplicateKindsAggregate(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 0, inventory: map[sim.ItemKind]int{"bread": 10}},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	at := time.Now().UTC()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 0,
		PayItems:  []sim.ItemKindQty{{Kind: "bread", Qty: 3}, {Kind: "bread", Qty: 4}},
		State:     sim.PayLedgerStatePending,
		CreatedAt: at, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	if _, err := w.Send(sim.AcceptPay("bob", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	a := readHoldings(t, w, "alice")
	b := readHoldings(t, w, "bob")
	if a.inv["bread"] != 3 {
		t.Errorf("alice.bread = %d, want 3 (10-7 aggregated)", a.inv["bread"])
	}
	if b.inv["bread"] != 7 {
		t.Errorf("bob.bread = %d, want 7 (3+4 aggregated)", b.inv["bread"])
	}
}

// ---- symmetric counter (option 1) -----------------------------------

// TestCounterPay_Barter_CounterWithGoods — the seller counters with goods
// terms ("I want 3 bread"). The parent flips Countered carrying
// CounterPayItems, and PayCountered carries them for the buyer.
func TestCounterPay_Barter_CounterWithGoods(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 10, inventory: map[sim.ItemKind]int{"bread": 10}},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	at := time.Now().UTC()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 4,
		State:     sim.PayLedgerStatePending,
		CreatedAt: at, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	events := capturePayWithItemEvents(t, w)
	// Counter with goods only (no coins): "I'd rather have 3 bread."
	if _, err := w.Send(sim.CounterPay("bob", 1, 0, []sim.PayItemInput{{Item: "bread", Qty: 3}}, "rather have bread", at)); err != nil {
		t.Fatalf("CounterPay: %v", err)
	}
	entry := readPayLedger(t, w)[1]
	if entry.State != sim.PayLedgerStateCountered {
		t.Fatalf("state = %q, want countered", entry.State)
	}
	if len(entry.CounterPayItems) != 1 || entry.CounterPayItems[0].Kind != "bread" || entry.CounterPayItems[0].Qty != 3 {
		t.Errorf("entry.CounterPayItems = %+v, want [{bread 3}]", entry.CounterPayItems)
	}
	if len(events.Counter) != 1 || len(events.Counter[0].CounterPayItems) != 1 {
		t.Errorf("PayCountered.CounterPayItems = %+v", events.Counter)
	}
}

// TestCounterPay_Barter_NoCoercionWhenGoodsInvolved — the
// non-increasing-coin coercion (counterAmount <= offered = "yes") applies
// ONLY to pure-coin haggles. A counter that touches goods — or a counter
// on a barter offer — is always a real counter, never a coerced accept.
func TestCounterPay_Barter_NoCoercionWhenGoodsInvolved(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 10, inventory: map[sim.ItemKind]int{"bread": 10}},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	at := time.Now().UTC()
	// Barter offer: 4 coins + 2 bread for stew. Seller counters at 4 coins
	// (== offered coins, which WOULD coerce on a pure-coin offer) but the
	// offer carries goods, so it must flip Countered, not Accepted.
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 4,
		PayItems:  []sim.ItemKindQty{{Kind: "bread", Qty: 2}},
		State:     sim.PayLedgerStatePending,
		CreatedAt: at, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	if _, err := w.Send(sim.CounterPay("bob", 1, 4, nil, "", at)); err != nil {
		t.Fatalf("CounterPay: %v", err)
	}
	if got := readPayLedger(t, w)[1].State; got != sim.PayLedgerStateCountered {
		t.Errorf("state = %q, want countered (no coercion when offer carries goods)", got)
	}
}

// TestCounterPay_Barter_EmptyCounterRejects — a counter must propose coins
// or goods; an all-zero counter rejects (symmetric with the offer rule).
func TestCounterPay_Barter_EmptyCounterRejects(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	at := time.Now().UTC()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 4,
		State:     sim.PayLedgerStatePending,
		CreatedAt: at, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	if _, err := w.Send(sim.CounterPay("bob", 1, 0, nil, "", at)); err == nil || !strings.Contains(err.Error(), "must propose coins or goods") {
		t.Fatalf("want empty-counter reject, got %v", err)
	}
}

// TestPayAcceptedFactText_Barter — the accepted-offer SalientFact reads in
// goods when the payment was barter (NPC memory says "I paid Bob 5 bread
// for 1 stew.", not "0 coins").
func TestPayAcceptedFactText_Barter(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 0, inventory: map[sim.ItemKind]int{"bread": 5}},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	at := time.Now().UTC()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 0,
		PayItems:  []sim.ItemKindQty{{Kind: "bread", Qty: 5}},
		State:     sim.PayLedgerStatePending,
		CreatedAt: at, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	if _, err := w.Send(sim.AcceptPay("bob", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	// The buyer-side relationship fact records the barter payment in goods.
	rel := w.Published().Actors["alice"].Relationships["bob"]
	var joined strings.Builder
	for _, f := range rel.SalientFacts {
		joined.WriteString(f.Text)
		joined.WriteString(" | ")
	}
	if !strings.Contains(joined.String(), "5 bread") {
		t.Errorf("alice→bob salient facts missing barter payment phrase; got: %s", joined.String())
	}
}
