package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_labor_boost_test.go — LLM-224. The settings/labor-produce-boost control
// route: live-tune the per-worker produce boost, validation (0 is a VALID off-switch,
// unlike the seek-work ceiling), and the GET /settings read-back.

func TestUmbilicalLaborBoost_TunesAndReadsBack(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)

	rec := postReq(t, h, "/api/village/umbilical/settings/labor-produce-boost", "tok", `{"boost_pct":75}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("tune = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out umbilicalLaborBoostResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.BoostPct != 75 {
		t.Errorf("response = %+v, want boost_pct 75", out)
	}

	// Applied to live WorldSettings.
	res, _ := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Settings.LaborProduceBoostPct, nil
	}})
	if got, _ := res.(int); got != 75 {
		t.Errorf("live settings boost = %v, want 75", res)
	}

	// GET /settings reflects it (the read side pairs with the set).
	grec := req(t, h, "/api/village/umbilical/settings", "tok")
	if grec.Code != http.StatusOK {
		t.Fatalf("GET settings = %d, want 200", grec.Code)
	}
	var sdto UmbilicalSettingsDTO
	if err := json.Unmarshal(grec.Body.Bytes(), &sdto); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if sdto.LaborProduceBoostPct != 75 {
		t.Errorf("GET settings labor_produce_boost_pct = %d, want 75", sdto.LaborProduceBoostPct)
	}

	// 0 is the explicit off-switch — accepted, not rejected.
	if rec := postReq(t, h, "/api/village/umbilical/settings/labor-produce-boost", "tok", `{"boost_pct":0}`); rec.Code != http.StatusOK {
		t.Errorf("boost_pct 0 = %d, want 200 (0 disables the boost)", rec.Code)
	}
}

// TestUmbilicalLaborBoost_Validation: empty body and a negative percent → 400.
func TestUmbilicalLaborBoost_Validation(t *testing.T) {
	_, h := controlServer(t, operatorPerms)
	bad := []string{
		`{}`,                // nothing supplied
		`{"boost_pct":-10}`, // negative is rejected
	}
	for _, body := range bad {
		if rec := postReq(t, h, "/api/village/umbilical/settings/labor-produce-boost", "tok", body); rec.Code != http.StatusBadRequest {
			t.Errorf("body %s = %d, want 400", body, rec.Code)
		}
	}
}
