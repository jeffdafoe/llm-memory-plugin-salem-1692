package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// iron_golden_test.go — golden scenarios + the liveness invariant for LLM-442
// (imported iron gates nail production). The nail recipe's BASE leg is the
// inputless rough-nails fallback (1/hr — slow, low-yield, but never gated);
// the NORMAL leg is the iron booster (+4 per batch, bar consumed at landing).
// These scenarios pin the three iron situations the ticket names:
//
//   - iron short, a vendor stocked  -> the booster motivate-line ("## Keeping
//     up production") and the iron "## Restocking" buy line render together
//     (the LLM-64 motivate/act split);
//   - no iron anywhere              -> both lines stay SILENT (the LLM-216/
//     LLM-260 no-dead-end gate: never render a want with no legal outlet),
//     while "## Your trade" still offers nails — the rough-nails leg IS the
//     produce path, so nail production never disappears with the import chain;
//   - iron in hand above threshold  -> no low-stock nag at all; the boost is
//     consumed silently at batch landing.
//
// Registered into perceptionScenarios so TestPerceptionGoldens covers them
// alongside the rest.

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "smith_out_of_iron_vendor_stocked",
			summary: "LLM-442: an Ezekiel-shaped smith at his forge, nail recipe carrying the iron booster " +
				"(1 bar per batch -> +4 nails), iron a hand-authored buy entry at 0 on hand, and the " +
				"distributor's store stocked with bars. Pins the booster motivate-line ('a measure of bar iron " +
				"in each batch of nails adds 4 extra to the yield') alongside the iron '## Restocking' buy line " +
				"(LLM-64 split: motivate here, where/how there), with '## Your trade' still offering nails — " +
				"the rough base leg needs no iron.",
			build: smithOutOfIronVendorStocked,
		},
		perceptionScenario{
			name: "smith_no_iron_anywhere_rough_nails_live",
			summary: "LLM-442 liveness: the same smith with ZERO iron in the village (the distributor's shelf " +
				"is bare). The booster motivate-line and the iron Restocking line must both stay silent — the " +
				"LLM-216/LLM-260 no-dead-end gate — and '## Your trade' must still offer nails: the inputless " +
				"rough-nails base leg is the absorbing-state guarantee, so a broken import chain slows the " +
				"forge but can never stop it.",
			build: smithNoIronAnywhere,
		},
		perceptionScenario{
			name: "smith_iron_in_hand_no_nag",
			summary: "LLM-442: the smith holding a full shelf of bars (6 of cap 6). No low-stock lines render — " +
				"the booster is consumed silently at batch landing — and '## Your trade' reads as any other " +
				"day at the forge.",
			build: smithIronInHand,
		},
	)
}

// ironSmith builds the Ezekiel-shaped subject: a blacksmith on shift inside his
// forge, producing nails under the LLM-442 two-leg recipe, with the
// hand-authored `buy iron` entry (boost inputs are deliberately not derived —
// derived_demand.go) and ironOnHand bars in his pack.
func ironSmith(ironOnHand int) *sim.ActorSnapshot {
	start, end := 360, 1080 // 06:00–18:00
	inv := map[sim.ItemKind]int{"nail": 8}
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
			{Item: "nail", Source: sim.RestockSourceProduce, Max: 20},
			{Item: "iron", Source: sim.RestockSourceBuy, Max: 6},
		}},
	}
}

// ironScenarioSnapshot assembles the shared world: the smith at his forge and
// the distributor keeper at the distributor-tagged General Store holding
// vendorIron bars (0 = the bare-shelf world). The nail recipe carries the
// LLM-442 shape: inputless 1/hr base (the rough-nails leg), iron boost +4.
func ironScenarioSnapshot(smith *sim.ActorSnapshot, vendorIron int) (*sim.Snapshot, sim.ActorID) {
	const (
		smithID  = sim.ActorID("ezekiel")
		josiahID = sim.ActorID("josiah")
		forge    = sim.StructureID("crane_forge")
	)
	now := 600 // 10:00 — on shift
	josiah := distributorKeeper(sim.TilePos{X: 41, Y: 40}, "")
	if vendorIron > 0 {
		josiah.Inventory = map[sim.ItemKind]int{"iron": vendorIron}
	}
	vobjs, structs := distributorObjects()
	vobjs[sim.VillageObjectID(forge)] = &sim.VillageObject{ID: sim.VillageObjectID(forge), Pos: sim.WorldPos{X: 640, Y: 640}}
	structs[forge] = plainStructure(forge, "Crane Forge")
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{smithID: smith, josiahID: josiah},
		Structures:       structs,
		VillageObjects:   vobjs,
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"nail": {Name: "nail", DisplayLabel: "Nail", DisplayLabelSingular: "nail", DisplayLabelPlural: "nails", Category: sim.ItemCategory("tool")},
			"iron": {Name: "iron", DisplayLabel: "bar iron", DisplayLabelSingular: "bar of iron", DisplayLabelPlural: "bars of iron", Category: sim.ItemCategoryMaterial},
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"nail": {
				OutputItem: "nail", OutputQty: 1, RateQty: 1, RatePerHours: 1,
				BoostInputs:    []sim.BoostInput{{Item: "iron", Qty: 1, BonusQty: 4}},
				WholesalePrice: 1, RetailPrice: 2,
			},
			"iron": {
				OutputItem: "iron", OutputQty: 1, RateQty: 1, RatePerHours: 1,
				WholesalePrice: 2, RetailPrice: 3,
			},
		},
		RestockReorderPct: 25,
	}
	return snap, smithID
}

func smithOutOfIronVendorStocked() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, smithID := ironScenarioSnapshot(ironSmith(0), 6)
	return snap, smithID, nil
}

func smithNoIronAnywhere() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, smithID := ironScenarioSnapshot(ironSmith(0), 0)
	return snap, smithID, nil
}

func smithIronInHand() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, smithID := ironScenarioSnapshot(ironSmith(6), 6)
	return snap, smithID, nil
}

// TestGoldensRoughNailsAlwaysCraftable is the LLM-442 liveness invariant in
// executable form: across EVERY iron scenario — bars in hand, bars at the
// vendor, or none anywhere — the smith's "## Your trade" scene offers nails.
// The rough-nails base leg is inputless, so no state of the import chain may
// ever strip nail production from the forge; a failure here means someone has
// re-introduced the absorbing state the two-leg design exists to prevent.
func TestGoldensRoughNailsAlwaysCraftable(t *testing.T) {
	ironScenarios := map[string]bool{
		"smith_out_of_iron_vendor_stocked":        true,
		"smith_no_iron_anywhere_rough_nails_live": true,
		"smith_iron_in_hand_no_nag":               true,
	}
	for _, sc := range perceptionScenarios {
		if !ironScenarios[sc.name] {
			continue
		}
		out := renderScenario(sc)
		if !strings.Contains(out, "## Your trade") {
			t.Errorf("scenario %q: smith is missing the '## Your trade' scene entirely:\n%s", sc.name, out)
			continue
		}
		if !strings.Contains(out, "nails") {
			t.Errorf("scenario %q: '## Your trade' does not offer nails — the rough-nails liveness leg is gone:\n%s", sc.name, out)
		}
	}
}

// TestGoldensNoIronDeadEnd — when no iron is acquirable anywhere, no cue may
// mention iron at all (LLM-216/LLM-260: a want with no legal outlet feeds
// futile improvisation). The rough leg carries the turn silently.
func TestGoldensNoIronDeadEnd(t *testing.T) {
	out := renderScenario(perceptionScenario{name: "smith_no_iron_anywhere_rough_nails_live", build: smithNoIronAnywhere})
	if strings.Contains(strings.ToLower(out), "iron") {
		t.Errorf("no-iron-anywhere scenario still mentions iron somewhere — dead-end nudge leaked:\n%s", out)
	}
}
