package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_settings_test.go — coverage for the settings read (LLM-110): the get
// that pairs with the settings/need-threshold control route.

func TestUmbilicalSettings_NeedThresholds(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)
	if _, err := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.NeedThresholds = sim.NeedThresholds{"hunger": 20, "thirst": 18}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed thresholds: %v", err)
	}

	rec := req(t, h, "/api/village/umbilical/settings", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("settings = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out UmbilicalSettingsDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.NeedThresholds["hunger"] != 20 || out.NeedThresholds["thirst"] != 18 {
		t.Fatalf("need_thresholds = %v, want hunger:20 thirst:18", out.NeedThresholds)
	}
}
