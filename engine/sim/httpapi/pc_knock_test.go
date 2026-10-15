package httpapi

import (
	"net/http"
	"testing"
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
