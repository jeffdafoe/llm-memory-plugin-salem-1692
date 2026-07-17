package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// inventory_readout_test.go — ZBBS-HOME-361. The standing "You are carrying: …"
// line: buildInventoryView resolution/sort, and the render shape. The fix that
// restored v1's inventory readout v2 dropped — so an NPC can see its own goods
// (to eat, to sell) regardless of whether a need is pressing.

func invSnap(inv map[sim.ItemKind]int, kinds map[sim.ItemKind]*sim.ItemKindDef) *sim.Snapshot {
	return &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"josiah": {State: sim.StateIdle, Needs: map[sim.NeedKey]int{"hunger": 5}, Inventory: inv},
		},
		ItemKinds: kinds,
	}
}

func TestBuildInventoryView_ResolvesSortsAndFiltersByLabel(t *testing.T) {
	kinds := map[sim.ItemKind]*sim.ItemKindDef{
		"bread":  {Name: "bread", DisplayLabel: "bread"},
		"cheese": {Name: "cheese", DisplayLabel: "cheese"},
		"flour":  {Name: "flour", DisplayLabel: "flour"},
	}
	snap := invSnap(map[sim.ItemKind]int{"cheese": 24, "bread": 65, "flour": 0}, kinds)
	av := buildActorView(snap, "josiah", snap.Actors["josiah"])
	if len(av.Inventory) != 2 {
		t.Fatalf("want 2 items (flour qty 0 dropped), got %+v", av.Inventory)
	}
	// Sorted by label ascending: bread before cheese.
	if av.Inventory[0].Label != "bread" || av.Inventory[0].Qty != 65 {
		t.Errorf("item[0] = %+v, want bread x65", av.Inventory[0])
	}
	if av.Inventory[1].Label != "cheese" || av.Inventory[1].Qty != 24 {
		t.Errorf("item[1] = %+v, want cheese x24", av.Inventory[1])
	}
}

func TestBuildInventoryView_EmptyIsNil(t *testing.T) {
	snap := invSnap(map[sim.ItemKind]int{}, nil)
	if av := buildActorView(snap, "josiah", snap.Actors["josiah"]); av.Inventory != nil {
		t.Errorf("empty inventory should yield nil view, got %+v", av.Inventory)
	}
	// All-zero quantities also collapse to nil.
	snap2 := invSnap(map[sim.ItemKind]int{"bread": 0}, nil)
	if av := buildActorView(snap2, "josiah", snap2.Actors["josiah"]); av.Inventory != nil {
		t.Errorf("all-zero inventory should yield nil view, got %+v", av.Inventory)
	}
}

// Two item kinds sharing a display label must order deterministically via the
// raw ItemKind tie-break — the same map-iteration nondeterminism class that has
// bitten nearby perception code (cf. satiation own-stock). (code_review)
func TestBuildInventoryView_DuplicateLabelDeterministicTieBreak(t *testing.T) {
	kinds := map[sim.ItemKind]*sim.ItemKindDef{
		"apple_a": {Name: "apple_a", DisplayLabel: "apple"},
		"apple_b": {Name: "apple_b", DisplayLabel: "apple"},
	}
	for i := 0; i < 25; i++ {
		snap := invSnap(map[sim.ItemKind]int{"apple_b": 1, "apple_a": 1}, kinds)
		av := buildActorView(snap, "josiah", snap.Actors["josiah"])
		if len(av.Inventory) != 2 || av.Inventory[0].kind != "apple_a" || av.Inventory[1].kind != "apple_b" {
			t.Fatalf("nondeterministic/tie-break order: %+v", av.Inventory)
		}
	}
}

func TestBuildInventoryView_FallsBackToRawKind(t *testing.T) {
	// No ItemKinds catalog → label falls back to the raw kind string.
	snap := invSnap(map[sim.ItemKind]int{"iron_ingot": 3}, nil)
	av := buildActorView(snap, "josiah", snap.Actors["josiah"])
	if len(av.Inventory) != 1 || av.Inventory[0].Label != "iron_ingot" {
		t.Errorf("want raw-kind fallback label 'iron_ingot', got %+v", av.Inventory)
	}
}

func TestRenderActor_CarryingLine(t *testing.T) {
	var b strings.Builder
	renderActor(&b, ActorView{
		State: sim.StateIdle,
		Inventory: []InventoryItem{
			{Label: "bread", Qty: 65},
			{Label: "cheese", Qty: 24},
		},
	})
	out := b.String()
	if !strings.Contains(out, "You are carrying: bread (x65), cheese (x24).") {
		t.Errorf("carrying line missing/!exact:\n%s", out)
	}
}

// LLM-339: the carry line renders the qty-aware count noun ("flasks of water"),
// not the bare display label ("Water") that left NPCs inventing a container
// ("buckets of water"). Driven end-to-end through buildActorView so the
// build-side resolution (ItemKindDef.CountNoun via buildInventoryView) is
// covered alongside the render.
func TestRenderActor_CarryingLine_CountNoun(t *testing.T) {
	kinds := map[sim.ItemKind]*sim.ItemKindDef{
		"water": {
			Name:                 "water",
			DisplayLabel:         "Water",
			DisplayLabelSingular: "flask of water",
			DisplayLabelPlural:   "flasks of water",
		},
	}
	cases := []struct {
		qty  int
		want string
	}{
		{20, "You are carrying: flasks of water (x20)."},
		{1, "You are carrying: flask of water (x1)."},
	}
	for _, tc := range cases {
		snap := invSnap(map[sim.ItemKind]int{"water": tc.qty}, kinds)
		av := buildActorView(snap, "josiah", snap.Actors["josiah"])
		var b strings.Builder
		renderActor(&b, av)
		if out := b.String(); !strings.Contains(out, tc.want) {
			t.Errorf("qty %d: want %q in carry line, got:\n%s", tc.qty, tc.want, out)
		}
	}
}

func TestRenderActor_NoCarryingLineWhenEmpty(t *testing.T) {
	var b strings.Builder
	renderActor(&b, ActorView{State: sim.StateIdle})
	if strings.Contains(b.String(), "You are carrying") {
		t.Errorf("empty inventory must render no carrying line, got:\n%s", b.String())
	}
}

// LLM-166: an INEDIBLE carried ingredient is annotated with what it's used to
// produce, so a hungry model doesn't read a food-named non-food (raw "Meat") as
// a meal. Edible items (the satiation cue owns those) and non-ingredient
// materials carry no annotation.
func TestBuildInventoryView_IngredientUseAnnotation(t *testing.T) {
	kinds := map[sim.ItemKind]*sim.ItemKindDef{
		// food, no Satisfies -> inedible raw (a cooking input)
		"meat": {Name: "meat", DisplayLabel: "Meat"},
		// edible -> consumable, so no use annotation even though it's a stew input
		"milk": {Name: "milk", DisplayLabel: "Milk", Satisfies: []sim.ItemSatisfaction{{Attribute: "thirst", Immediate: 4}}},
		// inedible but no recipe uses it -> nothing to say
		"horseshoe": {Name: "horseshoe", DisplayLabel: "Horseshoe"},
	}
	snap := invSnap(map[sim.ItemKind]int{"meat": 7, "milk": 4, "horseshoe": 2}, kinds)
	snap.RecipeUses = map[sim.ItemKind][]sim.ItemKind{
		"meat": {"stew"},
		"milk": {"stew"}, // edible -> still suppressed by the Consumable() gate
	}
	av := buildActorView(snap, "josiah", snap.Actors["josiah"])
	got := map[sim.ItemKind]string{}
	for _, it := range av.Inventory {
		got[it.kind] = it.Use
	}
	if got["meat"] != "used to produce stew" {
		t.Errorf("meat.Use = %q, want %q", got["meat"], "used to produce stew")
	}
	if got["milk"] != "" {
		t.Errorf("milk is edible -> want no use annotation, got %q", got["milk"])
	}
	if got["horseshoe"] != "" {
		t.Errorf("horseshoe is in no recipe -> want no annotation, got %q", got["horseshoe"])
	}
}

func TestRenderActor_CarryingLine_IngredientUse(t *testing.T) {
	var b strings.Builder
	renderActor(&b, ActorView{
		State: sim.StateIdle,
		Inventory: []InventoryItem{
			{Label: "Cheese", Qty: 15},
			{Label: "Meat", Qty: 7, Use: "used to produce stew"},
		},
	})
	out := b.String()
	// The use folds into the quantity parens so the comma list stays unambiguous.
	if !strings.Contains(out, "Meat (x7, used to produce stew)") {
		t.Errorf("want folded use annotation, got:\n%s", out)
	}
	// An unannotated item keeps the bare form.
	if !strings.Contains(out, "Cheese (x15)") {
		t.Errorf("unannotated item should stay bare, got:\n%s", out)
	}
}
