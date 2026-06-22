package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// llm67_test.go — LLM-67. An actor on a take_break (State == StateResting) is
// already recovering tiredness, so perception must not keep surfacing tiredness
// as something to address: that cue is the entire stimulus for the re-take_break
// loop. Two seams are gated: the "Address now" need line (renderFeltNeeds) and
// the "## How you can rest" menu (buildRecoveryOptions). Need-aware throughout —
// hunger/thirst stay actionable (a break doesn't feed or water you, and LLM-62
// lets them interrupt a break).

func TestRenderFeltNeeds_OnBreakSuppressesTiredness(t *testing.T) {
	th := sim.NeedThresholds{} // Get falls back to registry defaults (tiredness red = 20)

	t.Run("resting, tiredness only — no Address now, still felt", func(t *testing.T) {
		line := renderFeltNeeds(map[sim.NeedKey]int{"tiredness": 24}, th, true)
		if line == "" {
			t.Fatal("want a felt line, got empty")
		}
		if strings.Contains(line, "Address now") {
			t.Errorf("on-break tiredness must not be an Address-now imperative: %q", line)
		}
		if !strings.HasPrefix(line, "You feel") {
			t.Errorf("tiredness must stay legible as a felt state (mark-don't-hide): %q", line)
		}
	})

	t.Run("resting, tiredness + hunger — hunger stays actionable, tiredness does not", func(t *testing.T) {
		line := renderFeltNeeds(map[sim.NeedKey]int{"hunger": 24, "tiredness": 24}, th, true)
		if !strings.Contains(line, "Address now: hunger.") {
			t.Errorf("hunger must stay actionable on a break (it doesn't feed you): %q", line)
		}
		// `pressing` is keyed by the need name; tiredness must be absent from it.
		// (The felt clause uses adjectives like "weary"/"exhausted", not the key.)
		if strings.Contains(line, "tiredness") {
			t.Errorf("tiredness key must not appear in the Address-now list while resting: %q", line)
		}
		// Tiredness stays VISIBLE in the felt clause (mark-don't-hide) — resting
		// suppresses the imperative, not the actor's awareness of the need. With
		// both hunger and tiredness felt, the "You feel …" clause lists two items.
		if i := strings.Index(line, "You feel "); i < 0 || !strings.Contains(line[i:], ", ") {
			t.Errorf("tiredness should remain visible in the felt clause alongside hunger: %q", line)
		}
	})

	t.Run("not resting — tiredness is actionable (baseline unchanged)", func(t *testing.T) {
		line := renderFeltNeeds(map[sim.NeedKey]int{"tiredness": 24}, th, false)
		if !strings.Contains(line, "Address now: tiredness.") {
			t.Errorf("off-break tiredness must still surface as Address now: %q", line)
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
