package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_seek_work_test.go — LLM-194. The settings/seek-work-ceiling control route:
// live-tune the seek-work coin ceiling, validation, and the GET /settings read-back.

func TestUmbilicalSeekWorkCeiling_TunesAndReadsBack(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)

	rec := postReq(t, h, "/api/village/umbilical/settings/seek-work-ceiling", "tok", `{"coin_ceiling":40}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("tune = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out umbilicalSeekWorkCeilingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.CoinCeiling != 40 {
		t.Errorf("response = %+v, want coin_ceiling 40", out)
	}

	// Applied to live WorldSettings.
	res, _ := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Settings.SeekWorkCoinCeiling, nil
	}})
	if got, _ := res.(int); got != 40 {
		t.Errorf("live settings ceiling = %v, want 40", res)
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
	if sdto.SeekWorkCoinCeiling != 40 {
		t.Errorf("GET settings seek_work_coin_ceiling = %d, want 40", sdto.SeekWorkCoinCeiling)
	}
}

// TestUmbilicalSeekWorkNeedMargin_TunesAndReadsBack — LLM-276: the
// settings/seek-work-need-margin control route live-tunes the redirect margin, applies
// to live WorldSettings, and reads back on GET /settings.
func TestUmbilicalSeekWorkNeedMargin_TunesAndReadsBack(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)

	rec := postReq(t, h, "/api/village/umbilical/settings/seek-work-need-margin", "tok", `{"margin":7}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("tune = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out umbilicalSeekWorkNeedMarginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Margin != 7 {
		t.Errorf("response = %+v, want margin 7", out)
	}

	res, _ := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Settings.SeekWorkNeedYieldMargin, nil
	}})
	if got, _ := res.(int); got != 7 {
		t.Errorf("live settings margin = %v, want 7", res)
	}

	grec := req(t, h, "/api/village/umbilical/settings", "tok")
	if grec.Code != http.StatusOK {
		t.Fatalf("GET settings = %d, want 200", grec.Code)
	}
	var sdto UmbilicalSettingsDTO
	if err := json.Unmarshal(grec.Body.Bytes(), &sdto); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if sdto.SeekWorkNeedYieldMargin != 7 {
		t.Errorf("GET settings seek_work_need_yield_margin = %d, want 7", sdto.SeekWorkNeedYieldMargin)
	}
}

// TestUmbilicalSeekWorkNeedMargin_Validation: empty body and a zero/negative margin → 400
// (a zero would collapse the redirect band).
func TestUmbilicalSeekWorkNeedMargin_Validation(t *testing.T) {
	_, h := controlServer(t, operatorPerms)
	bad := []string{
		`{}`,            // nothing supplied
		`{"margin":0}`,  // zero is rejected
		`{"margin":-3}`, // negative is rejected
	}
	for _, body := range bad {
		if rec := postReq(t, h, "/api/village/umbilical/settings/seek-work-need-margin", "tok", body); rec.Code != http.StatusBadRequest {
			t.Errorf("body %s = %d, want 400", body, rec.Code)
		}
	}
}

// TestUmbilicalSeekWorkCeiling_Validation: empty body and a zero/negative ceiling → 400
// (a zero would suppress seek-work for every worker).
func TestUmbilicalSeekWorkCeiling_Validation(t *testing.T) {
	_, h := controlServer(t, operatorPerms)
	bad := []string{
		`{}`,                  // nothing supplied
		`{"coin_ceiling":0}`,  // zero is rejected
		`{"coin_ceiling":-5}`, // negative is rejected
	}
	for _, body := range bad {
		if rec := postReq(t, h, "/api/village/umbilical/settings/seek-work-ceiling", "tok", body); rec.Code != http.StatusBadRequest {
			t.Errorf("body %s = %d, want 400", body, rec.Code)
		}
	}
}
