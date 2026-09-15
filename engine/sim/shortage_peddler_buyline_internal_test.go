package sim

import (
	"testing"
	"time"
)

// shortage_peddler_buyline_internal_test.go — the buy-line half of the sweep
// (LLM-657): a keeper's bought good that he neither makes nor gathers, holds
// none of, and that no village supplier holds is a shortage too. The live shape
// is the wright's whetstone — consumed by a service, not a recipe — and the
// wright keeps his post as its OWNER, with no businessowner attribute.

// wrightWorld is shortageWorld plus the wright's shape: Lewis owns and works a
// business-tagged workshop (no BusinessownerState), keeps one buy line
// (whetstone, cap 4) and holds none; Ezekiel at the forge makes whetstones from
// iron he has on hand, and holds none.
func wrightWorld() *World {
	w := shortageWorld()
	w.Actors["lewis"] = &Actor{
		ID: "lewis", DisplayName: "Lewis Walker", Kind: KindNPCShared, State: StateIdle,
		WorkStructureID: "workshop", InsideStructureID: "workshop", Pos: GridPoint{X: 20, Y: 20},
		RestockPolicy: &RestockPolicy{Restock: []RestockEntry{{Item: WhetstoneKind, Source: RestockSourceBuy, Max: 4}}},
		Inventory:     map[ItemKind]int{"nail": 6},
	}
	w.Actors["ezekiel"] = &Actor{
		ID: "ezekiel", DisplayName: "Ezekiel Cheever", Kind: KindNPCStateful, State: StateIdle,
		WorkStructureID: "forge", InsideStructureID: "forge",
		BusinessownerState: &BusinessownerState{Flavor: "blacksmith"},
		RestockPolicy:      &RestockPolicy{Restock: []RestockEntry{{Item: WhetstoneKind, Source: RestockSourceProduce, Max: 4}}},
		Inventory:          map[ItemKind]int{"iron": 2},
	}
	w.VillageObjects["workshop"] = &VillageObject{ID: "workshop", Pos: WorldPos{X: 640, Y: 640}, Tags: []string{TagBusiness, TagWright}, OwnerActorID: "lewis"}
	w.VillageObjects["forge"] = &VillageObject{ID: "forge", Pos: WorldPos{X: 500, Y: 500}, Tags: []string{TagBusiness}}
	w.Structures["workshop"] = &Structure{ID: "workshop", DisplayName: "Lewis's Workshop"}
	w.Structures["forge"] = &Structure{ID: "forge", DisplayName: "The Forge"}
	w.Recipes[WhetstoneKind] = &ItemRecipe{OutputItem: WhetstoneKind, OutputQty: 1, RateQty: 1, RatePerHours: 1,
		Inputs: []RecipeInput{{Item: "iron", Qty: 1}}}
	w.ItemKinds[WhetstoneKind] = &ItemKindDef{Name: WhetstoneKind, DisplayLabel: "Whetstone",
		DisplayLabelSingular: "whetstone", DisplayLabelPlural: "whetstones", Capabilities: []string{"portable"}}
	w.ItemKinds["iron"] = &ItemKindDef{Name: "iron", DisplayLabel: "Iron", Capabilities: []string{"portable"}}
	w.ItemKinds["nail"] = &ItemKindDef{Name: "nail", DisplayLabel: "Nail", Capabilities: []string{"portable"}}
	return w
}

func TestInputShortagesNowBuyLines(t *testing.T) {
	both := []shortageKey{{keeper: "john", item: "meat"}, {keeper: "lewis", item: WhetstoneKind}}
	johnOnly := []shortageKey{{keeper: "john", item: "meat"}}
	check := func(name string, w *World, wantKeys []shortageKey) {
		t.Helper()
		got := inputShortagesNow(w)
		if len(got) != len(wantKeys) {
			t.Errorf("%s: shortages = %v, want %v", name, got, wantKeys)
			return
		}
		for i := range got {
			if got[i] != wantKeys[i] {
				t.Errorf("%s: shortages = %v, want %v", name, got, wantKeys)
				return
			}
		}
	}
	check("live shape: the wright's empty whetstone line, the maker holding none", wrightWorld(), both)

	w := wrightWorld()
	w.Actors["ezekiel"].Inventory[WhetstoneKind] = 1
	check("the maker holds one", w, johnOnly)

	w = wrightWorld()
	w.Actors["lewis"].Inventory[WhetstoneKind] = 1
	check("the wright holds one — not short, whatever his cap", w, johnOnly)

	// Keeper-ness: businessowner OR owner of the business he works at. Neither,
	// and he is a hand at somebody's post, not a keeper the peddler binds to.
	w = wrightWorld()
	w.VillageObjects["workshop"].OwnerActorID = ""
	check("neither businessowner nor owner of his post", w, johnOnly)
	w = wrightWorld()
	w.VillageObjects["workshop"].OwnerActorID = ""
	w.Actors["lewis"].BusinessownerState = &BusinessownerState{Flavor: "wright"}
	check("a businessowner's buy line", w, both)
	w = wrightWorld()
	w.VillageObjects["workshop"].Tags = []string{TagWright}
	check("owner of a post that is no business", w, johnOnly)

	// The distributor's buy lines are the factor's business: josiah's empty meat
	// line is in the base fixture and never counted; an empty whetstone line
	// on him counts no more.
	w = wrightWorld()
	w.Actors["josiah"].RestockPolicy.Restock = append(w.Actors["josiah"].RestockPolicy.Restock,
		RestockEntry{Item: WhetstoneKind, Source: RestockSourceBuy, Max: 6})
	check("the distributor's buy lines", w, both)

	// His own to make: the whetstone drops out of the buy-line walk — and the
	// recipe walk picks up the iron it takes, which nobody here supplies.
	w = wrightWorld()
	w.Actors["lewis"].RestockPolicy.Restock = append(w.Actors["lewis"].RestockPolicy.Restock,
		RestockEntry{Item: WhetstoneKind, Source: RestockSourceProduce})
	check("his own to make", w, []shortageKey{{keeper: "john", item: "meat"}, {keeper: "lewis", item: "iron"}})

	w = wrightWorld()
	w.ItemKinds[WhetstoneKind].Capabilities = []string{"service"}
	check("a service is not a good a peddler can carry", w, johnOnly)

	// A reseller (not the distributor) holding whetstones is retail stock, not
	// a village supply (LLM-252) — the shortage stands.
	w = wrightWorld()
	w.Actors["reseller"] = &Actor{ID: "reseller", Kind: KindNPCShared, WorkStructureID: "farm",
		RestockPolicy: &RestockPolicy{Restock: []RestockEntry{{Item: WhetstoneKind, Source: RestockSourceBuy, Max: 4}}},
		Inventory:     map[ItemKind]int{WhetstoneKind: 2}}
	check("a reseller holding some", w, both)

	// John's meat buy line and his stew's meat input are one shortage.
	w = wrightWorld()
	w.Actors["john"].Inventory["meat"] = 0
	check("a buy line that is also a recipe input", w, both)
}

// TestDueShortagePeddlerBindsTheWright — the errand for a buy-line shortage is
// the same keeper-bound sell errand: Counterparty = his workshop, Keeper = him.
func TestDueShortagePeddlerBindsTheWright(t *testing.T) {
	now := time.Date(2026, 9, 15, 20, 0, 0, 0, time.UTC)
	w := wrightWorld()
	w.Environment.InputShortages = []InputShortage{{KeeperID: "lewis", Item: WhetstoneKind, Days: 3, LastSeenAt: now}}
	i, errand, ok := dueShortagePeddler(w, now)
	if !ok || i != 0 || errand == nil {
		t.Fatalf("three days short: due = (%d, %+v, %v), want index 0 with an errand", i, errand, ok)
	}
	if errand.Direction != TradeDirectionSell || errand.Good != WhetstoneKind || errand.Counterparty != "workshop" || !errand.Peddler || errand.Keeper != "lewis" {
		t.Errorf("errand = %+v, want a peddler sell of whetstone bound to the workshop, for lewis", errand)
	}
	// Ezekiel forged one this morning: resolved since the sweep, dropped on the spot.
	w.Actors["ezekiel"].Inventory[WhetstoneKind] = 1
	if _, _, ok := dueShortagePeddler(w, now); ok {
		t.Error("the maker holds one: due, want dropped")
	}
	if len(w.Environment.InputShortages) != 0 {
		t.Errorf("resolved entry still stored: %+v, want dropped", w.Environment.InputShortages)
	}
}

// TestPeddlerShipmentQtyCappedByBuyLine — no recipe of the wright's takes a
// whetstone, so the pack floors at one a batch; his buy line's cap bounds it at
// the room he has left, never below one.
func TestPeddlerShipmentQtyCappedByBuyLine(t *testing.T) {
	w := wrightWorld()
	lewis := w.Actors["lewis"]
	for _, tc := range []struct {
		name    string
		held    int
		batches int
		want    int
	}{
		{"two batches, one a batch", 0, 2, 2},
		{"five batches, capped at the line's 4", 0, 5, 4},
		{"holding three of four: the one he has room for", 3, 2, 1},
		{"holding four of four (never short, but never zero)", 4, 2, 1},
	} {
		lewis.Inventory[WhetstoneKind] = tc.held
		if got := peddlerShipmentQty(w, lewis, WhetstoneKind, tc.batches); got != tc.want {
			t.Errorf("%s: qty = %d, want %d", tc.name, got, tc.want)
		}
	}
	// A recipe input with a buy line: batches × per-batch, bounded by the line's room.
	john := w.Actors["john"] // meat buy cap 12, holding 1
	if got := peddlerShipmentQty(w, john, "meat", 2); got != 4 {
		t.Errorf("john, 2 batches: qty = %d, want 4", got)
	}
	if got := peddlerShipmentQty(w, john, "meat", 10); got != 11 {
		t.Errorf("john, 10 batches: qty = %d, want 11 (room under his cap of 12)", got)
	}
}
