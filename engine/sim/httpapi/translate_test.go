package httpapi

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"

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

func TestTranslateEvent_ObjectMoved(t *testing.T) {
	frame, ok := TranslateEvent(&sim.VillageObjectMoved{
		ObjectID: "bench-1",
		X:        150.5,
		Y:        175.25,
	})
	if !ok {
		t.Fatal("VillageObjectMoved should translate")
	}
	if frame.Type != "object_moved" {
		t.Fatalf("type = %q, want object_moved", frame.Type)
	}
	d := frame.Data.(objectMovedWireDTO)
	want := objectMovedWireDTO{ID: "bench-1", X: 150.5, Y: 175.25}
	if d != want {
		t.Errorf("object_moved payload = %+v, want %+v", d, want)
	}
}

func TestTranslateEvent_ObjectDeleted(t *testing.T) {
	frame, ok := TranslateEvent(&sim.VillageObjectDeleted{ObjectID: "bench-1"})
	if !ok {
		t.Fatal("VillageObjectDeleted should translate")
	}
	if frame.Type != "object_deleted" {
		t.Fatalf("type = %q, want object_deleted", frame.Type)
	}
	d := frame.Data.(objectDeletedWireDTO)
	if d != (objectDeletedWireDTO{ID: "bench-1"}) {
		t.Errorf("object_deleted payload = %+v, want {bench-1}", d)
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

func TestTranslateEvent_Spoke(t *testing.T) {
	at := time.Date(1692, 5, 14, 12, 30, 0, 0, time.FixedZone("local", -4*3600))
	frame, ok := TranslateEvent(&sim.Spoke{
		SpeakerID:    "hannah",
		HuddleID:     "huddle-1",
		RecipientIDs: []sim.ActorID{"bram", "ezekiel"},
		Text:         "Good evening.",
		At:           at,
	})
	if !ok {
		t.Fatal("Spoke should translate")
	}
	if frame.Type != "npc_spoke" {
		t.Fatalf("type = %q, want npc_spoke", frame.Type)
	}
	d, isType := frame.Data.(spokeWireDTO)
	if !isType {
		t.Fatalf("data type = %T, want spokeWireDTO", frame.Data)
	}
	if d.ID != "hannah" || d.HuddleID != "huddle-1" || d.Text != "Good evening." {
		t.Errorf("spoke scalar fields = %+v", d)
	}
	if !reflect.DeepEqual(d.RecipientIDs, []string{"bram", "ezekiel"}) {
		t.Errorf("recipients = %+v, want [bram ezekiel]", d.RecipientIDs)
	}
	if d.At != at.UTC().Format(time.RFC3339) {
		t.Errorf("at = %q, want %q", d.At, at.UTC().Format(time.RFC3339))
	}
	// SpeechID aliases the event's EventID, which is 0 until World.emit stamps
	// it. This event was constructed directly (never emitted), so 0 is expected
	// here; the live path stamps it via emit. Asserted so a mapping change is caught.
	if d.SpeechID != 0 {
		t.Errorf("speech_id = %d, want 0 for an un-emitted event", d.SpeechID)
	}
}

// TestTranslateEvent_SpokeEmptyHuddle: speaking to no one still translates (a
// valid v2 state). Asserts the WIRE shape (marshal-level), since omitempty only
// applies during marshaling — checking the typed DTO wouldn't catch a contract
// regression: huddle_id must be absent, recipient_ids must be [] (not null).
func TestTranslateEvent_SpokeEmptyHuddle(t *testing.T) {
	frame, ok := TranslateEvent(&sim.Spoke{SpeakerID: "bram", Text: "Anyone here?"})
	if !ok {
		t.Fatal("Spoke with empty huddle should still translate")
	}
	b, err := json.Marshal(frame.Data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytes.Contains(b, []byte(`"huddle_id"`)) {
		t.Errorf("huddle_id should be omitted for empty huddle: %s", b)
	}
	if !bytes.Contains(b, []byte(`"recipient_ids":[]`)) {
		t.Errorf("recipient_ids should marshal as [], got: %s", b)
	}
}

func TestTranslateEvent_PhaseApplied(t *testing.T) {
	frame, ok := TranslateEvent(&sim.PhaseApplied{From: sim.PhaseDay, To: sim.PhaseNight})
	if !ok {
		t.Fatal("PhaseApplied should translate")
	}
	if frame.Type != "world_phase_changed" {
		t.Fatalf("type = %q, want world_phase_changed", frame.Type)
	}
	d, isType := frame.Data.(phaseChangedWireDTO)
	if !isType {
		t.Fatalf("data type = %T, want phaseChangedWireDTO", frame.Data)
	}
	if d.Phase != "night" {
		t.Errorf("phase = %q, want night", d.Phase)
	}
}

// TestTranslateEvent_PhaseAppliedIdempotent: an admin force-phase to the current
// phase emits with From == To and still translates (the client treats it as a
// harmless no-op set).
func TestTranslateEvent_PhaseAppliedIdempotent(t *testing.T) {
	frame, ok := TranslateEvent(&sim.PhaseApplied{From: sim.PhaseDay, To: sim.PhaseDay})
	if !ok {
		t.Fatal("idempotent PhaseApplied should still translate")
	}
	if d := frame.Data.(phaseChangedWireDTO); d.Phase != "day" {
		t.Errorf("phase = %q, want day", d.Phase)
	}
}

func TestTranslateEvent_VillageObjectStateChanged(t *testing.T) {
	frame, ok := TranslateEvent(&sim.VillageObjectStateChanged{
		ObjectID:  "lamp-3",
		FromState: "unlit",
		ToState:   "lit",
	})
	if !ok {
		t.Fatal("VillageObjectStateChanged should translate")
	}
	if frame.Type != "object_state_changed" {
		t.Fatalf("type = %q, want object_state_changed", frame.Type)
	}
	d, isType := frame.Data.(objectStateChangedWireDTO)
	if !isType {
		t.Fatalf("data type = %T, want objectStateChangedWireDTO", frame.Data)
	}
	if d.ID != "lamp-3" || d.State != "lit" {
		t.Errorf("object state fields = %+v, want {lamp-3 lit}", d)
	}
}

// TestTranslateEvent_UnmappedDropped covers the default case: per-tile
// ActorMoved is engine-internal and must not reach the client.
func TestTranslateEvent_UnmappedDropped(t *testing.T) {
	if _, ok := TranslateEvent(&sim.ActorMoved{ActorID: "hannah"}); ok {
		t.Error("ActorMoved should be dropped (engine-internal), not translated")
	}
}
