package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// carter_golden_test.go — golden scenarios for the carter (sim/carter.go), the
// inside-supply visitor: a route of legs through the village, buying residue
// from its holders for coin and selling it to keepers who hold a line for it.
// A buy leg is mechanical — the engine settles it as he stands with the holder,
// so both cues only say what is happening; a sell leg is the peddler's offer
// with the goods' provenance and his ask spelled out.

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "carter_at_holder",
			summary: "Carter: on a BUY leg, standing at Ellis Farm with Elizabeth Ellis, whose 25 wheat he has come for. " +
				"'## Your rounds' tells him the bargain settles itself — no haggling, no tool — and his commerce tools are withheld.",
			build: carterAtHolderScenario,
		},
		perceptionScenario{
			name: "holder_views_carter",
			summary: "Carter: Elizabeth Ellis's own view with the carter co-present on his buy leg. '## A trader's come to deal' " +
				"says he has come for the wheat she has no trade for and counts out the coin himself — nothing for her to do.",
			build: holderViewsCarterScenario,
		},
		perceptionScenario{
			name: "carter_at_keepers_shop",
			summary: "Carter: on a SELL leg at the Mill with Joseph Scott, carrying the 25 wheat off Elizabeth Ellis's shelves. " +
				"'## Your rounds' steers him to make the offer with sell (item, qty, his ask in amount, target_buyer).",
			build: carterAtKeepersShopScenario,
		},
		perceptionScenario{
			name: "keeper_views_carter",
			summary: "Carter: Joseph Scott's own view with the carter co-present on his sell leg. '## A trader's come to deal' " +
				"names the wheat, whose shelves it came off, what he makes with it, lists the lot, and hands him pay_with_item.",
			build: keeperViewsCarterScenario,
		},
	)
}

const (
	carterScenarioFarm   = sim.StructureID("farm")
	carterScenarioMill   = sim.StructureID("mill")
	carterScenarioHuddle = sim.HuddleID("h1")
	carterID             = sim.ActorID("vstr-0000cart")
	carterHolderID       = sim.ActorID("liz")
	carterKeeperID       = sim.ActorID("joseph")
)

func carterLegs(buyDone bool) []sim.CarterLeg {
	return []sim.CarterLeg{
		{Buy: true, Good: "wheat", Qty: 25, Counterparty: carterScenarioFarm, Keeper: carterHolderID, Unit: 1, Done: buyDone},
		{Good: "wheat", Qty: 25, Counterparty: carterScenarioMill, Keeper: carterKeeperID, Unit: 1},
	}
}

// carterActor is the carter on his buy leg (at the farm, empty-packed) or his
// sell leg (at the mill, carrying the wheat), the current leg projected onto
// the errand the way sim.projectCarterLeg does.
func carterActor(selling bool) *sim.ActorSnapshot {
	trade := &sim.TradeErrand{Direction: sim.TradeDirectionSell, Carter: true, Legs: carterLegs(selling),
		Good: "wheat", Counterparty: carterScenarioFarm, Keeper: carterHolderID}
	inside := carterScenarioFarm
	pos := sim.TilePos{X: 10, Y: 10}
	coins := 45
	inventory := map[sim.ItemKind]int{}
	if selling {
		trade.Counterparty, trade.Keeper, trade.ShipmentQty = carterScenarioMill, carterKeeperID, 25
		inside = carterScenarioMill
		pos = sim.TilePos{X: 30, Y: 30}
		coins = 20
		inventory = map[sim.ItemKind]int{"wheat": 25}
	}
	return &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Asa Larkin the carter",
		State:             sim.StateIdle,
		Pos:               pos,
		InsideStructureID: inside,
		CurrentHuddleID:   carterScenarioHuddle,
		Coins:             coins,
		Inventory:         inventory,
		Needs:             map[sim.NeedKey]int{},
		VisitorState: &sim.VisitorState{
			SpendBudget: 45,
			Archetype:   sim.CarterArchetype,
			Origin:      "Ipswich",
			Disposition: "plainspoken",
			Phase:       sim.VisitorPhaseMakingRounds,
			Trade:       trade,
		},
	}
}

func carterHolder(withWheat bool) *sim.ActorSnapshot {
	inventory := map[sim.ItemKind]int{"milk": 2, "cheese": 3}
	if withWheat {
		inventory["wheat"] = 25
	}
	return &sim.ActorSnapshot{
		Kind:               sim.KindNPCShared,
		DisplayName:        "Elizabeth Ellis",
		Role:               "dairykeeper",
		State:              sim.StateIdle,
		Pos:                sim.TilePos{X: 11, Y: 10},
		WorkStructureID:    carterScenarioFarm,
		InsideStructureID:  carterScenarioFarm,
		HomeStructureID:    carterScenarioFarm,
		CurrentHuddleID:    carterScenarioHuddle,
		Coins:              1,
		Inventory:          inventory,
		Needs:              map[sim.NeedKey]int{},
		BusinessownerState: &sim.BusinessownerState{Flavor: "dairykeeper"},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "milk", Source: sim.RestockSourceProduce, Max: 30},
			{Item: "cheese", Source: sim.RestockSourceProduce, Max: 15},
		}},
	}
}

func carterKeeper() *sim.ActorSnapshot {
	return &sim.ActorSnapshot{
		Kind:               sim.KindNPCShared,
		DisplayName:        "Joseph Scott",
		Role:               "miller",
		State:              sim.StateIdle,
		Pos:                sim.TilePos{X: 31, Y: 30},
		WorkStructureID:    carterScenarioMill,
		InsideStructureID:  carterScenarioMill,
		HomeStructureID:    carterScenarioMill,
		CurrentHuddleID:    carterScenarioHuddle,
		Coins:              700,
		Inventory:          map[sim.ItemKind]int{"wheat": 3, "flour": 10},
		Needs:              map[sim.NeedKey]int{},
		BusinessownerState: &sim.BusinessownerState{Flavor: "miller"},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "flour", Source: sim.RestockSourceProduce, Max: 20},
			{Item: "wheat", Source: sim.RestockSourceBuy, Max: 50},
		}},
	}
}

func carterSnapshot(selling bool) *sim.Snapshot {
	now := 960 // 16:00, the afternoon he arrives in
	actors := map[sim.ActorID]*sim.ActorSnapshot{
		carterID:       carterActor(selling),
		carterHolderID: carterHolder(!selling),
		carterKeeperID: carterKeeper(),
	}
	members := map[sim.ActorID]struct{}{carterID: {}, carterHolderID: {}}
	if selling {
		members = map[sim.ActorID]struct{}{carterID: {}, carterKeeperID: {}}
	}
	return &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           actors,
		Structures: map[sim.StructureID]*sim.Structure{
			carterScenarioFarm: plainStructure(carterScenarioFarm, "Ellis Farm"),
			carterScenarioMill: plainStructure(carterScenarioMill, "Mill"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(carterScenarioFarm): {ID: sim.VillageObjectID(carterScenarioFarm), Pos: sim.WorldPos{X: 160, Y: 160},
				Tags: []string{sim.TagBusiness, "farm", "wholesaler"}, OwnerActorID: carterHolderID},
			sim.VillageObjectID(carterScenarioMill): {ID: sim.VillageObjectID(carterScenarioMill), Pos: sim.WorldPos{X: 480, Y: 480},
				Tags: []string{sim.TagBusiness, "wholesaler"}, OwnerActorID: carterKeeperID},
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			carterScenarioHuddle: {ID: carterScenarioHuddle, Members: members},
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"flour": {OutputItem: "flour", OutputQty: 5, RateQty: 4, RatePerHours: 1, WholesalePrice: 3, RetailPrice: 4,
				Inputs: []sim.RecipeInput{{Item: "wheat", Qty: 5}}},
			"wheat":  {OutputItem: "wheat", OutputQty: 1, RateQty: 3, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 2},
			"milk":   {OutputItem: "milk", OutputQty: 4, RateQty: 4, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 2},
			"cheese": {OutputItem: "cheese", OutputQty: 1, RateQty: 2, RatePerHours: 1, WholesalePrice: 2, RetailPrice: 4, Inputs: []sim.RecipeInput{{Item: "milk", Qty: 3}}},
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"wheat":  {Name: "wheat", DisplayLabel: "Wheat", DisplayLabelSingular: "sheaf of wheat", DisplayLabelPlural: "sheaves of wheat", Capabilities: []string{"portable"}},
			"flour":  {Name: "flour", DisplayLabel: "Flour", DisplayLabelSingular: "sack of flour", DisplayLabelPlural: "sacks of flour", Capabilities: []string{"portable"}},
			"milk":   {Name: "milk", DisplayLabel: "Milk", Capabilities: []string{"portable"}},
			"cheese": {Name: "cheese", DisplayLabel: "Cheese", DisplayLabelSingular: "wheel of cheese", DisplayLabelPlural: "wheels of cheese", Capabilities: []string{"portable"}, Category: sim.ItemCategoryFood},
		},
	}
}

func carterAtHolderScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return carterSnapshot(false), carterID, nil
}

func holderViewsCarterScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return carterSnapshot(false), carterHolderID, nil
}

func carterAtKeepersShopScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return carterSnapshot(true), carterID, nil
}

func keeperViewsCarterScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return carterSnapshot(true), carterKeeperID, nil
}

// TestCarterCuesCarryTheHandoff pins the load-bearing tokens beneath the goldens:
// on the buy leg neither side is handed a tool (the engine settles it); on the
// sell leg the carter is told to make the offer with sell against the keeper by
// name, with his ask and the exact catalog kind, and the keeper is told whose
// shelves the goods came off, what they are for, and handed pay_with_item
// against the carter by name.
func TestCarterCuesCarryTheHandoff(t *testing.T) {
	buying := renderScenario(perceptionScenario{build: carterAtHolderScenario})
	for _, want := range []string{"come for the wheat they've no trade for", "no haggling wanted"} {
		if !strings.Contains(buying, want) {
			t.Errorf("carter's buy-leg rounds cue lacks %q:\n%s", want, buying)
		}
	}
	for _, never := range []string{"call sell", "pay_with_item"} {
		if strings.Contains(buying, never) {
			t.Errorf("carter's buy-leg rounds cue hands him a tool (%q) — the engine settles the buy:\n%s", never, buying)
		}
	}
	holder := renderScenario(perceptionScenario{build: holderViewsCarterScenario})
	for _, want := range []string{"Asa Larkin the carter, a carter out of Ipswich, has come for the wheat you've no trade for", "counts out the coin himself"} {
		if !strings.Contains(holder, want) {
			t.Errorf("holder's cue lacks %q:\n%s", want, holder)
		}
	}
	selling := renderScenario(perceptionScenario{build: carterAtKeepersShopScenario})
	for _, want := range []string{
		"You're with Joseph Scott at Mill", "off Elizabeth Ellis's shelves",
		`call sell with item "wheat", qty 25, about 50 coin for the lot in amount`, `target_buyer "Joseph Scott"`,
	} {
		if !strings.Contains(selling, want) {
			t.Errorf("carter's sell-leg rounds cue lacks %q:\n%s", want, selling)
		}
	}
	keeper := renderScenario(perceptionScenario{build: keeperViewsCarterScenario})
	for _, want := range []string{
		"Asa Larkin the carter, a carter out of Ipswich, has come to you with wheat off Elizabeth Ellis's shelves — the makings of your flour",
		"In his pack: 25 sheaves of wheat",
		`pay_with_item (seller "Asa Larkin the carter", item "wheat"`,
	} {
		if !strings.Contains(keeper, want) {
			t.Errorf("keeper's cue lacks %q:\n%s", want, keeper)
		}
	}
}
