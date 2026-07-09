package sim_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// LLM-249 — PromoteObjectToStructure. Reuses buildObjectAdminWorld (see
// village_object_admin_test.go): eligible root structure-category placements
// "mill"/"mill2" (asset "millhouse", Name "Mill") with no Structure; a building
// "tavern" (asset "bldg") that already has a Structure; a bare prop "prop-1"
// (asset "prop", category prop); and an overlay "sign" (attached to "post").
// Note the tavern VILLAGE OBJECT is seeded with an empty DisplayName while its
// Structure says "Tavern" — a natural divergence the rename-sync tests exercise.

func TestPromoteObjectToStructure_DefaultsNameFromAsset(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	// mill has an empty DisplayName, so the structure name defaults to the asset
	// catalog name ("Mill").
	res, err := w.Send(sim.PromoteObjectToStructure("mill", "", nil))
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	r := res.(sim.PromoteStructureResult)
	if r.ID != "mill" || r.DisplayName != "Mill" {
		t.Errorf("result = %+v, want id mill name 'Mill'", r)
	}
	st := w.Published().Structures["mill"]
	if st == nil {
		t.Fatal("structure mill not registered live")
	}
	if st.DisplayName != "Mill" {
		t.Errorf("structure DisplayName = %q, want 'Mill'", st.DisplayName)
	}
	if len(st.Tags) != 0 {
		t.Errorf("structure Tags = %v, want empty", st.Tags)
	}
}

func TestPromoteObjectToStructure_DefaultsNameFromObject(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	if _, err := w.Send(sim.SetVillageObjectDisplayName("mill", "Old Mill")); err != nil {
		t.Fatalf("set name: %v", err)
	}
	// Empty display_name → falls back to the object's own name before the asset.
	res, err := w.Send(sim.PromoteObjectToStructure("mill", "  ", nil))
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if r := res.(sim.PromoteStructureResult); r.DisplayName != "Old Mill" {
		t.Errorf("result name = %q, want 'Old Mill'", r.DisplayName)
	}
	if st := w.Published().Structures["mill"]; st == nil || st.DisplayName != "Old Mill" {
		t.Errorf("structure = %+v, want DisplayName 'Old Mill'", st)
	}
}

func TestPromoteObjectToStructure_ExplicitNameAndTags(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	// Trailing dup " business " collapses to one; trimming applied.
	res, err := w.Send(sim.PromoteObjectToStructure("mill2", "  Granary  ", []string{" business ", "mill", "business"}))
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	r := res.(sim.PromoteStructureResult)
	if r.DisplayName != "Granary" {
		t.Errorf("result name = %q, want trimmed 'Granary'", r.DisplayName)
	}
	if strings.Join(r.Tags, ",") != "business,mill" {
		t.Errorf("result tags = %v, want [business mill] (trimmed + de-duped)", r.Tags)
	}
	st := w.Published().Structures["mill2"]
	if st == nil || strings.Join(st.Tags, ",") != "business,mill" {
		t.Errorf("structure = %+v, want tags [business mill]", st)
	}
}

func TestPromoteObjectToStructure_AlreadyStructure(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	// tavern already backs a structure — promoting again would clobber it.
	if _, err := w.Send(sim.PromoteObjectToStructure("tavern", "", nil)); !errors.Is(err, sim.ErrVillageObjectIsAlreadyStructure) {
		t.Errorf("err = %v, want ErrVillageObjectIsAlreadyStructure", err)
	}
}

func TestPromoteObjectToStructure_NotFound(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	if _, err := w.Send(sim.PromoteObjectToStructure("ghost", "", nil)); !errors.Is(err, sim.ErrVillageObjectNotFound) {
		t.Errorf("err = %v, want ErrVillageObjectNotFound", err)
	}
}

func TestPromoteObjectToStructure_NotPromotable(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	cases := map[string]sim.VillageObjectID{
		"prop category": "prop-1", // category "prop", not a building
		"overlay":       "sign",   // attached to "post" — an overlay, never a building
	}
	for name, id := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := w.Send(sim.PromoteObjectToStructure(id, "Building", nil)); !errors.Is(err, sim.ErrObjectNotPromotable) {
				t.Errorf("err = %v, want ErrObjectNotPromotable", err)
			}
			if _, ok := w.Published().Structures[sim.StructureID(id)]; ok {
				t.Error("rejected promote still created a structure")
			}
		})
	}
}

func TestPromoteObjectToStructure_InvalidName(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	cases := map[string]string{
		"control char": "bad\x07name",
		"over cap":     strings.Repeat("z", sim.MaxVillageObjectDisplayNameLen+1),
	}
	for name, val := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := w.Send(sim.PromoteObjectToStructure("mill", val, nil)); !errors.Is(err, sim.ErrInvalidDisplayName) {
				t.Errorf("err = %v, want ErrInvalidDisplayName", err)
			}
			if _, ok := w.Published().Structures["mill"]; ok {
				t.Error("rejected promote still created a structure")
			}
		})
	}
}

func TestPromoteObjectToStructure_InvalidTag(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	cases := map[string][]string{
		"empty tag":    {"business", "   "},
		"control char": {"mi\x07ll"},
	}
	for name, tags := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := w.Send(sim.PromoteObjectToStructure("mill", "Granary", tags)); !errors.Is(err, sim.ErrInvalidTag) {
				t.Errorf("err = %v, want ErrInvalidTag", err)
			}
			if _, ok := w.Published().Structures["mill"]; ok {
				t.Error("rejected promote still created a structure")
			}
		})
	}
}

// --- rename-sync: a building's Structure name tracks the object rename --------

func TestSetVillageObjectDisplayName_SyncsStructure(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	if _, err := w.Send(sim.SetVillageObjectDisplayName("tavern", "The Alehouse")); err != nil {
		t.Fatalf("rename: %v", err)
	}
	pub := w.Published()
	if got := pub.VillageObjects["tavern"].DisplayName; got != "The Alehouse" {
		t.Errorf("object DisplayName = %q, want 'The Alehouse'", got)
	}
	if got := pub.Structures["tavern"].DisplayName; got != "The Alehouse" {
		t.Errorf("structure DisplayName = %q, want 'The Alehouse' (rename must sync the building's nav label)", got)
	}
}

func TestSetVillageObjectDisplayName_ClearFallsBackToAsset(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	if _, err := w.Send(sim.SetVillageObjectDisplayName("tavern", "Alehouse")); err != nil {
		t.Fatalf("rename: %v", err)
	}
	// Clearing the object name is valid; the structure can't go empty (non-empty
	// invariant), so it falls back to the asset catalog name ("Tavern").
	if _, err := w.Send(sim.SetVillageObjectDisplayName("tavern", "")); err != nil {
		t.Fatalf("clear: %v", err)
	}
	pub := w.Published()
	if got := pub.VillageObjects["tavern"].DisplayName; got != "" {
		t.Errorf("object DisplayName = %q, want cleared", got)
	}
	if got := pub.Structures["tavern"].DisplayName; got != "Tavern" {
		t.Errorf("structure DisplayName = %q, want asset-name fallback 'Tavern'", got)
	}
}

func TestSetVillageObjectDisplayName_ClearRejectsWhenNoFallback(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	// corrupt-bld backs a structure but its asset name is blank, so clearing the
	// object name has no non-empty fallback — the clear is rejected rather than
	// leaving the structure name stale (finding 5).
	if _, err := w.Send(sim.SetVillageObjectDisplayName("corrupt-bld", "")); !errors.Is(err, sim.ErrInvalidDisplayName) {
		t.Errorf("err = %v, want ErrInvalidDisplayName", err)
	}
	// The structure keeps its original name; nothing was mutated.
	if got := w.Published().Structures["corrupt-bld"].DisplayName; got != "Corrupt Hall" {
		t.Errorf("structure DisplayName = %q, want unchanged 'Corrupt Hall'", got)
	}
}
