package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// seedFenceAsset adds a tagged fence asset to the harness world. The harness
// terrain is all-zero bytes, which TerrainCost treats as walkable, except the
// deep-water tile it seeds at (2,1) — so a run over (2,1) is the blocked case.
func seedFenceAsset(t *testing.T, w *sim.World) {
	t.Helper()
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Assets["fence"] = &sim.Asset{
			ID: "fence", Name: "Ranch Fence", Category: "fence", DefaultState: "h",
			AnchorX: 0.5, AnchorY: 0.85, IsObstacle: true, Layer: "objects",
			States: []sim.AssetState{
				{ID: 11, State: "corner-tl", Tags: []string{sim.TagFenceCornerTL}},
				{ID: 12, State: "h", Tags: []string{sim.TagFenceH}},
				{ID: 13, State: "corner-tr", Tags: []string{sim.TagFenceCornerTR}},
				{ID: 14, State: "v-top", Tags: []string{sim.TagFenceVTop}},
				{ID: 15, State: "v", Tags: []string{sim.TagFenceV}},
				{ID: 16, State: "v-bottom", Tags: []string{sim.TagFenceVBottom, sim.TagFencePost}},
				{ID: 17, State: "corner-bl", Tags: []string{sim.TagFenceCornerBL, sim.TagFenceEndLeft}},
				{ID: 18, State: "corner-br", Tags: []string{sim.TagFenceCornerBR, sim.TagFenceEndRight}},
			},
		}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seedFenceAsset: %v", err)
	}
}

// px is the world-pixel centre of padded tile (x, y).
func px(x, y int) (float64, float64) {
	c := sim.TilePos{X: x, Y: y}.Center()
	return c.X, c.Y
}

func TestHandleAdminFencePlace_RingThenDelete(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	seedFenceAsset(t, w)
	srv := NewServer(w, okAuth{})

	x1, y1 := px(sim.PadX+10, sim.PadY+10)
	x2, y2 := px(sim.PadX+13, sim.PadY+12)
	body, _ := json.Marshal(map[string]any{"asset_id": "fence", "x1": x1, "y1": y1, "x2": x2, "y2": y2})
	rec := post(t, srv, "/api/village/admin/fence/place", string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("place status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var placed adminFencePlaceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &placed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if placed.RunID == "" || len(placed.ObjectIDs) != 10 {
		t.Fatalf("place response = %+v, want a run id and 10 ids", placed)
	}

	rec = post(t, srv, "/api/village/admin/fence/delete", `{"run_id":"`+placed.RunID+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var deleted adminFenceDeleteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &deleted); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(deleted.DeletedIDs) != 10 {
		t.Errorf("deleted %d ids, want 10", len(deleted.DeletedIDs))
	}

	rec = post(t, srv, "/api/village/admin/fence/delete", `{"run_id":"`+placed.RunID+`"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("second delete status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleAdminFencePlace_BlockedTileIs409: the harness water tile (2,1) under
// the run → 409 naming that tile's pixel origin, and nothing placed.
func TestHandleAdminFencePlace_BlockedTileIs409(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	seedFenceAsset(t, w)
	srv := NewServer(w, okAuth{})

	x1, y1 := px(0, 1)
	x2, y2 := px(4, 1)
	body, _ := json.Marshal(map[string]any{"asset_id": "fence", "x1": x1, "y1": y1, "x2": x2, "y2": y2})
	rec := post(t, srv, "/api/village/admin/fence/place", string(body))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	var blocked adminFenceBlockedResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &blocked); err != nil {
		t.Fatalf("decode: %v", err)
	}
	wantX, wantY := float64(2-sim.PadX)*sim.TileSize, float64(1-sim.PadY)*sim.TileSize
	if blocked.X != wantX || blocked.Y != wantY || blocked.Error == "" {
		t.Errorf("blocked = %+v, want tile origin (%v,%v) with an error message", blocked, wantX, wantY)
	}
	count, _ := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) { return len(world.VillageObjects), nil }})
	if count.(int) != 1 { // obj1 only
		t.Errorf("objects = %d, want 1 (nothing placed)", count.(int))
	}
}

func TestHandleAdminFencePlace_BadInput(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	seedFenceAsset(t, w)
	srv := NewServer(w, okAuth{})
	x1, y1 := px(sim.PadX+10, sim.PadY+10)
	x2, y2 := px(sim.PadX+13, sim.PadY+12)
	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"missing asset", map[string]any{"x1": x1, "y1": y1, "x2": x2, "y2": y2}, http.StatusBadRequest},
		{"unknown asset", map[string]any{"asset_id": "nope", "x1": x1, "y1": y1, "x2": x2, "y2": y2}, http.StatusBadRequest},
		{"asset without fence states", map[string]any{"asset_id": "asset-y", "x1": x1, "y1": y1, "x2": x2, "y2": y2}, http.StatusBadRequest},
		{"corner off the map", map[string]any{"asset_id": "fence", "x1": x1, "y1": y1, "x2": 99999999.0, "y2": y2}, http.StatusUnprocessableEntity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(tc.body)
			rec := post(t, srv, "/api/village/admin/fence/place", string(body))
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d; body=%s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
	if rec := post(t, srv, "/api/village/admin/fence/delete", `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("delete without run_id status = %d, want 400", rec.Code)
	}
}

func TestHandleAdminFencePlace_Forbidden(t *testing.T) {
	w := seededWorld(t)
	seedFenceAsset(t, w)
	srv := NewServer(w, okAuth{})
	x1, y1 := px(sim.PadX+10, sim.PadY+10)
	body, _ := json.Marshal(map[string]any{"asset_id": "fence", "x1": x1, "y1": y1, "x2": x1, "y2": y1})
	if rec := post(t, srv, "/api/village/admin/fence/place", string(body)); rec.Code != http.StatusForbidden {
		t.Errorf("place status = %d, want 403", rec.Code)
	}
	if rec := post(t, srv, "/api/village/admin/fence/delete", `{"run_id":"x"}`); rec.Code != http.StatusForbidden {
		t.Errorf("delete status = %d, want 403", rec.Code)
	}
}
