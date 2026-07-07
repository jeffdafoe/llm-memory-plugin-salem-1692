package sim_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// TestOwnedByOther covers the strict-owner gather/eat gate (LLM-50 D2): unowned
// is commons, the owner is allowed, only a non-owner at an owned object is gated.
func TestOwnedByOther(t *testing.T) {
	var nilObj *sim.VillageObject
	if nilObj.OwnedByOther("anyone") {
		t.Error("nil object should be treated as commons (OwnedByOther=false), not panic")
	}
	if (&sim.VillageObject{}).OwnedByOther("anyone") {
		t.Error("unowned object should be commons (OwnedByOther=false)")
	}
	owned := &sim.VillageObject{OwnerActorID: "prudence"}
	if owned.OwnedByOther("prudence") {
		t.Error("owner should not be gated from their own object")
	}
	if !owned.OwnedByOther("hannah") {
		t.Error("a non-owner should be gated from an owned object")
	}
	if !owned.OwnedByOther("") {
		t.Error("an empty/unknown actor id should be gated from an owned object")
	}
}

// TestConfigWarnings covers the LLM-60 advisory audit: a refresh-bearing
// (gather/eat) object with no display_name is flagged (the resolver can't reach
// it), a named one is not, and a nameless object with no refresh rows (a plain
// decorative prop) is left alone. nil entries are skipped, output is sorted by id.
func TestConfigWarnings(t *testing.T) {
	objects := map[sim.VillageObjectID]*sim.VillageObject{
		// nameless gatherable source → flagged ("gatherable source")
		"obj-a-gather": {ID: "obj-a-gather", Refreshes: []*sim.ObjectRefresh{{GatherItem: "blueberries"}}},
		// nameless eat-on-arrival source (refresh, no gather_item) → flagged
		"obj-b-eat": {ID: "obj-b-eat", Refreshes: []*sim.ObjectRefresh{{Attribute: "hunger", Amount: -8}}},
		// named gatherable source → NOT flagged
		"obj-c-named": {ID: "obj-c-named", DisplayName: "Raspberry Bush", Refreshes: []*sim.ObjectRefresh{{GatherItem: "raspberries"}}},
		// nameless but no refresh rows (decorative prop) → NOT flagged
		"obj-d-prop": {ID: "obj-d-prop"},
		// whitespace-only name is treated as empty → flagged
		"obj-e-blank": {ID: "obj-e-blank", DisplayName: "   ", Refreshes: []*sim.ObjectRefresh{{GatherItem: "blueberries"}}},
		"obj-f-nil":   nil,
	}

	warnings := sim.ConfigWarnings(objects)

	if len(warnings) != 3 {
		t.Fatalf("got %d warnings, want 3: %v", len(warnings), warnings)
	}
	// Sorted by id: obj-a-gather, obj-b-eat, obj-e-blank.
	if !strings.Contains(warnings[0], "obj-a-gather") || !strings.Contains(warnings[0], "gatherable source") {
		t.Errorf("warnings[0] = %q, want obj-a-gather / gatherable source", warnings[0])
	}
	if !strings.Contains(warnings[1], "obj-b-eat") || !strings.Contains(warnings[1], "eat-on-arrival source") {
		t.Errorf("warnings[1] = %q, want obj-b-eat / eat-on-arrival source", warnings[1])
	}
	if !strings.Contains(warnings[2], "obj-e-blank") {
		t.Errorf("warnings[2] = %q, want obj-e-blank", warnings[2])
	}
	for _, w := range warnings {
		if strings.Contains(w, "obj-c-named") || strings.Contains(w, "obj-d-prop") {
			t.Errorf("warning should not flag a named object or a refresh-less prop: %q", w)
		}
	}
	if len(sim.ConfigWarnings(nil)) != 0 {
		t.Error("ConfigWarnings(nil) should be empty")
	}
}

// TestConfigWarnings_WellNoRows covers the LLM-269 backstop: a `well`-tagged
// placement with zero refresh rows is flagged as a dead water source, while a
// well-tagged object that HAS rows (provisioning gave it them) is not.
func TestConfigWarnings_WellNoRows(t *testing.T) {
	objects := map[sim.VillageObjectID]*sim.VillageObject{
		// tagged well, no rows → flagged (dead water source)
		"dry-well": {ID: "dry-well", DisplayName: "Well", Tags: []string{"well"}},
		// tagged well, has rows → NOT flagged by the well check (named + rowed)
		"live-well": {ID: "live-well", DisplayName: "Well", Tags: []string{"well"},
			Refreshes: []*sim.ObjectRefresh{{Attribute: "thirst", Amount: -8}}},
		// no well tag, no rows → NOT flagged (plain prop)
		"plain-prop": {ID: "plain-prop"},
	}

	warnings := sim.ConfigWarnings(objects)
	if len(warnings) != 1 {
		t.Fatalf("got %d warnings, want 1: %v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0], "dry-well") || !strings.Contains(warnings[0], "no object_refresh rows") {
		t.Errorf("warning = %q, want dry-well flagged for no object_refresh rows", warnings[0])
	}
}

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
		"obj-A": {ID: "obj-A", AssetID: "tree-maple", CurrentState: "default", Pos: sim.WorldPos{X: 320, Y: 240}},
		"obj-B": {ID: "obj-B", AssetID: "lamp-iron", CurrentState: "unlit", Pos: sim.WorldPos{X: 100, Y: 100}, Tags: []string{"lamplighter-stop"}},
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
