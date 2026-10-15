package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// TestUmbilicalMinorDamage (LLM-790): a crate with a loose-lid state breaks
// by form (200, the response names it); then nothing is left (409); an
// unknown form is 400, an unknown id 404; the route honors the gate.
func TestUmbilicalMinorDamage(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)
	if _, err := srv.world.Send(sim.Command{Fn: func(wd *sim.World) (any, error) {
		wd.Assets["crate-asset"] = &sim.Asset{ID: "crate-asset", Name: "Crate", DefaultState: "default", States: []sim.AssetState{
			{ID: 200, State: "default"},
			{ID: 201, State: "lid-loose", Tags: []string{"minor-of-default", "minor-form-crate"}},
		}}
		wd.VillageObjects["crate"] = &sim.VillageObject{ID: "crate", AssetID: "crate-asset", CurrentState: "default", Pos: sim.TilePos{X: 80, Y: 10}.Center()}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if rec := postReq(t, h, "/api/village/umbilical/minor/damage", "tok", `{"form":"gate"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown form = %d, want 400", rec.Code)
	}
	if rec := postReq(t, h, "/api/village/umbilical/minor/damage", "tok", `{"id":"ghost"}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown id = %d, want 404", rec.Code)
	}
	if rec := postReq(t, h, "/api/village/umbilical/minor/damage", "tok", `{"form":"fence"}`); rec.Code != http.StatusConflict {
		t.Errorf("no fence = %d, want 409", rec.Code)
	}
	rec := postReq(t, h, "/api/village/umbilical/minor/damage", "tok", `{"form":"crate"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("crate = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out umbilicalMinorDamageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out != (umbilicalMinorDamageResponse{ID: "crate", State: "lid-loose", Form: "crate"}) {
		t.Errorf("response = %+v, want the crate's loose lid", out)
	}
	if rec := postReq(t, h, "/api/village/umbilical/minor/damage", "tok", `{}`); rec.Code != http.StatusConflict {
		t.Errorf("nothing left = %d, want 409", rec.Code)
	}
	_, gated := controlServer(t, nil)
	if rec := postReq(t, gated, "/api/village/umbilical/minor/damage", "tok", `{}`); rec.Code != http.StatusForbidden {
		t.Errorf("non-operator = %d, want 403", rec.Code)
	}
	if rec := postReq(t, gated, "/api/village/umbilical/minor/damage", "", `{}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", rec.Code)
	}
}
