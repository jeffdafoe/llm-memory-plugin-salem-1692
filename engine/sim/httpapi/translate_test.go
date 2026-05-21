package httpapi

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

func TestTranslateEvent_MoveStarted(t *testing.T) {
	frame, ok := TranslateEvent(&sim.ActorMoveStarted{
		ActorID:           "hannah",
		FromPosition:      sim.Position{X: 3, Y: 4},
		TargetPosition:    sim.Position{X: 10, Y: 12},
		DestinationKind:   sim.MoveDestinationStructureEnter,
		StructureID:       "tavern",
		MovementAttemptID: 7,
	})
	if !ok {
		t.Fatal("ActorMoveStarted should translate")
	}
	if frame.Type != "npc_walking" {
		t.Fatalf("type = %q, want npc_walking", frame.Type)
	}
	d, isType := frame.Data.(walkWireDTO)
	if !isType {
		t.Fatalf("data type = %T, want walkWireDTO", frame.Data)
	}
	want := walkWireDTO{
		ID: "hannah", FromX: 3, FromY: 4, TargetX: 10, TargetY: 12,
		DestKind: "structure_enter", StructureID: "tavern", AttemptID: 7,
	}
	if d != want {
		t.Errorf("walk payload = %+v, want %+v", d, want)
	}
}

func TestTranslateEvent_Arrived(t *testing.T) {
	frame, ok := TranslateEvent(&sim.ActorArrived{
		ActorID:           "hannah",
		FinalPosition:     sim.Position{X: 10, Y: 12},
		FinalStructureID:  "tavern",
		MovementAttemptID: 7,
	})
	if !ok {
		t.Fatal("ActorArrived should translate")
	}
	if frame.Type != "npc_arrived" {
		t.Fatalf("type = %q, want npc_arrived", frame.Type)
	}
	d := frame.Data.(arrivedWireDTO)
	want := arrivedWireDTO{ID: "hannah", X: 10, Y: 12, StructureID: "tavern", AttemptID: 7}
	if d != want {
		t.Errorf("arrived payload = %+v, want %+v", d, want)
	}
}

func TestTranslateEvent_MoveStopped(t *testing.T) {
	frame, ok := TranslateEvent(&sim.ActorMoveStopped{
		ActorID:           "hannah",
		Position:          sim.Position{X: 5, Y: 6},
		Reason:            sim.MoveStoppedBlocked,
		MovementAttemptID: 7,
	})
	if !ok {
		t.Fatal("ActorMoveStopped should translate")
	}
	if frame.Type != "npc_move_stopped" {
		t.Fatalf("type = %q, want npc_move_stopped", frame.Type)
	}
	d := frame.Data.(moveStoppedWireDTO)
	want := moveStoppedWireDTO{ID: "hannah", X: 5, Y: 6, Reason: "blocked", AttemptID: 7}
	if d != want {
		t.Errorf("stopped payload = %+v, want %+v", d, want)
	}
}

// TestTranslateEvent_UnmappedDropped covers the default case: per-tile
// ActorMoved is engine-internal and must not reach the client.
func TestTranslateEvent_UnmappedDropped(t *testing.T) {
	if _, ok := TranslateEvent(&sim.ActorMoved{ActorID: "hannah"}); ok {
		t.Error("ActorMoved should be dropped (engine-internal), not translated")
	}
}
