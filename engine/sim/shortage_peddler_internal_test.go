package sim

import (
	"math/rand"
	"testing"
	"time"
)

// shortage_peddler_internal_test.go — LLM-656. The sweep's eligibility rules, the
// day count across boundaries, the due/cooldown gate, and the pack sizing.

// shortageWorld is the live shape: John at the Tavern makes stew (2 meat a batch)
// and holds one cut; Elizabeth at the farm produces meat and holds none; Josiah
// at the distributor-tagged store holds none.
func shortageWorld() *World {
	return &World{
		Actors: map[ActorID]*Actor{
			"john": {
				ID: "john", DisplayName: "John Ellis", Kind: KindNPCStateful, State: StateIdle,
				WorkStructureID: "tavern", InsideStructureID: "tavern", Pos: GridPoint{X: 10, Y: 10},
				BusinessownerState: &BusinessownerState{Flavor: "tavernkeeper"},
				RestockPolicy: &RestockPolicy{Restock: []RestockEntry{
					{Item: "stew", Source: RestockSourceProduce, Max: 30},
					{Item: "meat", Source: RestockSourceBuy, Max: 12},
				}},
				Inventory: map[ItemKind]int{"meat": 1, "water": 10},
			},
			"liz": {
				ID: "liz", DisplayName: "Elizabeth Ellis", Kind: KindNPCShared, State: StateIdle,
				WorkStructureID: "farm", InsideStructureID: "farm",
				BusinessownerState: &BusinessownerState{Flavor: "dairykeeper"},
				RestockPolicy: &RestockPolicy{Restock: []RestockEntry{
					{Item: "meat", Source: RestockSourceProduce, Max: 20},
				}},
				Inventory: map[ItemKind]int{"milk": 7},
			},
			"josiah": {
				ID: "josiah", DisplayName: "Josiah Thorne", Kind: KindNPCStateful, State: StateIdle,
				WorkStructureID: "store", InsideStructureID: "store",
				BusinessownerState: &BusinessownerState{Flavor: "storekeeper"},
				RestockPolicy: &RestockPolicy{Restock: []RestockEntry{
					{Item: "meat", Source: RestockSourceBuy, Max: 6},
				}},
				Inventory: map[ItemKind]int{},
			},
		},
		VillageObjects: map[VillageObjectID]*VillageObject{
			"tavern": {ID: "tavern", Pos: WorldPos{X: 320, Y: 320}, Tags: []string{TagBusiness}},
			"farm":   {ID: "farm", Pos: WorldPos{X: 100, Y: 100}, Tags: []string{TagBusiness}},
			"store":  {ID: "store", Pos: WorldPos{X: 200, Y: 200}, Tags: []string{TagBusiness, TagDistributor}},
		},
		Structures: map[StructureID]*Structure{
			"tavern": {ID: "tavern", DisplayName: "The Tavern"},
			"farm":   {ID: "farm", DisplayName: "Ellis Farm"},
			"store":  {ID: "store", DisplayName: "General Store"},
		},
		Recipes: map[ItemKind]*ItemRecipe{
			"stew": {OutputItem: "stew", OutputQty: 6, RateQty: 30, RatePerHours: 6,
				Inputs: []RecipeInput{{Item: "meat", Qty: 2}, {Item: "water", Qty: 3}}},
			"meat": {OutputItem: "meat", OutputQty: 1, RateQty: 1, RatePerHours: 1},
		},
		ItemKinds: map[ItemKind]*ItemKindDef{
			"meat":  {Name: "meat", DisplayLabel: "Meat", DisplayLabelSingular: "cut of meat", DisplayLabelPlural: "cuts of meat", Capabilities: []string{"portable"}},
			"water": {Name: "water", DisplayLabel: "Water", Capabilities: []string{"portable"}},
			"stew":  {Name: "stew", DisplayLabel: "Stew", Capabilities: []string{}},
		},
		Settings: WorldSettings{ShortagePeddlerDays: 3, ShortagePeddlerBatches: 2},
	}
}

func TestInputShortagesNow(t *testing.T) {
	want := []shortageKey{{keeper: "john", item: "meat"}}
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
	check("live shape: one meat short, no supplier holds any", shortageWorld(), want)

	w := shortageWorld()
	w.Actors["liz"].Inventory["meat"] = 1
	check("the producer holds one", w, nil)

	w = shortageWorld()
	w.Actors["josiah"].Inventory["meat"] = 1
	check("the distributor holds one", w, nil)

	w = shortageWorld()
	w.Actors["john"].Inventory["meat"] = 2
	check("the keeper has a batch's worth", w, nil)

	w = shortageWorld()
	w.Actors["john"].Inventory["stew"] = 30
	check("the shelves are full — no batch to make", w, nil)

	w = shortageWorld()
	w.Actors["john"].RestockPolicy.Restock = append(w.Actors["john"].RestockPolicy.Restock,
		RestockEntry{Item: "meat", Source: RestockSourceForage})
	check("the keeper gathers it himself", w, nil)

	w = shortageWorld()
	w.ItemKinds["meat"].Capabilities = []string{"service"}
	check("a service is not a good a peddler can carry", w, nil)

	w = shortageWorld()
	w.Actors["john"].Inventory["water"] = 0
	check("water short too, but the keeper's own source gate is the supplier test: nobody produces water here", w,
		[]shortageKey{{keeper: "john", item: "meat"}, {keeper: "john", item: "water"}})

	// A fellow reseller's retail stock is not a village supply (LLM-252), and a
	// visitor standing in the village never is — a peddler is the answer to a
	// shortage, not evidence there is none.
	w = shortageWorld()
	w.Actors["reseller"] = &Actor{ID: "reseller", Kind: KindNPCShared, WorkStructureID: "farm",
		RestockPolicy: &RestockPolicy{Restock: []RestockEntry{{Item: "meat", Source: RestockSourceBuy, Max: 4}}},
		Inventory:     map[ItemKind]int{"meat": 3}}
	check("a reseller holding some", w, want)
	w = shortageWorld()
	w.Actors["vstr-1"] = &Actor{ID: "vstr-1", Kind: KindNPCShared, VisitorState: &VisitorState{},
		RestockPolicy: &RestockPolicy{Restock: []RestockEntry{{Item: "meat", Source: RestockSourceProduce}}},
		Inventory:     map[ItemKind]int{"meat": 4}}
	check("a visitor holding some", w, want)

	w = shortageWorld()
	w.Actors["john"].RestockPolicy = nil
	check("no policy, no produce entries", w, nil)
}

func TestSweepInputShortages(t *testing.T) {
	w := shortageWorld()
	day1 := time.Date(2026, 9, 12, 4, 0, 0, 0, time.UTC)
	day2 := day1.Add(24 * time.Hour)
	day3 := day2.Add(24 * time.Hour)

	got := sweepInputShortages(w, day1)
	if len(got) != 1 || got[0].KeeperID != "john" || got[0].Item != "meat" || got[0].Days != 1 || !got[0].LastSeenAt.Equal(day1) {
		t.Fatalf("day 1 sweep = %+v, want john/meat day 1", got)
	}
	// The same boundary again (a restart re-running the day) counts nothing.
	if got = sweepInputShortages(w, day1); got[0].Days != 1 {
		t.Errorf("same-boundary re-sweep: Days = %d, want 1", got[0].Days)
	}
	if got = sweepInputShortages(w, day2); got[0].Days != 2 || !got[0].LastSeenAt.Equal(day2) {
		t.Errorf("day 2 sweep = %+v, want Days 2", got)
	}
	// A peddler stamp rides through the sweep untouched.
	w.Environment.InputShortages[0].LastPeddlerAt = day2
	if got = sweepInputShortages(w, day3); got[0].Days != 3 || !got[0].LastPeddlerAt.Equal(day2) {
		t.Errorf("day 3 sweep = %+v, want Days 3 with the peddler stamp kept", got)
	}
	// Resolved (the producer made some): the entry is forgotten, and a recurrence
	// starts the count over.
	w.Actors["liz"].Inventory["meat"] = 2
	if got = sweepInputShortages(w, day3.Add(24*time.Hour)); len(got) != 0 {
		t.Errorf("resolved sweep = %+v, want none", got)
	}
	w.Actors["liz"].Inventory["meat"] = 0
	if got = sweepInputShortages(w, day3.Add(48*time.Hour)); len(got) != 1 || got[0].Days != 1 || !got[0].LastPeddlerAt.IsZero() {
		t.Errorf("recurrence sweep = %+v, want a fresh day-1 entry", got)
	}
}

func TestDueShortagePeddler(t *testing.T) {
	now := time.Date(2026, 9, 14, 20, 0, 0, 0, time.UTC)
	w := shortageWorld()
	w.Environment.InputShortages = []InputShortage{{KeeperID: "john", Item: "meat", Days: 2, LastSeenAt: now}}
	if _, _, ok := dueShortagePeddler(w, now); ok {
		t.Error("two days short: due, want not yet (threshold 3)")
	}
	w.Environment.InputShortages[0].Days = 3
	i, errand, ok := dueShortagePeddler(w, now)
	if !ok || i != 0 || errand == nil {
		t.Fatalf("three days short: due = (%d, %+v, %v), want index 0 with an errand", i, errand, ok)
	}
	if errand.Direction != TradeDirectionSell || errand.Good != "meat" || errand.Counterparty != "tavern" || !errand.Peddler {
		t.Errorf("errand = %+v, want a peddler sell of meat bound to the tavern", errand)
	}
	// Cooldown: a peddler sent an hour ago blocks; one sent four days ago does not.
	w.Environment.InputShortages[0].LastPeddlerAt = now.Add(-time.Hour)
	if _, _, ok := dueShortagePeddler(w, now); ok {
		t.Error("peddler an hour ago: due, want cooldown")
	}
	w.Environment.InputShortages[0].LastPeddlerAt = now.Add(-4 * 24 * time.Hour)
	if _, _, ok := dueShortagePeddler(w, now); !ok {
		t.Error("peddler four days ago: not due, want due")
	}
	// The keeper must be at his post — a shut shop is tried again next tick.
	w.Actors["john"].InsideStructureID = ""
	w.Actors["john"].Pos = GridPoint{X: 0, Y: 0}
	if _, _, ok := dueShortagePeddler(w, now); ok {
		t.Error("keeper away from his post: due, want not")
	}
	w.Actors["john"].InsideStructureID = "tavern"
	// The off-switch.
	w.Settings.ShortagePeddlerDays = 0
	if _, _, ok := dueShortagePeddler(w, now); ok {
		t.Error("days 0: due, want off")
	}
	w.Settings.ShortagePeddlerDays = 3
	// A keeper who is gone, or whose post is no longer structure-backed, is skipped.
	delete(w.Structures, "tavern")
	if _, _, ok := dueShortagePeddler(w, now); ok {
		t.Error("unbacked post: due, want skipped")
	}
}

func TestSeedPeddlerPack(t *testing.T) {
	w := shortageWorld()
	errand := &TradeErrand{Direction: TradeDirectionSell, Good: "meat", Counterparty: "tavern", Peddler: true}
	r := rand.New(rand.NewSource(1))
	pack, purse := seedPeddlerPack(r, w, errand, 2)
	if len(pack) != 1 || pack["meat"] != 4 {
		t.Errorf("pack = %v, want exactly 4 meat (2 batches × 2 a batch)", pack)
	}
	if purse < 30 || purse > 50 {
		t.Errorf("purse = %d, want a traveler's 30..50", purse)
	}
	// Batches clamp to at least one; an input no recipe of his names still ships one a batch.
	if pack, _ = seedPeddlerPack(r, w, errand, 0); pack["meat"] != 2 {
		t.Errorf("batches 0: pack = %v, want 2 (clamped to one batch)", pack)
	}
	other := &TradeErrand{Direction: TradeDirectionSell, Good: "salt", Counterparty: "tavern", Peddler: true}
	if pack, _ = seedPeddlerPack(r, w, other, 3); pack["salt"] != 3 {
		t.Errorf("unrecipe'd input: pack = %v, want 3 (one a batch)", pack)
	}
}

func TestPeddlerPersona(t *testing.T) {
	w := shortageWorld()
	errand := &TradeErrand{Direction: TradeDirectionSell, Good: "meat", Counterparty: "tavern", Peddler: true}
	if got := visitorMerchantLabel(w, errand); got != "meat-peddler" {
		t.Errorf("label = %q, want meat-peddler (the bare catalog label, not the count noun)", got)
	}
	// The factor keeps his own look; a peddler draws from the trader pool like a buyer.
	factor := &VisitorState{Trade: &TradeErrand{Direction: TradeDirectionSell, Good: "iron"}}
	if got := visitorSpriteName(factor); got != FactorSpriteName {
		t.Errorf("factor sprite = %q, want %q", got, FactorSpriteName)
	}
	peddler := &VisitorState{Trade: errand}
	if got := visitorSpriteName(peddler); got == FactorSpriteName || got == "" {
		t.Errorf("peddler sprite = %q, want a trader-pool sprite, not the factor's", got)
	}
	// The flag survives the visitor-state clone (the snapshot mirror).
	cp := cloneVisitorState(peddler)
	if cp.Trade == nil || !cp.Trade.Peddler {
		t.Errorf("clone dropped the Peddler flag: %+v", cp.Trade)
	}
}
