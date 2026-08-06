package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// restock_degraded_test.go — LLM-608. A worn business is shut for RESTOCKING its
// shelves (LLM-304), and that used to shut it out of buying anything at all,
// including the inputs its own production consumes. That closed the only exit
// LLM-446 built: the smith mends his forge with nails he forges himself, so once
// the nail recipe took an input he could not buy, nothing could reach the nail,
// the nail could not reach the mend, and the mend was the only thing that would
// have let him buy the input. The live case was Ezekiel Crane, 2026-08-06.
//
// These fixtures rebuild that shape: a maker whose recipe needs a bought input,
// whose own business is degraded, who also resells something unrelated.

// degradedSmith returns a maker of `output` who BUYS `input` for it and also
// resells `resale`, holding one of each. StallDegradedProducePct is the live
// setting (50 — the LLM-446 limping shop), not the legacy 0 full block.
func degradedSmith(output, input, resale sim.ItemKind) (*sim.ActorSnapshot, *sim.Snapshot) {
	subj := &sim.ActorSnapshot{
		Coins:     50,
		Inventory: map[sim.ItemKind]int{input: 1, resale: 1},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: output, Source: sim.RestockSourceProduce, Max: 60},
			{Item: input, Source: sim.RestockSourceBuy, Max: 10},
			{Item: resale, Source: sim.RestockSourceBuy, Max: 10},
		}},
	}
	snap := &sim.Snapshot{
		Actors:    map[sim.ActorID]*sim.ActorSnapshot{"smith": subj},
		ItemKinds: productionCatalog(),
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			output: {
				OutputItem: output, OutputQty: 30, RateQty: 30, RatePerHours: 6,
				Inputs: []sim.RecipeInput{{Item: input, Qty: 1}},
			},
		},
		RestockReorderPct:         25,
		StallWearDegradeThreshold: 600,
		StallDegradedProducePct:   50,
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"smiths_shop": {ID: "smiths_shop", OwnerActorID: "smith", Tags: []string{sim.TagBusiness}, Wear: 650},
		},
	}
	addSnapSupplier(snap, input)
	addSnapSupplier(snap, resale)
	return subj, snap
}

// A degraded keeper still sees the inputs his OWN production consumes, and does
// NOT see his resale stock. This is the fix: the water that makes the nail that
// mends the forge stays reachable, while the shelves keep waiting on the mending.
func TestBuildRestocking_DegradedKeepsProductionInputs(t *testing.T) {
	subj, snap := degradedSmith("stew", "skillet", "carrots")

	v := buildRestocking(snap, "smith", subj)
	if v == nil {
		t.Fatal("a degraded maker must still see the inputs his own production consumes")
	}
	if !v.Degraded {
		t.Error("view must be marked Degraded so render drops the shelf-restocking lead")
	}
	if len(v.Items) != 1 {
		t.Fatalf("want exactly the production input, got %d items: %+v", len(v.Items), v.Items)
	}
	if v.Items[0].kind != "skillet" {
		t.Errorf("item = %q, want the production input skillet", v.Items[0].kind)
	}
	// The resale good is the control: it is equally low, with an equally good
	// supplier, and must stay suppressed — LLM-304's actual intent.
	for _, it := range v.Items {
		if it.kind == "carrots" {
			t.Error("resale stock must stay suppressed while the business is worn")
		}
	}
}

// The whole section still goes silent when the keeper's recipes need nothing
// bought in — there is no production input to exempt, so LLM-304 stands whole.
func TestBuildRestocking_DegradedNoProductionInputsStaysSilent(t *testing.T) {
	subj, snap := degradedSmith("stew", "skillet", "carrots")
	// Make the stew recipe inputless: now `skillet` is plain resale stock too.
	snap.Recipes["stew"].Inputs = nil

	if v := buildRestocking(snap, "smith", subj); v != nil {
		t.Errorf("no production input to exempt — the section must stay suppressed, got %+v", v)
	}
}

// At StallDegradedProducePct 0 the legacy LLM-304 full block applies: production
// is refused outright, so buying an input it cannot consume is exactly the
// unactionable errand the cue exists to avoid. Same carve-out buildStallRepair
// makes for its forge-your-own-nails steer.
func TestBuildRestocking_DegradedProduceBlockedStaysSilent(t *testing.T) {
	subj, snap := degradedSmith("stew", "skillet", "carrots")
	snap.StallDegradedProducePct = 0

	if v := buildRestocking(snap, "smith", subj); v != nil {
		t.Errorf("production hard-blocked — the section must stay suppressed, got %+v", v)
	}
}

// A healthy business is untouched: both the production input and the resale good
// render, which is what makes the degraded case above a narrowing rather than a
// rewrite.
func TestBuildRestocking_UndegradedKeepsResaleStock(t *testing.T) {
	subj, snap := degradedSmith("stew", "skillet", "carrots")
	snap.VillageObjects["smiths_shop"].Wear = 0

	v := buildRestocking(snap, "smith", subj)
	if v == nil {
		t.Fatal("a healthy keeper must still see his whole buy directory")
	}
	if v.Degraded {
		t.Error("Degraded must be false for an unworn business")
	}
	if len(v.Items) != 2 {
		t.Fatalf("want both the input and the resale good, got %d: %+v", len(v.Items), v.Items)
	}
}

// The render must not tell a keeper to restock his shelves in the same prompt
// that "## Your business" tells him he cannot until he mends — the LLM-64
// contradiction trap. The degraded lead says what is true instead: the shelves
// wait, the makings do not.
func TestRenderRestocking_DegradedLeadNamesTheMakingsNotTheShelves(t *testing.T) {
	subj, snap := degradedSmith("stew", "skillet", "carrots")

	var b strings.Builder
	renderRestocking(&b, buildRestocking(snap, "smith", subj))
	out := b.String()

	if !strings.Contains(out, "Your shelves must wait on the mending") {
		t.Errorf("degraded lead missing; got:\n%s", out)
	}
	if strings.Contains(out, "Your shop stock of these bought-in goods") {
		t.Errorf("the shelf-restocking lead must not render while worn; got:\n%s", out)
	}
}
