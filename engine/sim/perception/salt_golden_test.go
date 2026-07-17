package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// salt_golden_test.go — golden scenarios + the never-gated invariant for LLM-444
// (imported salt as an OPTIONAL cooking booster). Salt is the deliberately softer
// half of the LLM-442 import pair: iron is a REQUIRED input with a rough-nails
// fallback; salt is a pure booster with NO fallback because nothing is ever
// blocked. A dish always cooks at its base yield with zero salt; salt only ever
// ADDS servings when the cook holds it. These scenarios pin the three salt
// situations, which are the same booster mechanism the dairy sage edge uses
// (dairy_keeper_out_of_booster_at_post) applied to a cook:
//
//   - salt short, a vendor stocked  -> the booster motivate-line ("## Keeping up
//     production": "a measure of salt in each batch of stew adds 2 extra to the
//     yield") and the salt "## Restocking" buy line render together (the LLM-64
//     motivate/act split);
//   - no salt anywhere              -> both lines stay SILENT (the LLM-216/
//     LLM-260 no-dead-end gate) — the deliberate asymmetry with LLM-442, which
//     surfaces a rough-nails fallback in this tier; salt has no fallback to
//     surface because cooking was never gated;
//   - salt in hand above threshold  -> no low-stock nag; the boost is consumed
//     silently at batch landing.
//
// Across ALL three, "## Your trade" still offers the dish — salt never gates
// cooking (TestGoldensSaltNeverGatesCooking), the executable form of the ticket's
// "every cooked dish remains producible with zero salt village-wide" invariant.
//
// Registered into perceptionScenarios so TestPerceptionGoldens covers them
// alongside the rest.

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "cook_out_of_salt_vendor_stocked",
			summary: "LLM-444: a John-shaped tavernkeeper at his tavern, stew recipe carrying the salt booster " +
				"(1 sack per batch -> +2 bowls), salt a hand-authored buy entry at 0 on hand, and the distributor's " +
				"store stocked with salt. Pins the booster motivate-line ('a measure of salt in each batch of stew " +
				"adds 2 extra to the yield') alongside the salt '## Restocking' buy line (LLM-64 split: motivate " +
				"here, where/how there), with '## Your trade' still offering stew — salt is never required.",
			build: cookOutOfSaltVendorStocked,
		},
		perceptionScenario{
			name: "cook_no_salt_anywhere_silent",
			summary: "LLM-444 asymmetry with LLM-442: the same cook with ZERO salt in the village (the distributor's " +
				"shelf is bare). The booster motivate-line and the salt Restocking line must both stay silent — the " +
				"LLM-216/LLM-260 no-dead-end gate — and, UNLIKE the iron no-anywhere tier, there is no fallback line " +
				"to surface because cooking was never gated. '## Your trade' still offers stew, unboosted.",
			build: cookNoSaltAnywhere,
		},
		perceptionScenario{
			name: "cook_salt_in_hand_no_nag",
			summary: "LLM-444: the cook holding a full shelf of salt (6 of cap 6). No low-stock lines render — the " +
				"booster is consumed silently at batch landing — and '## Your trade' reads as any other day at the " +
				"tavern.",
			build: cookSaltInHand,
		},
	)
}

// saltCook builds the John-shaped subject: a tavernkeeper on shift inside his
// tavern, producing stew under the LLM-444 salt-boosted recipe, with the
// hand-authored `buy salt` entry (boost inputs are deliberately not derived —
// derived_demand.go, LLM-260) and saltOnHand sacks in his pack.
func saltCook(saltOnHand int) *sim.ActorSnapshot {
	start, end := 360, 1080 // 06:00–18:00
	inv := map[sim.ItemKind]int{"stew": 6}
	if saltOnHand > 0 {
		inv["salt"] = saltOnHand
	}
	return &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "John Ellis",
		Role:              "tavernkeeper",
		State:             sim.StateIdle,
		WorkStructureID:   "ellis_tavern",
		InsideStructureID: "ellis_tavern",
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             30,
		Inventory:         inv,
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "stew", Source: sim.RestockSourceProduce, Max: 30},
			{Item: "salt", Source: sim.RestockSourceBuy, Max: 6},
		}},
	}
}

// saltScenarioSnapshot assembles the shared world: the cook at his tavern and the
// distributor keeper at the distributor-tagged General Store holding vendorSalt
// sacks (0 = the bare-shelf world). The stew recipe carries the LLM-444 shape:
// its base yield with a salt boost of +2; salt itself is an import-only price
// anchor (no producer). The stew recipe deliberately carries no required inputs
// here so the golden isolates the salt booster line (the shipped stew's required
// inputs are exercised by their own fixtures — keeperWornSkilletWearRunway etc.).
func saltScenarioSnapshot(cook *sim.ActorSnapshot, vendorSalt int) (*sim.Snapshot, sim.ActorID) {
	const (
		cookID   = sim.ActorID("john")
		josiahID = sim.ActorID("josiah")
		tavern   = sim.StructureID("ellis_tavern")
	)
	now := 600 // 10:00 — on shift
	josiah := distributorKeeper(sim.TilePos{X: 41, Y: 40}, "")
	if vendorSalt > 0 {
		josiah.Inventory = map[sim.ItemKind]int{"salt": vendorSalt}
	}
	vobjs, structs := distributorObjects()
	vobjs[sim.VillageObjectID(tavern)] = &sim.VillageObject{ID: sim.VillageObjectID(tavern), Pos: sim.WorldPos{X: 640, Y: 640}}
	structs[tavern] = plainStructure(tavern, "Ellis Tavern")
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{cookID: cook, josiahID: josiah},
		Structures:       structs,
		VillageObjects:   vobjs,
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"stew": {Name: "stew", DisplayLabel: "stew", DisplayLabelSingular: "bowl of stew", DisplayLabelPlural: "stew", Category: sim.ItemCategoryFood},
			"salt": {Name: "salt", DisplayLabel: "salt", DisplayLabelSingular: "sack of salt", DisplayLabelPlural: "sacks of salt", Category: sim.ItemCategoryMaterial, Capabilities: []string{"portable"}},
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"stew": {
				OutputItem: "stew", OutputQty: 6, RateQty: 30, RatePerHours: 6,
				BoostInputs:    []sim.BoostInput{{Item: "salt", Qty: 1, BonusQty: 2}},
				WholesalePrice: 3, RetailPrice: 5,
			},
			"salt": {
				OutputItem: "salt", OutputQty: 1, RateQty: 1, RatePerHours: 1,
				WholesalePrice: 2, RetailPrice: 3,
			},
		},
		RestockReorderPct: 25,
	}
	return snap, cookID
}

func cookOutOfSaltVendorStocked() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, cookID := saltScenarioSnapshot(saltCook(0), 6)
	return snap, cookID, nil
}

func cookNoSaltAnywhere() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, cookID := saltScenarioSnapshot(saltCook(0), 0)
	return snap, cookID, nil
}

func cookSaltInHand() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, cookID := saltScenarioSnapshot(saltCook(6), 6)
	return snap, cookID, nil
}

// TestGoldensSaltNeverGatesCooking is the LLM-444 liveness invariant in
// executable form: across EVERY salt scenario — salt in hand, salt at the
// vendor, or none anywhere — the cook's "## Your trade" scene offers stew. Salt
// is never a required input, so no state of the import chain may ever strip
// cooking from the kitchen; a failure here means salt has been silently promoted
// toward a gate on the survival good, which the ticket forbids permanently.
func TestGoldensSaltNeverGatesCooking(t *testing.T) {
	saltScenarios := map[string]bool{
		"cook_out_of_salt_vendor_stocked": true,
		"cook_no_salt_anywhere_silent":    true,
		"cook_salt_in_hand_no_nag":        true,
	}
	for _, sc := range perceptionScenarios {
		if !saltScenarios[sc.name] {
			continue
		}
		out := renderScenario(sc)
		if !strings.Contains(out, "## Your trade") {
			t.Errorf("scenario %q: cook is missing the '## Your trade' scene entirely:\n%s", sc.name, out)
			continue
		}
		if !strings.Contains(out, "stew") {
			t.Errorf("scenario %q: '## Your trade' does not offer stew — cooking has been gated on salt:\n%s", sc.name, out)
		}
	}
}

// TestGoldensNoSaltDeadEnd — when no salt is acquirable anywhere, no cue may
// mention salt at all (LLM-216/LLM-260: a want with no legal outlet feeds futile
// improvisation). Unlike iron there is no fallback line either — the turn carries
// on unboosted, in silence.
func TestGoldensNoSaltDeadEnd(t *testing.T) {
	out := renderScenario(perceptionScenario{name: "cook_no_salt_anywhere_silent", build: cookNoSaltAnywhere})
	if strings.Contains(strings.ToLower(out), "salt") {
		t.Errorf("no-salt-anywhere scenario still mentions salt somewhere — dead-end nudge leaked:\n%s", out)
	}
}
