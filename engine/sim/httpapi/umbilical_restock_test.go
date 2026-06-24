package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_restock_test.go — handler coverage for the restock-policy control
// routes (LLM-95). The deep mutation/projection logic is covered in
// engine/sim/restock_commands_test.go; these assert the HTTP plumbing: status
// mapping, request validation, and the echoed entries. Control-route gating
// (404 when control disabled, 403/401 on the auth gate) is covered generically
// for every control route in umbilical_test.go.

// restockServer builds a control-enabled server and seeds a small catalog:
// cheese (recipe present), ale (no recipe). hannah is the editable NPC; bram is
// a PC.
func restockServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	srv, h := controlServer(t, operatorPerms)
	_, err := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.ItemKinds = map[sim.ItemKind]*sim.ItemKindDef{
			"cheese": {Name: "cheese", DisplayLabel: "Cheese"},
			"ale":    {Name: "ale", DisplayLabel: "Ale"},
		}
		world.Recipes = map[sim.ItemKind]*sim.ItemRecipe{
			"cheese": {OutputItem: "cheese", OutputQty: 1, RateQty: 1, RatePerHours: 1},
		}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
	return srv, h
}

func TestUmbilicalRestockSet_AddsProduce(t *testing.T) {
	srv, h := restockServer(t)

	rec := postReq(t, h, "/api/village/umbilical/restock/set", "tok",
		`{"actor_id":"hannah","item":"cheese","source":"produce","cap":12}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("set = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out umbilicalRestockResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ActorID != "hannah" || len(out.Entries) != 1 ||
		out.Entries[0].Item != "cheese" || out.Entries[0].Source != "produce" || out.Entries[0].Cap != 12 {
		t.Fatalf("response = %+v, want one cheese/produce/cap12 entry", out)
	}

	// The live actor's projected policy reflects the edit.
	res, err := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors["hannah"].RestockPolicy != nil, nil
	}})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if has, _ := res.(bool); !has {
		t.Error("hannah has no RestockPolicy after set")
	}
}

func TestUmbilicalRestockSet_Validation(t *testing.T) {
	_, h := restockServer(t)
	cases := []struct {
		name string
		body string
	}{
		{"missing actor_id", `{"item":"cheese","source":"produce","cap":1}`},
		{"missing item", `{"actor_id":"hannah","source":"produce","cap":1}`},
		{"bad source", `{"actor_id":"hannah","item":"cheese","source":"hoard","cap":1}`},
		{"negative cap", `{"actor_id":"hannah","item":"cheese","source":"produce","cap":-1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postReq(t, h, "/api/village/umbilical/restock/set", "tok", tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s = %d, want 400; body=%s", tc.name, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestUmbilicalRestockSet_StatusMapping(t *testing.T) {
	_, h := restockServer(t)
	cases := []struct {
		name string
		body string
		want int
	}{
		{"unknown actor", `{"actor_id":"ghost","item":"cheese","source":"produce","cap":1}`, http.StatusNotFound},
		{"pc target", `{"actor_id":"bram","item":"cheese","source":"produce","cap":1}`, http.StatusNotFound},
		{"unknown item", `{"actor_id":"hannah","item":"dragonfruit","source":"buy","cap":1}`, http.StatusUnprocessableEntity},
		{"produce no recipe", `{"actor_id":"hannah","item":"ale","source":"produce","cap":1}`, http.StatusUnprocessableEntity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postReq(t, h, "/api/village/umbilical/restock/set", "tok", tc.body)
			if rec.Code != tc.want {
				t.Errorf("%s = %d, want %d; body=%s", tc.name, rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestUmbilicalRestockRemove(t *testing.T) {
	_, h := restockServer(t)

	if rec := postReq(t, h, "/api/village/umbilical/restock/set", "tok",
		`{"actor_id":"hannah","item":"cheese","source":"produce","cap":12}`); rec.Code != http.StatusOK {
		t.Fatalf("setup set = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	rec := postReq(t, h, "/api/village/umbilical/restock/remove", "tok",
		`{"actor_id":"hannah","item":"cheese"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("remove = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out umbilicalRestockResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Entries) != 0 {
		t.Errorf("entries = %+v, want empty after remove", out.Entries)
	}

	// Removing it again is a 404 (nothing to remove).
	if rec := postReq(t, h, "/api/village/umbilical/restock/remove", "tok",
		`{"actor_id":"hannah","item":"cheese"}`); rec.Code != http.StatusNotFound {
		t.Errorf("second remove = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}
