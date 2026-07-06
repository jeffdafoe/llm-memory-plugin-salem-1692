package sim_test

import (
	"strings"
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

// The at-cap rejection NAMES the goods the crafter can make now (LLM-300) so the
// weak model has a copyable next action, not a bare "make something needed". Here
// skillet is at cap (5/5) and nail is the lone below-cap good, so the steer names
// nail — by its catalog PLURAL phrase ("nails", not the singular "nail"/"Nail") —
// and, being the only alternative, points the produce tool straight at it ("call
// produce with nails", not the vaguer "one of those").
func TestSetProductionFocusAtCapRejectionNamesCraftableAlternatives(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildSmithWorld(t, now, twoProduceEntries(), map[sim.ItemKind]int{"skillet": 5})
	defer cancel()

	_, err := w.Send(sim.SetProductionFocus("smith", "skillet"))
	if err == nil {
		t.Fatalf("accepted a maxed-out skillet while nail still needs making; want an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "hold") {
		t.Fatalf("at-cap rejection should keep the 'all you can hold' framing; got %q", msg)
	}
	if !strings.Contains(msg, "call produce with nails") {
		t.Fatalf("at-cap rejection should name the lone alternative by its plural phrase and point produce at it (call produce with nails); got %q", msg)
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
