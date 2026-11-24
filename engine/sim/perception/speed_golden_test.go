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
		perceptionScenario{
			name: "smith_forges_shovel_no_iron_no_vendor",
			summary: "LLM-1006: the live 2026-11-24 shape. The smith is at his forge with no bar iron and no one " +
				"selling it, the shovel recipe taking water as its one required input and iron only as a speed " +
				"input. With no buy path the '## Keeping up production' speed line stays silent (LLM-260 gate), " +
				"so the '## Your trade' shovel line alone must answer the iron question: 'takes about 4 hours " +
				"without bar iron to hasten it, and you have the makings.' Live, the model filled that gap from " +
				"its prior ('Need two bars of iron for shovel work') and stalled the recipe.",
			build: smithForgesShovelNoIronNoVendor,
		},
	)
}

// smithForgesShovelNoIronNoVendor is the smith alone at his forge with water and
// no iron, under the live shovel recipe (water 1 required, iron speed input).
// No distributor or other iron seller exists, so iron has no buy path.
func smithForgesShovelNoIronNoVendor() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		smithID = sim.ActorID("ezekiel")
		forge   = sim.StructureID("crane_forge")
	)
	now := 600 // 10:00 — on shift
	smith := shovelSmith(0)
	smith.Inventory["water"] = 4
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{smithID: smith},
		Structures:       map[sim.StructureID]*sim.Structure{forge: plainStructure(forge, "Crane Forge")},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(forge): {ID: sim.VillageObjectID(forge), Pos: sim.WorldPos{X: 640, Y: 640}},
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"shovel": {Name: "shovel", DisplayLabel: "Shovel", DisplayLabelSingular: "shovel", DisplayLabelPlural: "shovels", Category: sim.ItemCategory("tool")},
			"iron":   {Name: "iron", DisplayLabel: "bar iron", DisplayLabelSingular: "bar of iron", DisplayLabelPlural: "bars of iron", Category: sim.ItemCategoryMaterial},
			"water":  {Name: "water", DisplayLabel: "water", DisplayLabelSingular: "flask of water", DisplayLabelPlural: "flasks of water", Category: sim.ItemCategoryDrink},
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"shovel": {
				OutputItem: "shovel", OutputQty: 1, RateQty: 1, RatePerHours: 4,
				Inputs:         []sim.RecipeInput{{Item: "water", Qty: 1}},
				SpeedInputs:    []sim.SpeedInput{{Item: "iron", Qty: 1, RatePct: 200}},
				WholesalePrice: 6, RetailPrice: 12,
			},
		},
		RestockReorderPct: 25,
	}
	return snap, smithID, nil
}

// TestGoldensElectiveLinesNeverReadAsRequired is the LLM-1006 cross-scenario
// invariant over "## Keeping up production": every elective line (a yield
// booster or a speed booster, both "- A measure of …") sits below the elective
// lead-in, says a batch goes ahead without the item, and never carries the
// required-input "running low" tail. The live smith read the old shared tail as
// a requirement and stopped forging shovels.
func TestGoldensElectiveLinesNeverReadAsRequired(t *testing.T) {
	sawElective := false
	for _, sc := range perceptionScenarios {
		sc := sc
		got := renderScenario(sc)
		if !strings.Contains(got, "## Keeping up production\n") {
			continue
		}
		// Walk the section line by line: required lines first, then at most one
		// standalone lead-in, then only elective lines. The lead-in appears iff an
		// elective line does.
		leadIns, electives := 0, 0
		pastLead := false
		for _, line := range strings.Split(sectionOf(got, "## Keeping up production"), "\n") {
			switch {
			case line == electiveLeadIn:
				leadIns++
				pastLead = true
			case strings.HasPrefix(line, "- A measure of "):
				electives++
				sawElective = true
				if !pastLead {
					t.Errorf("scenario %q: elective line is not under the lead-in %q:\n  %s", sc.name, electiveLeadIn, line)
				}
				if strings.Contains(line, "running low") {
					t.Errorf("scenario %q: elective line carries the required-input tail:\n  %s", sc.name, line)
				}
				if !strings.HasSuffix(line, boostWithoutClause) && !strings.HasSuffix(line, speedWithoutClause) {
					t.Errorf("scenario %q: elective line does not say a batch goes ahead without it:\n  %s", sc.name, line)
				}
			case strings.HasPrefix(line, "- "):
				if pastLead {
					t.Errorf("scenario %q: required-input line renders below the elective lead-in:\n  %s", sc.name, line)
				}
			case strings.Contains(line, electiveLeadIn):
				t.Errorf("scenario %q: elective lead-in is not a standalone line:\n  %s", sc.name, line)
			}
		}
		if (leadIns > 0) != (electives > 0) || leadIns > 1 {
			t.Errorf("scenario %q: %d lead-in line(s) for %d elective line(s); want exactly one iff any", sc.name, leadIns, electives)
		}
	}
	if !sawElective {
		t.Fatal("no scenario renders an elective line; the invariant checks nothing")
	}
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

// TestMissingSpeedInputs pins the LLM-1006 trade-line clause to the sim's own
// apply predicate (StartProductionCycle): a speed input applies when qty >= 1,
// rate_pct > 100 and inventory >= qty. The clause lists the speed inputs only
// when none applies, so the base-rate time it rides on is the batch's real time.
func TestMissingSpeedInputs(t *testing.T) {
	snap := &sim.Snapshot{ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
		"iron": {Name: "iron", DisplayLabel: "bar iron"},
		"coal": {Name: "coal", DisplayLabel: "coal"},
	}}
	cases := []struct {
		name   string
		speeds []sim.SpeedInput
		inv    map[sim.ItemKind]int
		want   []string
	}{
		{"no speed inputs", nil, nil, nil},
		{"one missing", []sim.SpeedInput{{Item: "iron", Qty: 1, RatePct: 200}}, nil, []string{"bar iron"}},
		{"one held", []sim.SpeedInput{{Item: "iron", Qty: 1, RatePct: 200}}, map[sim.ItemKind]int{"iron": 1}, nil},
		{"qty 2, one on hand", []sim.SpeedInput{{Item: "iron", Qty: 2, RatePct: 200}}, map[sim.ItemKind]int{"iron": 1}, []string{"bar iron"}},
		{"qty 2, two on hand", []sim.SpeedInput{{Item: "iron", Qty: 2, RatePct: 200}}, map[sim.ItemKind]int{"iron": 2}, nil},
		{"zero qty ignored", []sim.SpeedInput{{Item: "iron", Qty: 0, RatePct: 200}}, nil, nil},
		{"no real speedup ignored", []sim.SpeedInput{{Item: "iron", Qty: 1, RatePct: 100}}, nil, nil},
		{"two missing", []sim.SpeedInput{{Item: "iron", Qty: 1, RatePct: 200}, {Item: "coal", Qty: 1, RatePct: 150}}, nil, []string{"bar iron", "coal"}},
		{"one of two held", []sim.SpeedInput{{Item: "iron", Qty: 1, RatePct: 200}, {Item: "coal", Qty: 1, RatePct: 150}}, map[sim.ItemKind]int{"coal": 1}, nil},
	}
	for _, tc := range cases {
		recipe := &sim.ItemRecipe{OutputItem: "shovel", OutputQty: 1, RateQty: 1, RatePerHours: 4, SpeedInputs: tc.speeds}
		got := missingSpeedInputs(snap, recipe, tc.inv)
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
