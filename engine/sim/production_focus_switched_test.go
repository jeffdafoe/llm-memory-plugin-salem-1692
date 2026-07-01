package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// production_focus_switched_test.go — LLM-201: SetProductionFocus reports whether
// the call CHANGED the actor's active production item (ProductionFocusResult.
// Switched). The harness ends the tick on a no-switch produce — a "tend your post"
// no-op — while keeping a genuine switch non-terminal; this pins the world-side
// signal that decision rides on.

func TestSetProductionFocusReportsSwitched(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildSmithWorld(t, now.Add(-4*time.Hour), twoProduceEntries(), map[sim.ItemKind]int{})
	defer cancel()

	// First focus: from no focus to nail — a genuine change.
	res, err := w.Send(sim.SetProductionFocus("smith", "nail"))
	if err != nil {
		t.Fatalf("SetProductionFocus(nail): %v", err)
	}
	r, ok := res.(sim.ProductionFocusResult)
	if !ok {
		t.Fatalf("result type: got %T, want sim.ProductionFocusResult", res)
	}
	if !r.Switched {
		t.Errorf("first focus nail: Switched=false, want true (no focus -> nail is a change)")
	}

	// Re-issue the SAME item: no switch — the "tend your post" no-op the harness
	// ends the tick on.
	res, err = w.Send(sim.SetProductionFocus("smith", "nail"))
	if err != nil {
		t.Fatalf("SetProductionFocus(nail again): %v", err)
	}
	if res.(sim.ProductionFocusResult).Switched {
		t.Errorf("re-focus the current item: Switched=true, want false")
	}

	// Change to a genuinely different good: a switch.
	res, err = w.Send(sim.SetProductionFocus("smith", "skillet"))
	if err != nil {
		t.Fatalf("SetProductionFocus(skillet): %v", err)
	}
	if !res.(sim.ProductionFocusResult).Switched {
		t.Errorf("focus skillet after nail: Switched=false, want true (nail -> skillet is a change)")
	}
}
