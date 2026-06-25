package handlers

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/perception"
)

// tool_gating_degeneracy_test.go — LLM-94 Stage-1. move_to is dropped from a
// degeneracy-flagged actor's advertised set, in lockstep with the steering-cue
// thinning perception.Build does on the same DegenStage signal. Reuses
// walkGatingRegistry / specNameSet from tool_gating_walk_test.go (same package).

// degenSnap builds a snapshot whose single, stationary actor sits at the given
// degeneracy stage, keyed by actorID.
func degenSnap(actorID sim.ActorID, stage sim.DegeneracyStage) *sim.Snapshot {
	return &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			actorID: {DegenStage: stage},
		},
	}
}

func TestGateTools_Flagged_DropsMoveTo(t *testing.T) {
	r := walkGatingRegistry(t)
	payload := perception.Payload{ActorID: "stuck", Surroundings: speakAudience()}
	for _, stage := range []sim.DegeneracyStage{sim.DegeneracyFlagged, sim.DegeneracyThrottled} {
		specs := gateTools(r, payload, degenSnap("stuck", stage))
		names := specNameSet(specs)
		if names["move_to"] != 0 {
			t.Errorf("move_to advertised at stage %v — want it gated out", stage)
		}
		// The Stage-1 gate is move_to-specific: other tools are unaffected.
		if names["speak"] != 1 {
			t.Errorf("speak advertised %d times at stage %v — want 1 (only move_to is gated)", names["speak"], stage)
		}
	}
}

func TestGateTools_NotFlagged_KeepsMoveTo(t *testing.T) {
	r := walkGatingRegistry(t)
	payload := perception.Payload{ActorID: "fine"}
	specs := gateTools(r, payload, degenSnap("fine", sim.DegeneracyNone))
	names := specNameSet(specs)
	if names["move_to"] != 1 {
		t.Errorf("move_to advertised %d times for an unflagged actor — want 1", names["move_to"])
	}
}

// A nil snapshot (degraded perception) is treated as not-flagged: move_to stays
// advertised, matching actorIsFlaggedDegenerate's conservative nil handling.
func TestGateTools_NilSnapshot_KeepsMoveTo(t *testing.T) {
	r := walkGatingRegistry(t)
	specs := gateTools(r, perception.Payload{ActorID: "fine"}, nil)
	names := specNameSet(specs)
	if names["move_to"] != 1 {
		t.Errorf("move_to should stay advertised under a nil snapshot; got %d", names["move_to"])
	}
}
