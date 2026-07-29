package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// bake_seek_work_exclusion_test.go — LLM-459. The bake affordance and the seek-work
// directive address disjoint populations, and the engine must never put both in one
// prompt: bake (LLM-454) is for the villagers who stay home BECAUSE they have no reason
// to seek work, while the seek-work directory engages a workless, below-ceiling worker
// who is free to leave. Rendered together they are two competing action invitations, and
// the seek-work half wins on placement regardless — it is the closing triage coda and the
// prompt's only imperative — so the bake tool is advertised to an NPC being ordered to
// walk away from it (live 2026-07-18: Silence Walker, 8 coins and 4 flour, did laps while
// her three over-the-ceiling housemates baked).

// TestBuildBakeChoiceSuppressedBySeekWork exercises the suppression at the Build layer,
// so the gate is proven directly rather than only through whichever scenarios the golden
// matrix happens to carry. The positive control matters as much as the negative one: it
// pins that this actor DOES get a bake cue absent the seek-work directive, so a future
// change that broke bake outright couldn't leave the negative assertion passing vacuously.
func TestBuildBakeChoiceSuppressedBySeekWork(t *testing.T) {
	// Comfortable (40 coins, at/above the default ceiling of 25) → no seek-work
	// directory → the homebody population bake was written for.
	snap, actorID, _ := seekingWorkerAtHomeGetsNoBakeCue()
	snap.Actors[actorID].Coins = 40
	if p := Build(snap, actorID, nil); p.BakeChoice == nil {
		t.Fatal("comfortable worker at home with a household bake going: got no bake cue, want one — " +
			"the positive control for the suppression below is broken, so the negative case proves nothing")
	}

	// The live shape: same actor, same hearth, 8 coins → seek-work engaged.
	snap, actorID, _ = seekingWorkerAtHomeGetsNoBakeCue()
	p := Build(snap, actorID, nil)
	if len(p.SeekWorkPlaces) == 0 {
		t.Fatal("below-ceiling workless worker: got no seek-work directory, want one — the scenario " +
			"no longer reproduces the LLM-459 conflict, so the suppression below is untested")
	}
	if p.BakeChoice != nil {
		t.Errorf("below-ceiling workless worker at home with a household bake going: got bake cue %+v, want nil — "+
			"an engaged job-seeker is not the homebody population bake serves, and rendering both leaves the NPC "+
			"holding the bake tool under a 'call move_to now' imperative (LLM-459)", p.BakeChoice)
	}
}

// TestBuildCoPresentBakeAnnotationSuppressedBySeekWork exercises the LLM-562 scene
// half of the suppression at the Build layer: for an observer the seek-work directory
// has engaged, a co-present housemate mid-bake loses the busy-baking annotation — but
// stays in CoPresent, present and greetable. LLM-459 nilled BakeChoice (cue + tool);
// live 2026-07-29 showed that was affordance-deep only: Patience Walker's prompt still
// carried Lewis "(at the hearth, baking just now)" and she narrated joining the bread
// on nine straight home-arrival ticks with only move_to on offer. The positive control
// pins that a comfortable observer of the SAME scene keeps the annotation, so the
// negative case can't pass by the annotation being broken outright.
func TestBuildCoPresentBakeAnnotationSuppressedBySeekWork(t *testing.T) {
	// Comfortable (40 coins, at/above the default ceiling of 25) → no seek-work
	// directory → the annotation renders as LLM-440/454 built it.
	snap, actorID, _ := seekingWorkerHomeSceneHidesHousemateBake()
	snap.Actors[actorID].Coins = 40
	p := Build(snap, actorID, nil)
	if len(p.Surroundings.CoPresent) != 1 {
		t.Fatalf("comfortable observer: CoPresent = %v, want the one baking housemate", p.Surroundings.CoPresent)
	}
	if m := p.Surroundings.CoPresent[0]; !m.SourceActivityBusy || m.SourceActivityKind != sim.SourceActivityBake {
		t.Fatal("comfortable observer of a baking housemate: annotation absent — the positive control for " +
			"the suppression below is broken, so the negative case proves nothing")
	}

	// The live shape: same room, same baking housemate, 8 coins → seek-work engaged.
	snap, actorID, _ = seekingWorkerHomeSceneHidesHousemateBake()
	p = Build(snap, actorID, nil)
	if len(p.SeekWorkPlaces) == 0 {
		t.Fatal("below-ceiling workless worker: got no seek-work directory, want one — the scenario " +
			"no longer reproduces the LLM-562 conflict, so the suppression below is untested")
	}
	if len(p.Surroundings.CoPresent) != 1 {
		t.Fatalf("seek-work observer: CoPresent = %v, want the housemate still present — the strip must "+
			"quiet the annotation, not remove the person", p.Surroundings.CoPresent)
	}
	if m := p.Surroundings.CoPresent[0]; m.SourceActivityBusy || m.SourceActivityKind != "" || m.SourceActivityLabel != "" {
		t.Errorf("seek-work observer of a baking housemate: annotation fields still set (busy=%v kind=%q label=%q), "+
			"want all cleared — the scene keeps arguing for bread the toolset can't make (LLM-562)",
			m.SourceActivityBusy, m.SourceActivityKind, m.SourceActivityLabel)
	}
}

// TestNoSeekWorkPromptDepictsHousemateBake is the LLM-562 corpus invariant, the scene
// sibling of TestNoPromptOffersBakeAndSeekWorkTogether below: across every scenario
// the matrix renders, no single prompt may carry both the seek-work go-coda and the
// co-present "baking just now" annotation. The affordance invariant below couldn't
// catch the live bug — the cue and tool were correctly gone; it was the AMBIENT
// depiction of the bake that kept arguing for bread.
func TestNoSeekWorkPromptDepictsHousemateBake(t *testing.T) {
	var sawAnnotation, sawSeekWork int
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			out := renderScenario(sc)
			// "baking just now" is busyActivityPhrase's bake arm without pinning
			// the hearth prose around it.
			hasAnnotation := strings.Contains(out, "baking just now")
			hasSeekWork := strings.Contains(out, "No one here can hire you")
			if hasAnnotation {
				sawAnnotation++
			}
			if hasSeekWork {
				sawSeekWork++
			}
			if hasAnnotation && hasSeekWork {
				t.Errorf("scenario %q renders BOTH the seek-work go-coda and a co-present member's "+
					"baking annotation. The scene argues for bread while the toolset only offers move_to, "+
					"and the model reads the scene (LLM-562, live 2026-07-29). Strip the bake arm of the "+
					"co-present annotation when SeekWorkPlaces is populated (build.go).", sc.name)
			}
		})
	}
	// Vacuity floor, same reasoning as the invariant below: pure string matching, so
	// a phrasing drift on either side would silently hollow the check out.
	if sawAnnotation == 0 {
		t.Error("invariant matched no baking annotation in the whole matrix — busyActivityPhrase's bake arm " +
			"probably drifted, or comfortable_homebody_sees_housemate_bake was removed. The exclusion check " +
			"above is now vacuous on its annotation side.")
	}
	if sawSeekWork == 0 {
		t.Error("invariant matched no seek-work coda in the whole matrix — the render.go go-line phrasing " +
			"probably drifted, or the matrix lost its seek-work scenarios. The exclusion check above is " +
			"now vacuous on its seek-work side.")
	}
}

// TestNoPromptOffersBakeAndSeekWorkTogether is the corpus invariant: across every
// scenario the matrix renders, no single prompt may carry both the bake invitation and
// the seek-work go-coda. This guards the real Build → Render path against the two cues
// drifting back into contradiction through some future gate change that reaches neither
// call site directly.
func TestNoPromptOffersBakeAndSeekWorkTogether(t *testing.T) {
	var sawBake, sawSeekWork int
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			out := renderScenario(sc)
			// "Call bake to" covers both arms of renderBakeChoice — "to start" and
			// "to join in" — without pinning the surrounding prose.
			hasBake := strings.Contains(out, "Call bake to")
			hasSeekWork := strings.Contains(out, "No one here can hire you")
			if hasBake {
				sawBake++
			}
			if hasSeekWork {
				sawSeekWork++
			}
			if hasBake && hasSeekWork {
				t.Errorf("scenario %q renders BOTH the bake invitation and the seek-work go-coda. "+
					"They serve disjoint populations and the imperative coda wins on placement, so the "+
					"NPC is invited to bake and ordered to leave in the same prompt (LLM-459). Suppress "+
					"BakeChoice when SeekWorkPlaces is populated (build.go).", sc.name)
			}
		})
	}
	// Vacuity floor (code_review, and the LLM-457 lesson): this invariant is pure
	// string matching over the assembled prompt, so a wording change on EITHER cue
	// would make its half stop matching and the test would pass having asserted
	// nothing. comfortable_homebody_bakes renders the bake line and the seek-work
	// scenarios render the coda, so both floors are met today — if one drops to
	// zero, the phrasing drifted or the matrix lost a scenario.
	if sawBake == 0 {
		t.Error("invariant matched no bake invitation in the whole matrix — renderBakeChoice phrasing " +
			"probably drifted, or comfortable_homebody_bakes was removed. The exclusion check above is " +
			"now vacuous on its bake side; update the signature or restore a bake-rendering scenario.")
	}
	if sawSeekWork == 0 {
		t.Error("invariant matched no seek-work coda in the whole matrix — the render.go go-line phrasing " +
			"probably drifted, or the matrix lost its seek-work scenarios. The exclusion check above is " +
			"now vacuous on its seek-work side.")
	}
}
