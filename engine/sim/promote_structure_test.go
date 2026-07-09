package sim_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// LLM-249 — PromoteObjectToStructure. Reuses buildObjectAdminWorld (see
// village_object_admin_test.go): a bare prop "prop-1"/"post" (asset "prop",
// Name "Bench") with no Structure, and a building "tavern" (asset "bldg", Name
// "Tavern") that already has a Structure (DisplayName "Tavern"). Note the tavern
// VILLAGE OBJECT is seeded with an empty DisplayName while its Structure says
// "Tavern" — a natural divergence the rename-sync tests exercise.

func TestPromoteObjectToStructure_DefaultsNameFromAsset(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	// prop-1 has an empty DisplayName, so the structure name defaults to the
	// asset catalog name ("Bench").
	res, err := w.Send(sim.PromoteObjectToStructure("prop-1", "", nil))
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	r := res.(sim.PromoteStructureResult)
	if r.ID != "prop-1" || r.DisplayName != "Bench" {
		t.Errorf("result = %+v, want id prop-1 name 'Bench'", r)
	}
	st := w.Published().Structures["prop-1"]
	if st == nil {
		t.Fatal("structure prop-1 not registered live")
	}
	if st.DisplayName != "Bench" {
		t.Errorf("structure DisplayName = %q, want 'Bench'", st.DisplayName)
	}
	if len(st.Tags) != 0 {
		t.Errorf("structure Tags = %v, want empty", st.Tags)
	}
}

func TestPromoteObjectToStructure_DefaultsNameFromObject(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	if _, err := w.Send(sim.SetVillageObjectDisplayName("prop-1", "Old Mill")); err != nil {
		t.Fatalf("set name: %v", err)
	}
	// Empty display_name → falls back to the object's own name before the asset.
	res, err := w.Send(sim.PromoteObjectToStructure("prop-1", "  ", nil))
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if r := res.(sim.PromoteStructureResult); r.DisplayName != "Old Mill" {
		t.Errorf("result name = %q, want 'Old Mill'", r.DisplayName)
	}
	if st := w.Published().Structures["prop-1"]; st == nil || st.DisplayName != "Old Mill" {
		t.Errorf("structure = %+v, want DisplayName 'Old Mill'", st)
	}
}

func TestPromoteObjectToStructure_ExplicitNameAndTags(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	res, err := w.Send(sim.PromoteObjectToStructure("post", "  Granary  ", []string{" business ", "mill"}))
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	r := res.(sim.PromoteStructureResult)
	if r.DisplayName != "Granary" {
		t.Errorf("result name = %q, want trimmed 'Granary'", r.DisplayName)
	}
	if strings.Join(r.Tags, ",") != "business,mill" {
		t.Errorf("result tags = %v, want [business mill] (trimmed)", r.Tags)
	}
	st := w.Published().Structures["post"]
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

func TestPromoteObjectToStructure_InvalidName(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	cases := map[string]string{
		"control char": "bad\x07name",
		"over cap":     strings.Repeat("z", sim.MaxVillageObjectDisplayNameLen+1),
	}
	for name, val := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := w.Send(sim.PromoteObjectToStructure("prop-1", val, nil)); !errors.Is(err, sim.ErrInvalidDisplayName) {
				t.Errorf("err = %v, want ErrInvalidDisplayName", err)
			}
			if _, ok := w.Published().Structures["prop-1"]; ok {
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
			if _, err := w.Send(sim.PromoteObjectToStructure("prop-1", "Granary", tags)); !errors.Is(err, sim.ErrInvalidTag) {
				t.Errorf("err = %v, want ErrInvalidTag", err)
			}
			if _, ok := w.Published().Structures["prop-1"]; ok {
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
