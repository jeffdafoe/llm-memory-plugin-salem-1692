package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// speed_golden_test.go — golden scenario + the cross-scenario invariant for
// LLM-511 (a bar of iron halves shovel forge time). The shovel recipe carries a
// speed_input [{iron,1,200}]: a bar held at the START of a cycle is consumed to
// run the batch at 2x rate. Iron is a hand-authored buy entry (speed inputs,
// like boost inputs, are deliberately NOT derived into buy demand). When iron is
// low and buyable, the "## Keeping up production" speed line motivates the buy
// with the forgone quickness; the adjacent "## Restocking" line carries the
// where/how (the LLM-64 split). No rate number rides the scene (scenes, not
// stats) — "quick work" carries the felt speed.

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "smith_speeds_shovel_low_iron_vendor_stocked",
			summary: "LLM-511: an Ezekiel-shaped smith at his forge, shovel recipe carrying the iron SPEED input " +
				"(1 bar per cycle -> half the forge time), iron a hand-authored buy entry at 0 on hand, and the " +
				"distributor's store stocked with bars. Pins the speed motivate-line ('A measure of bar iron makes " +
				"each batch of Shovel quick work — shaped from what you have rather than made from scratch') alongside " +
				"the iron '## Restocking' buy line (the LLM-64 split: motivate here, where/how there), with NO rate " +
				"number in the scene. Cross-scenario guard: TestGoldensSpeedLineOnlyForSpeedBoostedRecipes.",
			build: smithSpeedsShovelLowIron,
		},
	)
}

// shovelSmith builds the Ezekiel-shaped subject: a blacksmith on shift inside his
// forge, producing shovels under the LLM-511 speed recipe, with the hand-authored
// `buy iron` entry (speed inputs are deliberately not derived — derived_demand.go)
// and ironOnHand bars in his pack.
func shovelSmith(ironOnHand int) *sim.ActorSnapshot {
	start, end := 360, 1080 // 06:00–18:00
	inv := map[sim.ItemKind]int{"shovel": 2}
	if ironOnHand > 0 {
		inv["iron"] = ironOnHand
	}
	return &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Ezekiel Crane",
		Role:              "blacksmith",
		State:             sim.StateIdle,
		WorkStructureID:   "crane_forge",
		InsideStructureID: "crane_forge",
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             20,
		Inventory:         inv,
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "shovel", Source: sim.RestockSourceProduce, Max: 20},
			{Item: "iron", Source: sim.RestockSourceBuy, Max: 6},
		}},
	}
}

// smithSpeedsShovelLowIron assembles the world: the smith at his forge with zero
// iron, and the distributor keeper at the distributor-tagged General Store
// holding bars (the actionable buy path the speed line is gated on). The shovel
// recipe carries the LLM-511 shape: base 1 per 4h, iron speed input at 200.
func smithSpeedsShovelLowIron() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		smithID  = sim.ActorID("ezekiel")
		josiahID = sim.ActorID("josiah")
		forge    = sim.StructureID("crane_forge")
	)
	now := 600 // 10:00 — on shift
	josiah := distributorKeeper(sim.TilePos{X: 41, Y: 40}, "")
	josiah.Inventory = map[sim.ItemKind]int{"iron": 6}
	vobjs, structs := distributorObjects()
	vobjs[sim.VillageObjectID(forge)] = &sim.VillageObject{ID: sim.VillageObjectID(forge), Pos: sim.WorldPos{X: 640, Y: 640}}
	structs[forge] = plainStructure(forge, "Crane Forge")
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{smithID: shovelSmith(0), josiahID: josiah},
		Structures:       structs,
		VillageObjects:   vobjs,
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"shovel": {Name: "shovel", DisplayLabel: "Shovel", DisplayLabelSingular: "shovel", DisplayLabelPlural: "shovels", Category: sim.ItemCategory("tool")},
			"iron":   {Name: "iron", DisplayLabel: "bar iron", DisplayLabelSingular: "bar of iron", DisplayLabelPlural: "bars of iron", Category: sim.ItemCategoryMaterial},
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"shovel": {
				OutputItem: "shovel", OutputQty: 1, RateQty: 1, RatePerHours: 4,
				SpeedInputs:    []sim.SpeedInput{{Item: "iron", Qty: 1, RatePct: 200}},
				WholesalePrice: 6, RetailPrice: 12,
			},
			"iron": {
				OutputItem: "iron", OutputQty: 1, RateQty: 1, RatePerHours: 1,
				WholesalePrice: 2, RetailPrice: 3,
			},
		},
		RestockReorderPct: 25,
	}
	return snap, smithID, nil
}

// TestGoldensSpeedLineOnlyForSpeedBoostedRecipes is the LLM-511 cross-scenario
// invariant: the speed motivate-line ("quick work") renders in EXACTLY the
// scenario whose subject produces a good whose recipe defines a speed input that
// is a low bought entry, and nowhere else in the matrix. A speed line leaking
// elsewhere would mean the gate regressed to reading required or boost inputs.
func TestGoldensSpeedLineOnlyForSpeedBoostedRecipes(t *testing.T) {
	const marker = "quick work"
	for _, sc := range perceptionScenarios {
		sc := sc
		got := renderScenario(sc)
		want := sc.name == "smith_speeds_shovel_low_iron_vendor_stocked"
		if has := strings.Contains(got, marker); has != want {
			t.Errorf("scenario %q: speed line present=%v, want %v", sc.name, has, want)
		}
	}
}
