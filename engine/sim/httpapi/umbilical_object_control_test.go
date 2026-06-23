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
		"/api/village/umbilical/object/set-state",
		"/api/village/umbilical/object/set-owner",
		"/api/village/umbilical/object/set-loiter-offset",
		"/api/village/umbilical/object/set-entry-policy",
		"/api/village/umbilical/object/add-tag",
		"/api/village/umbilical/object/remove-tag",
		"/api/village/umbilical/object/set-refresh",
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

// TestUmbilicalObjectSetState flips obj1's state (seeded "lit") and covers the
// not-found translation (SetVillageObjectState reports a missing object as a
// result Reason, which the handler turns into a 404).
func TestUmbilicalObjectSetState(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)

	rec := postReq(t, h, "/api/village/umbilical/object/set-state", "tok", `{"object_id":"obj1","state":"unlit"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("set-state = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out adminObjectStateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ID != "obj1" || out.State != "unlit" || !out.Applied {
		t.Errorf("response = %+v, want obj1/unlit/applied=true", out)
	}
	res, _ := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.VillageObjects["obj1"].CurrentState, nil
	}})
	if st, _ := res.(string); st != "unlit" {
		t.Errorf("live obj1 state = %q, want unlit", st)
	}

	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"object_id":"obj1"}`, http.StatusBadRequest},                // missing state
		{`{"state":"unlit"}`, http.StatusBadRequest},                   // missing object_id
		{`{"object_id":"ghost","state":"unlit"}`, http.StatusNotFound}, // not-found translation
	} {
		if rec := postReq(t, h, "/api/village/umbilical/object/set-state", "tok", tc.body); rec.Code != tc.want {
			t.Errorf("set-state %s = %d, want %d", tc.body, rec.Code, tc.want)
		}
	}
}

// TestUmbilicalObjectSetOwner sets/clears obj1's owner; covers the dangling-owner
// 422 (the only-422 case the shared error mapper routes through the default arm).
func TestUmbilicalObjectSetOwner(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)

	if rec := postReq(t, h, "/api/village/umbilical/object/set-owner", "tok", `{"object_id":"obj1","owner_actor_id":"hannah"}`); rec.Code != http.StatusOK {
		t.Fatalf("set-owner = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	res, _ := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.VillageObjects["obj1"].OwnerActorID, nil
	}})
	if owner, _ := res.(sim.ActorID); owner != "hannah" {
		t.Errorf("live owner = %q, want hannah", owner)
	}
	// Clear.
	if rec := postReq(t, h, "/api/village/umbilical/object/set-owner", "tok", `{"object_id":"obj1","owner_actor_id":""}`); rec.Code != http.StatusOK {
		t.Fatalf("clear owner = %d, want 200", rec.Code)
	}
	res, _ = srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.VillageObjects["obj1"].OwnerActorID, nil
	}})
	if owner, _ := res.(sim.ActorID); owner != "" {
		t.Errorf("live owner = %q, want cleared", owner)
	}

	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"owner_actor_id":"hannah"}`, http.StatusBadRequest},                            // missing object_id
		{`{"object_id":"ghost","owner_actor_id":"hannah"}`, http.StatusNotFound},          // unknown object
		{`{"object_id":"obj1","owner_actor_id":"ghost"}`, http.StatusUnprocessableEntity}, // dangling owner
	} {
		if rec := postReq(t, h, "/api/village/umbilical/object/set-owner", "tok", tc.body); rec.Code != tc.want {
			t.Errorf("set-owner %s = %d, want %d", tc.body, rec.Code, tc.want)
		}
	}
}

// TestUmbilicalObjectSetLoiterOffset sets/clears the offset and pins the
// both-or-neither rule (a lone axis is a 400).
func TestUmbilicalObjectSetLoiterOffset(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)

	rec := postReq(t, h, "/api/village/umbilical/object/set-loiter-offset", "tok", `{"object_id":"obj1","x":2,"y":-3}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("set offset = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out adminObjectLoiterOffsetResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.X == nil || out.Y == nil || *out.X != 2 || *out.Y != -3 {
		t.Errorf("response offset = (%v,%v), want (2,-3)", out.X, out.Y)
	}
	res, _ := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		o := world.VillageObjects["obj1"]
		return []*int{o.LoiterOffsetX, o.LoiterOffsetY}, nil
	}})
	if v, _ := res.([]*int); v[0] == nil || *v[0] != 2 || v[1] == nil || *v[1] != -3 {
		t.Errorf("live offset = %v, want (2,-3)", v)
	}

	// Clear (both omitted) → 200, nulls.
	if rec := postReq(t, h, "/api/village/umbilical/object/set-loiter-offset", "tok", `{"object_id":"obj1"}`); rec.Code != http.StatusOK {
		t.Fatalf("clear offset = %d, want 200", rec.Code)
	}
	// Only one axis → 400; unknown object → 404.
	if rec := postReq(t, h, "/api/village/umbilical/object/set-loiter-offset", "tok", `{"object_id":"obj1","x":2}`); rec.Code != http.StatusBadRequest {
		t.Errorf("lone axis = %d, want 400", rec.Code)
	}
	if rec := postReq(t, h, "/api/village/umbilical/object/set-loiter-offset", "tok", `{"object_id":"ghost","x":1,"y":1}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown object = %d, want 404", rec.Code)
	}
}

// TestUmbilicalObjectSetEntryPolicy applies a policy and pins the handler-side
// enum validation (400).
func TestUmbilicalObjectSetEntryPolicy(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)

	rec := postReq(t, h, "/api/village/umbilical/object/set-entry-policy", "tok", `{"object_id":"obj1","entry_policy":"owner-only"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("set-entry-policy = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out adminObjectEntryPolicyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.EntryPolicy != "owner-only" {
		t.Errorf("response policy = %q, want owner-only", out.EntryPolicy)
	}
	res, _ := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.VillageObjects["obj1"].EntryPolicy, nil
	}})
	if p, _ := res.(sim.EntryPolicy); p != sim.EntryPolicyOwner {
		t.Errorf("live policy = %q, want owner-only", p)
	}

	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"object_id":"obj1","entry_policy":"bogus"}`, http.StatusBadRequest}, // unknown policy
		{`{"entry_policy":"open"}`, http.StatusBadRequest},                     // missing object_id
		{`{"object_id":"ghost","entry_policy":"open"}`, http.StatusNotFound},   // unknown object
	} {
		if rec := postReq(t, h, "/api/village/umbilical/object/set-entry-policy", "tok", tc.body); rec.Code != tc.want {
			t.Errorf("set-entry-policy %s = %d, want %d", tc.body, rec.Code, tc.want)
		}
	}
}

// TestUmbilicalObjectTags adds and removes per-instance tags (obj1 seeds with
// ["vendor"]) and pins the invalid-tag (400) + always-an-array contract.
func TestUmbilicalObjectTags(t *testing.T) {
	_, h := controlServer(t, operatorPerms)

	// Add a tag → full set carries it.
	rec := postReq(t, h, "/api/village/umbilical/object/add-tag", "tok", `{"object_id":"obj1","tag":"market"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("add-tag = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out adminObjectTagResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	has := func(tags []string, t string) bool {
		for _, x := range tags {
			if x == t {
				return true
			}
		}
		return false
	}
	if !has(out.Tags, "market") || !has(out.Tags, "vendor") {
		t.Errorf("tags after add = %v, want vendor+market", out.Tags)
	}

	// Remove "market", then "vendor" — the second empties the set; its body must
	// carry [] (not null), the always-an-array contract.
	if rec := postReq(t, h, "/api/village/umbilical/object/remove-tag", "tok", `{"object_id":"obj1","tag":"market"}`); rec.Code != http.StatusOK {
		t.Fatalf("remove market = %d, want 200", rec.Code)
	}
	rec = postReq(t, h, "/api/village/umbilical/object/remove-tag", "tok", `{"object_id":"obj1","tag":"vendor"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"tags":[]`) {
		t.Errorf("last remove = %d body=%s, want 200 + tags:[]", rec.Code, rec.Body.String())
	}

	// Invalid (blank) tag → 400; unknown object → 404; missing object_id → 400.
	if rec := postReq(t, h, "/api/village/umbilical/object/add-tag", "tok", `{"object_id":"obj1","tag":"   "}`); rec.Code != http.StatusBadRequest {
		t.Errorf("blank tag = %d, want 400", rec.Code)
	}
	if rec := postReq(t, h, "/api/village/umbilical/object/add-tag", "tok", `{"object_id":"ghost","tag":"x"}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown object = %d, want 404", rec.Code)
	}
	if rec := postReq(t, h, "/api/village/umbilical/object/remove-tag", "tok", `{"tag":"x"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("missing object_id = %d, want 400", rec.Code)
	}
}

// TestUmbilicalObjectSetRefresh replaces the refresh-policy set (the partner to
// set-display-name for fixing a gather/eat source live) and covers clear + the
// invalid-row 400.
func TestUmbilicalObjectSetRefresh(t *testing.T) {
	_, h := controlServer(t, operatorPerms)

	body := `{"object_id":"obj1","rows":[{"attribute":"thirst","amount":-12,"available_quantity":3,"max_quantity":10,"refresh_mode":"continuous","refresh_period_hours":2}]}`
	rec := postReq(t, h, "/api/village/umbilical/object/set-refresh", "tok", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("set-refresh = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out adminObjectRefreshResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Rows) != 1 || out.Rows[0].Attribute != "thirst" || out.Rows[0].Amount != -12 {
		t.Errorf("rows = %+v, want one thirst row at -12", out.Rows)
	}

	// Empty rows clears, and the body carries [] (not null).
	rec = postReq(t, h, "/api/village/umbilical/object/set-refresh", "tok", `{"object_id":"obj1","rows":[]}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"rows":[]`) {
		t.Errorf("clear = %d body=%s, want 200 + rows:[]", rec.Code, rec.Body.String())
	}

	// Positive amount violates the amount_negative CHECK → 400; unknown object → 404.
	if rec := postReq(t, h, "/api/village/umbilical/object/set-refresh", "tok", `{"object_id":"obj1","rows":[{"attribute":"thirst","amount":5}]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("positive amount = %d, want 400", rec.Code)
	}
	if rec := postReq(t, h, "/api/village/umbilical/object/set-refresh", "tok", `{"object_id":"ghost","rows":[{"attribute":"thirst","amount":-1}]}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown object = %d, want 404", rec.Code)
	}
}
