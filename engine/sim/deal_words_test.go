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

// buildInnLotWorld is the Inn exchange: John Ellis (buyer) and Hannah Boggs
// (seller, fried meat and water) in one conversation, plus Prudence Ward, a
// second customer, for the untargeted-lot cases. Live catalog labels.
func buildInnLotWorld(t *testing.T) (*sim.World, func()) {
	t.Helper()
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "john", displayName: "John Ellis", kind: sim.KindNPCStateful, huddleID: "h1", coins: 50},
		{id: "prudence", displayName: "Prudence Ward", kind: sim.KindNPCStateful, huddleID: "h1", coins: 50},
		{id: "hannah", displayName: "Hannah Boggs", kind: sim.KindNPCStateful, huddleID: "h1",
			inventory: map[sim.ItemKind]int{"fried_meat": 4, "water": 6}},
	})
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.ItemKinds["fried_meat"] = &sim.ItemKindDef{Name: "fried_meat", DisplayLabel: "Fried Meat",
			DisplayLabelSingular: "fried meat", DisplayLabelPlural: "fried meat", Category: sim.ItemCategoryFood,
			Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger", Immediate: 12}}}
		world.ItemKinds["water"].DisplayLabelSingular = "flask of water"
		world.ItemKinds["water"].DisplayLabelPlural = "flasks of water"
		for _, a := range world.Actors {
			a.Needs = map[sim.NeedKey]int{"hunger": 12, "thirst": 10}
		}
		return nil, nil
	}}); err != nil {
		stop()
		t.Fatalf("seed: %v", err)
	}
	return w, stop
}

func offerFor(t *testing.T, w *sim.World, buyer sim.ActorID, item string, qty, amount int) sim.LedgerID {
	t.Helper()
	res, err := w.Send(sim.PayWithItem(buyer, "Hannah Boggs", item, qty, amount, true, nil, nil, 0, 0, "", time.Now().UTC()))
	if err != nil {
		t.Fatalf("PayWithItem %s: %v", buyer, err)
	}
	placed := res.(sim.PayWithItemResult)
	if placed.State != sim.PayLedgerStatePending {
		t.Fatalf("offer state = %q, want pending", placed.State)
	}
	return placed.LedgerID
}

func ledgerState(t *testing.T, w *sim.World, id sim.LedgerID) sim.PayLedgerEntry {
	t.Helper()
	return readPayLedger(t, w)[id]
}

// TestLotSetsAsideCoveredOffer_FullSequence is the whole Inn exchange done
// right: John offers 5 for a fried meat; Hannah answers with a lot — fried meat
// and water for 8 — which sets his offer aside; she can no longer accept the old
// offer; John takes the lot and pays 8, once.
func TestLotSetsAsideCoveredOffer_FullSequence(t *testing.T) {
	w, stop := buildInnLotWorld(t)
	defer stop()
	offer := offerFor(t, w, "john", "fried meat", 1, 5)

	res, err := w.Send(sim.SceneQuoteCreate("hannah",
		[]sim.QuoteLineInput{{ItemName: "fried_meat", Qty: 1}, {ItemName: "water", Qty: 1}},
		8, true, "John Ellis", nil, time.Now().UTC()))
	if err != nil {
		t.Fatalf("SceneQuoteCreate: %v", err)
	}
	lot := res.(sim.SceneQuoteCreateResult)
	want := []sim.SetAsideOffer{{LedgerID: offer, BuyerID: "john", BuyerName: "John Ellis", Goods: "1 fried meat"}}
	if !reflect.DeepEqual(lot.SetAside, want) {
		t.Fatalf("SetAside = %+v, want %+v", lot.SetAside, want)
	}
	e := ledgerState(t, w, offer)
	if e.State != sim.PayLedgerStateSupersededByLot {
		t.Fatalf("offer state = %q, want superseded_by_lot", e.State)
	}
	if e.Message != "1 fried meat and 1 flask of water for 8 coins" {
		t.Errorf("offer Message = %q, want the lot's terms", e.Message)
	}

	if _, err := w.Send(sim.AcceptPay("hannah", offer, time.Now().UTC())); err == nil {
		t.Fatal("Hannah accepted the set-aside offer — the double charge is still open")
	}

	took, err := w.Send(sim.PayWithItem("john", "Hannah Boggs", "fried meat", 1, 8, true, nil, nil, lot.QuoteID, 0, "", time.Now().UTC()))
	if err != nil {
		t.Fatalf("John takes the lot: %v", err)
	}
	if r := took.(sim.PayWithItemResult); r.State != sim.PayLedgerStateAccepted || len(r.Lines) != 2 {
		t.Fatalf("lot take = %+v, want accepted with both lines", r)
	}
	state, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return []int{world.Actors["john"].Coins, world.Actors["hannah"].Coins,
			world.Actors["hannah"].Inventory["fried_meat"], world.Actors["hannah"].Inventory["water"]}, nil
	}})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got, want := state.([]int), []int{42, 8, 3, 5}; !reflect.DeepEqual(got, want) {
		t.Errorf("[john coins, hannah coins, hannah meat, hannah water] = %v, want %v — paid once, both goods handed over", got, want)
	}
}

// TestLotSetsAside_OnlyWhatItAnswers: the lot replaces only an offer it can
// stand in for.
func TestLotSetsAside_OnlyWhatItAnswers(t *testing.T) {
	water := []sim.QuoteLineInput{{ItemName: "water", Qty: 2}}
	meatAndWater := []sim.QuoteLineInput{{ItemName: "fried_meat", Qty: 1}, {ItemName: "water", Qty: 1}}
	for _, tc := range []struct {
		name     string
		offers   func(t *testing.T, w *sim.World) []sim.LedgerID
		lines    []sim.QuoteLineInput
		target   string
		setAside []bool // per offer, in order
	}{
		{"a lot without the offer's good", func(t *testing.T, w *sim.World) []sim.LedgerID {
			return []sim.LedgerID{offerFor(t, w, "john", "fried meat", 1, 5)}
		}, water, "John Ellis", []bool{false}},
		{"a lot short of the offer's quantity", func(t *testing.T, w *sim.World) []sim.LedgerID {
			return []sim.LedgerID{offerFor(t, w, "john", "fried meat", 2, 10)}
		}, meatAndWater, "John Ellis", []bool{false}},
		{"a lot for another buyer", func(t *testing.T, w *sim.World) []sim.LedgerID {
			return []sim.LedgerID{offerFor(t, w, "john", "fried meat", 1, 5)}
		}, meatAndWater, "Prudence Ward", []bool{false}},
		{"a lot to the room, one buyer waiting", func(t *testing.T, w *sim.World) []sim.LedgerID {
			return []sim.LedgerID{offerFor(t, w, "john", "fried meat", 1, 5)}
		}, meatAndWater, "", []bool{true}},
		{"a lot to the room, two buyers waiting", func(t *testing.T, w *sim.World) []sim.LedgerID {
			return []sim.LedgerID{offerFor(t, w, "john", "fried meat", 1, 5), offerFor(t, w, "prudence", "fried meat", 1, 5)}
		}, meatAndWater, "", []bool{false, false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, stop := buildInnLotWorld(t)
			defer stop()
			offers := tc.offers(t, w)
			if _, err := w.Send(sim.SceneQuoteCreate("hannah", tc.lines, 8, true, tc.target, nil, time.Now().UTC())); err != nil {
				t.Fatalf("SceneQuoteCreate: %v", err)
			}
			for i, id := range offers {
				st := ledgerState(t, w, id).State
				if got := st == sim.PayLedgerStateSupersededByLot; got != tc.setAside[i] {
					t.Errorf("offer %d state = %q, set aside = %v, want %v", id, st, got, tc.setAside[i])
				}
				if !tc.setAside[i] && st != sim.PayLedgerStatePending {
					t.Errorf("offer %d state = %q, want still pending", id, st)
				}
			}
		})
	}
}
