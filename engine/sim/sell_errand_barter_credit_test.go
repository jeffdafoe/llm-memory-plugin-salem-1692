package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// sell_errand_barter_credit_test.go — LLM-876: a factor's errand good paid AS
// barter goods (the pay_items leg of commitPayTransfer, which does not go
// through transferItem) to his counterparty keeper counts toward
// TradeErrand.Delivered; the same payment to anyone else does not. Live,
// factors paid Josiah iron for flour and wheat and none of it counted.

const barterFactorID = sim.ActorID("vstr-0000fac0")

// buildBarterFactorWorld seats a factor on a sell-iron errand to the general
// store, holding 10 iron, in a huddle with josiah (the store's keeper) and
// hannah (a keeper elsewhere), each with bread to sell.
func buildBarterFactorWorld(t *testing.T) (*sim.World, func()) {
	t.Helper()
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: barterFactorID, displayName: "Tobias", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"iron": 10}},
		{id: "josiah", displayName: "Josiah", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"bread": 5}},
		{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"bread": 5}},
	})
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.ItemKinds["iron"] = &sim.ItemKindDef{Name: "iron", DisplayLabel: "Iron", Capabilities: []string{"portable"}}
		world.Actors["josiah"].WorkStructureID = "general_store"
		world.Actors["hannah"].WorkStructureID = "inn"
		world.Actors[barterFactorID].VisitorState = &sim.VisitorState{
			Phase: sim.VisitorPhaseMakingRounds,
			Trade: &sim.TradeErrand{Direction: sim.TradeDirectionSell, Good: "iron", Counterparty: "general_store", ShipmentQty: 10},
		}
		return nil, nil
	}}); err != nil {
		stop()
		t.Fatalf("seat the factor: %v", err)
	}
	return w, stop
}

func readFactorDelivered(t *testing.T, w *sim.World) int {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors[barterFactorID].VisitorState.Trade.Delivered, nil
	}})
	if err != nil {
		t.Fatalf("read Delivered: %v", err)
	}
	return res.(int)
}

// barterIronForBread has the factor pay 3 iron (as pay_items) for a bread from seller.
func barterIronForBread(t *testing.T, w *sim.World, seller sim.ActorID) {
	t.Helper()
	at := time.Now().UTC()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: barterFactorID, SellerID: seller,
		ItemKind: "bread", Qty: 1,
		PayItems:  []sim.ItemKindQty{{Kind: "iron", Qty: 3}},
		State:     sim.PayLedgerStatePending,
		CreatedAt: at, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	if _, err := w.Send(sim.AcceptPay(seller, 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	if got := readPayLedger(t, w)[1].State; got != sim.PayLedgerStateAccepted {
		t.Fatalf("ledger.State = %q, want accepted", got)
	}
}

func TestAcceptPay_ErrandGoodPaidToCounterpartyCountsDelivered(t *testing.T) {
	w, stop := buildBarterFactorWorld(t)
	defer stop()
	barterIronForBread(t, w, "josiah")
	if got := readFactorDelivered(t, w); got != 3 {
		t.Fatalf("Delivered = %d after paying the counterparty keeper 3 iron, want 3", got)
	}
}

func TestAcceptPay_ErrandGoodPaidElsewhereDoesNotCountDelivered(t *testing.T) {
	w, stop := buildBarterFactorWorld(t)
	defer stop()
	barterIronForBread(t, w, "hannah")
	if got := readFactorDelivered(t, w); got != 0 {
		t.Fatalf("Delivered = %d after paying a keeper who is not his counterparty, want 0", got)
	}
}
