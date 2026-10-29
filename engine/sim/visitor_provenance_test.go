package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// visitor_provenance_test.go — LLM-875 on the real accept path: goods a keeper
// pays a traveler WITH (the pay_items leg of commitPayTransfer, which does not
// go through transferItem) are recorded against the keeper, and a later sale of
// the same good back to him nets them out.

func visitorReceivedFromForTest(t *testing.T, w *sim.World, id sim.ActorID) map[sim.ActorID]map[sim.ItemKind]int {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors[id]
		if a == nil || a.VisitorState == nil {
			return map[sim.ActorID]map[sim.ItemKind]int(nil), nil
		}
		out := map[sim.ActorID]map[sim.ItemKind]int{}
		for rid, kinds := range a.VisitorState.ReceivedFrom {
			row := map[sim.ItemKind]int{}
			for k, q := range kinds {
				row[k] = q
			}
			out[rid] = row
		}
		return out, nil
	}})
	if err != nil {
		t.Fatalf("read ReceivedFrom for %s: %v", id, err)
	}
	return res.(map[sim.ActorID]map[sim.ItemKind]int)
}

func TestAcceptPay_BarterGoodsToVisitorRecordProvenance(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "josiah", displayName: "Josiah", kind: sim.KindNPCShared, huddleID: "h1", coins: 20, inventory: map[sim.ItemKind]int{"bread": 8}},
		{id: "vstr-0000fac0", displayName: "Tobias", kind: sim.KindNPCShared, huddleID: "h1", coins: 0, inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["vstr-0000fac0"].VisitorState = &sim.VisitorState{Phase: sim.VisitorPhaseMakingRounds}
		return nil, nil
	}}); err != nil {
		t.Fatalf("make Tobias a visitor: %v", err)
	}
	at := time.Now().UTC()

	// Josiah pays the traveler 5 bread + 2 coins for a stew.
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "josiah", SellerID: "vstr-0000fac0",
		ItemKind: "stew", Qty: 1, Amount: 2,
		PayItems:  []sim.ItemKindQty{{Kind: "bread", Qty: 5}},
		State:     sim.PayLedgerStatePending,
		CreatedAt: at, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	if _, err := w.Send(sim.AcceptPay("vstr-0000fac0", 1, at)); err != nil {
		t.Fatalf("AcceptPay (barter): %v", err)
	}
	got := visitorReceivedFromForTest(t, w, "vstr-0000fac0")
	if got["josiah"]["bread"] != 5 {
		t.Fatalf("ReceivedFrom = %v after the barter, want josiah:bread=5", got)
	}
	if got["josiah"]["stew"] != 0 {
		t.Fatalf("ReceivedFrom = %v: the stew went traveler→keeper and must not be recorded as the keeper's", got)
	}

	// Josiah buys 3 of the bread back for coin: the take-home leg nets them out.
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 2, BuyerID: "josiah", SellerID: "vstr-0000fac0",
		ItemKind: "bread", Qty: 3, Amount: 6,
		State:     sim.PayLedgerStatePending,
		CreatedAt: at, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	if _, err := w.Send(sim.AcceptPay("vstr-0000fac0", 2, at)); err != nil {
		t.Fatalf("AcceptPay (buy-back): %v", err)
	}
	got = visitorReceivedFromForTest(t, w, "vstr-0000fac0")
	if got["josiah"]["bread"] != 2 {
		t.Fatalf("ReceivedFrom = %v after the buy-back, want josiah:bread=2 (5 in, 3 back)", got)
	}
}

// makeVisitorForTest turns a fixture actor into a visitor holding the given
// provenance rows.
func makeVisitorForTest(t *testing.T, w *sim.World, id sim.ActorID, rows map[sim.ActorID]map[sim.ItemKind]int) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors[id].VisitorState = &sim.VisitorState{Phase: sim.VisitorPhaseMakingRounds, ReceivedFrom: rows}
		return nil, nil
	}}); err != nil {
		t.Fatalf("make %s a visitor: %v", id, err)
	}
}

// TestAcceptPay_ConsumeNowBuyBackNetsEatenUnits — code_review: a resident who
// buys his own goods back to eat on the spot has had ALL of them back, the eaten
// units as well as the surplus he pockets — and nothing more. Every one of the
// ten stew is his; with no needs set he eats the floor of one and keeps four, so
// five remain his. A clamp run between the two debits would leave four (round 2).
func TestAcceptPay_ConsumeNowBuyBackNetsEatenUnits(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "josiah", displayName: "Josiah", kind: sim.KindNPCShared, huddleID: "h1", coins: 30},
		{id: "vstr-0000fac0", displayName: "Tobias", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 10}},
	})
	defer stop()
	makeVisitorForTest(t, w, "vstr-0000fac0", map[sim.ActorID]map[sim.ItemKind]int{"josiah": {"stew": 10}})
	at := time.Now().UTC()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "josiah", SellerID: "vstr-0000fac0",
		ItemKind: "stew", Qty: 5, Amount: 10, ConsumeNow: true,
		State: sim.PayLedgerStatePending, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	if _, err := w.Send(sim.AcceptPay("vstr-0000fac0", 1, at)); err != nil {
		t.Fatalf("AcceptPay (consume_now buy-back): %v", err)
	}
	got := visitorReceivedFromForTest(t, w, "vstr-0000fac0")
	if got["josiah"]["stew"] != 5 {
		t.Fatalf("ReceivedFrom = %v after josiah had five of his ten back (one eaten, four kept), want josiah:stew=5", got)
	}
}

// TestPayWithItem_BundleEatHereBuyBackNetsEatenUnits — the bundle twin. Every
// unit the visitor holds came from jeff; jeff takes an eat-here bundle of two of
// each, eating one and keeping one per line, so one of each remains his.
func TestPayWithItem_BundleEatHereBuyBackNetsEatenUnits(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "jeff", displayName: "Jefferey", kind: sim.KindPC, huddleID: "h1", coins: 50},
		{id: "vstr-0000fac0", displayName: "Prudence", kind: sim.KindNPCShared, huddleID: "h1",
			inventory: map[sim.ItemKind]int{"ale": 3, "bread": 3}},
	})
	defer stop()
	makeVisitorForTest(t, w, "vstr-0000fac0", map[sim.ActorID]map[sim.ItemKind]int{"jeff": {"ale": 3, "bread": 3}})
	at := time.Now().UTC()
	seedQuote(t, w, sim.SceneQuote{
		ID:         9,
		SceneID:    "sc1",
		SellerID:   "vstr-0000fac0",
		Lines:      []sim.QuoteLine{{ItemKind: "ale", Qty: 2}, {ItemKind: "bread", Qty: 2}},
		Amount:     12,
		ConsumeNow: true,
		State:      sim.SceneQuoteStateActive,
		ExpiresAt:  at.Add(10 * time.Minute),
	})
	res, err := w.Send(sim.PayWithItem("jeff", "Prudence", "ale", 2, 12, true, nil, nil, 9, 0, "", at))
	if err != nil {
		t.Fatalf("PayWithItem (eat-here bundle): %v", err)
	}
	if out := res.(sim.PayWithItemResult); out.BuyerAte != 2 {
		t.Fatalf("BuyerAte = %d, want 2 (one of each line eaten, one of each kept)", out.BuyerAte)
	}
	got := visitorReceivedFromForTest(t, w, "vstr-0000fac0")
	if got["jeff"]["ale"] != 1 || got["jeff"]["bread"] != 1 {
		t.Fatalf("ReceivedFrom = %v, want jeff{ale:1 bread:1} — two of each came back, one of each is still his", got)
	}
}
