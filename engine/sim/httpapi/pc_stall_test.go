package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pc_stall_test.go — LLM-753 shelf read for the player's stall window.

// stallWorld stands up a running world with one stall whose keeper makes
// nails (16 held: a pile), gathers sage (3 held: a few), makes one skillet
// (one), and carries flour he does not deal in (not on the shelf).
func stallWorld(t *testing.T) *sim.World {
	t.Helper()
	w := seededWorld(t)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.ItemKinds = map[sim.ItemKind]*sim.ItemKindDef{
			"nail":    {Name: "nail", DisplayLabel: "Nails"},
			"sage":    {Name: "sage"},
			"skillet": {Name: "skillet", DisplayLabel: "Skillet"},
			"flour":   {Name: "flour", DisplayLabel: "Flour"},
		}
		world.Structures["stall"] = &sim.Structure{ID: "stall", DisplayName: "Stall"}
		world.VillageObjects["stall"] = &sim.VillageObject{ID: "stall"}
		world.Actors["npc-keeper"] = &sim.Actor{
			ID: "npc-keeper", DisplayName: "Keeper", Kind: sim.KindNPCStateful,
			State: sim.StateIdle, Pos: sim.TilePos{X: 3, Y: 4},
			WorkStructureID: "stall",
			RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
				{Item: "nail", Source: sim.RestockSourceProduce},
				{Item: "sage", Source: sim.RestockSourceForage},
				{Item: "skillet", Source: sim.RestockSourceProduce},
			}},
			Inventory: map[sim.ItemKind]int{"nail": 16, "sage": 3, "skillet": 1, "flour": 20},
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed stall world: %v", err)
	}
	return w
}

func TestHandlePCStall_ShelfTiersAndOrder(t *testing.T) {
	srv := NewServer(stallWorld(t), okAuth{})

	rec := get(t, srv, "/api/village/pc/stall?structure_id=stall")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var res pcStallResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.StructureID != "stall" {
		t.Errorf("structure_id = %q, want stall", res.StructureID)
	}
	// Sorted by label; sage has no catalog label and falls back to its kind.
	want := []pcStallWareDTO{
		{Item: "nail", DisplayLabel: "Nails", Stock: stallStockPlenty},
		{Item: "skillet", DisplayLabel: "Skillet", Stock: stallStockOne},
		{Item: "sage", DisplayLabel: "sage", Stock: stallStockFew},
	}
	if len(res.Wares) != len(want) {
		t.Fatalf("wares = %+v, want %+v", res.Wares, want)
	}
	for i := range want {
		if res.Wares[i] != want[i] {
			t.Errorf("wares[%d] = %+v, want %+v", i, res.Wares[i], want[i])
		}
	}
}

func TestHandlePCStall_BadRequests(t *testing.T) {
	srv := NewServer(stallWorld(t), okAuth{})
	status := func(path string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+testToken)
		srv.Handler().ServeHTTP(rec, req)
		return rec.Code
	}
	if code := status("/api/village/pc/stall"); code != http.StatusBadRequest {
		t.Errorf("no structure_id: status = %d, want 400", code)
	}
	if code := status("/api/village/pc/stall?structure_id=nowhere"); code != http.StatusNotFound {
		t.Errorf("unknown structure: status = %d, want 404", code)
	}
}

func TestStallStockTier(t *testing.T) {
	cases := map[int]string{1: stallStockOne, 2: stallStockFew, 5: stallStockFew, 6: stallStockPlenty, 90: stallStockPlenty}
	for spare, want := range cases {
		if got := stallStockTier(spare); got != want {
			t.Errorf("stallStockTier(%d) = %q, want %q", spare, got, want)
		}
	}
}
