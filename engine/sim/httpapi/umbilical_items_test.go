package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_items_test.go — handler coverage for the item-catalog read route
// (LLM-119): the read side of /item/set-satisfies. Asserts the catalog map +
// satiation entries, the single-item filter, dwell-triple passthrough, and the
// empty-catalog shape.

// seedItemsCatalog replaces the live catalog with a known set: ale (two
// satiation entries), stew (a dwell-bearing entry), and iron (a material with
// none).
func seedItemsCatalog(t *testing.T, srv *Server) {
	t.Helper()
	_, err := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.ItemKinds = map[sim.ItemKind]*sim.ItemKindDef{
			"ale": {Name: "ale", DisplayLabel: "Ale", Category: sim.ItemCategoryDrink, SortOrder: 10,
				Satisfies: []sim.ItemSatisfaction{
					{Attribute: "thirst", Immediate: 6},
					{Attribute: "hunger", Immediate: 2},
				}},
			"stew": {Name: "stew", DisplayLabel: "Hearty Stew", Category: sim.ItemCategoryFood, SortOrder: 5,
				Satisfies: []sim.ItemSatisfaction{
					{Attribute: "hunger", Immediate: 10, DwellAmount: 2, DwellPeriodMinutes: 15, DwellTotalTicks: 4},
				}},
			"iron": {Name: "iron", DisplayLabel: "Iron Ingot", Category: sim.ItemCategoryMaterial, SortOrder: 50},
		}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
}

func TestUmbilicalItems_ListAndFilter(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)
	seedItemsCatalog(t, srv)

	// Full catalog, sorted by name (ale, iron, stew).
	rec := req(t, h, "/api/village/umbilical/items", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("items = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out UmbilicalItemsDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Total != 3 || len(out.Items) != 3 {
		t.Fatalf("total=%d items=%d, want 3/3", out.Total, len(out.Items))
	}
	if out.Items[0].Name != "ale" || out.Items[1].Name != "iron" || out.Items[2].Name != "stew" {
		t.Fatalf("not sorted by name: %s, %s, %s", out.Items[0].Name, out.Items[1].Name, out.Items[2].Name)
	}
	// ale carries both satiation entries; iron carries none.
	if len(out.Items[0].Satisfies) != 2 {
		t.Fatalf("ale satisfies = %d, want 2", len(out.Items[0].Satisfies))
	}
	if len(out.Items[1].Satisfies) != 0 {
		t.Fatalf("iron satisfies = %d, want 0", len(out.Items[1].Satisfies))
	}

	// ?item= filters to one (case-insensitive against the canonical key) and the
	// dwell triple rides through on the read.
	rec = req(t, h, "/api/village/umbilical/items?item=STEW", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("items?item = %d, want 200", rec.Code)
	}
	out = UmbilicalItemsDTO{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode filtered: %v", err)
	}
	if out.Total != 1 || out.Items[0].Name != "stew" {
		t.Fatalf("filter = %+v, want only stew", out.Items)
	}
	st := out.Items[0].Satisfies[0]
	if st.Attribute != "hunger" || st.Amount != 10 || st.DwellAmount != 2 ||
		st.DwellPeriodMinutes != 15 || st.DwellTotalTicks != 4 {
		t.Fatalf("stew entry = %+v, want hunger/10 with dwell triple", st)
	}

	// Unknown item → empty list, still 200.
	rec = req(t, h, "/api/village/umbilical/items?item=dragonfruit", "tok")
	out = UmbilicalItemsDTO{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusOK || out.Total != 0 {
		t.Fatalf("unknown filter = %d total=%d, want 200/0", rec.Code, out.Total)
	}
}

func TestUmbilicalItems_EmptyCatalog(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)
	if _, err := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.ItemKinds = map[sim.ItemKind]*sim.ItemKindDef{}
		return nil, nil
	}}); err != nil {
		t.Fatalf("clear catalog: %v", err)
	}
	rec := req(t, h, "/api/village/umbilical/items", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("items = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out UmbilicalItemsDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Total != 0 || len(out.Items) != 0 {
		t.Fatalf("empty catalog total=%d, want 0", out.Total)
	}
}
