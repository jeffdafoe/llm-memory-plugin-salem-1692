package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// llm67_test.go — LLM-67 + LLM-85. Tiredness must never be surfaced as an
// "address this" imperative — that cue was the entire stimulus for the
// re-take_break loop. LLM-67 suppressed the imperative only while resting;
// LLM-85 moved tiredness out of renderFeltNeeds entirely onto its own
// descriptive line (renderTiredness, covered in llm85_test.go), so the
// imperative is gone at every tier, resting or not. renderFeltNeeds now handles
// hunger/thirst only; those stay actionable (a break doesn't feed or water you,
// and LLM-62 lets them interrupt a break). The "## How you can rest" menu
// (buildRecoveryOptions) remains the gated, actionable rest surface.

func TestRenderFeltNeeds_ExcludesTiredness(t *testing.T) {
	th := sim.NeedThresholds{} // Get falls back to registry defaults

	t.Run("tiredness only — renderFeltNeeds surfaces nothing", func(t *testing.T) {
		if line := renderFeltNeeds(map[sim.NeedKey]int{"tiredness": 24}, th); line != "" {
			t.Errorf("tiredness must not appear in the hunger/thirst felt line: %q", line)
		}
	})

	t.Run("tiredness + hunger — only hunger surfaces, still actionable", func(t *testing.T) {
		line := renderFeltNeeds(map[sim.NeedKey]int{"hunger": 24, "tiredness": 24}, th)
		if !strings.Contains(line, "Address now: hunger.") {
			t.Errorf("hunger must stay actionable: %q", line)
		}
		if strings.Contains(line, "tiredness") || strings.Contains(line, "weary") || strings.Contains(line, "exhausted") {
			t.Errorf("no tiredness vocabulary belongs in renderFeltNeeds: %q", line)
		}
	})
}

// TestBuildRecoveryOptions_OnBreakHomed_Suppressed — the bug's common case
// (Ezekiel/Elizabeth): a homed actor on a break should get NO "## How you can
// rest" menu (no rest spots, no RestInPlace "call take_break"). Excluding
// resting from `tired` drops it through to nil (not tired, not homeless).
func TestBuildRecoveryOptions_OnBreakHomed_Suppressed(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Needs:           map[sim.NeedKey]int{"tiredness": 22}, // red
		HomeStructureID: "cottage",
		State:           sim.StateResting, // on a take_break
	}
	snap := &sim.Snapshot{
		Actors:         map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{"oak": tirednessObject("oak", "the old oak", 96, 0, -12)},
	}
	if v := buildRecoveryOptions(snap, "ezekiel", subj); v != nil {
		t.Fatalf("a homed actor already on a break must get no rest menu (LLM-67), got %+v", v)
	}

	// Contrast: the SAME actor off break still gets the section (the gate is what
	// suppresses it, not the absence of options).
	subj.State = ""
	if v := buildRecoveryOptions(snap, "ezekiel", subj); v == nil {
		t.Fatal("off break, a tired homed actor must still get recovery options")
	}
}

// TestBuildRecoveryOptions_OnBreakHomeless_NoRestSpots — a homeless actor's
// lodging bootstrap fires every tick by design, so the section still renders
// while resting; but the redundant "rest here" free-spot affordance is gated
// out (gatherFreeRestSpots on !resting), leaving only the room-booking shelter
// cue (long-term, not redundant with the current break).
func TestBuildRecoveryOptions_OnBreakHomeless_NoRestSpots(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Needs:           map[sim.NeedKey]int{"tiredness": 1}, // rested; homeless arm fires regardless
		HomeStructureID: "",
		State:           sim.StateResting,
	}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"ezekiel": subj,
			"hannah":  {WorkStructureID: "inn"}, // inn keeper → room is bookable
		},
		Structures:     map[sim.StructureID]*sim.Structure{"inn": innStructure("inn", "Hannah's Inn")},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{"oak": tirednessObject("oak", "the old oak", 96, 0, -12)},
	}
	v := buildRecoveryOptions(snap, "ezekiel", subj)
	if v == nil {
		t.Fatal("a homeless actor's lodging bootstrap still fires while resting (room-booking is long-term shelter)")
	}
	var hasInn bool
	for _, o := range v.Options {
		if o.Kind == "rest" {
			t.Errorf("a resting actor must not be re-offered a free rest spot: %+v", o)
		}
		if o.Kind == "inn" {
			hasInn = true
		}
	}
	if !hasInn {
		t.Error("the homeless room-booking bootstrap (inn) should remain while resting")
	}
}
