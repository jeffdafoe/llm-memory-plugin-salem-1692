package httpapi

import (
	"reflect"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

func TestTranslateEvent_MoveStarted(t *testing.T) {
	frame, ok := TranslateEvent(&sim.ActorMoveStarted{
		ActorID:        "hannah",
		FromPosition:   sim.Position{X: 3, Y: 4},
		TargetPosition: sim.Position{X: 5, Y: 4},
		Path: []sim.GridPoint{
			{X: 3, Y: 4}, {X: 4, Y: 4}, {X: 5, Y: 4},
		},
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
	if d.ID != "hannah" || d.DestKind != "structure_enter" || d.StructureID != "tavern" || d.AttemptID != 7 {
		t.Errorf("walk payload scalar fields = %+v", d)
	}
	wantPath := []tilePointDTO{{X: 3, Y: 4}, {X: 4, Y: 4}, {X: 5, Y: 4}}
	if !reflect.DeepEqual(d.Path, wantPath) {
		t.Errorf("walk path = %+v, want %+v", d.Path, wantPath)
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
