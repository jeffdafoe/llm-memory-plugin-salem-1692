package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// items_test.go — ZBBS-HOME-423 catalog read for the Pay modal.

func TestHandleItems_SortedCatalog(t *testing.T) {
	w := seededWorld(t)
	w.ItemKinds = map[sim.ItemKind]*sim.ItemKindDef{
		"stew":        {Name: "stew", DisplayLabel: "Stew", Category: "food", SortOrder: 2},
		"ale":         {Name: "ale", DisplayLabel: "Ale", Category: "drink", SortOrder: 1},
		"nights_stay": {Name: "nights_stay", DisplayLabel: "Night's Stay", Category: "service", SortOrder: 1},
	}
	srv := NewServer(w, okAuth{})

	rec := get(t, srv, "/api/village/items")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var items []itemKindDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("len = %d, want 3; body=%s", len(items), rec.Body.String())
	}
	// sort_order then name: ale(1) before nights_stay(1) before stew(2).
	want := []string{"ale", "nights_stay", "stew"}
	for i, name := range want {
		if items[i].Name != name {
			t.Errorf("items[%d].Name = %q, want %q", i, items[i].Name, name)
		}
	}
	if items[1].DisplayLabel != "Night's Stay" || items[1].Category != "service" {
		t.Errorf("nights_stay row = %+v, want label Night's Stay / category service", items[1])
	}
}

// An empty catalog serializes as [] rather than null so the client's
// array-shape check doesn't trip.
func TestHandleItems_EmptyCatalogIsArray(t *testing.T) {
	w := seededWorld(t)
	w.ItemKinds = nil
	srv := NewServer(w, okAuth{})

	rec := get(t, srv, "/api/village/items")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "[]\n" && got != "[]" {
		t.Errorf("body = %q, want []", got)
	}
}

// TestItemDispositionClass (ZBBS-WORK-402/403) — the derived class the Pay
// modal's disposition machinery keys off: service → tonight, non-portable
// consumable → eat_here (the "people can't carry stew" data ruling),
// portable consumable / non-consumable / unseeded → choice (permissive).
func TestItemDispositionClass(t *testing.T) {
	cases := []struct {
		name string
		def  *sim.ItemKindDef
		want string
	}{
		{"service is tonight", &sim.ItemKindDef{
			Name: "nights_stay", Capabilities: []string{"service", "lodging"},
		}, "tonight"},
		{"non-portable consumable is eat_here", &sim.ItemKindDef{
			Name:      "stew",
			Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger"}},
		}, "eat_here"},
		{"portable consumable is choice", &sim.ItemKindDef{
			Name: "bread", Capabilities: []string{"portable"},
			Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger"}},
		}, "choice"},
		{"non-consumable is choice even without portable", &sim.ItemKindDef{
			Name: "iron_tongs",
		}, "choice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := itemDispositionClass(tc.def); got != tc.want {
				t.Errorf("itemDispositionClass(%s) = %q, want %q", tc.def.Name, got, tc.want)
			}
		})
	}
}
