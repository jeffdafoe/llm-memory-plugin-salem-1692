package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// input_short_steers_golden_test.go — golden scenarios + cross-scenario invariant
// for LLM-635. The live case (Ezekiel Crane, 2026-08-14, inside the LLM-634
// deadlock): he held 0 water (nail = 1 water), so LLM-324 dropped nails from
// "## Your trade" and the produce tool withdrew with it — correctly. But the
// mend steer still said "forge what you're short", the order book still said
// "you've yet to make it", and nothing anywhere named water. He enumerated his
// tool list on the record, found no "forge", concluded that done() at his post
// forges, and two mend-sized orders expired unstarted.
//
// The rule these pin: a cue that steers toward MAKING a good must, when that
// good is short a required input, name the input rather than instruct the make.
// Perception already knew the input (the same HasProduceInputs check that
// withdrew the tool); the steers simply weren't reading it. The rule is per
// GOOD, not per tool: another makeable good keeping the produce tool offered
// does not make "forge what you're short" actionable for nails.

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "smith_out_of_water_owes_nails_no_seller",
			summary: "LLM-635, the 2026-08-14 live shape. Ezekiel inside his degraded forge holding 1 of the 5 nails a " +
				"mend takes and 0 water (nail = 1 water), the only water seller dry (0 in stock, so no actionable buy " +
				"path — '## Restocking' and '## Keeping up production' are correctly silent, LLM-260), and a Ready 5-nail " +
				"order from Josiah Thorne (the distributor, standing in for the live buyer) awaiting the make. '## Your " +
				"trade' is absent and the produce tool withdrawn (LLM-324). The golden pins that BOTH steers toward making " +
				"nails now name the gap: the mend line reads 'nails are your own work, but you've no Water to forge them " +
				"with, and none is to be had just now' (no 'forge what you're short'), and the order line reads 'you've " +
				"yet to make it, and you've no Water for it'. Byte-stable: fixed PublishedAt, on shift, no clock read.",
			build: smithOutOfWaterOwesNailsNoSeller,
		},
		perceptionScenario{
			name: "smith_out_of_water_can_still_make_shovels",
			summary: "LLM-635 per-good arm. The same dry-village smith, but he also produces shovels from nothing, so " +
				"'## Your trade' renders (shovels) and the produce tool IS offered — for shovels. Nails are still short " +
				"of water, so the mend steer must still name water and must not say 'forge what you're short': the tool " +
				"being offered for another good does not make forging nails possible. Pins the invariant's per-good " +
				"scope alongside the trade scene.",
			build: smithOutOfWaterCanStillMakeShovels,
		},
		perceptionScenario{
			name: "smith_out_of_water_forage_entry_no_source",
			summary: "LLM-635 sourceability, negative arm. The dry-village smith now carries a `forage water` policy " +
				"entry but remembers NO water source (no known place, no forage_range tag). A policy entry permits a " +
				"gather without establishing one, and the forage cue is silent — so the mend steer must take the " +
				"'none is to be had just now' branch, never 'see to that first' with nothing beneath it (code_review).",
			build: smithOutOfWaterForageEntryNoSource,
		},
		perceptionScenario{
			name: "smith_out_of_water_own_spring",
			summary: "LLM-635 sourceability, forage arm. The dry-village smith owns a stocked Spring (yield-only water " +
				"row, 20 ripe) and remembers it as a gather:water place, with a `forage water` entry — the owned-bush " +
				"arm of the forage cue. '## Your bushes to harvest' renders with a move_to handle to the Spring, and the " +
				"mend steer reads 'see to that first, then mend it here' — the onward steer with its where/how beneath " +
				"it. Paired with smith_out_of_water_forage_entry_no_source: one fixture bit (a remembered source) moves " +
				"BOTH the forage section and the steer's branch together.",
			build: smithOutOfWaterOwnSpring,
		},
	)
}

// smithOutOfWaterOwesNailsNoSeller is the LLM-608 worn-forge world with the
// water seller DRY (Josiah holds 0 water — findItemVendors requires qty>0, so
// there is no actionable buy path), the smith down to 1 nail, and a Ready 5-nail
// order from Josiah on the book. Josiah is at his own store, not co-present, so
// the order is both awaiting-make and recipient-absent; AwaitingMake wins the
// render switch, which is the arm under test.
func smithOutOfWaterOwesNailsNoSeller() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, smithID := dryVillageSmith()
	published := time.Date(2026, 8, 14, 17, 50, 0, 0, time.UTC)
	snap.PublishedAt = published
	snap.LocalDateUTC = time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	snap.Orders = map[sim.OrderID]*sim.Order{
		4701: {
			ID:          4701,
			State:       sim.OrderStateReady,
			SellerID:    smithID,
			BuyerID:     "josiah",
			Item:        "nail",
			Qty:         5,
			ConsumerIDs: []sim.ActorID{"josiah"},
			CreatedAt:   published.Add(-2 * time.Minute),
			ExpiresAt:   published.Add(118 * time.Minute),
		},
	}
	return snap, smithID, nil
}

// dryVillageSmith is the shared base: the worn-forge smith with 1 nail and 0
// water, past the degrade threshold, and the distributor's water at 0.
func dryVillageSmith() (*sim.Snapshot, sim.ActorID) {
	smith := wornForgeSmith(0)
	smith.Inventory["nail"] = 1
	snap, smithID := wornForgeSnapshot(smith, 91) // past the degrade threshold
	snap.Actors["josiah"].Inventory["water"] = 0  // the village's water is out — no buy path
	return snap, smithID
}

func smithOutOfWaterCanStillMakeShovels() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, smithID := dryVillageSmith()
	smith := snap.Actors[smithID]
	smith.RestockPolicy.Restock = append(smith.RestockPolicy.Restock,
		sim.RestockEntry{Item: "shovel", Source: sim.RestockSourceProduce, Max: 10})
	snap.ItemKinds["shovel"] = &sim.ItemKindDef{Name: "shovel", DisplayLabel: "Shovel", DisplayLabelSingular: "shovel", DisplayLabelPlural: "shovels", Category: sim.ItemCategory("tool")}
	snap.Recipes["shovel"] = &sim.ItemRecipe{OutputItem: "shovel", OutputQty: 1, RateQty: 1, RatePerHours: 2, WholesalePrice: 5, RetailPrice: 10}
	return snap, smithID, nil
}

func smithOutOfWaterForageEntryNoSource() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, smithID := dryVillageSmith()
	smith := snap.Actors[smithID]
	smith.RestockPolicy.Restock = append(smith.RestockPolicy.Restock,
		sim.RestockEntry{Item: "water", Source: sim.RestockSourceForage, Max: 12})
	return snap, smithID, nil
}

func smithOutOfWaterOwnSpring() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, smithID, _ := smithOutOfWaterForageEntryNoSource()
	addOwnedSpring(snap, snap.Actors[smithID], smithID)
	return snap, smithID, nil
}

// TestGoldensProduceSteersNameTheGapWhenInputShort is the LLM-635
// cross-scenario invariant, per GOOD: wherever the subject is a producer, not
// mid-batch, and a good it makes is short a required input for one batch (the
// LLM-324 drop condition, recomputed here from the fixture rather than read off a
// view), no cue may instruct the make without naming that input. Concretely,
// over the whole matrix:
//
//   - the "## Your business" mend steer for a nail maker short of a nail input
//     names every missing input and does NOT say "forge what you're short" —
//     regardless of whether the produce tool happens to be offered for some
//     OTHER makeable good;
//   - every "## Orders to deliver" line for an order of that good the seller has
//     yet to make names every missing input.
//
// Mid-batch is excluded — the standing in-flight line carries what re-arms the
// bench (time), which is a different contract.
func TestGoldensProduceSteersNameTheGapWhenInputShort(t *testing.T) {
	mendExercised, orderExercised, toolOfferedExercised := false, false, false
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, _ := sc.build()
			a := snap.Actors[actorID]
			if a == nil || a.RestockPolicy == nil || snap.Recipes == nil || a.ProductionItem != "" {
				return
			}
			out := renderScenario(sc)
			for _, e := range a.RestockPolicy.ProduceEntries() {
				missing := missingProduceInputs(snap, a, e.Item)
				if len(missing) == 0 {
					continue // makeable — the plain steers are honest here
				}
				if e.Item == sim.NailItemKind {
					if section := promptSection(out, "## Your business"); strings.Contains(section, "nails are your own work") {
						mendExercised = true
						if fc := buildForgeChoice(snap, actorID, a); fc != nil && len(fc.Items) > 0 {
							toolOfferedExercised = true
						}
						if strings.Contains(section, "forge what you're short") {
							t.Errorf("scenario %q: mend steer says 'forge what you're short' while nails are short of %v (LLM-635)\n%s", sc.name, missing, section)
						}
						for _, m := range missing {
							if !strings.Contains(section, m) {
								t.Errorf("scenario %q: mend steer does not name the missing input %q (LLM-635)\n%s", sc.name, m, section)
							}
						}
					}
				}
				for _, line := range strings.Split(promptSection(out, "## Orders to deliver"), "\n") {
					if !strings.Contains(line, "you've yet to make it") || !strings.Contains(line, " "+string(e.Item)+" ") {
						continue
					}
					orderExercised = true
					for _, m := range missing {
						if !strings.Contains(line, m) {
							t.Errorf("scenario %q: order line says 'yet to make it' without naming the missing input %q (LLM-635)\n%s", sc.name, m, line)
						}
					}
				}
			}
		})
	}
	if !mendExercised || !orderExercised || !toolOfferedExercised {
		t.Fatalf("invariant not exercised (mend=%v order=%v tool-offered=%v) — the LLM-635 scenarios must be registered", mendExercised, orderExercised, toolOfferedExercised)
	}
}

// TestGoldensInputShortMendSteerNamesThePath pins the sourceability arms of the
// LLM-635 mend steer against the fixtures that hold each: "see to that first"
// renders ONLY with a where/how beneath it — a "## Restocking" destination
// (LLM-608's stocked distributor) or a "## Your bushes to harvest" handle (the
// owned spring) — and the dry village, WITH or without a bare forage entry, says
// "none is to be had just now" with neither section present.
func TestGoldensInputShortMendSteerNamesThePath(t *testing.T) {
	onward := func(name string, build func() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta), whereHow string) {
		out := renderScenario(perceptionScenario{name: name, build: build})
		if section := promptSection(out, "## Your business"); !strings.Contains(section, "see to that first") {
			t.Errorf("%s: mend steer should point onward to the input, got:\n%s", name, section)
		}
		if !strings.Contains(out, whereHow) {
			t.Errorf("%s: 'see to that first' rendered with no %q beneath it:\n%s", name, whereHow, out)
		}
	}
	onward("smith_worn_forge_out_of_water", smithWornForgeOutOfWater, "destination: general_store")
	onward("smith_out_of_water_own_spring", smithOutOfWaterOwnSpring, `destination "forge_spring"`)

	dry := func(name string, build func() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta)) {
		out := renderScenario(perceptionScenario{name: name, build: build})
		if section := promptSection(out, "## Your business"); !strings.Contains(section, "none is to be had just now") {
			t.Errorf("%s: mend steer should say the input is not to be had, got:\n%s", name, section)
		}
		for _, banned := range []string{"## Restocking", "## Your bushes to harvest", "## Free sources you can gather from"} {
			if strings.Contains(out, banned) {
				t.Errorf("%s: %q rendered where no path exists:\n%s", name, banned, out)
			}
		}
	}
	dry("smith_out_of_water_owes_nails_no_seller", smithOutOfWaterOwesNailsNoSeller)
	dry("smith_out_of_water_forage_entry_no_source", smithOutOfWaterForageEntryNoSource)
}
