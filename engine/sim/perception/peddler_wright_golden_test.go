package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// peddler_wright_golden_test.go — golden scenarios for a buy-line shortage
// peddler (LLM-657): a whetstone peddler sent to the wright. Lewis Walker owns
// and works his workshop with no businessowner attribute (the live shape), and
// no recipe of his takes a whetstone — his service consumes it — so the
// keeper's cue falls to its generic "makings your work has gone without" clause
// and the handoff otherwise reads as it does for the tavernkeeper's meat.

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "peddler_at_wright",
			summary: "LLM-657: a whetstone peddler stands in Lewis's Workshop with Lewis Walker, the wright he was " +
				"sent to — a keeper by ownership of his post, not by attribute. '## Your rounds' names him as the one " +
				"keeper and steers the offer with sell.",
			build: peddlerAtWrightScenario,
		},
		perceptionScenario{
			name: "wright_views_peddler",
			summary: "LLM-657: Lewis Walker's own view with the whetstone peddler co-present. '## A trader's come to " +
				"deal' says a peddler has brought whetstones — no recipe of his takes them, so the cue says his work " +
				"has gone without — lists the pack, and hands him the pay_with_item buy.",
			build: wrightViewsPeddlerScenario,
		},
	)
}

const (
	wrightScenarioShop   = sim.StructureID("workshop")
	wrightScenarioHuddle = sim.HuddleID("h2")
	wrightPeddlerID      = sim.ActorID("vstr-0000wped")
	wrightKeeperID       = sim.ActorID("lewis")
)

func wrightPeddlerActor() *sim.ActorSnapshot {
	return &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Eli Dunning the whetstone-peddler",
		State:             sim.StateIdle,
		Pos:               sim.TilePos{X: 80, Y: 80},
		InsideStructureID: wrightScenarioShop,
		CurrentHuddleID:   wrightScenarioHuddle,
		Coins:             34,
		Inventory:         map[sim.ItemKind]int{sim.WhetstoneKind: 2},
		Needs:             map[sim.NeedKey]int{},
		VisitorState: &sim.VisitorState{
			SpendBudget: 34,
			Archetype:   "whetstone-peddler",
			Origin:      "Rowley",
			Disposition: "plainspoken",
			Phase:       sim.VisitorPhaseMakingRounds,
			Trade: &sim.TradeErrand{Direction: sim.TradeDirectionSell, Good: sim.WhetstoneKind,
				Counterparty: wrightScenarioShop, Keeper: wrightKeeperID, ShipmentQty: 2, Peddler: true},
		},
	}
}

func wrightKeeper() *sim.ActorSnapshot {
	return &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Lewis Walker",
		Role:              "wright",
		State:             sim.StateIdle,
		Pos:               sim.TilePos{X: 81, Y: 80},
		WorkStructureID:   wrightScenarioShop,
		InsideStructureID: wrightScenarioShop,
		CurrentHuddleID:   wrightScenarioHuddle,
		Coins:             19,
		Inventory:         map[sim.ItemKind]int{"nail": 6, "hammer": 1},
		Needs:             map[sim.NeedKey]int{},
		// No BusinessownerState: he keeps the workshop as its owner (LLM-648).
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: sim.WhetstoneKind, Source: sim.RestockSourceBuy, Max: 4},
		}},
	}
}

func wrightPeddlerSnapshot() *sim.Snapshot {
	now := 960 // 16:00, the afternoon the peddler arrives in
	return &sim.Snapshot{
		LocalMinuteOfDay:             &now,
		NeedThresholds:               sim.NeedThresholds{},
		EquipmentServiceDueThreshold: 100,
		Actors:                       map[sim.ActorID]*sim.ActorSnapshot{wrightPeddlerID: wrightPeddlerActor(), wrightKeeperID: wrightKeeper()},
		Structures: map[sim.StructureID]*sim.Structure{
			wrightScenarioShop: plainStructure(wrightScenarioShop, "Lewis's Workshop"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(wrightScenarioShop): {ID: sim.VillageObjectID(wrightScenarioShop), Pos: sim.WorldPos{X: 640, Y: 640},
				Tags: []string{sim.TagBusiness, sim.TagWright}, OwnerActorID: wrightKeeperID},
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			wrightScenarioHuddle: {ID: wrightScenarioHuddle, Members: map[sim.ActorID]struct{}{wrightPeddlerID: {}, wrightKeeperID: {}}},
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			sim.WhetstoneKind:   {OutputItem: sim.WhetstoneKind, OutputQty: 1, RateQty: 1, RatePerHours: 1, WholesalePrice: 2, RetailPrice: 4, Inputs: []sim.RecipeInput{{Item: "iron", Qty: 1}}},
			"equipment_service": {WholesalePrice: 6, RetailPrice: 10},
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			sim.WhetstoneKind: {Name: sim.WhetstoneKind, DisplayLabel: "Whetstone", DisplayLabelSingular: "whetstone",
				DisplayLabelPlural: "whetstones", Capabilities: []string{"portable"}},
			"equipment_service": {Name: "equipment_service", DisplayLabel: "Equipment service",
				Capabilities: []string{"service", sim.CapabilityEquipmentService}},
			"iron":   {Name: "iron", DisplayLabel: "Iron", Capabilities: []string{"portable"}},
			"nail":   {Name: "nail", DisplayLabel: "Nail", DisplayLabelSingular: "nail", DisplayLabelPlural: "nails", Capabilities: []string{"portable"}},
			"hammer": {Name: "hammer", DisplayLabel: "Hammer", DisplayLabelSingular: "hammer", DisplayLabelPlural: "hammers", Capabilities: []string{"portable"}},
		},
	}
}

func peddlerAtWrightScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return wrightPeddlerSnapshot(), wrightPeddlerID, nil
}

func wrightViewsPeddlerScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return wrightPeddlerSnapshot(), wrightKeeperID, nil
}

// TestWrightPeddlerCuesCarryTheHandoff pins the load-bearing tokens beneath the
// wright goldens (LLM-657): the peddler is steered to sell to Lewis by name and
// exact kind, and Lewis — a keeper with no businessowner attribute — hears the
// trader's-come cue and is handed the buy. A businessowner gate creeping back
// into either side would silence one half of the deal.
func TestWrightPeddlerCuesCarryTheHandoff(t *testing.T) {
	peddler := renderScenario(perceptionScenario{build: peddlerAtWrightScenario})
	for _, want := range []string{
		"## Your rounds",
		"You're with Lewis Walker at Lewis's Workshop — the one keeper you came to deal with.",
		`call sell with item "whetstone"`,
		`target_buyer "Lewis Walker"`,
	} {
		if !strings.Contains(peddler, want) {
			t.Errorf("peddler's prompt lacks %q:\n%s", want, peddler)
		}
	}
	keeper := renderScenario(perceptionScenario{build: wrightViewsPeddlerScenario})
	for _, want := range []string{
		"## A trader's come to deal",
		"a peddler out of Rowley, has come to you with whetstones",
		"In his pack: 2 whetstones",
		`pay_with_item (seller "Eli Dunning the whetstone-peddler", item "whetstone"`,
	} {
		if !strings.Contains(keeper, want) {
			t.Errorf("wright's prompt lacks %q:\n%s", want, keeper)
		}
	}
	if strings.Contains(keeper, "the makings of your") {
		t.Errorf("wright's prompt claims a recipe takes the stone:\n%s", keeper)
	}
}
