package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// elective_supply_errand_test.go — LLM-1008. The at-post duty line's third form:
// when "## Restocking" is the only walk-to cue and every item it sends the keeper
// out for only boosts what she makes, the line permits the trip without claiming
// need. Live: "what you need is not to be had here" took Hannah Boggs out of the
// Inn mid-breakfast for salt, a porridge yield booster. The goldens that carry the
// form are the booster/speed-only restock scenarios (cook_out_of_salt_vendor_stocked,
// dairy_keeper_out_of_booster_at_post, smith_out_of_iron_vendor_stocked,
// smith_speeds_shovel_low_iron_vendor_stocked).

// TestElectiveOnlySupplyErrand walks the decision table: which errands are
// booster-only, and which keep the need line.
func TestElectiveOnlySupplyErrand(t *testing.T) {
	vendor := []RestockVendor{{StructureLabel: "The General Store", StructureID: "general_store"}}
	walkTo := func(kinds ...sim.ItemKind) *RestockingView {
		v := &RestockingView{}
		for _, k := range kinds {
			v.Items = append(v.Items, RestockItemView{Kind: k, ItemLabel: string(k), Cap: 6, Vendors: vendor})
		}
		return v
	}
	recipes := map[sim.ItemKind]*sim.ItemRecipe{
		// porridge: milk required, salt a yield booster.
		"porridge": {OutputItem: "porridge", OutputQty: 10, RateQty: 10, RatePerHours: 1,
			Inputs: []sim.RecipeInput{{Item: "milk", Qty: 1}}, BoostInputs: []sim.BoostInput{{Item: "salt", Qty: 1, BonusQty: 2}}},
		// shovel: water required, iron a speed input.
		"shovel": {OutputItem: "shovel", OutputQty: 1, RateQty: 1, RatePerHours: 4,
			Inputs: []sim.RecipeInput{{Item: "water", Qty: 1}}, SpeedInputs: []sim.SpeedInput{{Item: "iron", Qty: 1, RatePct: 200}}},
		// brine: salt REQUIRED, so a keeper making it needs salt.
		"brine": {OutputItem: "brine", OutputQty: 1, RateQty: 1, RatePerHours: 1,
			Inputs: []sim.RecipeInput{{Item: "salt", Qty: 1}}},
		// nail: iron listed as a speed input at 100% (no speedup) and coal as a
		// zero-bonus boost; the sim ignores both, so neither is a booster.
		"nail": {OutputItem: "nail", OutputQty: 1, RateQty: 4, RatePerHours: 1,
			Inputs:      []sim.RecipeInput{{Item: "water", Qty: 1}},
			SpeedInputs: []sim.SpeedInput{{Item: "iron", Qty: 1, RatePct: 100}},
			BoostInputs: []sim.BoostInput{{Item: "coal", Qty: 1, BonusQty: 0}}},
	}
	keeper := func(makes ...sim.ItemKind) *sim.ActorSnapshot {
		pol := &sim.RestockPolicy{}
		for _, m := range makes {
			pol.Restock = append(pol.Restock, sim.RestockEntry{Item: m, Source: sim.RestockSourceProduce, Max: 20})
		}
		return &sim.ActorSnapshot{RestockPolicy: pol}
	}
	snap := &sim.Snapshot{Recipes: recipes}

	cases := []struct {
		name  string
		p     Payload
		actor *sim.ActorSnapshot
		want  bool
	}{
		{"yield booster only", Payload{Restocking: walkTo("salt")}, keeper("porridge"), true},
		{"speed input only", Payload{Restocking: walkTo("iron")}, keeper("shovel"), true},
		{"boosters for two recipes", Payload{Restocking: walkTo("salt", "iron")}, keeper("porridge", "shovel"), true},
		{"booster plus a required input", Payload{Restocking: walkTo("salt", "milk")}, keeper("porridge"), false},
		{"booster in one recipe, required in another", Payload{Restocking: walkTo("salt")}, keeper("porridge", "brine"), false},
		{"pure reseller", Payload{Restocking: walkTo("salt")}, keeper(), false},
		{"item she does not make with", Payload{Restocking: walkTo("ale")}, keeper("porridge"), false},
		{"booster plus another walk-to cue", Payload{
			Restocking: walkTo("salt"),
			FarmUpkeep: &FarmUpkeepView{ShovelsOwed: 2, ShovelsShort: 2, ShovelVendors: vendor},
		}, keeper("porridge"), false},
		{"booster with a co-present seller only", Payload{Restocking: &RestockingView{Items: []RestockItemView{
			{Kind: "salt", ItemLabel: "salt", Cap: 6, CoPresentSeller: "Josiah Thorne", SellerStock: 6},
		}}}, keeper("porridge"), false},
		{"no restocking", Payload{}, keeper("porridge"), false},
		// The line is about what would take her away: a required input she can buy
		// from a seller in the room is no errand.
		{"walk-to booster, required input from a co-present seller", Payload{Restocking: &RestockingView{Items: []RestockItemView{
			{Kind: "salt", ItemLabel: "salt", Cap: 6, Vendors: vendor},
			{Kind: "milk", ItemLabel: "milk", Cap: 6, CoPresentSeller: "Josiah Thorne", SellerStock: 6},
		}}}, keeper("porridge"), true},
		{"speed input with no real speedup", Payload{Restocking: walkTo("iron")}, keeper("nail"), false},
		{"boost with zero bonus", Payload{Restocking: walkTo("coal")}, keeper("nail"), false},
	}
	for _, c := range cases {
		p := c.p
		if got := electiveOnlySupplyErrand(&p, snap, c.actor); got != c.want {
			t.Errorf("%s: electiveOnlySupplyErrand = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestGoldensElectiveSupplyErrandMatchesItsItems is the LLM-1008 cross-scenario
// invariant, checked against the rendered prompt and an independent read of the
// fixture's recipes. Over every scenario with an at-post subject:
//   - the elective line and the need line never both render;
//   - the elective line renders only when every walk-to bullet in the prompt is in
//     "## Restocking" and every walk-to restock item is a booster or speed input of
//     something the subject makes and a required input of nothing she makes;
//   - and when those hold, the elective line does render (the need line does not).
//
// cook_out_of_salt_vendor_stocked keeps it non-vacuous.
func TestGoldensElectiveSupplyErrandMatchesItsItems(t *testing.T) {
	const needLine = "what you need is not to be had here"
	sawElective := false
	for _, sc := range perceptionScenarios {
		sc := sc
		snap, actorID, warrants := sc.build()
		p := Build(snap, actorID, warrants)
		if p.DutySteer == nil || !p.DutySteer.AtPost {
			continue
		}
		out := combinedPrompt(Render(p, DefaultRenderConfig()))
		hasElective := strings.Contains(out, electiveErrandLine)
		hasNeed := strings.Contains(out, needLine)
		if hasElective && hasNeed {
			t.Errorf("scenario %q: both the elective and the need line render", sc.name)
		}
		allBullets := len(walkToBulletRE.FindAllString(out, -1))
		restockBullets := 0
		if strings.Contains(out, "## Restocking\n") {
			restockBullets = len(walkToBulletRE.FindAllString(sectionOf(out, "## Restocking"), -1))
		}
		kinds := p.Restocking.walkToKinds()
		qualifies := allBullets > 0 && allBullets == restockBullets && len(kinds) > 0
		if qualifies {
			// Independent re-derivation from the fixture, not via electiveOnlyInputs.
			actor := snap.Actors[actorID]
			required, elective := map[sim.ItemKind]bool{}, map[sim.ItemKind]bool{}
			if actor != nil && actor.RestockPolicy != nil {
				for _, e := range actor.RestockPolicy.Restock {
					if e.Source != sim.RestockSourceProduce || snap.Recipes[e.Item] == nil {
						continue
					}
					r := snap.Recipes[e.Item]
					for _, in := range r.Inputs {
						required[in.Item] = true
					}
					for _, bi := range r.BoostInputs {
						if bi.Qty > 0 && bi.BonusQty > 0 {
							elective[bi.Item] = true
						}
					}
					for _, si := range r.SpeedInputs {
						if si.Qty > 0 && si.RatePct > 100 {
							elective[si.Item] = true
						}
					}
				}
			}
			for _, k := range kinds {
				if !elective[k] || required[k] {
					qualifies = false
				}
			}
		}
		if hasElective {
			sawElective = true
		}
		// The forage forms deliberately win over both supply forms.
		if p.DutySteer.ForageErrand != ForageErrandNone {
			if hasElective || hasNeed {
				t.Errorf("scenario %q: a supply form renders beside the forage form", sc.name)
			}
			continue
		}
		// Assert the expected form outright: elective for qualifying bullets, need
		// for any other walk-to bullet, neither with no bullet. A qualifying prompt
		// that fell back to the bare stay line fails here too.
		switch {
		case allBullets == 0:
			if hasElective || hasNeed {
				t.Errorf("scenario %q: a supply form renders with no walk-to bullet (elective=%v need=%v)", sc.name, hasElective, hasNeed)
			}
		case qualifies:
			if !hasElective {
				t.Errorf("scenario %q: walk-to items only boost what she makes, but the elective form is absent (need=%v; kinds %v)", sc.name, hasNeed, kinds)
			}
		default:
			if !hasNeed {
				t.Errorf("scenario %q: a walk-to bullet that is not booster-only, but the need form is absent (elective=%v; bullets %d, in Restocking %d, kinds %v)",
					sc.name, hasElective, allBullets, restockBullets, kinds)
			}
		}
	}
	if !sawElective {
		t.Fatal("no scenario renders the elective errand line; the invariant checks nothing")
	}
}

// TestWalkToKindsMatchesRenderedBullets pins walkToKinds item by item against
// renderRestocking (code_review, LLM-1008): with one vendor per item, each item it
// returns is one rendered walk-to bullet. TestHasWalkToSupplierMatchesRenderedBullet
// only checks "any vs none"; if the selector dropped a required item the renderer
// still sends her out for, the elective check would wrongly see a booster-only trip.
func TestWalkToKindsMatchesRenderedBullets(t *testing.T) {
	vendor := []RestockVendor{{StructureLabel: "The General Store", StructureID: "general_store"}}
	cases := []struct {
		name string
		view *RestockingView
	}{
		{"booster and required input both remote", &RestockingView{Items: []RestockItemView{
			{Kind: "salt", ItemLabel: "salt", Cap: 6, Vendors: vendor},
			{Kind: "milk", ItemLabel: "milk", Cap: 6, Vendors: vendor},
		}}},
		{"one remote, one co-present", &RestockingView{Items: []RestockItemView{
			{Kind: "salt", ItemLabel: "salt", Cap: 6, Vendors: vendor},
			{Kind: "milk", ItemLabel: "milk", Cap: 6, CoPresentSeller: "Josiah Thorne", SellerStock: 6},
		}}},
		{"one remote, one blocked", &RestockingView{Items: []RestockItemView{
			{Kind: "salt", ItemLabel: "salt", Cap: 6, Vendors: vendor},
			{Kind: "milk", ItemLabel: "milk", Blocked: []RestockBlockedSupplier{{StructureLabel: "The General Store", Reason: restockBlockShut}}},
		}}},
		{"one remote, one pending offer", &RestockingView{Items: []RestockItemView{
			{Kind: "salt", ItemLabel: "salt", Cap: 6, Vendors: vendor},
			{Kind: "milk", ItemLabel: "milk", Cap: 6, CoPresentSeller: "Josiah Thorne", PendingOfferToCoPresentSeller: true, Vendors: vendor},
		}}},
	}
	for _, c := range cases {
		var b strings.Builder
		renderRestocking(&b, c.view)
		bullets := len(walkToBulletRE.FindAllString(b.String(), -1))
		if kinds := c.view.walkToKinds(); len(kinds) != bullets {
			t.Errorf("%s: walkToKinds = %v (%d) but %d walk-to bullet(s) render:\n%s", c.name, kinds, len(kinds), bullets, b.String())
		}
	}
}
