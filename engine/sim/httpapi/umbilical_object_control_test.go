package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/telemetry"
)

// Operator-gated object lifecycle on the umbilical (LLM-61). These mirror the
// admin/object/* HTTP contract (admin_object_handlers_test.go) but exercise the
// operator gate — no in-world admin actor is seeded, yet the operator
// (operatorPerms) is allowed. The seeded world (server_test.go) carries obj1
// (asset-x, world-pixel (5.5,6.5), display "Tavern") and asset-y (a Bush).

// TestUmbilicalObjectCreate places a new object and confirms it landed live —
// the thing an operator could not do before (the admin route 403s without an
// in-world admin actor row).
func TestUmbilicalObjectCreate(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)

	rec := postReq(t, h, "/api/village/umbilical/object/create", "tok", `{"asset_id":"asset-y","x":10,"y":10}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out adminObjectCreateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ID == "" || out.AssetID != "asset-y" || out.X != 10 || out.Y != 10 {
		t.Fatalf("response = %+v, want a minted asset-y at (10,10)", out)
	}
	// Confirm the object actually exists in the live world.
	res, _ := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		_, ok := world.VillageObjects[sim.VillageObjectID(out.ID)]
		return ok, nil
	}})
	if exists, _ := res.(bool); !exists {
		t.Errorf("created object %q not present in live world", out.ID)
	}

	// Missing asset_id → 400; explicit-empty attached_to → 400; off-map → 422.
	if rec := postReq(t, h, "/api/village/umbilical/object/create", "tok", `{"x":1,"y":1}`); rec.Code != http.StatusBadRequest {
		t.Errorf("missing asset_id = %d, want 400", rec.Code)
	}
	if rec := postReq(t, h, "/api/village/umbilical/object/create", "tok", `{"asset_id":"asset-y","x":1,"y":1,"attached_to":""}`); rec.Code != http.StatusBadRequest {
		t.Errorf("empty attached_to = %d, want 400", rec.Code)
	}
	if rec := postReq(t, h, "/api/village/umbilical/object/create", "tok", `{"asset_id":"asset-y","x":99999999,"y":0}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("off-map = %d, want 422", rec.Code)
	}
}

// TestUmbilicalObjectMove repositions obj1 and checks the live anchor, plus the
// rejection matrix.
func TestUmbilicalObjectMove(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)

	rec := postReq(t, h, "/api/village/umbilical/object/move", "tok", `{"object_id":"obj1","x":12.5,"y":20.25}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("move = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out adminObjectMoveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ID != "obj1" || out.X != 12.5 || out.Y != 20.25 {
		t.Errorf("response = %+v, want obj1 (12.5, 20.25)", out)
	}
	res, _ := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.VillageObjects["obj1"].Pos, nil
	}})
	if pos, _ := res.(sim.WorldPos); pos.X != 12.5 || pos.Y != 20.25 {
		t.Errorf("live obj1 pos = %v, want (12.5, 20.25)", pos)
	}

	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"x":1,"y":2}`, http.StatusBadRequest},                                    // missing object_id
		{`{"object_id":"ghost","x":1,"y":2}`, http.StatusNotFound},                  // unknown object
		{`{"object_id":"obj1","x":99999999,"y":0}`, http.StatusUnprocessableEntity}, // off-map
	} {
		if rec := postReq(t, h, "/api/village/umbilical/object/move", "tok", tc.body); rec.Code != tc.want {
			t.Errorf("move %s = %d, want %d", tc.body, rec.Code, tc.want)
		}
	}
}

// TestUmbilicalObjectDelete removes obj1, then proves the structure-backed
// refusal (422) and the not-found (404) paths.
func TestUmbilicalObjectDelete(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)

	rec := postReq(t, h, "/api/village/umbilical/object/delete", "tok", `{"object_id":"obj1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out adminObjectDeleteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.DeletedIDs) != 1 || out.DeletedIDs[0] != "obj1" {
		t.Errorf("deleted_ids = %v, want [obj1]", out.DeletedIDs)
	}
	res, _ := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		_, ok := world.VillageObjects["obj1"]
		return ok, nil
	}})
	if exists, _ := res.(bool); exists {
		t.Error("obj1 still present after delete")
	}

	// Missing object_id → 400; unknown → 404.
	if rec := postReq(t, h, "/api/village/umbilical/object/delete", "tok", `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("missing object_id = %d, want 400", rec.Code)
	}
	if rec := postReq(t, h, "/api/village/umbilical/object/delete", "tok", `{"object_id":"ghost"}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown object = %d, want 404", rec.Code)
	}

	// A structure-backed object is refused (422) — fresh server since obj1 is gone.
	srv2, h2 := controlServer(t, operatorPerms)
	seedStructureBridge(t, srv2.world, "obj1")
	if rec := postReq(t, h2, "/api/village/umbilical/object/delete", "tok", `{"object_id":"obj1"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("delete structure-backed = %d, want 422", rec.Code)
	}
}

// TestUmbilicalObjectSetDisplayName renames obj1 (the LLM-60 motivating case) and
// covers the rejection matrix.
func TestUmbilicalObjectSetDisplayName(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)

	rec := postReq(t, h, "/api/village/umbilical/object/set-display-name", "tok", `{"object_id":"obj1","display_name":"The Crow's Nest"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("set-display-name = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out adminObjectDisplayNameResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ID != "obj1" || out.DisplayName != "The Crow's Nest" {
		t.Errorf("response = %+v, want obj1 / The Crow's Nest", out)
	}
	res, _ := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.VillageObjects["obj1"].DisplayName, nil
	}})
	if name, _ := res.(string); name != "The Crow's Nest" {
		t.Errorf("live obj1 display name = %q, want The Crow's Nest", name)
	}

	// Missing object_id → 400; unknown → 404; over-cap name → 400.
	if rec := postReq(t, h, "/api/village/umbilical/object/set-display-name", "tok", `{"display_name":"X"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("missing object_id = %d, want 400", rec.Code)
	}
	if rec := postReq(t, h, "/api/village/umbilical/object/set-display-name", "tok", `{"object_id":"ghost","display_name":"X"}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown object = %d, want 404", rec.Code)
	}
	overCap := `{"object_id":"obj1","display_name":"` + strings.Repeat("z", sim.MaxVillageObjectDisplayNameLen+1) + `"}`
	if rec := postReq(t, h, "/api/village/umbilical/object/set-display-name", "tok", overCap); rec.Code != http.StatusBadRequest {
		t.Errorf("over-cap name = %d, want 400", rec.Code)
	}
}

// TestUmbilicalObjectControl_NewRoutesGated: the object routes obey the same two
// gates as the rest of the control whitelist — 404 when control is disabled (never
// registered), 403 for an authenticated non-operator.
func TestUmbilicalObjectControl_NewRoutesGated(t *testing.T) {
	paths := []string{
		"/api/village/umbilical/object/create",
		"/api/village/umbilical/object/move",
		"/api/village/umbilical/object/delete",
		"/api/village/umbilical/object/set-display-name",
	}
	// Umbilical on but control NOT enabled → 404.
	srv := NewServer(seededWorld(t), permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(4))
	off := srv.Handler()
	for _, p := range paths {
		if rec := postReq(t, off, p, "tok", `{}`); rec.Code != http.StatusNotFound {
			t.Errorf("%s control-disabled = %d, want 404", p, rec.Code)
		}
	}
	// Control on but non-operator → 403.
	_, nonOp := controlServer(t, nil)
	for _, p := range paths {
		if rec := postReq(t, nonOp, p, "tok", `{}`); rec.Code != http.StatusForbidden {
			t.Errorf("%s non-operator = %d, want 403", p, rec.Code)
		}
	}
}
