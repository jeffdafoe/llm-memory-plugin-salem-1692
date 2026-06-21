package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// seedGatherObjects installs a finite gatherable bush (forage-to-sell), an
// infinite gatherable well, and a non-gatherable bench on the running test
// world. The post-command republish makes them visible through w.Published(),
// which handleObjectGather reads.
func seedGatherObjects(t *testing.T, w *sim.World) {
	t.Helper()
	ip := func(v int) *int { return &v }
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.VillageObjects["bush1"] = &sim.VillageObject{
			ID: "bush1",
			Refreshes: []*sim.ObjectRefresh{{
				Attribute: "hunger", Amount: 0, GatherItem: "berries",
				AvailableQuantity: ip(7), MaxQuantity: ip(10),
				RefreshMode: sim.RefreshModePeriodic, RefreshPeriodHours: ip(168),
			}},
		}
		world.VillageObjects["well1"] = &sim.VillageObject{
			ID:        "well1",
			Refreshes: []*sim.ObjectRefresh{{Attribute: "thirst", Amount: -12, GatherItem: "water"}}, // infinite
		}
		world.VillageObjects["bench1"] = &sim.VillageObject{ID: "bench1"} // no refreshes
		return nil, nil
	}}); err != nil {
		t.Fatalf("seedGatherObjects: %v", err)
	}
}

func TestHandleObjectGather_FiniteBush(t *testing.T) {
	w := seededWorld(t)
	seedGatherObjects(t, w)
	srv := NewServer(w, okAuth{})

	rec := get(t, srv, "/api/village/object/gather?id=bush1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var res objectGatherResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.Gatherable || res.Item != "berries" ||
		res.Available == nil || *res.Available != 7 || res.Max == nil || *res.Max != 10 {
		t.Errorf("got %+v, want gatherable berries 7/10", res)
	}
}

// TestHandleObjectGather_InfiniteWell_NoCount — an infinite source is gatherable
// but carries no count (Available/Max nil), so the tooltip shows no "X berries".
func TestHandleObjectGather_InfiniteWell_NoCount(t *testing.T) {
	w := seededWorld(t)
	seedGatherObjects(t, w)
	srv := NewServer(w, okAuth{})

	rec := get(t, srv, "/api/village/object/gather?id=well1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var res objectGatherResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.Gatherable || res.Item != "water" || res.Available != nil {
		t.Errorf("got %+v, want gatherable water with nil Available (infinite)", res)
	}
}

func TestHandleObjectGather_NonGatherable(t *testing.T) {
	w := seededWorld(t)
	seedGatherObjects(t, w)
	srv := NewServer(w, okAuth{})

	rec := get(t, srv, "/api/village/object/gather?id=bench1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var res objectGatherResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Gatherable {
		t.Errorf("got %+v, want gatherable=false for a non-gatherable object", res)
	}
}

// getRaw issues an authenticated GET without asserting 200 (the shared get
// helper fails on any non-200), so the error-status cases can be checked.
func getRaw(srv *Server, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestHandleObjectGather_NotFound(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := getRaw(srv, "/api/village/object/gather?id=ghost")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleObjectGather_MissingID(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := getRaw(srv, "/api/village/object/gather")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}
