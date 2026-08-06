package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// worn_forge_golden_test.go — golden scenarios + the liveness invariant for
// LLM-608. The live case (Ezekiel Crane, 2026-08-06): his forge wore past the
// degrade threshold, every one of his recipes had just taken `water` as an input,
// and the degrade gate (LLM-304) suppressed the whole "## Restocking" section —
// including the water. He could not forge, so he could not make the fifth nail,
// so he could not mend, so the section stayed shut. Four nails, five needed, and
// no way through.
//
// The narrowed gate keeps the inputs his own production consumes while the
// shelves still wait on the mending. These scenarios pin the two situations that
// matter:
//
//   - worn forge, no water     -> "## Restocking" renders WATER (and says the
//     shelves wait), "## Your business" says badly worn, and "## Keeping up
//     production" has its matching act-half again (the LLM-64 split);
//   - mended forge, no water   -> the ordinary buy directory, resale stock and
//     all, so the degraded case reads as a narrowing rather than a rewrite.
//
// Registered into perceptionScenarios so TestPerceptionGoldens covers them.

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "smith_worn_forge_out_of_water",
			summary: "LLM-608: the Ezekiel-shaped smith at a forge worn past the degrade threshold, all three of " +
				"his recipes taking water, none on hand, the distributor stocked, and four nails against the five " +
				"a mend takes. Pins that the narrowed '## Restocking' section still names WATER — the input that " +
				"makes the nail that mends the forge — under a lead that says the shelves wait on the mending, " +
				"while his resale stock stays suppressed as LLM-304 intends.",
			build: smithWornForgeOutOfWater,
		},
		perceptionScenario{
			name: "smith_mended_forge_out_of_water",
			summary: "LLM-608 foil: the same smith with the forge mended. The ordinary buy directory returns — " +
				"water AND the resale good — under the plain 'your shop stock is running low' lead, so the " +
				"degraded scenario above reads as a narrowing of the section rather than a rewrite of it.",
			build: smithMendedForgeOutOfWater,
		},
	)
}

// wornForgeSmith builds the subject: a blacksmith on shift inside his forge,
// producing nails from water, holding `water` flasks and 4 nails — one short of
// the five a repair takes, which is the live shape of the deadlock. He also
// resells linens, the control good that must stay suppressed while worn.
func wornForgeSmith(water int) *sim.ActorSnapshot {
	start, end := 360, 1080 // 06:00–18:00
	inv := map[sim.ItemKind]int{"nail": 4, "linens": 1}
	if water > 0 {
		inv["water"] = water
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
		Coins:             487,
		Inventory:         inv,
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "nail", Source: sim.RestockSourceProduce, Max: 20},
			{Item: "water", Source: sim.RestockSourceBuy, Max: 12},
			{Item: "linens", Source: sim.RestockSourceBuy, Max: 6},
		}},
	}
}

// wornForgeSnapshot assembles the world: the smith at his own forge (a
// business-tagged object he owns, worn to `wear`) and the distributor keeper
// holding water and linens. The shop LIMPS rather than stopping —
// StallDegradedProducePct 50, the live setting — which is the LLM-446 escape the
// narrowed gate exists to keep open.
func wornForgeSnapshot(smith *sim.ActorSnapshot, wear int) (*sim.Snapshot, sim.ActorID) {
	const (
		smithID  = sim.ActorID("ezekiel")
		josiahID = sim.ActorID("josiah")
		forge    = sim.StructureID("crane_forge")
	)
	now := 600 // 10:00 — on shift
	josiah := distributorKeeper(sim.TilePos{X: 41, Y: 40}, "")
	josiah.Inventory = map[sim.ItemKind]int{"water": 20, "linens": 8}
	vobjs, structs := distributorObjects()
	vobjs[sim.VillageObjectID(forge)] = &sim.VillageObject{
		ID:           sim.VillageObjectID(forge),
		Pos:          sim.WorldPos{X: 640, Y: 640},
		OwnerActorID: smithID,
		Tags:         []string{sim.TagBusiness},
		Wear:         wear,
	}
	structs[forge] = plainStructure(forge, "Blacksmith")
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{smithID: smith, josiahID: josiah},
		Structures:       structs,
		VillageObjects:   vobjs,
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"nail":   {Name: "nail", DisplayLabel: "Nail", DisplayLabelSingular: "nail", DisplayLabelPlural: "nails", Category: sim.ItemCategory("tool")},
			"water":  {Name: "water", DisplayLabel: "Water", DisplayLabelSingular: "flask of water", DisplayLabelPlural: "flasks of water", Category: sim.ItemCategoryDrink},
			"linens": {Name: "linens", DisplayLabel: "linens", DisplayLabelSingular: "set of linens", DisplayLabelPlural: "sets of linens"},
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"nail": {
				OutputItem: "nail", OutputQty: 1, RateQty: 4, RatePerHours: 1,
				Inputs:         []sim.RecipeInput{{Item: "water", Qty: 1}},
				WholesalePrice: 1, RetailPrice: 2,
			},
			"water": {
				OutputItem: "water", OutputQty: 1, RateQty: 12, RatePerHours: 1,
				WholesalePrice: 1, RetailPrice: 1,
			},
			"linens": {
				OutputItem: "linens", OutputQty: 1, RateQty: 1, RatePerHours: 4,
				WholesalePrice: 2, RetailPrice: 4,
			},
		},
		RestockReorderPct:         25,
		StallWearRepairThreshold:  60,
		StallWearDegradeThreshold: 90,
		StallDegradedProducePct:   50,
		StallNailsPerRepair:       5,
	}
	return snap, smithID
}

func smithWornForgeOutOfWater() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, smithID := wornForgeSnapshot(wornForgeSmith(0), 91) // past the degrade threshold
	return snap, smithID, nil
}

func smithMendedForgeOutOfWater() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, smithID := wornForgeSnapshot(wornForgeSmith(0), 0)
	return snap, smithID, nil
}

// TestGoldensWornBusinessKeepsItsProductionInputs is the LLM-608 liveness
// invariant in executable form: a degraded maker who is out of a bought input his
// own recipes require, with a stocked supplier in the village, must be told where
// to get it. The deadlock had no bottom precisely because he was not — so a
// failure here means the exit LLM-446 built has been closed again.
func TestGoldensWornBusinessKeepsItsProductionInputs(t *testing.T) {
	out := renderScenario(perceptionScenario{name: "smith_worn_forge_out_of_water", build: smithWornForgeOutOfWater})
	section := restockingSection(out)
	if section == "" {
		t.Fatalf("worn forge: the restocking section vanished — the smith has no route to his own inputs:\n%s", out)
	}
	// Scoped to the SECTION, and to the destination: "## Keeping up production"
	// also says the word water, and it is precisely the section that says where to
	// GET the water that the degrade gate used to remove. Asserting on the whole
	// prompt would have passed against the very bug this pins.
	if !strings.Contains(strings.ToLower(section), "water") {
		t.Errorf("worn forge: the narrowed section does not name water — the input the mend depends on:\n%s", section)
	}
	if !strings.Contains(section, "destination: general_store") {
		t.Errorf("worn forge: no walk-to destination for the input, so the cue names a want with nowhere to act on it:\n%s", section)
	}
	// The shelves still wait: the lead must not invite a shelf restock, because
	// "## Your business" says in the same prompt that he cannot until he mends.
	if strings.Contains(out, "Your shop stock of these bought-in goods") {
		t.Errorf("worn forge: the shelf-restocking lead rendered, contradicting '## Your business':\n%s", out)
	}
	// The resale control good stays suppressed — this is a narrowing of LLM-304,
	// not a repeal of it. Scoped to the SECTION, not the whole prompt: the smith is
	// carrying a set of linens, so "## You" names them either way.
	if strings.Contains(strings.ToLower(section), "linens") {
		t.Errorf("worn forge: resale stock leaked into the narrowed section:\n%s", section)
	}
	// And the foil: mended, the same resale good IS in the directory — otherwise
	// this test would pass just as well against a section that named nothing.
	mended := restockingSection(renderScenario(perceptionScenario{name: "smith_mended_forge_out_of_water", build: smithMendedForgeOutOfWater}))
	if !strings.Contains(strings.ToLower(mended), "linens") {
		t.Errorf("mended forge: resale stock is missing from the ordinary buy directory, so the worn-forge assertion proves nothing:\n%s", mended)
	}
}

// TestGoldensProductionCueHasItsActHalf — the LLM-64 split: "## Keeping up
// production" motivates and "## Restocking" carries the where/how, and the two
// are meant to render together for the same item. The degrade gate broke that
// pairing (the motivate-half does not suppress on degrade), which is how the live
// smith came to be told his water was out and never told that any existed. Pinned
// across both worn-forge scenarios so the halves cannot drift apart again.
func TestGoldensProductionCueHasItsActHalf(t *testing.T) {
	for _, sc := range []perceptionScenario{
		{name: "smith_worn_forge_out_of_water", build: smithWornForgeOutOfWater},
		{name: "smith_mended_forge_out_of_water", build: smithMendedForgeOutOfWater},
	} {
		out := renderScenario(sc)
		if strings.Contains(out, "## Keeping up production") && !strings.Contains(out, "## Restocking") {
			t.Errorf("scenario %q: the production motivate-half renders with no act-half to answer it:\n%s", sc.name, out)
		}
	}
}
