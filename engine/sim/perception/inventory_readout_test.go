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
	av := buildActorView(snap, snap.Actors["josiah"])
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
	if av := buildActorView(snap, snap.Actors["josiah"]); av.Inventory != nil {
		t.Errorf("empty inventory should yield nil view, got %+v", av.Inventory)
	}
	// All-zero quantities also collapse to nil.
	snap2 := invSnap(map[sim.ItemKind]int{"bread": 0}, nil)
	if av := buildActorView(snap2, snap2.Actors["josiah"]); av.Inventory != nil {
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
		av := buildActorView(snap, snap.Actors["josiah"])
		if len(av.Inventory) != 2 || av.Inventory[0].kind != "apple_a" || av.Inventory[1].kind != "apple_b" {
			t.Fatalf("nondeterministic/tie-break order: %+v", av.Inventory)
		}
	}
}

func TestBuildInventoryView_FallsBackToRawKind(t *testing.T) {
	// No ItemKinds catalog → label falls back to the raw kind string.
	snap := invSnap(map[sim.ItemKind]int{"iron_ingot": 3}, nil)
	av := buildActorView(snap, snap.Actors["josiah"])
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

func TestRenderActor_NoCarryingLineWhenEmpty(t *testing.T) {
	var b strings.Builder
	renderActor(&b, ActorView{State: sim.StateIdle})
	if strings.Contains(b.String(), "You are carrying") {
		t.Errorf("empty inventory must render no carrying line, got:\n%s", b.String())
	}
}
