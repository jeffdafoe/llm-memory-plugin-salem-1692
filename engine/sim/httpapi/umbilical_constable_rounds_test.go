package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// umbilical_constable_rounds_test.go — LLM-548. The dwell/quiet knobs were removed
// with the dwell itself, and that is an UNVERSIONED break on an operator-gated
// surface: ContractVersion is documented as the version of the whole client-facing
// read API and the Godot client fails loudly on a mismatch, so bumping it to
// announce a change on a debug route would take every deployed client offline in
// exchange for nothing. The removal is therefore a deliberate compatibility
// exception rather than a versioned migration.
//
// These tests exist because that argument is only sound if the two behaviours it
// rests on are actually what ships: the old request shape is REJECTED rather than
// silently accepted-and-ignored, and the response no longer carries fields whose
// values could not mean anything. Both are asserted here so the exception cannot
// quietly become a lie (code_review).

// TestUmbilicalConstableRounds_SetsTheInterval is the happy path: the one knob that
// remains applies and is echoed back.
func TestUmbilicalConstableRounds_SetsTheInterval(t *testing.T) {
	_, h := controlServer(t, operatorPerms)

	rec := postReq(t, h, "/api/village/umbilical/constable-rounds/set", "tok", `{"interval_seconds":3600}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("set = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out umbilicalConstableRoundsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.IntervalSeconds != 3600 {
		t.Errorf("interval_seconds = %d, want 3600", out.IntervalSeconds)
	}
}

// TestUmbilicalConstableRounds_ZeroIsTheOffSwitch: 0 is a legitimate value, not a
// missing one — it disables rounds (ConstableRoundsDue treats interval <= 0 as off).
// It must not be rejected alongside the genuinely absent field.
func TestUmbilicalConstableRounds_ZeroIsTheOffSwitch(t *testing.T) {
	_, h := controlServer(t, operatorPerms)

	rec := postReq(t, h, "/api/village/umbilical/constable-rounds/set", "tok", `{"interval_seconds":0}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("set 0 = %d, want 200 — 0 is the off-switch, not a missing field; body=%s",
			rec.Code, rec.Body.String())
	}
	var out umbilicalConstableRoundsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.IntervalSeconds != 0 {
		t.Errorf("interval_seconds = %d, want 0 — the off-switch must round-trip as 0", out.IntervalSeconds)
	}
}

// TestUmbilicalConstableRounds_RetiredKnobsAreRejectedNotIgnored is the compatibility
// exception's load-bearing behaviour. The old body tuned a per-stop dwell and a quiet
// window; nothing paces him between stops any more, so there is no such thing to set.
//
// Rejecting is the deliberate choice over the alternative code_review offered —
// keeping the deprecated fields accepted and omitting them from the response. Silently
// accepting a knob that cannot affect anything is precisely the failure the removal
// exists to prevent: an operator who sets a dwell and watches for a change would be
// left with no signal at all. A 400 naming the surviving field is the discoverable
// path for the only callers this route has.
func TestUmbilicalConstableRounds_RetiredKnobsAreRejectedNotIgnored(t *testing.T) {
	_, h := controlServer(t, operatorPerms)

	for _, body := range []string{
		`{"dwell_seconds":45}`,
		`{"quiet_seconds":90}`,
		`{"dwell_seconds":45,"quiet_seconds":90}`,
		`{}`,
	} {
		rec := postReq(t, h, "/api/village/umbilical/constable-rounds/set", "tok", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("set %s = %d, want 400 — a body carrying only retired knobs must not read as success",
				body, rec.Code)
			continue
		}
		// The message has to name the surviving field, or the 400 tells an operator
		// nothing they can act on.
		if !strings.Contains(rec.Body.String(), "interval_seconds") {
			t.Errorf("set %s: 400 body does not name interval_seconds: %s", body, rec.Body.String())
		}
	}
}

// TestUmbilicalSettings_OmitsRetiredConstableKnobs pins the response half. A field
// reported here is one an operator can set; leaving dwell/quiet in place — even at a
// hardcoded value — would advertise a control that no longer exists.
func TestUmbilicalSettings_OmitsRetiredConstableKnobs(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)
	_ = srv

	rec := req(t, h, "/api/village/umbilical/settings", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("settings = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// Decoded loosely on purpose: the typed DTO cannot fail on a field it no longer
	// declares, so a struct decode would pass whether or not the key ships.
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, gone := range []string{"constable_rounds_dwell_seconds", "constable_rounds_quiet_seconds"} {
		if _, present := raw[gone]; present {
			t.Errorf("%s is still reported — it advertises a knob nothing reads", gone)
		}
	}
	if _, present := raw["constable_rounds_interval_seconds"]; !present {
		t.Error("constable_rounds_interval_seconds missing — the surviving knob must stay readable")
	}
}
