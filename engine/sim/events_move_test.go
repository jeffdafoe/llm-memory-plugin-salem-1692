package sim_test

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// Compile-time checks: the three movement events satisfy the Event bus
// interface, and ArrivalWarrantReason satisfies WarrantReason. These
// would fail to build if the isSimEvent / isWarrantReason markers were
// dropped.
var (
	_ sim.Event = &sim.ActorMoved{}
	_ sim.Event = &sim.ActorArrived{}
	_ sim.Event = &sim.ActorMoveStopped{}

	_ sim.WarrantReason = sim.ArrivalWarrantReason{}
)

// TestArrivalWarrantReasonKind covers that the reason reports the
// pre-existing WarrantKindArrived, both directly and through WarrantMeta.
func TestArrivalWarrantReasonKind(t *testing.T) {
	reason := sim.ArrivalWarrantReason{
		AttemptID:     7,
		AtStructureID: "inn",
		AtPosition:    sim.Position{X: 3, Y: 4},
	}
	if reason.Kind() != sim.WarrantKindArrived {
		t.Errorf("ArrivalWarrantReason.Kind() = %q, want %q", reason.Kind(), sim.WarrantKindArrived)
	}

	meta := sim.WarrantMeta{Reason: reason}
	if meta.Kind() != sim.WarrantKindArrived {
		t.Errorf("WarrantMeta.Kind() = %q, want %q", meta.Kind(), sim.WarrantKindArrived)
	}
}

// TestActorMovedStructureTransition covers the documented subscriber
// filter — a structure transition is detectable as
// FromStructureID != ToStructureID, with the empty string standing in
// for "outdoors".
func TestActorMovedStructureTransition(t *testing.T) {
	outToInn := sim.ActorMoved{FromStructureID: "", ToStructureID: "inn"}
	if outToInn.FromStructureID == outToInn.ToStructureID {
		t.Error("outdoors→inn should read as a structure transition")
	}

	withinInn := sim.ActorMoved{FromStructureID: "inn", ToStructureID: "inn"}
	if withinInn.FromStructureID != withinInn.ToStructureID {
		t.Error("inn→inn should not read as a structure transition")
	}
}
