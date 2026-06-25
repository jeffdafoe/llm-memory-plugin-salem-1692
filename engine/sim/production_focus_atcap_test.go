package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// SetProductionFocus rejects focusing a good that's already at its cap while
// ANOTHER makeable good is still below cap (LLM-116): focusing a full good
// produces nothing and would just re-wake the crafter every minute. Here skillet
// is at cap (5/5) and nail is below cap (0/20), so skillet is refused and nail
// accepted.
func TestSetProductionFocusRejectsAtCapWhenOtherBelowCap(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildSmithWorld(t, now, twoProduceEntries(), map[sim.ItemKind]int{"skillet": 5})
	defer cancel()

	if _, err := w.Send(sim.SetProductionFocus("smith", "skillet")); err == nil {
		t.Fatalf("accepted a maxed-out skillet while nail still needs making; want an error")
	}
	if _, err := w.Send(sim.SetProductionFocus("smith", "nail")); err != nil {
		t.Fatalf("rejected nail (below cap): %v", err)
	}
}

// When EVERY makeable good is at cap there is nothing better to pick, so focusing
// a full good is allowed (the crafter will make more once a sale frees headroom).
func TestSetProductionFocusAllowsAtCapWhenAllFull(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildSmithWorld(t, now, twoProduceEntries(), map[sim.ItemKind]int{"skillet": 5, "nail": 20})
	defer cancel()

	if _, err := w.Send(sim.SetProductionFocus("smith", "skillet")); err != nil {
		t.Fatalf("rejected skillet when everything is at cap (nothing better to pick): %v", err)
	}
}
