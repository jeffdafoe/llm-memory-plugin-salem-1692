package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_eco_test.go — LLM-313. The settings/eco-mode control route:
// live-tune the eco knobs, validation (0 gaps are VALID off-switches; a fully
// absent body is not), the GET /settings read-back with the live
// audience/engaged pair, and the /actors present flag.

func TestUmbilicalEcoMode_TunesAndReadsBack(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)

	rec := postReq(t, h, "/api/village/umbilical/settings/eco-mode", "tok",
		`{"enabled":true,"social_gap_seconds":75,"economy_gap_seconds":45}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("tune = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out umbilicalEcoModeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Enabled || out.SocialGapSeconds != 75 || out.EconomyGapSeconds != 45 {
		t.Errorf("response = %+v, want enabled/75/45", out)
	}
	// The control world has no PC with a fresh stamp, so eco is engaged.
	if out.AudienceActive {
		t.Error("audience_active = true with no fresh PC stamp, want false")
	}
	if !out.Engaged {
		t.Error("engaged = false with eco on and no audience, want true")
	}

	// Applied to live WorldSettings.
	res, _ := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Settings.EcoEnabled &&
			world.Settings.EcoSocialGap.Seconds() == 75 &&
			world.Settings.EcoEconomyGap.Seconds() == 45, nil
	}})
	if ok, _ := res.(bool); !ok {
		t.Error("live settings did not take the tuned eco values")
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
	if !sdto.EcoEnabled || sdto.EcoSocialGapSeconds != 75 || sdto.EcoEconomyGapSeconds != 45 {
		t.Errorf("GET settings eco block = %+v, want enabled/75/45", sdto)
	}
	if sdto.EcoAudienceActive {
		t.Error("GET settings eco_audience_active = true, want false")
	}
	if !sdto.EcoEngaged {
		t.Error("GET settings eco_engaged = false, want true")
	}

	// Partial update: flipping just the master switch leaves the gaps alone.
	if rec := postReq(t, h, "/api/village/umbilical/settings/eco-mode", "tok", `{"enabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("partial tune = %d, want 200", rec.Code)
	}
	res, _ = srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return !world.Settings.EcoEnabled && world.Settings.EcoSocialGap.Seconds() == 75, nil
	}})
	if ok, _ := res.(bool); !ok {
		t.Error("partial update clobbered the untouched gap or missed the switch")
	}

	// 0 gaps are explicit off-switches — accepted, not rejected.
	if rec := postReq(t, h, "/api/village/umbilical/settings/eco-mode", "tok",
		`{"social_gap_seconds":0,"economy_gap_seconds":0}`); rec.Code != http.StatusOK {
		t.Errorf("zero gaps = %d, want 200 (0 disables that throttle)", rec.Code)
	}
}

// TestUmbilicalEcoMode_Validation: empty body and negative gaps → 400.
func TestUmbilicalEcoMode_Validation(t *testing.T) {
	_, h := controlServer(t, operatorPerms)
	bad := []string{
		`{}`,                          // nothing supplied
		`{"social_gap_seconds":-5}`,   // negative is rejected
		`{"economy_gap_seconds":-1}`,  // negative is rejected
		`{"social_gap_seconds":90}`,   // at the default warrant stale horizon
		`{"economy_gap_seconds":600}`, // past the horizon
	}
	for _, body := range bad {
		if rec := postReq(t, h, "/api/village/umbilical/settings/eco-mode", "tok", body); rec.Code != http.StatusBadRequest {
			t.Errorf("body %s = %d, want 400", body, rec.Code)
		}
	}
}

// TestUmbilicalActors_PresentFlag: PCs carry present (fresh stamp ⇒ true,
// none/stale ⇒ false); non-PC rows omit the field.
func TestUmbilicalActors_PresentFlag(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)

	if _, err := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["pc-fresh"] = &sim.Actor{ID: "pc-fresh", Kind: sim.KindPC, DisplayName: "Fresh"}
		world.Actors["pc-gone"] = &sim.Actor{ID: "pc-gone", Kind: sim.KindPC, DisplayName: "Gone"}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed PCs: %v", err)
	}
	// Stamp pc-fresh via the same command the /pc/me poll uses.
	if _, err := srv.world.Send(sim.StampPCSeen("pc-fresh")); err != nil {
		t.Fatalf("StampPCSeen: %v", err)
	}

	rec := req(t, h, "/api/village/umbilical/actors", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET actors = %d, want 200", rec.Code)
	}
	var dto UmbilicalActorsDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode actors: %v", err)
	}
	byID := map[string]UmbilicalActorRowDTO{}
	for _, row := range dto.Actors {
		byID[row.ID] = row
	}
	fresh, ok := byID["pc-fresh"]
	if !ok || fresh.Present == nil || !*fresh.Present {
		t.Errorf("pc-fresh present = %v, want true", fresh.Present)
	}
	gone, ok := byID["pc-gone"]
	if !ok || gone.Present == nil || *gone.Present {
		t.Errorf("pc-gone present = %v, want false (nil stamp is stale by design)", gone.Present)
	}
	for id, row := range byID {
		if row.Kind == "pc" {
			// Every PC row carries the flag (the harness world may seed its
			// own PCs beyond the two above).
			if row.Present == nil {
				t.Errorf("PC %s omits present, want a value", id)
			}
			continue
		}
		if row.Present != nil {
			t.Errorf("non-PC %s carries present=%v, want omitted", id, *row.Present)
		}
	}
}
