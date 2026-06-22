package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

func foragePolicy(item sim.ItemKind, cap int) *sim.RestockPolicy {
	return &sim.RestockPolicy{Restock: []sim.RestockEntry{
		{Item: item, Source: sim.RestockSourceForage, Max: cap},
	}}
}

// forageBush builds an owned forage-to-sell bush: a finite, gatherable,
// yield-only (Amount 0) refresh row for item with `avail` ripe units.
func forageBush(owner sim.ActorID, item sim.ItemKind, avail int) *sim.VillageObject {
	a := avail
	m := 10
	return &sim.VillageObject{
		OwnerActorID: owner,
		Refreshes: []*sim.ObjectRefresh{
			{Attribute: "hunger", Amount: 0, GatherItem: item, AvailableQuantity: &a, MaxQuantity: &m},
		},
	}
}

func TestBuildForage_NoPolicy_Nil(t *testing.T) {
	subj := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"raspberries": 0}}
	snap := &sim.Snapshot{
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{"prudence": subj},
		RestockReorderPct: 25,
	}
	if v := buildForage(snap, "prudence", subj); v != nil {
		t.Fatalf("expected nil view with no RestockPolicy, got %+v", v)
	}
}

func TestBuildForage_DisabledPct_Nil(t *testing.T) {
	subj := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"raspberries": 0}, RestockPolicy: foragePolicy("raspberries", 10)}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{"prudence": subj},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"bushA": forageBush("prudence", "raspberries", 10),
		},
		RestockReorderPct: 0, // feature disabled
	}
	if v := buildForage(snap, "prudence", subj); v != nil {
		t.Fatalf("expected nil view when RestockReorderPct==0, got %+v", v)
	}
}

func TestBuildForage_AboveThreshold_Nil(t *testing.T) {
	// 5 of 10 = 50%, above the 25% reorder threshold → no cue.
	subj := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"raspberries": 5}, RestockPolicy: foragePolicy("raspberries", 10)}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{"prudence": subj},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"bushA": forageBush("prudence", "raspberries", 10),
		},
		RestockReorderPct: 25,
	}
	if v := buildForage(snap, "prudence", subj); v != nil {
		t.Fatalf("expected nil view above reorder threshold, got %+v", v)
	}
}

func TestBuildForage_LowStock_SurfacesOwnedBushes(t *testing.T) {
	// 2 of 10 = 20%, below 25% → low. Owns two raspberry bushes (4 + 10 ripe);
	// a third raspberry bush belongs to someone else and must be excluded.
	subj := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"raspberries": 2}, RestockPolicy: foragePolicy("raspberries", 10)}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{"prudence": subj},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"bushA": forageBush("prudence", "raspberries", 4),
			"bushB": forageBush("prudence", "raspberries", 10), // ripest → move handle
			"bushC": forageBush("other", "raspberries", 9),     // not hers
		},
		RestockReorderPct: 25,
	}
	v := buildForage(snap, "prudence", subj)
	if v == nil || len(v.Items) != 1 {
		t.Fatalf("expected one low item, got %+v", v)
	}
	it := v.Items[0]
	if it.CurrentQty != 2 || it.Cap != 10 {
		t.Errorf("on-hand/cap: got %d/%d want 2/10", it.CurrentQty, it.Cap)
	}
	if it.BushCount != 2 {
		t.Errorf("BushCount: got %d want 2 (the other's bush excluded)", it.BushCount)
	}
	if it.RipeUnits != 14 {
		t.Errorf("RipeUnits: got %d want 14", it.RipeUnits)
	}
	if it.MoveHandle != "bushB" {
		t.Errorf("MoveHandle: got %q want \"bushB\" (the ripest)", it.MoveHandle)
	}
}

func TestBuildForage_LowStock_NoOwnedBushes_Nil(t *testing.T) {
	// Low on raspberries but owns no raspberry bushes (only a blueberry one) →
	// nothing to point at, so no cue for raspberries.
	subj := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"raspberries": 1}, RestockPolicy: foragePolicy("raspberries", 10)}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{"prudence": subj},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"bushA": forageBush("prudence", "blueberries", 10),
		},
		RestockReorderPct: 25,
	}
	if v := buildForage(snap, "prudence", subj); v != nil {
		t.Fatalf("expected nil view when no owned bushes for the low item, got %+v", v)
	}
}

func TestBuildForage_NoneRipe_NoMoveHandle(t *testing.T) {
	// Owns bushes but all picked clean (0 ripe): still surface the section (she
	// knows it's low + she has a farm) but with no move handle.
	subj := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"raspberries": 0}, RestockPolicy: foragePolicy("raspberries", 10)}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{"prudence": subj},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"bushA": forageBush("prudence", "raspberries", 0),
		},
		RestockReorderPct: 25,
	}
	v := buildForage(snap, "prudence", subj)
	if v == nil || len(v.Items) != 1 {
		t.Fatalf("expected one item, got %+v", v)
	}
	if v.Items[0].RipeUnits != 0 || v.Items[0].MoveHandle != "" {
		t.Errorf("expected 0 ripe + empty move handle, got %d / %q", v.Items[0].RipeUnits, v.Items[0].MoveHandle)
	}
}

func TestRenderForage_LowStock(t *testing.T) {
	v := &ForageView{Items: []ForageItemView{
		{ItemLabel: "raspberries", CurrentQty: 2, Cap: 10, BushCount: 2, RipeUnits: 14, MoveHandle: "bushB"},
	}}
	var b strings.Builder
	renderForage(&b, v)
	out := b.String()
	for _, want := range []string{
		"## Your bushes to harvest",
		"raspberries: 2 on hand of 10 cap (room for 8 more)",
		"You own 2 bush(es)",
		"14 ripe to pick now",
		`structure_id "bushB"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderForage_Nil_NoOutput(t *testing.T) {
	var b strings.Builder
	renderForage(&b, nil)
	if b.Len() != 0 {
		t.Fatalf("expected empty render for nil view, got %q", b.String())
	}
}
