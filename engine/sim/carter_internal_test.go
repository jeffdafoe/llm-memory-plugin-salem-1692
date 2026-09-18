package sim

import (
	"strings"
	"testing"
	"time"
)

// carter_internal_test.go — the carter's rules (carter.go): what counts as
// residue, the going rate, the route planner's matched / shortage / export
// legs, the due gate, and the mechanical buy leg with its projection.

var carterNow = time.Date(2026, 9, 17, 19, 30, 0, 0, time.UTC)

// carterWorld is the live 2026-09-17 shape: Elizabeth at the dairy holds 25
// wheat and 8 iron she has no line for, a coat she wears, and 30 firewood she
// burns; Joseph at the mill buys wheat (holds 3, a batch takes 5) and has coin;
// Ezekiel at the smithy buys iron and holds none but has coin; John at the
// tavern buys meat, holds none, and is broke; Josiah at the store is the
// distributor and holds nothing.
func carterWorld() *World {
	w := &World{
		Actors: map[ActorID]*Actor{
			"liz": {
				ID: "liz", DisplayName: "Elizabeth Ellis", Kind: KindNPCShared, State: StateIdle,
				WorkStructureID: "farm", InsideStructureID: "farm", Coins: 1,
				BusinessownerState: &BusinessownerState{Flavor: "dairykeeper"},
				RestockPolicy: &RestockPolicy{Restock: []RestockEntry{
					{Item: "milk", Source: RestockSourceProduce, Max: 30},
					{Item: "sage", Source: RestockSourceBuy, Max: 3},
				}},
				Inventory: map[ItemKind]int{"wheat": 25, "iron": 8, "milk": 2, "coat": 1, "firewood": 30, "sage": 3, "meat": 2},
			},
			"joseph": {
				ID: "joseph", DisplayName: "Joseph Scott", Kind: KindNPCShared, State: StateIdle,
				WorkStructureID: "mill", InsideStructureID: "mill", Coins: 700,
				BusinessownerState: &BusinessownerState{Flavor: "miller"},
				RestockPolicy: &RestockPolicy{Restock: []RestockEntry{
					{Item: "flour", Source: RestockSourceProduce, Max: 20},
					{Item: "wheat", Source: RestockSourceBuy, Max: 50},
				}},
				Inventory: map[ItemKind]int{"wheat": 3, "flour": 10},
			},
			"ezekiel": {
				ID: "ezekiel", DisplayName: "Ezekiel Crane", Kind: KindNPCStateful, State: StateIdle,
				WorkStructureID: "smithy", InsideStructureID: "smithy", Coins: 190,
				BusinessownerState: &BusinessownerState{Flavor: "blacksmith"},
				RestockPolicy: &RestockPolicy{Restock: []RestockEntry{
					{Item: "nail", Source: RestockSourceProduce, Max: 20},
					{Item: "iron", Source: RestockSourceBuy, Max: 6},
				}},
				Inventory: map[ItemKind]int{"nail": 7},
			},
			"john": {
				ID: "john", DisplayName: "John Ellis", Kind: KindNPCStateful, State: StateIdle,
				WorkStructureID: "tavern", InsideStructureID: "tavern", Coins: 3,
				BusinessownerState: &BusinessownerState{Flavor: "tavernkeeper"},
				RestockPolicy: &RestockPolicy{Restock: []RestockEntry{
					{Item: "stew", Source: RestockSourceProduce, Max: 30},
					{Item: "meat", Source: RestockSourceBuy, Max: 12},
				}},
				Inventory: map[ItemKind]int{"water": 10, "ale": 40},
			},
			"josiah": {
				ID: "josiah", DisplayName: "Josiah Thorne", Kind: KindNPCStateful, State: StateIdle,
				WorkStructureID: "store", InsideStructureID: "store", Coins: 1,
				BusinessownerState: &BusinessownerState{Flavor: "storekeeper"},
				RestockPolicy: &RestockPolicy{Restock: []RestockEntry{
					{Item: "cheese", Source: RestockSourceBuy, Max: 6},
				}},
				Inventory: map[ItemKind]int{"salt": 40},
			},
		},
		VillageObjects: map[VillageObjectID]*VillageObject{
			"farm":   {ID: "farm", Pos: WorldPos{X: 100, Y: 100}, Tags: []string{TagBusiness}},
			"mill":   {ID: "mill", Pos: WorldPos{X: 300, Y: 300}, Tags: []string{TagBusiness}},
			"smithy": {ID: "smithy", Pos: WorldPos{X: 400, Y: 200}, Tags: []string{TagBusiness}},
			"tavern": {ID: "tavern", Pos: WorldPos{X: 320, Y: 320}, Tags: []string{TagBusiness}},
			"store":  {ID: "store", Pos: WorldPos{X: 200, Y: 200}, Tags: []string{TagBusiness, TagDistributor}},
		},
		Structures: map[StructureID]*Structure{
			"farm":   {ID: "farm", DisplayName: "Ellis Farm"},
			"mill":   {ID: "mill", DisplayName: "Mill"},
			"smithy": {ID: "smithy", DisplayName: "Blacksmith"},
			"tavern": {ID: "tavern", DisplayName: "The Tavern"},
			"store":  {ID: "store", DisplayName: "General Store"},
		},
		Recipes: map[ItemKind]*ItemRecipe{
			"flour": {OutputItem: "flour", OutputQty: 5, RateQty: 4, RatePerHours: 1, WholesalePrice: 3, RetailPrice: 4,
				Inputs: []RecipeInput{{Item: "wheat", Qty: 5}}},
			"wheat": {OutputItem: "wheat", OutputQty: 1, RateQty: 3, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 2},
			"milk":  {OutputItem: "milk", OutputQty: 4, RateQty: 4, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 2},
			"nail":  {OutputItem: "nail", OutputQty: 4, RateQty: 4, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 2},
			"stew": {OutputItem: "stew", OutputQty: 6, RateQty: 30, RatePerHours: 6, WholesalePrice: 3, RetailPrice: 5,
				Inputs: []RecipeInput{{Item: "meat", Qty: 2}, {Item: "water", Qty: 3}}},
			"meat": {OutputItem: "meat", OutputQty: 1, RateQty: 1, RatePerHours: 1, WholesalePrice: 2, RetailPrice: 4},
			"ale":  {OutputItem: "ale", OutputQty: 1, RateQty: 4, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 1},
		},
		ItemKinds: map[ItemKind]*ItemKindDef{
			"wheat":    {Name: "wheat", DisplayLabel: "Wheat", DisplayLabelSingular: "sheaf of wheat", DisplayLabelPlural: "sheaves of wheat", Capabilities: []string{"portable"}},
			"iron":     {Name: "iron", DisplayLabel: "Iron", DisplayLabelSingular: "bar of iron", DisplayLabelPlural: "bars of iron", Capabilities: []string{"portable"}},
			"milk":     {Name: "milk", DisplayLabel: "Milk", Capabilities: []string{"portable"}},
			"flour":    {Name: "flour", DisplayLabel: "Flour", Capabilities: []string{"portable"}},
			"nail":     {Name: "nail", DisplayLabel: "Nail", Capabilities: []string{"portable"}},
			"sage":     {Name: "sage", DisplayLabel: "Sage", Capabilities: []string{"portable"}},
			"salt":     {Name: "salt", DisplayLabel: "Salt", Capabilities: []string{"portable"}},
			"water":    {Name: "water", DisplayLabel: "Water", Capabilities: []string{"portable"}},
			"meat":     {Name: "meat", DisplayLabel: "Meat", DisplayLabelSingular: "cut of meat", DisplayLabelPlural: "cuts of meat", Capabilities: []string{"portable"}, Category: ItemCategoryFood},
			"stew":     {Name: "stew", DisplayLabel: "Stew"},
			"coat":     {Name: "coat", DisplayLabel: "Coat", Capabilities: []string{"portable"}, WearMinutes: 600},
			"firewood": {Name: "firewood", DisplayLabel: "Firewood", Capabilities: []string{"portable"}, Satisfies: []ItemSatisfaction{{Attribute: "cold", Immediate: 5}}},
			"ale":      {Name: "ale", DisplayLabel: "Ale", Capabilities: []string{"portable"}, Satisfies: []ItemSatisfaction{{Attribute: "thirst", Immediate: 4}}},
		},
		Settings: WorldSettings{CarterDays: 3, RestockReorderPct: 25},
	}
	// Iron has no recipe: it is priced off what the village has paid for it.
	w.PriceBook = map[PriceBookKey]*RingBuffer[PriceObservation]{}
	buf := NewRingBuffer[PriceObservation](PriceBookRingCapacity)
	buf.Push(PriceObservation{BuyerID: "josiah", Amount: 30, Qty: 10, Consumers: 1, At: carterNow.Add(-5 * 24 * time.Hour)})
	w.PriceBook[PriceBookKey{SellerID: "vstr-factor", Item: "iron"}] = buf
	return w
}

func TestResidueSpare(t *testing.T) {
	w := carterWorld()
	liz := w.Actors["liz"]
	for _, tc := range []struct {
		item ItemKind
		want int
		why  string
	}{
		{"wheat", 25, "no line for it — pure residue"},
		{"iron", 8, "no line for it — pure residue"},
		{"milk", 0, "her own produce line"},
		{"sage", 0, "her buy line — makings, not residue"},
		{"coat", 0, "the one garment she wears is spoken for"},
		{"firewood", 20, "a consumable keeps the larder keep-back"},
		{"meat", 0, "two cuts of food are under the larder keep-back"},
	} {
		if got := residueSpare(w, liz, tc.item); got != tc.want {
			t.Errorf("residueSpare(liz, %s) = %d, want %d (%s)", tc.item, got, tc.want, tc.why)
		}
	}
	// The distributor's shelf is the market, never residue; a visitor's pack is
	// not a shelf at all.
	if lots := residueLots(w, 4, carterNow); len(lots) == 0 {
		t.Fatal("no residue lots in the live shape")
	} else {
		for _, l := range lots {
			if l.holder.ID == "josiah" {
				t.Errorf("the distributor's %s counted as residue", l.item)
			}
		}
	}
	john := w.Actors["john"]
	if got := residueSpare(w, john, "ale"); got != 30 {
		t.Errorf("residueSpare(john, ale) = %d, want 30 — 40 tankards less the keep-back; a keeper's forty tankards are not a larder", got)
	}
}

func TestCarterUnitPrice(t *testing.T) {
	w := carterWorld()
	if got := carterUnitPrice(w, "wheat", carterNow); got != 1 {
		t.Errorf("wheat = %d, want the recipe's wholesale 1", got)
	}
	if got := carterUnitPrice(w, "iron", carterNow); got != 3 {
		t.Errorf("iron = %d, want 3 — 30 coin over 10 bars in the price book", got)
	}
	if got := carterUnitPrice(w, "salt", carterNow); got != 0 {
		t.Errorf("salt = %d, want 0 — no recipe, no observation: unpriced, never residue", got)
	}
	// An observation older than the window is not the going rate.
	if got := carterUnitPrice(w, "iron", carterNow.Add(60*24*time.Hour)); got != 0 {
		t.Errorf("iron two months on = %d, want 0", got)
	}
}

func TestPlanCarterRoute(t *testing.T) {
	w := carterWorld()
	legs := planCarterRoute(w, 100, 4, false, carterNow)
	// Joseph (wheat, a production input) and Ezekiel (iron, a production
	// input? no — a plain buy line) both have coin; John's meat line has a
	// standing-shortage-free keeper with 3 coin who cannot pay for 2 meat at 3
	// each, and the meat is under the larder keep-back anyway. Band shut: no
	// export legs.
	want := []CarterLeg{
		{Buy: true, Good: "wheat", Qty: 25, Counterparty: "farm", Keeper: "liz", Unit: 1},
		{Good: "wheat", Qty: 25, Counterparty: "mill", Keeper: "joseph", Unit: 1},
		{Buy: true, Good: "iron", Qty: 6, Counterparty: "farm", Keeper: "liz", Unit: 3},
		{Good: "iron", Qty: 6, Counterparty: "smithy", Keeper: "ezekiel", Unit: 3},
	}
	if len(legs) != len(want) {
		t.Fatalf("legs = %+v, want %+v", legs, want)
	}
	for i := range want {
		if legs[i] != want[i] {
			t.Errorf("leg %d = %+v, want %+v", i, legs[i], want[i])
		}
	}
	if got := carterRouteBuyTotal(legs); got != 25+18 {
		t.Errorf("buy total = %d, want 43", got)
	}
	if legs[1].Price() != 50 || legs[3].Price() != 24 {
		t.Errorf("asks = %d / %d, want 50 / 24 (the going rate plus one a unit)", legs[1].Price(), legs[3].Price())
	}

	// A buyer who cannot pay coin gets no matched leg — unless his shortage stands.
	w.Actors["joseph"].Coins = 0
	legs = planCarterRoute(w, 100, 4, false, carterNow)
	for _, l := range legs {
		if l.Good == "wheat" {
			t.Errorf("a broke miller was planned a wheat leg: %+v", l)
		}
	}
	w.Environment.InputShortages = []InputShortage{{KeeperID: "joseph", Item: "wheat", Days: 3}}
	legs = planCarterRoute(w, 100, 4, false, carterNow)
	if len(legs) < 2 || legs[0].Good != "wheat" || legs[1].Keeper != "joseph" {
		t.Errorf("a standing shortage was not served first: %+v", legs)
	}
	if !carterRouteHasShortage(w, legs) {
		t.Error("carterRouteHasShortage = false on a route that serves the shortage")
	}

	// The band open: what no buyer wants still goes, by value, within the purse.
	w = carterWorld()
	w.Actors["joseph"].Coins = 0
	w.Actors["ezekiel"].Coins = 0
	legs = planCarterRoute(w, 60, 4, true, carterNow)
	if len(legs) == 0 {
		t.Fatal("band open, no buyers: want export legs")
	}
	buyTotal := 0
	for _, l := range legs {
		if !l.Buy {
			t.Errorf("export run planned a sell leg: %+v", l)
		}
		buyTotal += l.Price()
	}
	if buyTotal > 60-carterTravelReserve {
		t.Errorf("export legs come to %d, over the %d the purse allows", buyTotal, 60-carterTravelReserve)
	}
	if legs[0].Good != "ale" || legs[0].Qty != 30 || legs[0].Keeper != "john" {
		t.Errorf("the first export leg = %+v, want John's 30 spare ale, the most valuable lot", legs[0])
	}
	// The purse bounds the route: a small purse buys less.
	legs = planCarterRoute(w, 30, 4, true, carterNow)
	if got := carterRouteBuyTotal(legs); got > 10 {
		t.Errorf("30-coin purse bought %d of residue, want at most 10", got)
	}
}

func TestDueCarter(t *testing.T) {
	w := carterWorld()
	legs, purse, ok := dueCarter(w, carterNow)
	if !ok || len(legs) != 4 || purse != 43+carterTravelReserve {
		t.Fatalf("dueCarter = %v legs=%d purse=%d, want a 4-leg route with 63 coin", ok, len(legs), purse)
	}
	// Off switch.
	w.Settings.CarterDays = 0
	if _, _, ok := dueCarter(w, carterNow); ok {
		t.Error("CarterDays 0: a carter was due")
	}
	// Cooldown holds a matched run back, not a shortage run.
	w.Settings.CarterDays = 3
	w.Environment.LastCarterAt = carterNow.Add(-24 * time.Hour)
	if _, _, ok := dueCarter(w, carterNow); ok {
		t.Error("a day into the cooldown: a carter was due")
	}
	w.Environment.InputShortages = []InputShortage{{KeeperID: "joseph", Item: "wheat", Days: 3}}
	if _, _, ok := dueCarter(w, carterNow); !ok {
		t.Error("a standing shortage the village can cover waited on the cooldown")
	}
	w.Environment.InputShortages = nil
	w.Environment.LastCarterAt = carterNow.Add(-4 * 24 * time.Hour)
	if _, _, ok := dueCarter(w, carterNow); !ok {
		t.Error("cooldown over: no carter")
	}
	// The first leg's holder must be at her post.
	w.Actors["liz"].State = StateSleeping
	if _, _, ok := dueCarter(w, carterNow); ok {
		t.Error("the first holder abed: a carter was sent anyway")
	}
	w.Actors["liz"].State = StateIdle
	// No buyer and the band shut (unconfigured, or resident coin over it):
	// nothing brings him. Band open and residue worth the spawn threshold: he
	// comes on his own.
	w.Actors["joseph"].Coins = 0
	w.Actors["ezekiel"].Coins = 0
	if _, _, ok := dueCarter(w, carterNow); ok {
		t.Error("no buyer, band unconfigured: a carter was due")
	}
	w.Settings.VisitorCoinBandHigh = 1300
	w.Actors["liz"].Coins = 2000 // resident coin over the band
	if _, _, ok := dueCarter(w, carterNow); ok {
		t.Error("no buyer, band shut: a carter was due")
	}
	w.Actors["liz"].Coins = 1
	if legs, _, ok := dueCarter(w, carterNow); !ok {
		t.Error("no buyer, band open, 99 coin of residue: no carter")
	} else {
		for _, l := range legs {
			if !l.Buy {
				t.Errorf("export run has a sell leg: %+v", l)
			}
		}
	}
	w.Settings.CarterResidueSpawnCoins = 200
	if _, _, ok := dueCarter(w, carterNow); ok {
		t.Error("residue under the spawn threshold brought a carter")
	}
}

// TestPeddlerNeverWaitsOnACarterWhoCannotCome — the peddler's own due gate
// does not consult the carter at all (code_review): the supersede is the spawn
// ordering in dispatchVisitorSpawn, so a shortage whose residue sits on a shut
// shelf still brings the peddler.
func TestPeddlerNeverWaitsOnACarterWhoCannotCome(t *testing.T) {
	w := carterWorld()
	s := InputShortage{KeeperID: "joseph", Item: "wheat", Days: 3}
	w.Settings.ShortagePeddlerDays = 3
	w.Environment.InputShortages = []InputShortage{s}
	if _, _, ok := dueShortagePeddler(w, carterNow); !ok {
		t.Error("the peddler was not due for a standing shortage")
	}
	w.Actors["liz"].State = StateSleeping // the holder abed: no carter can make the run
	if _, _, ok := dueCarter(w, carterNow); ok {
		t.Error("a carter was due with the holder abed")
	}
	if _, _, ok := dueShortagePeddler(w, carterNow); !ok {
		t.Error("the peddler was held back although no carter can come")
	}
}

// TestSettleCarterBuyLegRejectsBadLeg — an out-of-band leg with no price or no
// quantity moves nothing (code_review): no goods, no coin, no log row.
func TestSettleCarterBuyLegRejectsBadLeg(t *testing.T) {
	for _, bad := range []CarterLeg{
		{Buy: true, Good: "wheat", Qty: 25, Counterparty: "farm", Keeper: "liz", Unit: 0},
		{Buy: true, Good: "wheat", Qty: 0, Counterparty: "farm", Keeper: "liz", Unit: 1},
	} {
		w := carterWorld()
		tr := &TradeErrand{Direction: TradeDirectionSell, Carter: true, Legs: []CarterLeg{bad}}
		carter := &Actor{ID: "vstr-cart", DisplayName: "Asa Larkin the carter", Kind: KindNPCShared, State: StateIdle,
			InsideStructureID: "farm", Coins: 63, Inventory: map[ItemKind]int{}, VisitorState: &VisitorState{Trade: tr, SpendBudget: 63}}
		projectCarterLeg(tr, carter.Inventory)
		w.Actors[carter.ID] = carter
		advanceCarter(w, carter, carterNow)
		if carter.Inventory["wheat"] != 0 || w.Actors["liz"].Inventory["wheat"] != 25 || carter.Coins != 63 || w.Actors["liz"].Coins != 1 || len(w.ActionLog) != 0 {
			t.Errorf("leg %+v moved something: carter %v/%d coin, liz %v/%d coin, log %d", bad, carter.Inventory, carter.Coins, w.Actors["liz"].Inventory, w.Actors["liz"].Coins, len(w.ActionLog))
		}
	}
}

// TestProjectCarterLegSkipsBadLegs — projection is the one place every route
// passes through, so a malformed leg of either kind is marked done there and
// never walked to or advertised (code_review): a zero-unit sell leg does not
// become the current leg, and a route of nothing but bad legs settles.
func TestProjectCarterLegSkipsBadLegs(t *testing.T) {
	tr := &TradeErrand{Direction: TradeDirectionSell, Carter: true, Legs: []CarterLeg{
		{Good: "wheat", Qty: 25, Counterparty: "mill", Keeper: "joseph", Unit: 0},
		{Buy: true, Good: "iron", Qty: 6, Counterparty: "farm", Keeper: "liz", Unit: 3},
	}}
	projectCarterLeg(tr, map[ItemKind]int{"wheat": 25})
	if !tr.Legs[0].Done || tr.Good != "iron" || tr.ShipmentQty != 0 || tr.Settled {
		t.Errorf("after projection: legs=%+v good=%s shipment=%d settled=%v, want the zero-unit sell skipped and the iron buy current", tr.Legs, tr.Good, tr.ShipmentQty, tr.Settled)
	}
	tr = &TradeErrand{Direction: TradeDirectionSell, Carter: true, Legs: []CarterLeg{
		{Good: "wheat", Qty: 0, Counterparty: "mill", Keeper: "joseph", Unit: 1},
	}}
	projectCarterLeg(tr, map[ItemKind]int{"wheat": 25})
	if !tr.Settled || tr.CarterLeg() != nil {
		t.Errorf("a route of one bad leg: settled=%v leg=%+v, want settled with no leg", tr.Settled, tr.CarterLeg())
	}
}

// TestCarterPartialSaleHoldsTheLeg pins the decision (code_review): a keeper
// who takes less than three quarters of the lot leaves the carter on that leg —
// the peddler's own rule — and the unsold balance stays in his pack to leave
// with him at daybreak. No decline protocol exists in the commerce path, so the
// engine cannot tell "still considering" from "refused the rest".
func TestCarterPartialSaleHoldsTheLeg(t *testing.T) {
	w := carterWorld()
	carter, tr := carterOnRoute(w)
	carter.InsideStructureID = "farm"
	advanceCarter(w, carter, carterNow) // the wheat buy settles; the sell at the mill is projected
	carter.InsideStructureID = "mill"
	if err := transferItem(w, carter, w.Actors["joseph"], "wheat", 10); err != nil {
		t.Fatal(err)
	}
	advanceCarter(w, carter, carterNow)
	if tr.Legs[1].Done || tr.Counterparty != "mill" || tr.Delivered != 10 {
		t.Errorf("after 10 of 25 taken: leg done=%v counterparty=%s delivered=%d, want the sell leg still current", tr.Legs[1].Done, tr.Counterparty, tr.Delivered)
	}
	if err := transferItem(w, carter, w.Actors["joseph"], "wheat", 9); err != nil {
		t.Fatal(err)
	}
	advanceCarter(w, carter, carterNow)
	if !tr.Legs[1].Done || carter.Inventory["wheat"] != 6 {
		t.Errorf("after 19 of 25 taken: leg done=%v carter wheat=%d, want done (three quarters landed) with 6 left in the pack", tr.Legs[1].Done, carter.Inventory["wheat"])
	}
}

// carterOnRoute puts a carter at the farm with the live route projected.
func carterOnRoute(w *World) (*Actor, *TradeErrand) {
	legs := planCarterRoute(w, 100, 4, false, carterNow)
	tr := &TradeErrand{Direction: TradeDirectionSell, Carter: true, Legs: legs}
	carter := &Actor{ID: "vstr-cart", DisplayName: "Asa Larkin the carter", Kind: KindNPCShared, State: StateIdle,
		Coins: 63, Inventory: map[ItemKind]int{}, VisitorState: &VisitorState{Trade: tr, SpendBudget: 63}}
	projectCarterLeg(tr, carter.Inventory)
	w.Actors[carter.ID] = carter
	return carter, tr
}

func TestAdvanceCarter(t *testing.T) {
	w := carterWorld()
	carter, tr := carterOnRoute(w)
	if tr.Good != "wheat" || tr.Counterparty != "farm" || tr.Keeper != "liz" || tr.ShipmentQty != 0 || !tr.CarterBuying() {
		t.Fatalf("projection = %+v, want the wheat buy at the farm", tr)
	}
	// On the road: nothing settles.
	advanceCarter(w, carter, carterNow)
	if tr.Legs[0].Done || len(carter.Inventory) != 0 {
		t.Fatal("a buy leg settled before he arrived")
	}
	// At the farm with Elizabeth: the buy settles mechanically, at 1 a sheaf.
	carter.InsideStructureID = "farm"
	advanceCarter(w, carter, carterNow)
	liz := w.Actors["liz"]
	if !tr.Legs[0].Done || carter.Inventory["wheat"] != 25 || liz.Inventory["wheat"] != 0 {
		t.Fatalf("buy leg: done=%v carter wheat=%d liz wheat=%d, want done/25/0", tr.Legs[0].Done, carter.Inventory["wheat"], liz.Inventory["wheat"])
	}
	if carter.Coins != 38 || liz.Coins != 26 {
		t.Errorf("coin: carter %d liz %d, want 38 / 26", carter.Coins, liz.Coins)
	}
	if len(w.ActionLog) != 1 || w.ActionLog[0].ActionType != ActionTypePaid || w.ActionLog[0].Amount != 25 ||
		w.ActionLog[0].CounterpartyName != "Elizabeth Ellis" || w.ActionLog[0].Text != "25x wheat" {
		t.Errorf("action log = %+v, want one paid row, 25 coin to Elizabeth Ellis for 25x wheat", w.ActionLog)
	}
	if carter.VisitorState.SpendBudget != 38 {
		t.Errorf("spend budget = %d after the buy, want 38 — the mechanical buy draws the LLM-644 budget like every other door", carter.VisitorState.SpendBudget)
	}
	if rec := w.coinPairRecord("liz", "vstr-cart"); rec == nil || len(rec.Received) != 1 || rec.Received[0].Kind != CoinPaymentForGoods {
		t.Errorf("coin record for liz<-carter = %+v, want one ForGoods receipt", rec)
	}
	// The next leg is projected: the sell at the mill, shipment 25.
	if tr.Good != "wheat" || tr.Counterparty != "mill" || tr.Keeper != "joseph" || tr.ShipmentQty != 25 || tr.Delivered != 0 || tr.CarterBuying() {
		t.Fatalf("projection after the buy = %+v, want the wheat sell at the mill", tr)
	}
	// The keeper takes the lot: the sell settles and the iron buy is projected.
	carter.InsideStructureID = "mill"
	if err := transferItem(w, carter, w.Actors["joseph"], "wheat", 25); err != nil {
		t.Fatal(err)
	}
	advanceCarter(w, carter, carterNow)
	if !tr.Legs[1].Done || tr.Good != "iron" || tr.Counterparty != "farm" || !tr.CarterBuying() {
		t.Fatalf("projection after the sell = %+v, want the iron buy at the farm", tr)
	}
	// Back at the farm: the iron leg settles, then at the smithy Ezekiel takes
	// most of it and the route is done — the errand settles itself.
	carter.InsideStructureID = "farm"
	advanceCarter(w, carter, carterNow)
	if carter.Inventory["iron"] != 6 || carter.Coins != 38-18 {
		t.Fatalf("iron buy: carter iron=%d coin=%d, want 6 / 20", carter.Inventory["iron"], carter.Coins)
	}
	carter.InsideStructureID = "smithy"
	if err := transferItem(w, carter, w.Actors["ezekiel"], "iron", 5); err != nil {
		t.Fatal(err)
	}
	advanceCarter(w, carter, carterNow)
	if !tr.Settled || tr.CarterLeg() != nil {
		t.Errorf("route done: settled=%v leg=%+v, want settled with no leg", tr.Settled, tr.CarterLeg())
	}
	if carter.Inventory["iron"] != 1 {
		t.Errorf("the unsold bar = %d, want 1 — it leaves with him", carter.Inventory["iron"])
	}
}

func TestSettleCarterBuyLegReadsTheShelfLive(t *testing.T) {
	w := carterWorld()
	carter, tr := carterOnRoute(w)
	carter.InsideStructureID = "farm"
	// The residue moved since the route was planned: he buys what is there.
	w.Actors["liz"].Inventory["wheat"] = 10
	advanceCarter(w, carter, carterNow)
	if carter.Inventory["wheat"] != 10 || carter.Coins != 53 || tr.Legs[0].Qty != 10 {
		t.Errorf("after a shrunken lot: wheat=%d coin=%d leg qty=%d, want 10 / 53 / 10", carter.Inventory["wheat"], carter.Coins, tr.Legs[0].Qty)
	}
	if tr.ShipmentQty != 10 {
		t.Errorf("the sell leg's shipment = %d, want 10 — sized to what he holds", tr.ShipmentQty)
	}
	// Gone entirely: the stop is over, the paired sell leg is skipped, and the
	// route moves on to the iron.
	w = carterWorld()
	carter, tr = carterOnRoute(w)
	carter.InsideStructureID = "farm"
	delete(w.Actors["liz"].Inventory, "wheat")
	advanceCarter(w, carter, carterNow)
	if !tr.Legs[0].Done || !tr.Legs[1].Done || tr.Good != "iron" || len(w.ActionLog) != 0 {
		t.Errorf("empty shelf: legs=%+v good=%s log=%d, want both wheat legs done, iron projected, nothing logged", tr.Legs, tr.Good, len(w.ActionLog))
	}
	// The holder away from her post: he waits.
	w = carterWorld()
	carter, tr = carterOnRoute(w)
	carter.InsideStructureID = "farm"
	w.Actors["liz"].InsideStructureID = ""
	w.Actors["liz"].Pos = GridPoint{X: 500, Y: 500}
	advanceCarter(w, carter, carterNow)
	if tr.Legs[0].Done || carter.Inventory["wheat"] != 0 {
		t.Error("the holder away: the buy settled anyway")
	}
}

func TestCarterPersonaAndRoute(t *testing.T) {
	w := carterWorld()
	tr := &TradeErrand{Direction: TradeDirectionSell, Carter: true}
	if got := visitorMerchantLabel(w, tr); got != "carter" {
		t.Errorf("label = %q, want carter", got)
	}
	vs := &VisitorState{Trade: tr}
	if got := visitorSpriteName(vs); got == FactorSpriteName || got == "" {
		t.Errorf("sprite = %q, want a trader-pool sprite, not the factor's", got)
	}
	tr.Good = "wheat"
	first := visitorSpriteName(vs)
	tr.Good = "iron"
	if second := visitorSpriteName(vs); second != first {
		t.Errorf("sprite changed with the leg's good (%q -> %q)", first, second)
	}
	legs := planCarterRoute(w, 100, 4, false, carterNow)
	desc := describeCarterRoute(w, legs)
	for _, want := range []string{"buy 25 wheat at Ellis Farm (Elizabeth Ellis) for 25", "sell 25 wheat at Mill (Joseph Scott) for 50", "sell 6 iron at Blacksmith (Ezekiel Crane) for 24"} {
		if !strings.Contains(desc, want) {
			t.Errorf("route description %q lacks %q", desc, want)
		}
	}
}

// The live first run: two iron lots for the smith became buy-sell-buy-sell, and
// on the second call at the smithy eight minutes after the first the carter
// read the business as done and walked off with four bars. A want is one call.
func TestPlanCarterRouteCallsOnEachBuyerOnce(t *testing.T) {
	w := carterWorld()
	// The iron sits on two shelves; the smith also lacks water for his nails.
	w.Actors["liz"].Inventory["iron"] = 2
	w.Actors["joseph"].Inventory["iron"] = 4
	w.Actors["joseph"].Inventory["water"] = 30
	w.Recipes["water"] = &ItemRecipe{OutputItem: "water", OutputQty: 1, RateQty: 1, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 1}
	ez := w.Actors["ezekiel"]
	ez.RestockPolicy.Restock = append(ez.RestockPolicy.Restock, RestockEntry{Item: "water", Source: RestockSourceBuy, Max: 10})

	legs := planCarterRoute(w, 100, 4, false, carterNow)
	var smithLegs []CarterLeg
	for i, l := range legs {
		if !l.Buy && l.Keeper == "ezekiel" {
			smithLegs = append(smithLegs, l)
			continue
		}
		// Once he has sold at the smithy, nothing on the route may take him away
		// and back: no buy for the smith after the smith's first sell leg.
		if l.Buy && len(smithLegs) > 0 && i+1 < len(legs) {
			for _, later := range legs[i+1:] {
				if !later.Buy && later.Keeper == "ezekiel" {
					t.Fatalf("the route returns to the smithy after leaving it: %+v", legs)
				}
			}
		}
	}
	if len(smithLegs) != 2 {
		t.Fatalf("smith sell legs = %+v, want one for iron and one for water (route %+v)", smithLegs, legs)
	}
	for _, l := range smithLegs {
		switch l.Good {
		case "iron":
			if l.Qty != 6 {
				t.Errorf("iron sell leg = %d bars, want the 6 off both shelves in one offer", l.Qty)
			}
		case "water":
		default:
			t.Errorf("unexpected smith sell leg %+v", l)
		}
	}
	ironBuys := 0
	for _, l := range legs {
		if l.Buy && l.Good == "iron" {
			ironBuys++
		}
	}
	if ironBuys != 2 {
		t.Errorf("iron buy legs = %d, want 2 (one per shelf): %+v", ironBuys, legs)
	}

	// The coin rule holds across the lots of one want: a smith with 8 coin can
	// pay for two bars at 4, not two from each shelf.
	ez.Coins = 8
	for _, l := range planCarterRoute(w, 100, 4, false, carterNow) {
		if !l.Buy && l.Good == "iron" && l.Qty != 2 {
			t.Errorf("iron sell leg = %d bars to a smith with 8 coin, want 2", l.Qty)
		}
	}
}
