package perception

import (
	"strings"
	"testing"
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

// TestNoPromptOffersBakeAndSeekWorkTogether is the corpus invariant: across every
// scenario the matrix renders, no single prompt may carry both the bake invitation and
// the seek-work go-coda. This guards the real Build → Render path against the two cues
// drifting back into contradiction through some future gate change that reaches neither
// call site directly.
func TestNoPromptOffersBakeAndSeekWorkTogether(t *testing.T) {
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			out := renderScenario(sc)
			// "Call bake to" covers both arms of renderBakeChoice — "to start" and
			// "to join in" — without pinning the surrounding prose.
			hasBake := strings.Contains(out, "Call bake to")
			hasSeekWork := strings.Contains(out, "No one here can hire you")
			if hasBake && hasSeekWork {
				t.Errorf("scenario %q renders BOTH the bake invitation and the seek-work go-coda. "+
					"They serve disjoint populations and the imperative coda wins on placement, so the "+
					"NPC is invited to bake and ordered to leave in the same prompt (LLM-459). Suppress "+
					"BakeChoice when SeekWorkPlaces is populated (build.go).", sc.name)
			}
		})
	}
}
