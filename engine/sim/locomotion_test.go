package sim_test

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// TestMoveDestinationConstructors covers the three constructors: each
// sets Kind and exactly one payload pointer, leaving the other nil.
func TestMoveDestinationConstructors(t *testing.T) {
	enter := sim.NewStructureEnterDestination("inn")
	if enter.Kind != sim.MoveDestinationStructureEnter {
		t.Errorf("enter.Kind = %q, want %q", enter.Kind, sim.MoveDestinationStructureEnter)
	}
	if enter.StructureID == nil || *enter.StructureID != "inn" {
		t.Errorf("enter.StructureID = %v, want &\"inn\"", enter.StructureID)
	}
	if enter.Position != nil {
		t.Errorf("enter.Position = %v, want nil", enter.Position)
	}

	visit := sim.NewStructureVisitDestination("tavern")
	if visit.Kind != sim.MoveDestinationStructureVisit {
		t.Errorf("visit.Kind = %q, want %q", visit.Kind, sim.MoveDestinationStructureVisit)
	}
	if visit.StructureID == nil || *visit.StructureID != "tavern" {
		t.Errorf("visit.StructureID = %v, want &\"tavern\"", visit.StructureID)
	}
	if visit.Position != nil {
		t.Errorf("visit.Position = %v, want nil", visit.Position)
	}

	pos := sim.NewPositionDestination(sim.Position{X: 7, Y: 9})
	if pos.Kind != sim.MoveDestinationPosition {
		t.Errorf("pos.Kind = %q, want %q", pos.Kind, sim.MoveDestinationPosition)
	}
	if pos.Position == nil || *pos.Position != (sim.Position{X: 7, Y: 9}) {
		t.Errorf("pos.Position = %v, want &{7 9}", pos.Position)
	}
	if pos.StructureID != nil {
		t.Errorf("pos.StructureID = %v, want nil", pos.StructureID)
	}
}

// TestCloneMoveDestination covers the deep copy: mutating the clone's
// payload through its pointers must not reach back into the original.
func TestCloneMoveDestination(t *testing.T) {
	orig := sim.NewStructureEnterDestination("inn")
	clone := sim.CloneMoveDestination(orig)
	if clone.StructureID == orig.StructureID {
		t.Error("clone StructureID aliases the original pointer")
	}
	*clone.StructureID = "mutated"
	if *orig.StructureID != "inn" {
		t.Errorf("mutating clone leaked into original: %q", *orig.StructureID)
	}

	origPos := sim.NewPositionDestination(sim.Position{X: 1, Y: 2})
	clonePos := sim.CloneMoveDestination(origPos)
	if clonePos.Position == origPos.Position {
		t.Error("clone Position aliases the original pointer")
	}
	*clonePos.Position = sim.Position{X: 99, Y: 99}
	if *origPos.Position != (sim.Position{X: 1, Y: 2}) {
		t.Errorf("mutating clone leaked into original: %+v", *origPos.Position)
	}
}

// TestCloneMoveIntent covers nil-safety plus the deep copy of the
// embedded destination.
func TestCloneMoveIntent(t *testing.T) {
	if got := sim.CloneMoveIntent(nil); got != nil {
		t.Errorf("CloneMoveIntent(nil) = %v, want nil", got)
	}

	orig := &sim.MoveIntent{
		Destination: sim.NewStructureVisitDestination("tavern"),
		AttemptID:   42,
	}
	clone := sim.CloneMoveIntent(orig)
	if clone == orig {
		t.Fatal("CloneMoveIntent returned the same pointer")
	}
	if clone.AttemptID != 42 {
		t.Errorf("clone.AttemptID = %d, want 42", clone.AttemptID)
	}
	if clone.Destination.StructureID == orig.Destination.StructureID {
		t.Error("clone destination aliases the original StructureID pointer")
	}
	*clone.Destination.StructureID = "mutated"
	if *orig.Destination.StructureID != "tavern" {
		t.Errorf("mutating clone leaked into original: %q", *orig.Destination.StructureID)
	}
}
