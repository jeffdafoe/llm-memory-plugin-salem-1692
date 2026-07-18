package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// reactor_bake_need_wake_test.go — LLM-465. Executable coverage for the load-bearing
// half of the safety argument behind admitting a red-need housemate to a household bake
// (code_review: "the joiner can still respond to an urgent need" was asserted in prose
// and proven nowhere).
//
// The claim has two mechanisms. The tool half — that a baking actor keeps move_to under
// a red hunger/thirst and loses it under tiredness — is already pinned by
// handlers.TestGateTools_Baking_MoveToByNeed across all four tiers. This file pins the
// reactor half: that the source-activity shelve actually lets a need warrant through, so
// something wakes the joiner to use that move_to. Without both, admitting the join could
// convert a merely loose actor into one shelved at the hearth until dusk — a worse bug
// than the one LLM-465 fixes.

// bakingActorWithNeedWarrant puts alice mid-bake with a red-need warrant for `need` and
// returns whether the reactor considers her eligible to tick.
func bakingActorWithNeedWarrant(t *testing.T, need sim.NeedKey) bool {
	t.Helper()
	w, cancel := buildReactorTestWorld(t)
	defer cancel()
	var eligible bool
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			now := time.Now().UTC()
			a := world.Actors["alice"]
			// A bake window runs to dusk — hours out, so the shelve is unambiguously
			// live and any tick that happens is the carve-out doing its job.
			a.SourceActivity = &sim.SourceActivity{
				Kind:      sim.SourceActivityBake,
				StartedAt: now,
				Until:     now.Add(5 * time.Hour),
			}
			a.Warrants = []sim.WarrantMeta{{Reason: sim.NeedThresholdWarrantReason{Need: need}}}
			e, _ := sim.ActorCanReactNowAt(world, a, now)
			eligible = e
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	return eligible
}

// TestActorCanReactNow_BakerWakesForHungerAndThirstNotTiredness pins exactly the boundary
// bakeTrappingRedNeed encodes, from the reactor's side. Hunger and thirst reach a shelved
// baker (she can break off to eat or drink); tiredness does not, by design — a break
// cures tiredness so it never justifies abandoning work in progress. That asymmetry is
// the whole reason the LLM-465 join gate admits the first two and still bars the third.
func TestActorCanReactNow_BakerWakesForHungerAndThirstNotTiredness(t *testing.T) {
	if !bakingActorWithNeedWarrant(t, "hunger") {
		t.Error("a baker with a red HUNGER warrant did not tick — the LLM-465 join gate admits a " +
			"hungry housemate on the promise that something wakes him to go eat. If this shelve " +
			"swallows the warrant, that join shelves him at the hearth until dusk instead.")
	}
	if !bakingActorWithNeedWarrant(t, "thirst") {
		t.Error("a baker with a red THIRST warrant did not tick — same contract as hunger; the " +
			"join gate treats the two identically and the reactor must too")
	}
	if bakingActorWithNeedWarrant(t, "tiredness") {
		t.Error("a baker with a red TIREDNESS warrant ticked. That contradicts the deliberate " +
			"exclusion in hasBreakInterruptingNeedWarrant (a break cures tiredness, so it never " +
			"justifies abandoning work) — and bakeTrappingRedNeed bars the tired join BECAUSE " +
			"nothing wakes him. If this ever starts ticking, revisit that gate: the trap it " +
			"guards against would no longer exist.")
	}
}
