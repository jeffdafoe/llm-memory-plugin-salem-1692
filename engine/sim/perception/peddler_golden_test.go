package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// peddler_golden_test.go — golden scenarios for the shortage peddler (LLM-656):
// the wholesale factor's sell errand with the pack narrowed to one missing good
// and the counterparty moved to the short keeper's own shop. His "## Your rounds"
// steer has him make the offer (sell), since the keeper's own cues have been
// silent on the good for days; the keeper's "## A trader's come to deal" names
// the good, what he makes with it, and the pay_with_item buy.

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "peddler_at_keepers_shop",
			summary: "LLM-656: a meat peddler stands in the Tavern with John Ellis, the keeper he was sent to. " +
				"'## Your rounds' names the one keeper and the good, and steers him to make the offer himself " +
				"with sell (item, qty, amount, target_buyer) — the keeper's cues cannot ask for a good nobody holds.",
			build: peddlerAtKeepersShopScenario,
		},
		perceptionScenario{
			name: "keeper_views_peddler",
			summary: "LLM-656: John Ellis's own view with the meat peddler co-present. '## A trader's come to " +
				"deal' says a peddler has brought meat, the makings of his stew (read off his recipe), lists the " +
				"pack, and hands him the pay_with_item buy with coin or goods in payment.",
			build: keeperViewsPeddlerScenario,
		},
	)
}

const (
	peddlerScenarioTavern = sim.StructureID("tavern")
	peddlerScenarioHuddle = sim.HuddleID("h1")
	peddlerID             = sim.ActorID("vstr-0000ped0")
	peddlerKeeperID       = sim.ActorID("john")
)

func peddlerActor() *sim.ActorSnapshot {
	return &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Asa Larkin the meat-peddler",
		State:             sim.StateIdle,
		Pos:               sim.TilePos{X: 40, Y: 40},
		InsideStructureID: peddlerScenarioTavern,
		CurrentHuddleID:   peddlerScenarioHuddle,
		Coins:             38,
		Inventory:         map[sim.ItemKind]int{"meat": 4},
		Needs:             map[sim.NeedKey]int{},
		VisitorState: &sim.VisitorState{
			SpendBudget: 38,
			Archetype:   "meat-peddler",
			Origin:      "Ipswich",
			Disposition: "plainspoken",
			Phase:       sim.VisitorPhaseMakingRounds,
			Trade: &sim.TradeErrand{Direction: sim.TradeDirectionSell, Good: "meat",
				Counterparty: peddlerScenarioTavern, ShipmentQty: 4, Peddler: true},
		},
	}
}

func peddlerKeeper() *sim.ActorSnapshot {
	return &sim.ActorSnapshot{
		Kind:               sim.KindNPCStateful,
		DisplayName:        "John Ellis",
		Role:               "tavernkeeper",
		State:              sim.StateIdle,
		Pos:                sim.TilePos{X: 41, Y: 40},
		WorkStructureID:    peddlerScenarioTavern,
		InsideStructureID:  peddlerScenarioTavern,
		HomeStructureID:    peddlerScenarioTavern,
		CurrentHuddleID:    peddlerScenarioHuddle,
		Coins:              5,
		Inventory:          map[sim.ItemKind]int{"meat": 1, "water": 7, "milk": 4, "carrots": 6, "ale": 10},
		Needs:              map[sim.NeedKey]int{},
		BusinessownerState: &sim.BusinessownerState{Flavor: "tavernkeeper"},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "stew", Source: sim.RestockSourceProduce, Max: 30},
			{Item: "ale", Source: sim.RestockSourceProduce, Max: 20},
			{Item: "meat", Source: sim.RestockSourceBuy, Max: 12},
		}},
	}
}

func peddlerSnapshot() *sim.Snapshot {
	now := 960 // 16:00, the afternoon the peddler arrives in
	return &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{peddlerID: peddlerActor(), peddlerKeeperID: peddlerKeeper()},
		Structures: map[sim.StructureID]*sim.Structure{
			peddlerScenarioTavern: plainStructure(peddlerScenarioTavern, "The Tavern"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(peddlerScenarioTavern): {ID: sim.VillageObjectID(peddlerScenarioTavern), Pos: sim.WorldPos{X: 320, Y: 320},
				Tags: []string{sim.TagBusiness, sim.VisitorTagTavern}, OwnerActorID: peddlerKeeperID},
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			peddlerScenarioHuddle: {ID: peddlerScenarioHuddle, Members: map[sim.ActorID]struct{}{peddlerID: {}, peddlerKeeperID: {}}},
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"stew": {OutputItem: "stew", OutputQty: 6, RateQty: 30, RatePerHours: 6, WholesalePrice: 3, RetailPrice: 5,
				Inputs: []sim.RecipeInput{{Item: "meat", Qty: 2}, {Item: "water", Qty: 3}, {Item: "milk", Qty: 2}, {Item: "carrots", Qty: 3}}},
			"ale":  {OutputItem: "ale", OutputQty: 1, RateQty: 4, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 1},
			"meat": {OutputItem: "meat", OutputQty: 1, RateQty: 1, RatePerHours: 1, WholesalePrice: 2, RetailPrice: 4},
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"meat": {Name: "meat", DisplayLabel: "Meat", DisplayLabelSingular: "cut of meat", DisplayLabelPlural: "cuts of meat",
				Capabilities: []string{"portable"}, Category: sim.ItemCategoryFood},
			"stew": {Name: "stew", DisplayLabel: "Stew", DisplayLabelSingular: "bowl of stew", DisplayLabelPlural: "bowls of stew",
				Category: sim.ItemCategoryFood},
			"ale": {Name: "ale", DisplayLabel: "Ale", DisplayLabelSingular: "tankard of ale", DisplayLabelPlural: "tankards of ale",
				Capabilities: []string{"portable"}},
			"water":   {Name: "water", DisplayLabel: "Water", Capabilities: []string{"portable"}},
			"milk":    {Name: "milk", DisplayLabel: "Milk", Capabilities: []string{"portable"}},
			"carrots": {Name: "carrots", DisplayLabel: "Carrots", Capabilities: []string{"portable"}, Category: sim.ItemCategoryFood},
		},
	}
}

func peddlerAtKeepersShopScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return peddlerSnapshot(), peddlerID, nil
}

func keeperViewsPeddlerScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return peddlerSnapshot(), peddlerKeeperID, nil
}

// TestPeddlerCuesCarryTheHandoff pins the load-bearing tokens beneath the goldens
// (LLM-656): the peddler is told to make the offer with sell against the keeper by
// name and the exact catalog kind, and the keeper is told what the good is for and
// handed pay_with_item against the peddler by name. Either side alone can close
// the deal; a golden churn must not silently drop the other.
func TestPeddlerCuesCarryTheHandoff(t *testing.T) {
	peddler := renderScenario(perceptionScenario{build: peddlerAtKeepersShopScenario})
	for _, want := range []string{
		"## Your rounds",
		"You're with John Ellis at The Tavern — the one keeper you came to deal with.",
		`call sell with item "meat"`,
		`target_buyer "John Ellis"`,
	} {
		if !strings.Contains(peddler, want) {
			t.Errorf("peddler's prompt lacks %q:\n%s", want, peddler)
		}
	}
	if strings.Contains(peddler, "cloth, iron, and salt") {
		t.Errorf("peddler's prompt carries the factor's bale wording:\n%s", peddler)
	}
	keeper := renderScenario(perceptionScenario{build: keeperViewsPeddlerScenario})
	for _, want := range []string{
		"## A trader's come to deal",
		"a peddler out of Ipswich, has come to you with meat — the makings of your stew",
		"In his pack: 4 cuts of meat",
		`pay_with_item (seller "Asa Larkin the meat-peddler", item "meat"`,
	} {
		if !strings.Contains(keeper, want) {
			t.Errorf("keeper's prompt lacks %q:\n%s", want, keeper)
		}
	}
	if strings.Contains(keeper, "a factor") {
		t.Errorf("keeper's prompt calls the peddler a factor:\n%s", keeper)
	}
}
