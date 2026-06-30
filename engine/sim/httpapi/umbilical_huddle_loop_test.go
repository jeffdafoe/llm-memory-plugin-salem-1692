package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_huddle_loop_test.go — LLM-183. The settings/huddle-loop control route:
// live-tune the loop-sweep knobs, the master off-switch (timeout 0), validation,
// and the GET /settings read-back.

func TestUmbilicalHuddleLoop_TunesAndReadsBack(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)

	rec := postReq(t, h, "/api/village/umbilical/settings/huddle-loop", "tok",
		`{"timeout_seconds":90,"repeat_percent":70,"cadence_seconds":20}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("tune = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out umbilicalHuddleLoopResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.TimeoutSeconds != 90 || out.RepeatPercent != 70 || out.CadenceSeconds != 20 || !out.Enabled {
		t.Errorf("response = %+v, want 90/70/20 enabled", out)
	}

	// Applied to live WorldSettings (seconds -> Duration).
	res, _ := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return []any{world.Settings.HuddleLoopTimeout, world.Settings.HuddleLoopRepeatPercent, world.Settings.HuddleLoopSweepCadence}, nil
	}})
	v, _ := res.([]any)
	if v[0].(time.Duration) != 90*time.Second || v[1].(int) != 70 || v[2].(time.Duration) != 20*time.Second {
		t.Errorf("settings = %v/%v/%v, want 90s/70/20s", v[0], v[1], v[2])
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
	if !sdto.HuddleLoopEnabled || sdto.HuddleLoopTimeoutSeconds != 90 ||
		sdto.HuddleLoopRepeatPercent != 70 || sdto.HuddleLoopSweepCadenceSeconds != 20 {
		t.Errorf("GET settings huddle-loop = %+v, want enabled 90/70/20", sdto)
	}
}

// TestUmbilicalHuddleLoop_TimeoutZeroDisables: timeout_seconds 0 is the master
// off-switch — valid, and reported back as disabled.
func TestUmbilicalHuddleLoop_TimeoutZeroDisables(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)
	if _, err := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.HuddleLoopTimeout = 60 * time.Second
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed enabled: %v", err)
	}

	rec := postReq(t, h, "/api/village/umbilical/settings/huddle-loop", "tok", `{"timeout_seconds":0}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out umbilicalHuddleLoopResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Enabled || out.TimeoutSeconds != 0 {
		t.Errorf("response = %+v, want disabled/0", out)
	}
}

// TestUmbilicalHuddleLoop_Validation: empty body and out-of-range knobs → 400.
func TestUmbilicalHuddleLoop_Validation(t *testing.T) {
	_, h := controlServer(t, operatorPerms)
	bad := []string{
		`{}`,                     // nothing supplied
		`{"timeout_seconds":-1}`, // negative timeout
		`{"repeat_percent":0}`,   // below 1
		`{"repeat_percent":101}`, // above 100
		`{"cadence_seconds":0}`,  // non-positive cadence
	}
	for _, body := range bad {
		if rec := postReq(t, h, "/api/village/umbilical/settings/huddle-loop", "tok", body); rec.Code != http.StatusBadRequest {
			t.Errorf("body %s = %d, want 400", body, rec.Code)
		}
	}
}
