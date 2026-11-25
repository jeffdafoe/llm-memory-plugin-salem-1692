package handlers

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// commission_accept_result_test.go — LLM-1026. An accept that mints a commission
// must not read like a handover. The live case: Ezekiel Crane held no shovels,
// believed he held three, accepted Elizabeth Ellis's 18 coins for three with
// "Here's your three shovels", and was told "The sale is settled". The result now
// says the coin is taken and the goods are owed, on accept_pay and on
// counter_pay's coercion to an accept, with or without a spoken line.

// buildCommissionResponseWorld stands a buyer (Alice) and a nail-making smith
// (Bob) together in the forge. Bob holds nailsHeld nails.
func buildCommissionResponseWorld(t *testing.T, nailsHeld int) *sim.World {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		"nail": {Name: "nail", DisplayLabel: "Nail", DisplayLabelSingular: "nail", DisplayLabelPlural: "nails", Category: sim.ItemCategoryMaterial},
	})
	handles.Recipes.Seed(map[sim.ItemKind]*sim.ItemRecipe{
		"nail": {OutputItem: "nail", OutputQty: 1, RateQty: 1, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 2},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"forge": {ID: "forge", DisplayName: "The Forge"},
	})
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{"bldg-asset": {ID: "bldg-asset", Category: "structure"}})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"forge": {ID: "forge", AssetID: "bldg-asset", Pos: sim.WorldPos{X: 160, Y: 160}},
	})
	inv := map[sim.ItemKind]int{}
	if nailsHeld > 0 {
		inv["nail"] = nailsHeld
	}
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"alice": {
			ID: "alice", DisplayName: "Alice", Kind: sim.KindNPCShared,
			State: sim.StateIdle, Coins: 50, InsideStructureID: "forge",
			Acquaintances: map[string]sim.Acquaintance{"Bob": {}},
			RecentActions: sim.NewRingBuffer[sim.Action](4),
		},
		"bob": {
			ID: "bob", DisplayName: "Bob", Kind: sim.KindNPCStateful,
			State: sim.StateIdle, InsideStructureID: "forge", WorkStructureID: "forge",
			Inventory:     inv,
			RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{{Item: "nail", Source: sim.RestockSourceProduce, Max: 20}}},
			Acquaintances: map[string]sim.Acquaintance{"Alice": {}},
			RecentActions: sim.NewRingBuffer[sim.Action](4),
		},
		// Carol and Dave are recipients Alice can buy for.
		"carol": {
			ID: "carol", DisplayName: "Carol", Kind: sim.KindNPCShared,
			State: sim.StateIdle, InsideStructureID: "forge",
			RecentActions: sim.NewRingBuffer[sim.Action](4),
		},
		"dave": {
			ID: "dave", DisplayName: "Dave", Kind: sim.KindNPCShared,
			State: sim.StateIdle, InsideStructureID: "forge",
			RecentActions: sim.NewRingBuffer[sim.Action](4),
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	return w
}

// placePendingNailOffer has Alice offer `amount` coins for `qty` nails to keep.
func placePendingNailOffer(t *testing.T, w *sim.World, qty, amount int, now time.Time) sim.LedgerID {
	t.Helper()
	if _, err := sim.EnsureColocatedHuddle("alice", now).Fn(w); err != nil {
		t.Fatalf("EnsureColocatedHuddle: %v", err)
	}
	res, err := sim.PayWithItem("alice", "Bob", "nail", qty, amount, false, nil, nil, 0, 0, "", now).Fn(w)
	if err != nil {
		t.Fatalf("PayWithItem: %v", err)
	}
	placed, ok := res.(sim.PayWithItemResult)
	if !ok || placed.State != sim.PayLedgerStatePending {
		t.Fatalf("offer = %+v, want a pending offer", res)
	}
	return placed.LedgerID
}

func runAcceptPay(t *testing.T, w *sim.World, ledgerID sim.LedgerID, say string) string {
	t.Helper()
	vc := &ValidatedCall{Name: "accept_pay", DecodedArgs: AcceptPayArgs{LedgerID: LenientID(ledgerID), Say: say}}
	cmd, err := HandleAcceptPay(HandlerInput{ActorID: "bob", AttemptID: "tk-1", HasNewNews: true, Args: vc.DecodedArgs})
	if err != nil {
		t.Fatalf("HandleAcceptPay: %v", err)
	}
	res, err := cmd.Fn(w)
	if err != nil {
		t.Fatalf("accept_pay: %v", err)
	}
	return commitResultContent(vc, res)
}

func TestAcceptPay_CommissionResultSaysGoodsOwed(t *testing.T) {
	for _, say := range []string{"", "Fair price. Here's your three nails."} {
		w := buildCommissionResponseWorld(t, 0)
		now := time.Now().UTC()
		ledgerID := placePendingNailOffer(t, w, 3, 18, now)

		got := runAcceptPay(t, w, ledgerID, say)
		want := "You've taken Alice's 18 coins for 3 nails you have yet to make."
		if !strings.Contains(got, want) {
			t.Errorf("say=%q: result %q missing %q", say, got, want)
		}
		if !strings.Contains(got, "Alice has not got them yet") || !strings.Contains(got, fmt.Sprintf("order #%d", ledgerID)) {
			t.Errorf("say=%q: result %q does not say the goods are still owed, by order number", say, got)
		}
		if strings.Contains(got, "The sale is settled") {
			t.Errorf("say=%q: a commission still reads as a settled handover: %q", say, got)
		}
		if say != "" && !strings.Contains(got, "You said:") {
			t.Errorf("say=%q: result %q dropped the say echo", say, got)
		}
	}
}

// Alice pays for one nail each for Carol and Dave. The result counts both nails
// and does not say Alice is owed them: she paid, they receive.
func TestAcceptPay_CommissionForOthersOwedOnTheOrder(t *testing.T) {
	w := buildCommissionResponseWorld(t, 0)
	now := time.Now().UTC()
	for _, who := range []sim.ActorID{"alice", "carol", "dave"} {
		if _, err := sim.EnsureColocatedHuddle(who, now).Fn(w); err != nil {
			t.Fatalf("EnsureColocatedHuddle(%s): %v", who, err)
		}
	}
	res, err := sim.PayWithItem("alice", "Bob", "nail", 1, 12, false, []string{"Carol", "Dave"}, nil, 0, 0, "", now).Fn(w)
	if err != nil {
		t.Fatalf("PayWithItem: %v", err)
	}
	placed, ok := res.(sim.PayWithItemResult)
	if !ok || placed.State != sim.PayLedgerStatePending {
		t.Fatalf("offer = %+v, want pending", res)
	}

	got := runAcceptPay(t, w, placed.LedgerID, "")
	for _, want := range []string{
		"You've taken Alice's 12 coins for 2 nails you have yet to make.",
		fmt.Sprintf("They are not handed over yet — you owe them as order #%d, to hand over to those it is for once made.", placed.LedgerID),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("result %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "Alice has not got") {
		t.Errorf("result %q names the payer as the one owed", got)
	}
}

// A sale handed over from stock keeps the plain settle.
func TestAcceptPay_InStockResultStaysSettled(t *testing.T) {
	w := buildCommissionResponseWorld(t, 3)
	now := time.Now().UTC()
	ledgerID := placePendingNailOffer(t, w, 3, 18, now)

	got := runAcceptPay(t, w, ledgerID, "")
	if !strings.Contains(got, "The sale is settled") || strings.Contains(got, "yet to make") {
		t.Errorf("in-stock accept result = %q, want the plain settle", got)
	}
}

// counter_pay at or below the offer coerces to an accept (LLM-13); on a
// commission it must say the same thing accept_pay does.
func TestCounterPayCoercion_CommissionResultSaysGoodsOwed(t *testing.T) {
	w := buildCommissionResponseWorld(t, 0)
	now := time.Now().UTC()
	ledgerID := placePendingNailOffer(t, w, 3, 18, now)

	vc := &ValidatedCall{Name: "counter_pay", DecodedArgs: CounterPayArgs{LedgerID: LenientID(ledgerID), Amount: 18}}
	cmd, err := HandleCounterPay(HandlerInput{ActorID: "bob", AttemptID: "tk-1", HasNewNews: true, Args: vc.DecodedArgs})
	if err != nil {
		t.Fatalf("HandleCounterPay: %v", err)
	}
	res, err := cmd.Fn(w)
	if err != nil {
		t.Fatalf("counter_pay: %v", err)
	}
	if state, _, _, _ := payResponseState(res); state != sim.PayLedgerStateAccepted {
		t.Fatalf("counter at the offered price resolved to %q, want accepted (the coercion)", state)
	}
	got := commitResultContent(vc, res)
	if !strings.Contains(got, "You've taken Alice's 18 coins for 3 nails you have yet to make.") {
		t.Errorf("counter_pay coercion result %q does not name the commission", got)
	}
}

// The deposit and single-good arms of the lead.
func TestAcceptSettleLead_DepositAndSingle(t *testing.T) {
	deposit := acceptSettleLead(payResponseResult{
		State:      sim.PayLedgerStateAccepted,
		Commission: &sim.CommissionTaken{OrderID: 7, BuyerName: "Alice", Qty: 1, Noun: "nail", Charged: 5, BalanceDue: 10},
	})
	for _, want := range []string{
		"You've taken Alice's deposit of 5 coins on 1 nail you have yet to make, with 10 coins more to collect when you hand it over.",
		"Alice has not got it yet — you owe it as order #7",
	} {
		if !strings.Contains(deposit, want) {
			t.Errorf("deposit lead %q missing %q", deposit, want)
		}
	}
	if got := acceptSettleLead(sim.PayLedgerStateAccepted); got != "The sale is settled. " {
		t.Errorf("bare-state lead = %q, want the plain settle", got)
	}
	partly := acceptSettleLead(payResponseResult{
		State:      sim.PayLedgerStateAccepted,
		Commission: &sim.CommissionTaken{OrderID: 8, BuyerName: "Alice", Qty: 3, Noun: "nails", Charged: 18, PartlyOnHand: true},
	})
	want := "You've taken Alice's 18 coins for 3 nails you cannot yet hand over in full. Alice has not got them yet — you owe them as order #8, to hand over once you have them all. "
	if partly != want {
		t.Errorf("partly-on-hand lead = %q, want %q", partly, want)
	}
	free := acceptSettleLead(payResponseResult{
		State:      sim.PayLedgerStateAccepted,
		Commission: &sim.CommissionTaken{OrderID: 9, BuyerName: "Alice", Qty: 2, Noun: "nails"},
	})
	if !strings.HasPrefix(free, "You've promised Alice 2 nails you have yet to make.") {
		t.Errorf("zero-coin lead = %q", free)
	}
}
