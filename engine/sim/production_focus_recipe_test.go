package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// An actor with two produce entries where only ONE is recipe-backed is NOT a
// multi-output chooser (LLM-116 review fix): the no-recipe entry is inert, so it
// auto-produces the single makeable good without needing a focus — it must not
// stall waiting for a craft choice it can never be offered. ("horseshoe" is seeded
// as an item kind but has no recipe.)
func TestProduceTickRecipelessSecondEntryAutoProduces(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildSmithWorld(t, now.Add(-4*time.Hour), []sim.RestockEntry{
		{Item: "skillet", Source: sim.RestockSourceProduce, Max: 5},    // recipe-backed
		{Item: "horseshoe", Source: sim.RestockSourceProduce, Max: 10}, // no recipe
	}, map[sim.ItemKind]int{})
	defer cancel()

	res, err := w.Send(sim.ApplyProduceTick(now))
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	r := res.(sim.ProduceTickResult)
	if r.Executions == 0 {
		t.Fatalf("single-makeable producer made nothing; want it to auto-produce skillet without a focus")
	}
	for _, c := range r.Changes {
		if c.Item != "skillet" {
			t.Fatalf("produced %q; only skillet is recipe-backed", c.Item)
		}
	}
}

// SetProductionFocus rejects a good the actor produces but cannot MAKE (no recipe),
// so focus can never be set to something produce_tick will skip forever.
func TestSetProductionFocusRejectsRecipelessItem(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildSmithWorld(t, now, []sim.RestockEntry{
		{Item: "skillet", Source: sim.RestockSourceProduce, Max: 5},
		{Item: "horseshoe", Source: sim.RestockSourceProduce, Max: 10},
	}, map[sim.ItemKind]int{})
	defer cancel()

	if _, err := w.Send(sim.SetProductionFocus("smith", "horseshoe")); err == nil {
		t.Fatalf("SetProductionFocus accepted a good with no recipe; want an error")
	}
}
