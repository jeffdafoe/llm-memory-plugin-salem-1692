package handlers

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/perception"
)

// tool_gating_walk_test.go — ZBBS-HOME-337 / 338. The walking gate: while the
// subject is mid-walk, the movement-incompatible action tools are dropped from
// the advertised set and the `stop` tool is advertised; stationary, the
// inverse.

// walkGatingRegistry registers the walk-incompatible tools plus stop, move_to
// (a non-gated mover), and the done terminal — enough to assert the gate.
func walkGatingRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	for name, fn := range map[string]func(*Registry) error{
		"speak":         RegisterSpeak,
		"consume":       RegisterConsume,
		"gather":        RegisterGather,
		"pay_with_item": RegisterPayWithItemFamily,
		"move_to":       RegisterMoveTo,
		"stop":          RegisterStop,
	} {
		if err := fn(r); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("RegisterTerminal: %v", err)
	}
	return r
}

// movingSnap builds a snapshot whose single actor is moving (MoveDestKind set)
// or stationary (empty), keyed by actorID.
func movingSnap(actorID sim.ActorID, moving bool) *sim.Snapshot {
	var kind sim.MoveDestinationKind
	if moving {
		kind = sim.MoveDestinationStructureEnter
	}
	return &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			actorID: {MoveDestKind: kind},
		},
	}
}

func TestGateTools_Moving_DropsWalkIncompatible_AdvertisesStop(t *testing.T) {
	r := walkGatingRegistry(t)
	payload := perception.Payload{ActorID: "walker"}
	specs := gateTools(r, payload, movingSnap("walker", true))
	names := specNameSet(specs)

	for _, gated := range []string{"consume", "speak", "gather", "pay_with_item"} {
		if names[gated] != 0 {
			t.Errorf("tool %q advertised while moving — want it gated out", gated)
		}
	}
	if names["stop"] != 1 {
		t.Errorf("stop advertised %d times while moving — want 1", names["stop"])
	}
	// A non-gated mover stays available so the actor can re-route.
	if names["move_to"] != 1 {
		t.Errorf("move_to advertised %d times while moving — want 1", names["move_to"])
	}
}

func TestGateTools_Stationary_AdvertisesWalkTools_DropsStop(t *testing.T) {
	r := walkGatingRegistry(t)
	payload := perception.Payload{ActorID: "walker"}
	specs := gateTools(r, payload, movingSnap("walker", false))
	names := specNameSet(specs)

	for _, ungated := range []string{"consume", "speak", "pay_with_item"} {
		if names[ungated] != 1 {
			t.Errorf("tool %q advertised %d times while stationary — want 1", ungated, names[ungated])
		}
	}
	if names["stop"] != 0 {
		t.Errorf("stop advertised while stationary — want it gated out (nothing to stop)")
	}
}

// A nil snapshot (degraded perception) is treated as not-moving: the action
// tools stay advertised and stop is gated out — conservative, matching
// actorIsMoving's nil handling.
func TestGateTools_NilSnapshot_TreatedAsStationary(t *testing.T) {
	r := walkGatingRegistry(t)
	specs := gateTools(r, perception.Payload{ActorID: "walker"}, nil)
	names := specNameSet(specs)
	if names["consume"] != 1 || names["speak"] != 1 {
		t.Errorf("walk tools should stay advertised under a nil snapshot; got consume=%d speak=%d", names["consume"], names["speak"])
	}
	if names["stop"] != 0 {
		t.Errorf("stop should be gated out under a nil snapshot (not confirmed moving)")
	}
}
