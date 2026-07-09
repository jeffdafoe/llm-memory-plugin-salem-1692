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

// obj1 is seeded at CurrentState "lit" (asset-x defines "unlit" and "lit").
func TestHandleAdminObjectSetState_Accepted(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/set-state", `{"object_id":"obj1","state":"unlit"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var res adminObjectStateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.ID != "obj1" || res.State != "unlit" || !res.Applied {
		t.Errorf("response = %+v, want obj1/unlit/applied=true", res)
	}
	if got := w.Published().VillageObjects["obj1"].CurrentState; got != "unlit" {
		t.Errorf("current_state = %q, want unlit", got)
	}
}

// Setting an object to the state it is already in is an idempotent 200 with
// applied=false (no spurious VillageObjectStateChanged emitted).
func TestHandleAdminObjectSetState_AlreadyAtTarget(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/set-state", `{"object_id":"obj1","state":"lit"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var res adminObjectStateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Applied {
		t.Errorf("applied = true, want false (already at target)")
	}
}

func TestHandleAdminObjectSetState_NotFound(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/set-state", `{"object_id":"ghost","state":"unlit"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminObjectSetState_MissingObjectID(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/set-state", `{"state":"unlit"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminObjectSetState_MissingState(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/set-state", `{"object_id":"obj1"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleAdminObjectSetState_Forbidden: caller resolves to no actor → 403,
// and the object's state must be left untouched.
func TestHandleAdminObjectSetState_Forbidden(t *testing.T) {
	w := seededWorld(t)
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/set-state", `{"object_id":"obj1","state":"unlit"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	if got := w.Published().VillageObjects["obj1"].CurrentState; got != "lit" {
		t.Errorf("current_state = %q, want unchanged (lit) after forbidden request", got)
	}
}

// --- set-owner ---

// seededWorld seeds actor "hannah", used here as a valid owner.
func TestHandleAdminObjectSetOwner_Accepted(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/set-owner", `{"object_id":"obj1","owner_actor_id":"hannah"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var res adminObjectOwnerResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.ID != "obj1" || res.OwnerActorID != "hannah" {
		t.Errorf("response = %+v, want obj1/hannah", res)
	}
	if got := w.Published().VillageObjects["obj1"].OwnerActorID; got != "hannah" {
		t.Errorf("owner = %q, want hannah", got)
	}
}

func TestHandleAdminObjectSetOwner_Clear(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/set-owner", `{"object_id":"obj1","owner_actor_id":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := w.Published().VillageObjects["obj1"].OwnerActorID; got != "" {
		t.Errorf("owner = %q, want cleared", got)
	}
}

func TestHandleAdminObjectSetOwner_OwnerNotFound(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/set-owner", `{"object_id":"obj1","owner_actor_id":"ghost"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminObjectSetOwner_NotFound(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/set-owner", `{"object_id":"ghost","owner_actor_id":"hannah"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminObjectSetOwner_MissingObjectID(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/set-owner", `{"owner_actor_id":"hannah"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminObjectSetOwner_Forbidden(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := post(t, srv, "/api/village/admin/object/set-owner", `{"object_id":"obj1","owner_actor_id":"hannah"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// --- set-loiter-offset ---

func TestHandleAdminObjectSetLoiterOffset_Accepted(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/set-loiter-offset", `{"object_id":"obj1","x":2,"y":-3}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var res adminObjectLoiterOffsetResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.X == nil || res.Y == nil || *res.X != 2 || *res.Y != -3 {
		t.Errorf("response = (%v,%v), want (2,-3)", res.X, res.Y)
	}
	obj := w.Published().VillageObjects["obj1"]
	if obj.LoiterOffsetX == nil || *obj.LoiterOffsetX != 2 || obj.LoiterOffsetY == nil || *obj.LoiterOffsetY != -3 {
		t.Errorf("stored offset = (%v,%v), want (2,-3)", obj.LoiterOffsetX, obj.LoiterOffsetY)
	}
}

func TestHandleAdminObjectSetLoiterOffset_Clear(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/set-loiter-offset", `{"object_id":"obj1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var res adminObjectLoiterOffsetResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.X != nil || res.Y != nil {
		t.Errorf("response = (%v,%v), want cleared (null,null)", res.X, res.Y)
	}
}

func TestHandleAdminObjectSetLoiterOffset_OnlyOneAxis(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/set-loiter-offset", `{"object_id":"obj1","x":2}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminObjectSetLoiterOffset_NotFound(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/set-loiter-offset", `{"object_id":"ghost","x":1,"y":1}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// --- set-display-name (ZBBS-HOME-283) ---------------------------------

func TestHandleAdminObjectSetDisplayName_Accepted(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/set-display-name", `{"object_id":"obj1","display_name":"The Crow's Nest"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var res adminObjectDisplayNameResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.ID != "obj1" || res.DisplayName != "The Crow's Nest" {
		t.Errorf("response = %+v, want obj1 / The Crow's Nest", res)
	}
	if got := w.Published().VillageObjects["obj1"].DisplayName; got != "The Crow's Nest" {
		t.Errorf("stored display name = %q, want The Crow's Nest", got)
	}
}

func TestHandleAdminObjectSetDisplayName_InvalidName(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	// An over-cap display name is invalid input → 400 (sim.ErrInvalidDisplayName).
	body := `{"object_id":"obj1","display_name":"` + strings.Repeat("z", sim.MaxVillageObjectDisplayNameLen+1) + `"}`
	rec := post(t, srv, "/api/village/admin/object/set-display-name", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminObjectSetDisplayName_MissingObjectID(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/set-display-name", `{"display_name":"X"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminObjectSetDisplayName_NotFound(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/set-display-name", `{"object_id":"ghost","display_name":"X"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminObjectSetDisplayName_Forbidden(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := post(t, srv, "/api/village/admin/object/set-display-name", `{"object_id":"obj1","display_name":"X"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// --- add-tag / remove-tag (ZBBS-HOME-283) -----------------------------

func TestHandleAdminObjectAddTag_Accepted(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/add-tag", `{"object_id":"obj1","tag":"vendor"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var res adminObjectTagResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.ID != "obj1" || len(res.Tags) != 1 || res.Tags[0] != "vendor" {
		t.Errorf("response = %+v, want obj1 / [vendor]", res)
	}
}

func TestHandleAdminObjectAddTag_InvalidTag(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	// A blank tag is invalid input → 400 (sim.ErrInvalidTag).
	rec := post(t, srv, "/api/village/admin/object/add-tag", `{"object_id":"obj1","tag":"   "}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminObjectAddTag_NotFound(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/add-tag", `{"object_id":"ghost","tag":"vendor"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleAdminObjectRemoveTag_LastTagEmptyArray pins the "always an array"
// response contract: removing the last tag returns tags as [] (not null).
func TestHandleAdminObjectRemoveTag_LastTagEmptyArray(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	if rec := post(t, srv, "/api/village/admin/object/add-tag", `{"object_id":"obj1","tag":"vendor"}`); rec.Code != http.StatusOK {
		t.Fatalf("seed add status = %d; body=%s", rec.Code, rec.Body.String())
	}
	rec := post(t, srv, "/api/village/admin/object/remove-tag", `{"object_id":"obj1","tag":"vendor"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"tags":[]`) {
		t.Errorf("body = %s, want tags as []", rec.Body.String())
	}
}

func TestHandleAdminObjectRemoveTag_Forbidden(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := post(t, srv, "/api/village/admin/object/remove-tag", `{"object_id":"obj1","tag":"vendor"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminObjectSetLoiterOffset_Forbidden(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := post(t, srv, "/api/village/admin/object/set-loiter-offset", `{"object_id":"obj1","x":1,"y":1}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// --- set-entry-policy ---

func TestHandleAdminObjectSetEntryPolicy_Accepted(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/set-entry-policy", `{"object_id":"obj1","entry_policy":"owner-only"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var res adminObjectEntryPolicyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.EntryPolicy != "owner-only" {
		t.Errorf("entry_policy = %q, want owner-only", res.EntryPolicy)
	}
	if got := w.Published().VillageObjects["obj1"].EntryPolicy; got != sim.EntryPolicyOwner {
		t.Errorf("stored policy = %q, want owner-only", got)
	}
}

func TestHandleAdminObjectSetEntryPolicy_Invalid(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/set-entry-policy", `{"object_id":"obj1","entry_policy":"bogus"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminObjectSetEntryPolicy_NotFound(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/set-entry-policy", `{"object_id":"ghost","entry_policy":"open"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminObjectSetEntryPolicy_MissingObjectID(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/set-entry-policy", `{"entry_policy":"open"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminObjectSetEntryPolicy_Forbidden(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := post(t, srv, "/api/village/admin/object/set-entry-policy", `{"object_id":"obj1","entry_policy":"open"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// --- promote-to-structure (LLM-249) ---

func TestHandleAdminObjectPromoteToStructure_Accepted(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	// obj1 is a bare object (DisplayName "Tavern") with no backing structure.
	rec := post(t, srv, "/api/village/admin/object/promote-to-structure", `{"object_id":"obj1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var res adminObjectPromoteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.ID != "obj1" || res.DisplayName != "Tavern" {
		t.Errorf("response = %+v, want id obj1 name Tavern (defaulted from object)", res)
	}
	if st := w.Published().Structures["obj1"]; st == nil {
		t.Error("structure obj1 not registered live")
	}
}

func TestHandleAdminObjectPromoteToStructure_AlreadyStructure(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	if rec := post(t, srv, "/api/village/admin/object/promote-to-structure", `{"object_id":"obj1"}`); rec.Code != http.StatusOK {
		t.Fatalf("first promote status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// Promoting the same object again conflicts with the existing structure.
	rec := post(t, srv, "/api/village/admin/object/promote-to-structure", `{"object_id":"obj1"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second promote status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminObjectPromoteToStructure_NotFound(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/promote-to-structure", `{"object_id":"ghost"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminObjectPromoteToStructure_MissingObjectID(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/promote-to-structure", `{"display_name":"Mill"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminObjectPromoteToStructure_Forbidden(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := post(t, srv, "/api/village/admin/object/promote-to-structure", `{"object_id":"obj1"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
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

func TestHandleAdminObjectSetRefresh_Accepted(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	body := `{"object_id":"obj1","rows":[` +
		`{"attribute":"thirst","amount":-12,"available_quantity":3,"max_quantity":10,"refresh_mode":"continuous","refresh_period_hours":2},` +
		`{"attribute":"tiredness","amount":-4,"dwell_delta":-2,"dwell_period_minutes":30}` +
		`]}`
	rec := post(t, srv, "/api/village/admin/object/set-refresh", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var res adminObjectRefreshResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.ID != "obj1" || len(res.Rows) != 2 {
		t.Fatalf("response = %+v, want obj1 with 2 rows", res)
	}
	if res.Rows[0].Attribute != "thirst" || res.Rows[0].Amount != -12 ||
		res.Rows[0].AvailableQuantity == nil || *res.Rows[0].AvailableQuantity != 3 ||
		res.Rows[0].RefreshMode != "continuous" {
		t.Errorf("row 0 = %+v, want finite thirst", res.Rows[0])
	}
	if res.Rows[1].Attribute != "tiredness" || res.Rows[1].AvailableQuantity != nil ||
		res.Rows[1].RefreshMode != "" || res.Rows[1].DwellDelta == nil || *res.Rows[1].DwellDelta != -2 {
		t.Errorf("row 1 = %+v, want infinite tiredness+dwell", res.Rows[1])
	}
}

// TestHandleAdminObjectSetRefresh_PreservesGatherItem is the LLM-109 regression:
// a set-refresh on a gather source must carry gather_item end to end (wire -> sim
// -> wire) instead of silently stripping the source's harvestable yield. It also
// covers the forage-to-sell row (amount == 0), which is INVALID without a
// gather_item — so before the fix it could not be set at all (the dropped field
// failed validation as "amount may be zero only on a gather source").
func TestHandleAdminObjectSetRefresh_PreservesGatherItem(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	// Eat-and-pick gather source: a need drop on arrival AND a harvestable yield.
	body := `{"object_id":"obj1","rows":[` +
		`{"attribute":"hunger","amount":-2,"available_quantity":3,"max_quantity":3,"refresh_mode":"periodic","refresh_period_hours":6,"gather_item":"berries"}` +
		`]}`
	rec := post(t, srv, "/api/village/admin/object/set-refresh", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var res adminObjectRefreshResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(res.Rows) != 1 || res.Rows[0].GatherItem != "berries" {
		t.Fatalf("response rows = %+v, want gather_item berries echoed back", res.Rows)
	}
	// The live object actually carries the yield item — not stripped to "".
	got, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return string(world.VillageObjects["obj1"].Refreshes[0].GatherItem), nil
	}})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if got.(string) != "berries" {
		t.Errorf("live GatherItem = %q, want berries", got)
	}

	// Forage-to-sell row (amount == 0) is valid ONLY with a gather_item, so its
	// acceptance proves the field now reaches validation (pre-LLM-109 this 400'd).
	forage := `{"object_id":"obj1","rows":[` +
		`{"attribute":"hunger","amount":0,"available_quantity":3,"max_quantity":3,"refresh_mode":"periodic","refresh_period_hours":6,"gather_item":"raspberries"}` +
		`]}`
	rec = post(t, srv, "/api/village/admin/object/set-refresh", forage)
	if rec.Code != http.StatusOK {
		t.Fatalf("forage-to-sell status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	res = adminObjectRefreshResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode forage: %v", err)
	}
	if len(res.Rows) != 1 || res.Rows[0].Amount != 0 || res.Rows[0].GatherItem != "raspberries" {
		t.Errorf("forage row = %+v, want amount 0 + gather_item raspberries", res.Rows)
	}
}

// TestHandleAdminObjectSetRefresh_ClearsToEmptyArray: an empty rows clears the
// set and the response body carries [] (not null) per the always-an-array rule.
func TestHandleAdminObjectSetRefresh_ClearsToEmptyArray(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/set-refresh", `{"object_id":"obj1","rows":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// The body must contain "rows":[] — a null would break the editor's array read.
	if !strings.Contains(rec.Body.String(), `"rows":[]`) {
		t.Errorf("body = %s, want rows:[]", rec.Body.String())
	}
}

func TestHandleAdminObjectSetRefresh_Invalid(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	// Positive amount violates the amount_negative CHECK → 400.
	rec := post(t, srv, "/api/village/admin/object/set-refresh",
		`{"object_id":"obj1","rows":[{"attribute":"thirst","amount":5}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminObjectSetRefresh_NotFound(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/set-refresh",
		`{"object_id":"ghost","rows":[{"attribute":"thirst","amount":-1}]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminObjectSetRefresh_MissingObjectID(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/admin/object/set-refresh", `{"rows":[]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminObjectSetRefresh_Forbidden(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := post(t, srv, "/api/village/admin/object/set-refresh",
		`{"object_id":"obj1","rows":[]}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminObjectSetRefresh_TrailingJSON(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})
	rec := post(t, srv, "/api/village/admin/object/set-refresh",
		`{"object_id":"obj1","rows":[]} garbage`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}
