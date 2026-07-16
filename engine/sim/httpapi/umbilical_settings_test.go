package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

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

// decodeSettings fetches and decodes GET /umbilical/settings, failing the test on
// a non-200 or a decode error.
func decodeSettings(t *testing.T, h http.Handler) UmbilicalSettingsDTO {
	t.Helper()
	rec := req(t, h, "/api/village/umbilical/settings", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("settings = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out UmbilicalSettingsDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// TestUmbilicalSettings_VisitorCascade_Explicit verifies the visitor-cascade knobs
// (LLM-437) pass through verbatim when set to concrete non-default values — the
// normal case, where world.Settings already holds the env-configured figures.
func TestUmbilicalSettings_VisitorCascade_Explicit(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)
	if _, err := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.VisitorSpawnChancePermille = 25
		world.Settings.VisitorMaxConcurrent = 3
		world.Settings.VisitorTickInterval = 90 * time.Second
		world.Settings.VisitorReturnMinDays = 7
		world.Settings.VisitorReturnMaxDays = 30
		world.Settings.VisitorFactorPackUnits = 4
		world.Settings.VisitorFactorPurseMin = 150
		world.Settings.VisitorFactorPurseMax = 250
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed visitor settings: %v", err)
	}

	out := decodeSettings(t, h)
	if out.VisitorSpawnChancePermille != 25 {
		t.Errorf("visitor_spawn_chance_permille = %d, want 25", out.VisitorSpawnChancePermille)
	}
	if out.VisitorMaxConcurrent != 3 {
		t.Errorf("visitor_max_concurrent = %d, want 3", out.VisitorMaxConcurrent)
	}
	if out.VisitorTickIntervalSeconds != 90 {
		t.Errorf("visitor_tick_interval_seconds = %d, want 90", out.VisitorTickIntervalSeconds)
	}
	if out.VisitorReturnMinDays != 7 {
		t.Errorf("visitor_return_min_days = %d, want 7", out.VisitorReturnMinDays)
	}
	if out.VisitorReturnMaxDays != 30 {
		t.Errorf("visitor_return_max_days = %d, want 30", out.VisitorReturnMaxDays)
	}
	if out.VisitorFactorPackUnits != 4 {
		t.Errorf("visitor_factor_pack_units = %d, want 4", out.VisitorFactorPackUnits)
	}
	if out.VisitorFactorPurseMin != 150 {
		t.Errorf("visitor_factor_purse_min = %d, want 150", out.VisitorFactorPurseMin)
	}
	if out.VisitorFactorPurseMax != 250 {
		t.Errorf("visitor_factor_purse_max = %d, want 250", out.VisitorFactorPurseMax)
	}
}

// TestUmbilicalSettings_VisitorCascade_Effective pins the effective-value clamps
// (LLM-437): with everything stored at 0 the report shows the figure each
// dispatcher actually uses. Note the deliberate asymmetry — spawn-chance stays 0
// (OFF is a real setting), max-concurrent/tick/return-min resolve to their Default,
// but the purse and return-max knobs clamp to a floor/companion (0 and the effective
// min) rather than the env-loader default, mirroring dispatchVisitorSpawn/scheduleReturn.
func TestUmbilicalSettings_VisitorCascade_Effective(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)
	if _, err := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.VisitorSpawnChancePermille = 0
		world.Settings.VisitorMaxConcurrent = 0
		world.Settings.VisitorTickInterval = 0
		world.Settings.VisitorReturnMinDays = 0
		world.Settings.VisitorReturnMaxDays = 0
		world.Settings.VisitorFactorPackUnits = 0
		world.Settings.VisitorFactorPurseMin = 0
		world.Settings.VisitorFactorPurseMax = 0
		return nil, nil
	}}); err != nil {
		t.Fatalf("zero visitor settings: %v", err)
	}

	out := decodeSettings(t, h)
	if out.VisitorSpawnChancePermille != 0 {
		t.Errorf("visitor_spawn_chance_permille = %d, want 0 (raw, OFF)", out.VisitorSpawnChancePermille)
	}
	if out.VisitorMaxConcurrent != sim.DefaultVisitorMaxConcurrent {
		t.Errorf("visitor_max_concurrent = %d, want %d (default)", out.VisitorMaxConcurrent, sim.DefaultVisitorMaxConcurrent)
	}
	wantTick := int(sim.DefaultVisitorTickInterval / time.Second)
	if out.VisitorTickIntervalSeconds != wantTick {
		t.Errorf("visitor_tick_interval_seconds = %d, want %d (default)", out.VisitorTickIntervalSeconds, wantTick)
	}
	if out.VisitorReturnMinDays != sim.DefaultVisitorReturnMinDays {
		t.Errorf("visitor_return_min_days = %d, want %d (default)", out.VisitorReturnMinDays, sim.DefaultVisitorReturnMinDays)
	}
	// return-max clamps to the effective min, NOT to DefaultVisitorReturnMaxDays.
	if out.VisitorReturnMaxDays != sim.DefaultVisitorReturnMinDays {
		t.Errorf("visitor_return_max_days = %d, want %d (clamped to eff min)", out.VisitorReturnMaxDays, sim.DefaultVisitorReturnMinDays)
	}
	if out.VisitorFactorPackUnits != sim.DefaultVisitorFactorPackUnits {
		t.Errorf("visitor_factor_pack_units = %d, want %d (default)", out.VisitorFactorPackUnits, sim.DefaultVisitorFactorPackUnits)
	}
	// purse floor clamps neg->0; a stored 0 stays 0, it does NOT resolve to the
	// env-loader default (120/200).
	if out.VisitorFactorPurseMin != 0 {
		t.Errorf("visitor_factor_purse_min = %d, want 0 (floor, not env default)", out.VisitorFactorPurseMin)
	}
	if out.VisitorFactorPurseMax != 0 {
		t.Errorf("visitor_factor_purse_max = %d, want 0 (clamped to eff min)", out.VisitorFactorPurseMax)
	}
}
