package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_settings_set_test.go — LLM-577. The generic settings/set route and
// the widened GET /settings `all` block.

func TestUmbilicalSettingSet_WritesKeysNoRouteEverCovered(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)

	// world_dusk_time and the visitor_* family are the two shapes of the gap
	// this ticket closes: dusk was neither readable nor writable over HTTP, and
	// the visitor knobs were readable but had no setter.
	cases := []struct {
		key, value string
	}{
		{"world_dusk_time", "18:30"},
		{"visitor_spawn_chance_permille", "12"},
		{"cold_night_multiplier_x100", "150"},
		{"eco_social_gap_seconds", "45"},
		{"eco_enabled", "false"},
	}
	for _, tc := range cases {
		body, err := json.Marshal(umbilicalSettingSetRequest{Key: tc.key, Value: tc.value})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rec := postReq(t, h, "/api/village/umbilical/settings/set", "tok", string(body))
		if rec.Code != http.StatusOK {
			t.Fatalf("set %s = %d, want 200; body=%s", tc.key, rec.Code, rec.Body.String())
		}
		var out umbilicalSettingSetResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out.Key != tc.key || out.Value != tc.value {
			t.Errorf("response = %+v, want key %q value %q", out, tc.key, tc.value)
		}
		if !out.Persisted {
			t.Errorf("%s reported persisted=false — a live tune that reverts on restart", tc.key)
		}
	}

	// Applied to the live world, not just echoed back.
	res, _ := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return [3]any{world.Settings.DuskTime, world.Settings.VisitorSpawnChancePermille, world.Settings.EcoSocialGap}, nil
	}})
	got, ok := res.([3]any)
	if !ok {
		t.Fatal("unexpected settings read")
	}
	if got[0] != "18:30" {
		t.Errorf("live DuskTime = %v, want 18:30", got[0])
	}
	if got[1] != 12 {
		t.Errorf("live VisitorSpawnChancePermille = %v, want 12", got[1])
	}
	if got[2] != 45*time.Second {
		t.Errorf("live EcoSocialGap = %v, want 45s", got[2])
	}
}

// The GET's `all` block must cover every registered key and reflect a write, so
// an operator can discover a key and then set it without reading the source.
func TestUmbilicalSettings_AllBlockIsCompleteAndReflectsWrites(t *testing.T) {
	_, h := controlServer(t, operatorPerms)

	if rec := postReq(t, h, "/api/village/umbilical/settings/set", "tok",
		`{"key":"world_dusk_time","value":"18:30"}`); rec.Code != http.StatusOK {
		t.Fatalf("set dusk = %d; body=%s", rec.Code, rec.Body.String())
	}

	grec := req(t, h, "/api/village/umbilical/settings", "tok")
	if grec.Code != http.StatusOK {
		t.Fatalf("GET settings = %d, want 200", grec.Code)
	}
	var dto UmbilicalSettingsDTO
	if err := json.Unmarshal(grec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode settings: %v", err)
	}

	for _, key := range sim.SettingKeys() {
		if _, ok := dto.All[key]; !ok {
			t.Errorf("GET /settings all is missing registered key %q", key)
		}
		if _, ok := dto.AllMeta[key]; !ok {
			t.Errorf("GET /settings all_meta is missing registered key %q", key)
		}
	}
	if dto.All["world_dusk_time"] != "18:30" {
		t.Errorf("all[world_dusk_time] = %q, want 18:30", dto.All["world_dusk_time"])
	}
	// The metadata a caller needs before writing.
	meta := dto.AllMeta["world_dusk_time"]
	if meta.Kind != string(sim.SettingKindString) || !meta.Persisted {
		t.Errorf("all_meta[world_dusk_time] = %+v, want a persisted string", meta)
	}
	if meta.TakesEffect != string(sim.SettingEffectImmediate) {
		t.Errorf("all_meta[world_dusk_time].takes_effect = %q, want %q",
			meta.TakesEffect, sim.SettingEffectImmediate)
	}
}

// A typo'd key must be a 400, not a 200 that changed nothing — the failure an
// operator would otherwise take for success.
func TestUmbilicalSettingSet_Validation(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)

	bad := []string{
		`{}`, // no key
		`{"key":"world_dusk_tiem","value":"19:00"}`,       // typo'd key
		`{"key":"seek_work_coin_ceiling","value":"lots"}`, // wrong shape for the kind
		`{"key":"eco_social_gap_seconds","value":"-5"}`,   // negative duration
		`{"key":"world_timezone","value":"Mars/Olympus_Mons"}`,
		`{"key":"world_dusk_time","value":"   "}`, // empty reads as unset to the loader
	}
	for _, body := range bad {
		if rec := postReq(t, h, "/api/village/umbilical/settings/set", "tok", body); rec.Code != http.StatusBadRequest {
			t.Errorf("body %s = %d, want 400", body, rec.Code)
		}
	}

	// A rejected timezone must not have half-applied (name moved, zone didn't).
	res, _ := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Settings.Timezone, nil
	}})
	if tz, _ := res.(string); tz == "Mars/Olympus_Mons" {
		t.Error("a rejected timezone write still landed")
	}
}

// The route is part of the control surface, so it must be absent unless control
// is explicitly enabled — the same second opt-in every mutator rides.
func TestUmbilicalSettingSet_RequiresControlEnabled(t *testing.T) {
	srv := NewServer(seededWorld(t), permAuth{operatorPerms})
	h := srv.Handler() // umbilical on, control NOT enabled

	for _, p := range []string{
		"/api/village/umbilical/settings/set",
		"/api/village/umbilical/npc/set-schedule",
	} {
		if rec := postReq(t, h, p, "tok", `{}`); rec.Code != http.StatusNotFound {
			t.Errorf("%s with control disabled = %d, want 404", p, rec.Code)
		}
	}
}
