package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pc_leave_note_test.go — LLM-19: POST /api/village/pc/leave-note, the
// door-note frames and the object flag. The note itself (who it may be for,
// refusals, replacement, read-after-turn) is covered in the sim package
// (door_note_test.go); these cover the HTTP surface.

func TestHandlePCLeaveNote_BadBody(t *testing.T) {
	w := seededWorld(t)
	seedTavernPC(t, w, "tester")
	srv := NewServer(w, okAuth{})
	for _, body := range []string{
		``, `{}`,
		`{"structure_id": "x", "text": "hi"}`,
		`{"recipient_id": "r", "text": "hi"}`,
		`{"structure_id": "x", "recipient_id": "r", "text": ""}`,
		`{"structure_id": "x", "recipient_id": "r", "text": "hi", "y": 1}`,
		`{"structure_id": "x", "recipient_id": "r", "text": "hi"} {}`,
		`{"structure_id": "x", "recipient_id": "r", "text": "` + strings.Repeat("a", sim.MaxDoorNoteChars+1) + `"}`,
		`{"structure_id": "x", "recipient_id": "r", "text": "bell\u0007"}`,
	} {
		if rec := post(t, srv, "/api/village/pc/leave-note", body); rec.Code != http.StatusBadRequest {
			t.Errorf("body %.60q: status = %d, want 400", body, rec.Code)
		}
	}
}

func TestHandlePCLeaveNote_PCNotFound(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	if rec := post(t, srv, "/api/village/pc/leave-note", `{"structure_id": "x", "recipient_id": "r", "text": "hi"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// A note at a structure that is no door to knock on (here, no such house) is
// refused with 422.
func TestHandlePCLeaveNote_Refused(t *testing.T) {
	w := seededWorld(t)
	seedTavernPC(t, w, "tester")
	srv := NewServer(w, okAuth{})
	if rec := post(t, srv, "/api/village/pc/leave-note", `{"structure_id": "no-such-house", "recipient_id": "r", "text": "hi"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
}

func TestLeaveNoteErrorResponse(t *testing.T) {
	if code, _ := leaveNoteErrorResponse(errPCNotFound); code != http.StatusNotFound {
		t.Errorf("no PC -> %d, want 404", code)
	}
	if code, _ := leaveNoteErrorResponse(sim.ErrDoorNoteText); code != http.StatusBadRequest {
		t.Errorf("bad text -> %d, want 400", code)
	}
	if code, msg := leaveNoteErrorResponse(fmt.Errorf("%w: they do not live or keep here", sim.ErrDoorNoteRefused)); code != http.StatusUnprocessableEntity || msg == "" {
		t.Errorf("refusal -> %d %q, want 422 with the reason", code, msg)
	}
	if code, msg := leaveNoteErrorResponse(errors.New("internal")); code != http.StatusInternalServerError || msg != "leaving the note failed" {
		t.Errorf("unexpected -> %d %q, want 500", code, msg)
	}
}

// An unanswered knock's response carries note_for; an answered one omits it.
func TestPCKnockResponse_NoteFor(t *testing.T) {
	b, _ := json.Marshal(pcKnockResponse{NoteFor: []pcKnockRecipientDTO{{ID: "spouse", Name: "Sarah Owen"}}})
	if got := string(b); got != `{"answered":false,"note_for":[{"id":"spouse","name":"Sarah Owen"}]}` {
		t.Errorf("unanswered = %s", got)
	}
	b, _ = json.Marshal(pcKnockResponse{Answered: true})
	if got := string(b); got != `{"answered":true}` {
		t.Errorf("answered = %s", got)
	}
}

// The author's confirmation is private to them; the door flag carries no text
// and reaches everyone.
func TestTranslateEvent_DoorNote(t *testing.T) {
	frame, ok := TranslateEvent(&sim.DoorNoteLeft{ActorID: "pc", StructureID: "cottage", Text: "You tuck the note into the door."})
	if !ok || frame.Type != "room_event" {
		t.Fatalf("DoorNoteLeft -> %+v %v, want a room_event", frame, ok)
	}
	if d := frame.Data.(roomEventWireDTO); d.Kind != "note_left" || !d.Private || d.ActorID != "pc" || d.StructureID != "cottage" || d.Text == "" {
		t.Errorf("note_left frame = %+v", d)
	}
	frame, ok = TranslateEvent(&sim.DoorNotePinned{StructureID: "cottage", HasNote: true})
	if !ok || frame.Type != "door_note_changed" {
		t.Fatalf("DoorNotePinned -> %+v %v, want door_note_changed", frame, ok)
	}
	b, _ := json.Marshal(frame.Data)
	if got := string(b); got != `{"id":"cottage","has_note":true}` {
		t.Errorf("door_note_changed = %s", got)
	}
	frame, _ = TranslateEvent(&sim.DoorNotePinned{StructureID: "cottage"})
	if b, _ := json.Marshal(frame.Data); string(b) != `{"id":"cottage","has_note":false}` {
		t.Errorf("cleared door_note_changed = %s (has_note must be sent false, not dropped)", b)
	}
}

// The load DTO flags a structure with an unread note, and only that one.
func TestObjectsFromSnapshot_HasNote(t *testing.T) {
	w := seededWorld(t)
	snap := w.Published()
	if len(snap.VillageObjects) == 0 {
		t.Skip("harness: seeded world has no objects")
	}
	var target sim.VillageObjectID
	for id := range snap.VillageObjects {
		target = id
		break
	}
	snap.DoorNotes = map[sim.DoorNoteID]*sim.DoorNote{
		"note-0000cc01": {ID: "note-0000cc01", StructureID: sim.StructureID(target), Body: "hi"},
	}
	for _, dto := range objectsFromSnapshot(snap, snap.Assets) {
		if want := dto.ID == string(target); dto.HasNote != want {
			t.Errorf("object %s has_note = %v, want %v", dto.ID, dto.HasNote, want)
		}
	}
}
