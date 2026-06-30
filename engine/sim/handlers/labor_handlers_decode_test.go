package handlers

import (
	"encoding/json"
	"fmt"
	"testing"
)

// labor_handlers_decode_test.go — LLM-190. The solicit_work duration bound moved
// to the 2h–8h band (120..480); DecodeSolicitWorkArgs enforces it against
// sim.MinLaborDurationMinutes / MaxLaborDurationMinutes.

func TestDecodeSolicitWorkArgs_DurationBounds(t *testing.T) {
	body := func(dur int) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"employer":"Josiah","reward":5,"duration_minutes":%d}`, dur))
	}
	// Below the 2h floor.
	if _, err := DecodeSolicitWorkArgs(body(119)); err == nil {
		t.Errorf("duration 119 should be rejected (below the 120 floor)")
	}
	// Each tier endpoint is accepted.
	for _, dur := range []int{120, 240, 360, 480} {
		if _, err := DecodeSolicitWorkArgs(body(dur)); err != nil {
			t.Errorf("duration %d should be accepted: %v", dur, err)
		}
	}
	// Above the 8h ceiling.
	if _, err := DecodeSolicitWorkArgs(body(481)); err == nil {
		t.Errorf("duration 481 should be rejected (above the 480 ceiling)")
	}
}
