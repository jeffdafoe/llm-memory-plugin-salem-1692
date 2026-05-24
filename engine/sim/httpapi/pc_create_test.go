package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

func TestHandlePCCreate_Creates(t *testing.T) {
	// seededWorld has no lodging structure with private rooms, so the PC is
	// created but unlodged — fine for exercising the route + materialization.
	w := seededWorld(t)
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/pc/create", `{"character_name":"Tester","sprite_id":"sprite-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp pcCreateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Created || resp.ActorID == "" {
		t.Fatalf("response = %+v, want created=true + an actor id", resp)
	}
	// The new PC is in the snapshot, bound to the session login "tester".
	a := w.Published().Actors[sim.ActorID(resp.ActorID)]
	if a == nil || a.Kind != sim.KindPC || a.LoginUsername != "tester" || a.DisplayName != "Tester" {
		t.Errorf("created actor = %+v, want PC tester/Tester", a)
	}
}

func TestHandlePCCreate_Idempotent(t *testing.T) {
	w := seededWorld(t)
	srv := NewServer(w, okAuth{})
	post(t, srv, "/api/village/pc/create", `{"character_name":"Tester"}`)
	rec := post(t, srv, "/api/village/pc/create", `{"character_name":"Renamed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp pcCreateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Created {
		t.Error("created = true on re-create, want false")
	}
}

func TestHandlePCCreate_MissingName(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := post(t, srv, "/api/village/pc/create", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlePCCreate_UnknownSprite(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := post(t, srv, "/api/village/pc/create", `{"character_name":"Tester","sprite_id":"sprite-nope"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}
