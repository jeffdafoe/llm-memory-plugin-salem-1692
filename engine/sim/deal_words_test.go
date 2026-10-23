package sim_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// TestDealWordsFor checks the spoken line of a trade against its ledger entry,
// on the live 2026-10-23 Inn exchange: John Ellis's fried-meat-only offer to
// Hannah Boggs, who also holds water and raw meat.
func TestDealWordsFor(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "john", displayName: "John Ellis", kind: sim.KindNPCStateful, huddleID: "h1", coins: 50},
		{id: "hannah", displayName: "Hannah Boggs", kind: sim.KindNPCStateful, huddleID: "h1",
			inventory: map[sim.ItemKind]int{"fried_meat": 3, "water": 4, "meat": 2, "ale": 0}},
	})
	defer stop()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.ItemKinds["fried_meat"] = &sim.ItemKindDef{Name: "fried_meat", DisplayLabel: "Fried Meat",
			DisplayLabelSingular: "fried meat", DisplayLabelPlural: "fried meat", Category: sim.ItemCategoryFood}
		world.ItemKinds["meat"] = &sim.ItemKindDef{Name: "meat", DisplayLabel: "Meat",
			DisplayLabelSingular: "cut of meat", DisplayLabelPlural: "cuts of meat", Category: sim.ItemCategoryFood}
		world.ItemKinds["water"].DisplayLabelSingular = "flask of water"
		world.ItemKinds["water"].DisplayLabelPlural = "flasks of water"
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed kinds: %v", err)
	}
	at := time.Now().UTC()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "john", SellerID: "hannah", ItemKind: "fried_meat", Qty: 1, Amount: 5,
		ConsumeNow: true, State: sim.PayLedgerStatePending, ExpiresAt: at.Add(3 * time.Minute),
	})
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 2, BuyerID: "john", SellerID: "hannah", ItemKind: "fried_meat", Qty: 1,
		PayItems: []sim.ItemKindQty{{Kind: "water", Qty: 1}},
		State:    sim.PayLedgerStatePending, ExpiresAt: at.Add(3 * time.Minute),
	})
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 3, BuyerID: "john", SellerID: "hannah", ItemKind: "meat", Qty: 2, Amount: 6,
		State: sim.PayLedgerStatePending, ExpiresAt: at.Add(3 * time.Minute),
	})

	for _, tc := range []struct {
		name   string
		ledger sim.LedgerID
		say    string
		want   sim.DealWords
	}{
		{"John's opener names the water", 1,
			"Good day to you, Hannah. Would you sell me a fried meat and a flask of water? I'll give you five coins for the meat and one for the water.",
			sim.DealWords{Named: []string{"flask of water"}, Deal: "1 fried meat"}},
		{"Hannah's counter names the water", 1,
			"John, I'll do the fried meat and the water together for eight coins. The meat alone runs me near five in the making — I can't let it go for five and the water free.",
			sim.DealWords{Named: []string{"water"}, Deal: "1 fried meat"}},
		{"the deal's own good, by name and by head noun", 1,
			"Eight coins it is, John. Here's your meat — hot from the skillet, the best fried meat this side of Salem.",
			sim.DealWords{}},
		{"a good paid WITH is part of the deal", 2,
			"A fried meat for a flask of water, then.",
			sim.DealWords{}},
		{"a good the seller holds none of", 1,
			"A fried meat, and an ale if you have one.",
			sim.DealWords{}},
		{"a longer name for another good wins over the deal's head noun", 3,
			"Two cuts of meat — and a fried meat for the road.",
			sim.DealWords{Named: []string{"fried meat"}, Deal: "2 cuts of meat"}},
		{"no words", 1, "", sim.DealWords{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
				return sim.DealWordsFor(world, tc.ledger, tc.say), nil
			}})
			if err != nil {
				t.Fatalf("DealWordsFor: %v", err)
			}
			if got := res.(sim.DealWords); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("DealWordsFor = %+v, want %+v", got, tc.want)
			}
		})
	}
}
