package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pc_knock_test.go — LLM-785: POST /api/village/pc/knock. The knock itself
// (answered, unanswered, refusals) is covered in the sim package
// (knock_at_door_test.go); these cover the HTTP surface: body shape,
// session→PC, status mapping.

func TestHandlePCKnock_BadBody(t *testing.T) {
	w := seededWorld(t)
	seedTavernPC(t, w, "tester")
	srv := NewServer(w, okAuth{})
	for _, body := range []string{``, `{}`, `{"structure_id": ""}`, `{"structure_id": "x", "y": 1}`, `{"structure_id": "x"} {}`} {
		if rec := post(t, srv, "/api/village/pc/knock", body); rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", body, rec.Code)
		}
	}
}

func TestHandlePCKnock_PCNotFound(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	if rec := post(t, srv, "/api/village/pc/knock", `{"structure_id": "x"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// A knock on a structure that is no door to knock on (here, no such house) is
// refused with 422.
func TestHandlePCKnock_Refused(t *testing.T) {
	w := seededWorld(t)
	seedTavernPC(t, w, "tester")
	srv := NewServer(w, okAuth{})
	if rec := post(t, srv, "/api/village/pc/knock", `{"structure_id": "no-such-house"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
}

// Only a refusal is the player's 422; an unexpected error is a 500 that says
// nothing of the engine's insides (code_review).
func TestKnockErrorResponse(t *testing.T) {
	if code, _ := knockErrorResponse(errPCNotFound); code != http.StatusNotFound {
		t.Errorf("no PC -> %d, want 404", code)
	}
	if code, msg := knockErrorResponse(fmt.Errorf("%w: you are not at that door", sim.ErrKnockRefused)); code != http.StatusUnprocessableEntity || msg == "" {
		t.Errorf("refusal -> %d %q, want 422 with the reason", code, msg)
	}
	if code, msg := knockErrorResponse(errors.New("internal: scene table corrupt")); code != http.StatusInternalServerError || msg != "knock failed" {
		t.Errorf("unexpected -> %d %q, want 500 \"knock failed\"", code, msg)
	}
}
