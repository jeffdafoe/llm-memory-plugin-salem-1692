package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// satiation_test.go — ZBBS-HOME-304. Covers the firing gate (per-need red
// threshold), the consume-first own-stock line, vendor seller cues via the
// shared finder, tiredness-isolation (tiredness items don't leak into the
// eat/drink section), and the render shape.

// foodDrinkCatalog: bread/stew ease hunger, water/ale ease thirst, coca_tea
// eases tiredness (the isolation control — must NOT appear in satiation).
func foodDrinkCatalog() map[sim.ItemKind]*sim.ItemKindDef {
	return map[sim.ItemKind]*sim.ItemKindDef{
		"bread":    {Name: "bread", DisplayLabel: "bread", Category: sim.ItemCategoryFood, Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger", Immediate: 6}}},
		"stew":     {Name: "stew", DisplayLabel: "stew", Category: sim.ItemCategoryFood, Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger", Immediate: 12}}},
		"water":    {Name: "water", DisplayLabel: "water", Category: sim.ItemCategoryDrink, Satisfies: []sim.ItemSatisfaction{{Attribute: "thirst", Immediate: 5}}},
		"coca_tea": {Name: "coca_tea", DisplayLabel: "coca tea", Category: sim.ItemCategoryDrink, Satisfies: []sim.ItemSatisfaction{{Attribute: "tiredness", Immediate: 12}}},
	}
}

func TestBuildSatiation_NotPressing_Nil(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Needs:     map[sim.NeedKey]int{"hunger": 1, "thirst": 1},
		Inventory: map[sim.ItemKind]int{"bread": 3},
	}
	snap := &sim.Snapshot{
		Actors:    map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj},
		ItemKinds: foodDrinkCatalog(),
	}
	if v := buildSatiation(snap, "ezekiel", subj); v != nil {
		t.Errorf("want nil when no consumable need is pressing, got %+v", v)
	}
}

func TestBuildSatiation_OwnStockHunger(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Needs:     map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold},
		Inventory: map[sim.ItemKind]int{"bread": 3, "stew": 1, "coca_tea": 5}, // coca_tea is tiredness — must not appear
	}
	snap := &sim.Snapshot{
		Actors:    map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj},
		ItemKinds: foodDrinkCatalog(),
	}
	v := buildSatiation(snap, "ezekiel", subj)
	if v == nil || len(v.Needs) != 1 {
		t.Fatalf("want 1 pressing need (hunger), got %+v", v)
	}
	n := v.Needs[0]
	if n.Need != "hunger" || n.Verb != "eat" {
		t.Errorf("need/verb = %q/%q, want hunger/eat", n.Need, n.Verb)
	}
	if len(n.OwnStock) != 2 {
		t.Fatalf("want 2 own-stock satisfiers (bread, stew; coca tea excluded), got %+v", n.OwnStock)
	}
	// Strongest first: stew (12) before bread (6).
	if n.OwnStock[0].Label != "stew" || n.OwnStock[0].Magnitude != 12 || n.OwnStock[1].Label != "bread" {
		t.Errorf("own-stock order wrong (want stew then bread): %+v", n.OwnStock)
	}
}

// Two own-stock item kinds with the SAME display label and SAME magnitude must
// order deterministically via the ItemKind tie-break, since Inventory is a map.
// (code_review)
func TestBuildSatiation_OwnStockDeterministicTieBreak(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Needs:     map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold},
		Inventory: map[sim.ItemKind]int{"ration_a": 2, "ration_b": 2},
	}
	// Both kinds: same label "ration", same hunger magnitude 5 — only ItemKind differs.
	cat := map[sim.ItemKind]*sim.ItemKindDef{
		"ration_a": {Name: "ration_a", DisplayLabel: "ration", Category: sim.ItemCategoryFood, Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger", Immediate: 5}}},
		"ration_b": {Name: "ration_b", DisplayLabel: "ration", Category: sim.ItemCategoryFood, Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger", Immediate: 5}}},
	}
	snap := &sim.Snapshot{
		Actors:    map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj},
		ItemKinds: cat,
	}
	var first []sim.ItemKind
	for i := 0; i < 25; i++ {
		v := buildSatiation(snap, "ezekiel", subj)
		if v == nil || len(v.Needs) != 1 || len(v.Needs[0].OwnStock) != 2 {
			t.Fatalf("want 2 own-stock items, got %+v", v)
		}
		got := []sim.ItemKind{v.Needs[0].OwnStock[0].kind, v.Needs[0].OwnStock[1].kind}
		if first == nil {
			first = got
			continue
		}
		if got[0] != first[0] || got[1] != first[1] {
			t.Fatalf("nondeterministic own-stock order: first=%v now=%v", first, got)
		}
	}
	// ItemKind ascending: ration_a before ration_b.
	if first[0] != "ration_a" || first[1] != "ration_b" {
		t.Errorf("tie-break order = %v, want [ration_a ration_b]", first)
	}
}

func TestBuildSatiation_VendorCueThirst(t *testing.T) {
	subj := &sim.ActorSnapshot{Needs: map[sim.NeedKey]int{"thirst": sim.DefaultThirstRedThreshold}}
	vendor := &sim.ActorSnapshot{WorkStructureID: "well_house", Inventory: map[sim.ItemKind]int{"water": 9}}
	snap := &sim.Snapshot{
		Actors:     map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj, "wally": vendor},
		Structures: map[sim.StructureID]*sim.Structure{"well_house": {ID: "well_house", DisplayName: "Well House"}},
		ItemKinds:  foodDrinkCatalog(),
	}
	v := buildSatiation(snap, "ezekiel", subj)
	if v == nil || len(v.Needs) != 1 {
		t.Fatalf("want 1 pressing need (thirst), got %+v", v)
	}
	n := v.Needs[0]
	if n.Need != "thirst" || n.Verb != "drink" {
		t.Errorf("need/verb = %q/%q, want thirst/drink", n.Need, n.Verb)
	}
	if len(n.OwnStock) != 0 {
		t.Errorf("want no own-stock (actor carries nothing), got %+v", n.OwnStock)
	}
	if len(n.Vendors) != 1 {
		t.Fatalf("want 1 vendor cue, got %+v", n.Vendors)
	}
	vd := n.Vendors[0]
	if vd.StructureLabel != "Well House" || vd.ItemLabel != "water" || vd.Magnitude != 5 || vd.CostText != "ask the seller" {
		t.Errorf("vendor cue wrong (no price history → ask the seller): %+v", vd)
	}
}

func TestBuildSatiation_VendorPriceFromPriceBook(t *testing.T) {
	subj := &sim.ActorSnapshot{Needs: map[sim.NeedKey]int{"thirst": sim.DefaultThirstRedThreshold}}
	vendor := &sim.ActorSnapshot{WorkStructureID: "well_house", Inventory: map[sim.ItemKind]int{"water": 9}}
	pb := sim.NewRingBuffer[sim.PriceObservation](4)
	pb.Push(sim.PriceObservation{BuyerID: "ezekiel", Amount: 1, Qty: 1, Consumers: 1, At: time.Now().UTC()})
	snap := &sim.Snapshot{
		Actors:     map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj, "wally": vendor},
		Structures: map[sim.StructureID]*sim.Structure{"well_house": {ID: "well_house", DisplayName: "Well House"}},
		ItemKinds:  foodDrinkCatalog(),
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: "wally", Item: "water"}: pb,
		},
	}
	v := buildSatiation(snap, "ezekiel", subj)
	if v == nil || len(v.Needs) != 1 || len(v.Needs[0].Vendors) != 1 {
		t.Fatalf("want 1 vendor cue, got %+v", v)
	}
	if got := v.Needs[0].Vendors[0].CostText; got != "~1 coins" {
		t.Errorf("CostText = %q, want '~1 coins' (last-paid)", got)
	}
}

func TestBuildSatiation_BothNeeds_HungerFirst(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Needs:     map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold, "thirst": sim.DefaultThirstRedThreshold},
		Inventory: map[sim.ItemKind]int{"bread": 2, "water": 2},
	}
	snap := &sim.Snapshot{
		Actors:    map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj},
		ItemKinds: foodDrinkCatalog(),
	}
	v := buildSatiation(snap, "ezekiel", subj)
	if v == nil || len(v.Needs) != 2 {
		t.Fatalf("want 2 pressing needs, got %+v", v)
	}
	if v.Needs[0].Need != "hunger" || v.Needs[1].Need != "thirst" {
		t.Errorf("need order = %q,%q; want hunger,thirst", v.Needs[0].Need, v.Needs[1].Need)
	}
}

func TestRenderSatiation_NilAndEmpty(t *testing.T) {
	var b strings.Builder
	renderSatiation(&b, nil)
	renderSatiation(&b, &SatiationView{})
	if b.String() != "" {
		t.Errorf("nil/empty view should render nothing, got %q", b.String())
	}
}

func TestRenderSatiation_Bullets(t *testing.T) {
	var b strings.Builder
	renderSatiation(&b, &SatiationView{Needs: []SatiationNeedView{
		{
			Need: "hunger", Verb: "eat",
			OwnStock: []OwnStockItem{{Label: "stew", Magnitude: 12}, {Label: "bread", Magnitude: 6}},
			Vendors:  []SatiationVendor{{StructureLabel: "The Tavern", ItemLabel: "ale", Magnitude: 4, CostText: "~2 coins"}},
		},
	}})
	out := b.String()
	if !strings.Contains(out, "## What you can eat or drink") {
		t.Errorf("missing header: %q", out)
	}
	if !strings.Contains(out, "You have stew (~12), bread (~6) on hand — consume to eat.") {
		t.Errorf("own-stock line wrong: %q", out)
	}
	if !strings.Contains(out, "The Tavern — buy ale, eases hunger (~4), ~2 coins") {
		t.Errorf("vendor bullet wrong: %q", out)
	}
}
