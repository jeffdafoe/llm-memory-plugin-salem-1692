package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// input_short_steers_golden_test.go — golden scenario + cross-scenario invariant
// for LLM-635. The live case (Ezekiel Crane, 2026-08-14, inside the LLM-634
// deadlock): he held 0 water (nail = 1 water), so LLM-324 dropped nails from
// "## Your trade" and the produce tool withdrew with it — correctly. But the
// mend steer still said "forge what you're short", the order book still said
// "you've yet to make it", and nothing anywhere named water. He enumerated his
// tool list on the record, found no "forge", concluded that done() at his post
// forges, and two mend-sized orders expired unstarted.
//
// The rule these pin: a cue that steers toward MAKING a good must, when the
// produce tool has been withdrawn for want of an input, name that input rather
// than instruct the make. Perception already knew the input (the same
// HasProduceInputs check that withdrew the tool); the steers simply weren't
// reading it.

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "smith_out_of_water_owes_nails_no_seller",
			summary: "LLM-635, the 2026-08-14 live shape. Ezekiel inside his degraded forge holding 1 of the 5 nails a " +
				"mend takes and 0 water (nail = 1 water), the only water seller dry (0 in stock, so no actionable buy " +
				"path — '## Restocking' and '## Keeping up production' are correctly silent, LLM-260), and a Ready 5-nail " +
				"order from Josiah Thorne (the distributor, standing in for the live buyer) awaiting the make. '## Your trade' is absent and the produce tool withdrawn " +
				"(LLM-324). The golden pins that BOTH steers toward making nails now name the gap: the mend line reads " +
				"'nails are your own work, but you've no Water to forge them with, and none is to be had just now' (no " +
				"'forge what you're short'), and the order line reads 'you've yet to make it, and you've no Water for it'. " +
				"Byte-stable: fixed PublishedAt, on shift, no clock read.",
			build: smithOutOfWaterOwesNailsNoSeller,
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
	smith := wornForgeSmith(0)
	smith.Inventory["nail"] = 1
	snap, smithID := wornForgeSnapshot(smith, 91) // past the degrade threshold
	snap.Actors["josiah"].Inventory["water"] = 0  // the village's water is out — no buy path
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

// TestGoldensProduceSteersNameTheGapWhenToolWithdrawn is the LLM-635
// cross-scenario invariant: wherever the subject is a producer whose produce
// tool is withdrawn because a good it makes is short an input (the LLM-324
// drop — recomputed here from the fixture, not read off the view), no cue may
// instruct the make without naming that input. Concretely, over the whole matrix:
//
//   - the "## Your business" mend steer for a nail maker short of a nail input
//     names every missing input and does NOT say "forge what you're short";
//   - every "## Orders to deliver" line for an order of that good the seller has
//     yet to make names every missing input.
//
// The trigger is the WITHDRAWN tool: with the tool offered (inputs on hand) the
// plain steers are correct and this invariant is N/A. Mid-batch is excluded — the
// tool is withdrawn for a different reason there and the standing in-flight line
// carries what re-arms it (time), which is a different contract.
func TestGoldensProduceSteersNameTheGapWhenToolWithdrawn(t *testing.T) {
	exercised := false
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, _ := sc.build()
			a := snap.Actors[actorID]
			if a == nil || a.RestockPolicy == nil || snap.Recipes == nil || a.ProductionItem != "" {
				return
			}
			fc := buildForgeChoice(snap, actorID, a)
			if fc != nil && len(fc.Items) > 0 {
				return // produce tool offered — the plain steers are honest here
			}
			out := renderScenario(sc)
			for _, e := range a.RestockPolicy.ProduceEntries() {
				missing := missingProduceInputs(snap, a, e.Item)
				if len(missing) == 0 {
					continue // not input-short — withdrawn for another reason, or makeable
				}
				if e.Item == sim.NailItemKind {
					if section := promptSection(out, "## Your business"); strings.Contains(section, "nails are your own work") {
						exercised = true
						if strings.Contains(section, "forge what you're short") {
							t.Errorf("scenario %q: mend steer says 'forge what you're short' while the produce tool is withdrawn for want of %v (LLM-635)\n%s", sc.name, missing, section)
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
					exercised = true
					for _, m := range missing {
						if !strings.Contains(line, m) {
							t.Errorf("scenario %q: order line says 'yet to make it' without naming the missing input %q (LLM-635)\n%s", sc.name, m, line)
						}
					}
				}
			}
		})
	}
	if !exercised {
		t.Fatal("no scenario exercised the withdrawn-tool arm — smith_out_of_water_owes_nails_no_seller must be registered")
	}
}

// TestGoldensInputShortMendSteerNamesThePath pins the two sourceability arms of
// the LLM-635 mend steer against the two worn-forge fixtures: with the
// distributor stocked (LLM-608's fixture) the line points onward ("see to that
// first") beneath a "## Restocking" line that names the destination; with the
// village dry it says so ("none is to be had just now") and no restock line
// renders — so the steer never sends the smith on an errand the where/how
// section does not carry.
func TestGoldensInputShortMendSteerNamesThePath(t *testing.T) {
	stocked := renderScenario(perceptionScenario{name: "smith_worn_forge_out_of_water", build: smithWornForgeOutOfWater})
	if section := promptSection(stocked, "## Your business"); !strings.Contains(section, "see to that first") {
		t.Errorf("stocked distributor: mend steer should point onward to the input, got:\n%s", section)
	}
	if !strings.Contains(stocked, "destination: general_store") {
		t.Errorf("stocked distributor: 'see to that first' rendered with no restock destination beneath it:\n%s", stocked)
	}
	dry := renderScenario(perceptionScenario{name: "smith_out_of_water_owes_nails_no_seller", build: smithOutOfWaterOwesNailsNoSeller})
	if section := promptSection(dry, "## Your business"); !strings.Contains(section, "none is to be had just now") {
		t.Errorf("dry village: mend steer should say the input is not to be had, got:\n%s", section)
	}
	if strings.Contains(dry, "## Restocking") || strings.Contains(dry, "## Your trade") {
		t.Errorf("dry village: a buy or trade section rendered where no path exists:\n%s", dry)
	}
}
