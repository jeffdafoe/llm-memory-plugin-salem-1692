package httpapi

import (
	"net/http"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

func TestHandlePCSprite_Sets(t *testing.T) {
	w := seededWorld(t) // world.Sprites has sprite-1 + sprite-2
	seedPC(t, w, "pc-tester", "tester", 5, 5)
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/pc/sprite", `{"sprite_id":"sprite-2"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// The change rides the published snapshot — no WS event.
	if got := w.Published().Actors["pc-tester"].SpriteID; got != sim.SpriteID("sprite-2") {
		t.Errorf("SpriteID = %q, want sprite-2", got)
	}
}

func TestHandlePCSprite_PCNotFound(t *testing.T) {
	// Base seeded world: no PC bound to login "tester".
	srv := NewServer(seededWorld(t), okAuth{})
	rec := post(t, srv, "/api/village/pc/sprite", `{"sprite_id":"sprite-2"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlePCSprite_UnknownSprite(t *testing.T) {
	w := seededWorld(t)
	seedPC(t, w, "pc-tester", "tester", 5, 5)
	srv := NewServer(w, okAuth{})
	rec := post(t, srv, "/api/village/pc/sprite", `{"sprite_id":"sprite-nope"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlePCSprite_MissingSprite(t *testing.T) {
	w := seededWorld(t)
	seedPC(t, w, "pc-tester", "tester", 5, 5)
	srv := NewServer(w, okAuth{})
	rec := post(t, srv, "/api/village/pc/sprite", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}
