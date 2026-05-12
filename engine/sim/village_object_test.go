package sim_test

import (
	"context"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// TestAssetFindState exercises the per-state lookup.
func TestAssetFindState(t *testing.T) {
	a := &sim.Asset{
		ID: "lamp-iron",
		States: []sim.AssetState{
			{ID: 1, State: "unlit"},
			{ID: 2, State: "lit"},
		},
	}
	if got := a.FindState("lit"); got == nil || got.ID != 2 {
		t.Errorf("FindState(lit) = %v, want ID=2", got)
	}
	if got := a.FindState("absent"); got != nil {
		t.Errorf("FindState(absent) = %v, want nil", got)
	}
}

// TestAssetStateForTag picks the deterministic-lowest tagged state — this
// is the behavior world_phase's flip resolver depends on.
func TestAssetStateForTag(t *testing.T) {
	a := &sim.Asset{
		ID: "lamp-iron",
		States: []sim.AssetState{
			{ID: 5, State: "unlit", Tags: []string{"night-active"}},
			{ID: 2, State: "lit", Tags: []string{"day-active"}},
			{ID: 1, State: "default", Tags: []string{"day-active"}}, // lower ID, should win
		},
	}
	got := a.StateForTag("day-active")
	if got == nil {
		t.Fatal("StateForTag(day-active) = nil")
	}
	if got.ID != 1 {
		t.Errorf("StateForTag(day-active).ID = %d, want 1 (lowest ID wins)", got.ID)
	}
	if got := a.StateForTag("absent"); got != nil {
		t.Errorf("StateForTag(absent) = %v, want nil", got)
	}
}

// TestAssetStateHasTag exercises the tag-presence helper.
func TestAssetStateHasTag(t *testing.T) {
	s := &sim.AssetState{Tags: []string{"day-active", "lamplighter-target"}}
	if !s.HasTag("day-active") {
		t.Error("HasTag(day-active) = false, want true")
	}
	if s.HasTag("night-active") {
		t.Error("HasTag(night-active) = true, want false")
	}
}

// TestVillageObjectHelpers covers tag check, display-name fallback, and
// loiter-offset fallback.
func TestVillageObjectHelpers(t *testing.T) {
	loiterX, loiterY := 2, -1
	o := &sim.VillageObject{
		ID:            "obj-1",
		DisplayName:   "The Old Mill",
		Tags:          []string{"vendor", "mill"},
		LoiterOffsetX: &loiterX,
		LoiterOffsetY: &loiterY,
	}
	if !o.HasTag("vendor") {
		t.Error("HasTag(vendor) = false")
	}
	if o.HasTag("absent") {
		t.Error("HasTag(absent) = true")
	}
	if got := o.EffectiveDisplayName("Mill (Catalog)"); got != "The Old Mill" {
		t.Errorf("EffectiveDisplayName with override = %q, want %q", got, "The Old Mill")
	}

	bare := &sim.VillageObject{ID: "obj-2"}
	if got := bare.EffectiveDisplayName("Maple Tree"); got != "Maple Tree" {
		t.Errorf("EffectiveDisplayName fallback = %q, want %q", got, "Maple Tree")
	}

	x, y := o.EffectiveLoiterOffset(99, 99) // ignore catalog defaults — overrides set
	if x != 2 || y != -1 {
		t.Errorf("EffectiveLoiterOffset with overrides = (%d, %d), want (2, -1)", x, y)
	}
	x, y = bare.EffectiveLoiterOffset(3, 4) // fall back to catalog
	if x != 3 || y != 4 {
		t.Errorf("EffectiveLoiterOffset fallback = (%d, %d), want (3, 4)", x, y)
	}
}

// TestLoadWorldAssetsAndObjects exercises the full load path through
// LoadWorld, asserting Assets + VillageObjects + Snapshot all populated.
func TestLoadWorldAssetsAndObjects(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"tree-maple": {
			ID:           "tree-maple",
			Name:         "Maple Tree",
			Category:     "tree",
			DefaultState: "default",
			States: []sim.AssetState{
				{ID: 10, State: "default", Sheet: "trees.png"},
			},
		},
		"lamp-iron": {
			ID:       "lamp-iron",
			Name:     "Iron Lamp",
			Category: "structure",
			States: []sim.AssetState{
				{ID: 20, State: "unlit", Tags: []string{"night-active"}},
				{ID: 21, State: "lit", Tags: []string{"day-active"}},
			},
		},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"obj-A": {ID: "obj-A", AssetID: "tree-maple", CurrentState: "default", X: 320, Y: 240},
		"obj-B": {ID: "obj-B", AssetID: "lamp-iron", CurrentState: "unlit", X: 100, Y: 100, Tags: []string{"lamplighter-stop"}},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}

	if len(w.Assets) != 2 {
		t.Errorf("w.Assets count = %d, want 2", len(w.Assets))
	}
	if w.Assets["lamp-iron"].Name != "Iron Lamp" {
		t.Errorf("lamp-iron name = %q, want %q", w.Assets["lamp-iron"].Name, "Iron Lamp")
	}

	if len(w.VillageObjects) != 2 {
		t.Errorf("w.VillageObjects count = %d, want 2", len(w.VillageObjects))
	}
	if w.VillageObjects["obj-B"].CurrentState != "unlit" {
		t.Errorf("obj-B state = %q, want unlit", w.VillageObjects["obj-B"].CurrentState)
	}

	// Published snapshot should also carry village objects.
	snap := w.Published()
	if len(snap.VillageObjects) != 2 {
		t.Errorf("snap.VillageObjects count = %d, want 2", len(snap.VillageObjects))
	}
	if !snap.VillageObjects["obj-B"].HasTag("lamplighter-stop") {
		t.Error("snap.VillageObjects[obj-B] missing lamplighter-stop tag")
	}
}
