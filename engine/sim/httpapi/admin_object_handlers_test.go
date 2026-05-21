package httpapi

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// seedStructureBridge marks obj1 as a structure's placement by adding a
// Structure whose id matches the object id (the shared-identity bridge), so a
// delete of obj1 must be refused.
func seedStructureBridge(t *testing.T, w *sim.World, id string) {
	t.Helper()
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Structures[sim.StructureID(id)] = &sim.Structure{ID: sim.StructureID(id)}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seedStructureBridge: %v", err)
	}
}

func TestHandleAdminObjectMove_Accepted(t *testing.T) {
	w := seededWorld(t) // seeds obj1 at (5.5, 6.5)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/move", `{"object_id":"obj1","x":12.5,"y":20.25}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var res adminObjectMoveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.ID != "obj1" || res.X != 12.5 || res.Y != 20.25 {
		t.Errorf("response = %+v, want obj1 (12.5, 20.25)", res)
	}
}

func TestHandleAdminObjectMove_NotFound(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/move", `{"object_id":"ghost","x":1,"y":2}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminObjectMove_OffMap(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/move", `{"object_id":"obj1","x":99999999,"y":0}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminObjectMove_MissingObjectID(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/move", `{"x":1,"y":2}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleAdminObjectMove_Forbidden: the caller resolves to no actor → 403.
func TestHandleAdminObjectMove_Forbidden(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := post(t, srv, "/api/village/admin/object/move", `{"object_id":"obj1","x":1,"y":2}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminObjectMove_TrailingJSON(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})
	rec := post(t, srv, "/api/village/admin/object/move", `{"object_id":"obj1","x":1,"y":2} garbage`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminObjectMove_MissingToken(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/village/admin/object/move",
		strings.NewReader(`{"object_id":"obj1","x":1,"y":2}`))
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminObjectDelete_Accepted(t *testing.T) {
	w := seededWorld(t) // obj1 has no matching Structure → deletable
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/delete", `{"object_id":"obj1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var res adminObjectDeleteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(res.DeletedIDs) != 1 || res.DeletedIDs[0] != "obj1" {
		t.Errorf("deleted_ids = %v, want [obj1]", res.DeletedIDs)
	}
}

func TestHandleAdminObjectDelete_NotFound(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/delete", `{"object_id":"ghost"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleAdminObjectDelete_RefusesStructure: obj1 is made a structure bridge
// (a Structure shares its id) → delete refused with 422.
func TestHandleAdminObjectDelete_RefusesStructure(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	seedStructureBridge(t, w, "obj1")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/delete", `{"object_id":"obj1"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleAdminObjectDelete_Forbidden: caller resolves to no actor → 403.
func TestHandleAdminObjectDelete_Forbidden(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := post(t, srv, "/api/village/admin/object/delete", `{"object_id":"obj1"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleAdminObjectDelete_ForbiddenDoesNotMutate proves the authz boundary:
// a forbidden delete must leave the object in place.
func TestHandleAdminObjectDelete_ForbiddenDoesNotMutate(t *testing.T) {
	w := seededWorld(t)
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/delete", `{"object_id":"obj1"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := w.Published().VillageObjects["obj1"]; !ok {
		t.Error("obj1 deleted despite forbidden request")
	}
}

func TestHandleAdminObjectDelete_MissingToken(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/village/admin/object/delete",
		strings.NewReader(`{"object_id":"obj1"}`))
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
}

// TestValidateObjectPosition unit-tests the move position guard directly: the
// non-finite path (400) can't be reached through the JSON decoder (NaN/Inf
// aren't valid JSON numbers), so it's exercised here alongside the bounds rule.
func TestValidateObjectPosition(t *testing.T) {
	if status, msg := validateObjectPosition(10, 10); msg != "" || status != 0 {
		t.Errorf("in-bounds: status=%d msg=%q, want (0, \"\")", status, msg)
	}
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if status, msg := validateObjectPosition(bad, 0); status != http.StatusBadRequest || msg == "" {
			t.Errorf("non-finite %v: status=%d msg=%q, want 400 + msg", bad, status, msg)
		}
	}
	farX := float64(sim.MapW-sim.PadX)*sim.TileSize + 1
	if status, _ := validateObjectPosition(farX, 0); status != http.StatusUnprocessableEntity {
		t.Errorf("off-map x: status=%d, want 422", status)
	}
}
