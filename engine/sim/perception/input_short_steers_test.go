package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// input_short_steers_test.go — LLM-635 unit coverage for the pieces the golden
// composes: missingProduceInputs, the two sourceability arms of the mend steer,
// and the order-book "no X for it" clause. Fixtures reuse the LLM-608 worn-forge
// world (worn_forge_golden_test.go): a degraded smith making nails from water.

func TestMissingProduceInputs(t *testing.T) {
	snap, smithID := wornForgeSnapshot(wornForgeSmith(0), 91)
	smith := snap.Actors[smithID]

	if got := missingProduceInputs(snap, smith, "nail"); len(got) != 1 || got[0] != "Water" {
		t.Errorf("out of water: got %v, want [Water]", got)
	}
	if got := missingProduceInputs(snap, smith, "water"); got != nil {
		t.Errorf("inputless recipe: got %v, want nil", got)
	}
	if got := missingProduceInputs(snap, smith, "no-such-good"); got != nil {
		t.Errorf("unknown recipe: got %v, want nil", got)
	}
	smith.Inventory["water"] = 1
	if got := missingProduceInputs(snap, smith, "nail"); got != nil {
		t.Errorf("one batch of water on hand: got %v, want nil", got)
	}
	// Two short inputs come back sorted by label, whatever the recipe order.
	snap.Recipes["nail"].Inputs = []sim.RecipeInput{{Item: "water", Qty: 2}, {Item: "iron", Qty: 1}}
	snap.ItemKinds["iron"] = &sim.ItemKindDef{Name: "iron", DisplayLabel: "Bar iron"}
	if got := missingProduceInputs(snap, smith, "nail"); len(got) != 2 || got[0] != "Bar iron" || got[1] != "Water" {
		t.Errorf("two short inputs: got %v, want [Bar iron Water]", got)
	}
	if got := missingInputsPhrase([]string{"Bar iron", "Water"}); got != "Bar iron or Water" {
		t.Errorf("phrase = %q, want %q", got, "Bar iron or Water")
	}
	if got := missingProduceInputs(nil, smith, "nail"); got != nil {
		t.Errorf("nil snapshot: got %v, want nil", got)
	}
}

// The mend steer's sourceability: an actionable buy path OR an actionable own
// forage source counts; a bare forage policy entry with no remembered source does
// NOT (it permits a gather without establishing one — code_review); a dry village
// counts as nothing; a mixed shortage counts per input.
func TestBuildStallRepair_NailInputShortArms(t *testing.T) {
	const (
		forageNone   = iota // no forage entry
		forageEntry         // a `forage water` entry, no source remembered
		forageSpring        // the entry AND an owned, remembered, stocked spring
	)
	build := func(water, sellerWater int, forage int) (*StallRepairView, *sim.Snapshot, sim.ActorID) {
		smith := wornForgeSmith(water)
		if forage != forageNone {
			smith.RestockPolicy.Restock = append(smith.RestockPolicy.Restock,
				sim.RestockEntry{Item: "water", Source: sim.RestockSourceForage, Max: 12})
		}
		snap, smithID := wornForgeSnapshot(smith, 91)
		snap.Actors["josiah"].Inventory["water"] = sellerWater
		if forage == forageSpring {
			addOwnedSpring(snap, smith, smithID)
		}
		return buildStallRepair(snap, smithID, snap.Actors[smithID]), snap, smithID
	}

	v, _, _ := build(0, 20, forageNone)
	if v == nil || !v.MakesNails {
		t.Fatalf("fixture drift: want a MakesNails view, got %+v", v)
	}
	if len(v.NailInputsShort) != 1 || v.NailInputsShort[0] != "Water" || v.NailInputsSourceable != 1 {
		t.Errorf("stocked seller: short=%v sourceable=%d, want [Water] 1", v.NailInputsShort, v.NailInputsSourceable)
	}

	v, _, _ = build(0, 0, forageNone)
	if len(v.NailInputsShort) != 1 || v.NailInputsSourceable != 0 {
		t.Errorf("dry seller: short=%v sourceable=%d, want [Water] 0", v.NailInputsShort, v.NailInputsSourceable)
	}

	v, _, _ = build(0, 0, forageEntry)
	if v.NailInputsSourceable != 0 {
		t.Errorf("dry seller + bare forage entry: sourceable=%d, want 0 (a policy entry is not a source)", v.NailInputsSourceable)
	}

	v, snap, smithID := build(0, 0, forageSpring)
	if v.NailInputsSourceable != 1 {
		t.Errorf("dry seller + own stocked spring: sourceable=%d, want 1 (he can gather it himself)", v.NailInputsSourceable)
	}
	// And the forage cue really would render for it — the where/how the steer leans on.
	if fv := buildForage(snap, smithID, snap.Actors[smithID], false); fv == nil || !fv.Actionable() {
		t.Errorf("own stocked spring: buildForage = %+v, want an actionable view", fv)
	}

	v, _, _ = build(1, 0, forageNone)
	if len(v.NailInputsShort) != 0 {
		t.Errorf("water on hand: short=%v, want none (the plain forge-your-own steer applies)", v.NailInputsShort)
	}

	// Mixed shortage: water sourceable (stocked seller), iron not (nobody sells it).
	smith := wornForgeSmith(0)
	snap, smithID = wornForgeSnapshot(smith, 91)
	snap.Recipes["nail"].Inputs = []sim.RecipeInput{{Item: "water", Qty: 1}, {Item: "iron", Qty: 1}}
	snap.ItemKinds["iron"] = &sim.ItemKindDef{Name: "iron", DisplayLabel: "Bar iron"}
	v = buildStallRepair(snap, smithID, snap.Actors[smithID])
	if len(v.NailInputsShort) != 2 || v.NailInputsSourceable != 1 {
		t.Errorf("mixed shortage: short=%v sourceable=%d, want [Bar iron Water] 1", v.NailInputsShort, v.NailInputsSourceable)
	}
}

// addOwnedSpring gives the smith his own water source: an owned, yield-only
// (forage-to-sell) object with 20 ripe, remembered as a gather:water place — the
// LLM-77 seed the forage cue's owned-bush arm reads. Placed off his tile so it is a
// walk destination, not a bush he already stands at.
func addOwnedSpring(snap *sim.Snapshot, smith *sim.ActorSnapshot, smithID sim.ActorID) {
	zero := 0
	snap.VillageObjects["forge_spring"] = &sim.VillageObject{
		ID:            "forge_spring",
		DisplayName:   "Spring",
		Pos:           sim.WorldPos{X: 800, Y: 640},
		OwnerActorID:  smithID,
		LoiterOffsetX: &zero,
		LoiterOffsetY: &zero,
		Refreshes: []*sim.ObjectRefresh{
			{Amount: 0, GatherItem: "water", AvailableQuantity: intp(20), MaxQuantity: intp(20)},
		},
	}
	smith.KnownPlaces = map[sim.PlaceRef]*sim.KnownPlace{
		"forge_spring": {Ref: "forge_spring", Kind: sim.PlaceKindObject, Affordances: []string{"gather:water"}},
	}
}

// The render never says "forge what you're short" while an input is missing, names
// the input, keeps the "nails are your own work" anchor the LLM-446 invariant reads,
// and stays free of the token "buy" on both arms.
func TestRenderStallRepair_NailInputShort(t *testing.T) {
	for _, tc := range []struct {
		name       string
		short      []string
		sourceable int
		want       string
	}{
		{"path exists", []string{"Water"}, 1, "no Water to forge them with: see to that first, then mend it here."},
		{"none to be had", []string{"Water"}, 0, "no Water to forge them with, and none is to be had just now."},
		{"mixed", []string{"Bar iron", "Water"}, 1, "no Bar iron or Water to forge them with, and only some of it is to be had just now: see to what you can, then mend it here."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			renderStallRepair(&b, &StallRepairView{
				Degraded: true, NailsNeeded: 5, NailsHeld: 1, Name: "Blacksmith",
				MakesNails: true, NailInputsShort: tc.short, NailInputsSourceable: tc.sourceable,
			})
			out := b.String()
			if !strings.Contains(out, tc.want) {
				t.Errorf("missing %q in:\n%s", tc.want, out)
			}
			if strings.Contains(out, "forge what you're short") {
				t.Errorf("instructs a withdrawn produce tool:\n%s", out)
			}
			if !strings.Contains(out, "nails are your own work") {
				t.Errorf("lost the own-work anchor:\n%s", out)
			}
			if strings.Contains(out, "buy") {
				t.Errorf("token 'buy' in the sole-nail-producer section:\n%s", out)
			}
		})
	}
	// Foil: with no missing input the plain steer is unchanged.
	var b strings.Builder
	renderStallRepair(&b, &StallRepairView{Degraded: true, NailsNeeded: 5, NailsHeld: 1, Name: "Blacksmith", MakesNails: true})
	if !strings.Contains(b.String(), "forge what you're short, then mend it here.") {
		t.Errorf("inputs on hand: plain forge-your-own steer missing:\n%s", b.String())
	}
}

// The order-book line names the missing input right after "yet to make it", and
// stays passive (no deliver_order instruction) exactly as before.
func TestRenderOrdersReady_AwaitingMake_NamesMissingInput(t *testing.T) {
	var b strings.Builder
	renderPendingDeliveriesFromMe(&b, []OrderView{
		{ID: 4701, Item: "nail", Qty: 5, BuyerName: "Josiah Thorne", AwaitingMake: true,
			MissingInputs: []string{"Water"}, ExpiresAt: time.Now().Add(2 * time.Hour)},
	}, startOfUTCDay(time.Now()), time.Time{})
	out := b.String()
	if !strings.Contains(out, "you've yet to make it, and you've no Water for it") {
		t.Errorf("order line does not name the missing input:\n%s", out)
	}
	if strings.Contains(out, "deliver_order") {
		t.Errorf("an unmakeable order must stay passive:\n%s", out)
	}
}

// buildPendingOrderViews fills MissingInputs only for an awaiting-make order,
// from the seller's own recipe and inventory.
func TestBuildPendingOrderViews_AwaitingMake_MissingInputs(t *testing.T) {
	snap, smithID, _ := smithOutOfWaterOwesNailsNoSeller()
	fromMe, _ := buildPendingOrderViews(snap, smithID)
	if len(fromMe) != 1 || !fromMe[0].AwaitingMake {
		t.Fatalf("fromMe = %+v, want one awaiting-make order", fromMe)
	}
	if got := fromMe[0].MissingInputs; len(got) != 1 || got[0] != "Water" {
		t.Errorf("MissingInputs = %v, want [Water]", got)
	}
	// Once he holds the water the make is possible: nothing to name.
	snap.Actors[smithID].Inventory["water"] = 5
	fromMe, _ = buildPendingOrderViews(snap, smithID)
	if got := fromMe[0].MissingInputs; len(got) != 0 {
		t.Errorf("water on hand: MissingInputs = %v, want none", got)
	}
}
