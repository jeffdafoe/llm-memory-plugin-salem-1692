package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// seedPC adds a PC actor bound to loginUsername at (x,y). Runs through Send so
// the mutation lands on the world goroutine and is visible to the next command.
func seedPC(t *testing.T, w *sim.World, id, loginUsername string, x, y int) {
	t.Helper()
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors[sim.ActorID(id)] = &sim.Actor{
			ID: sim.ActorID(id), DisplayName: id, Kind: sim.KindPC,
			State: sim.StateIdle, CurrentX: x, CurrentY: y,
			LoginUsername: loginUsername,
		}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seedPC: %v", err)
	}
}

// post issues an authenticated POST (Bearer testToken) and returns the recorder
// without asserting status — the write tests check varied statuses.
func post(t *testing.T, srv *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestHandlePCMove_PositionAccepted(t *testing.T) {
	w := seededWorld(t)
	// okAuth resolves any non-empty token to username "tester".
	seedPC(t, w, "pc-tester", "tester", 10, 10)
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/pc/move",
		`{"destination":{"kind":"position","position":{"x":12,"y":10}}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var res pcMoveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.MovementAttemptID == 0 {
		t.Errorf("movement_attempt_id = 0, want a stamped attempt")
	}
}

func TestHandlePCMove_PCNotFound(t *testing.T) {
	// Base seeded world: bram is a PC but has no LoginUsername, so "tester"
	// resolves to no PC.
	srv := NewServer(seededWorld(t), okAuth{})
	rec := post(t, srv, "/api/village/pc/move",
		`{"destination":{"kind":"position","position":{"x":12,"y":10}}}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlePCMove_StructureNotFound(t *testing.T) {
	w := seededWorld(t)
	seedPC(t, w, "pc-tester", "tester", 10, 10)
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/pc/move",
		`{"destination":{"kind":"structure_enter","structure_id":"does-not-exist"}}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlePCMove_MalformedBody(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := post(t, srv, "/api/village/pc/move", `{not json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlePCMove_UnknownKind(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := post(t, srv, "/api/village/pc/move",
		`{"destination":{"kind":"teleport"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlePCMove_PositionMissing(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := post(t, srv, "/api/village/pc/move",
		`{"destination":{"kind":"position"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlePCMove_OutOfBounds(t *testing.T) {
	// Bounds are checked before the command runs, so no PC is needed: a tile
	// outside the grid is a 422 (well-formed but unreachable), not a 400.
	srv := NewServer(seededWorld(t), okAuth{})
	rec := post(t, srv, "/api/village/pc/move",
		`{"destination":{"kind":"position","position":{"x":999999,"y":0}}}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlePCMove_NPCWithLoginNotMoved(t *testing.T) {
	// An NPC that happens to carry LoginUsername "tester" must NOT be movable
	// via pc/move — only KindPC actors resolve. With no matching PC, the
	// session resolves to nothing → 404. Guards the kind check in findPCByLogin.
	w := seededWorld(t)
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["npc-tester"] = &sim.Actor{
			ID: "npc-tester", DisplayName: "npc-tester", Kind: sim.KindNPCShared,
			State: sim.StateIdle, CurrentX: 10, CurrentY: 10, LoginUsername: "tester",
		}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seed npc: %v", err)
	}
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/pc/move",
		`{"destination":{"kind":"position","position":{"x":12,"y":10}}}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlePCMove_AmbiguousPayload(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	cases := []struct {
		name string
		body string
	}{
		{"position with structure_id", `{"destination":{"kind":"position","position":{"x":12,"y":10},"structure_id":"inn"}}`},
		{"structure_enter with position", `{"destination":{"kind":"structure_enter","structure_id":"inn","position":{"x":12,"y":10}}}`},
		{"structure_visit with position", `{"destination":{"kind":"structure_visit","structure_id":"inn","position":{"x":12,"y":10}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := post(t, srv, "/api/village/pc/move", tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandlePCMove_TrailingJSON(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := post(t, srv, "/api/village/pc/move",
		`{"destination":{"kind":"position","position":{"x":12,"y":10}}} garbage`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlePCMove_MissingToken(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/village/pc/move",
		strings.NewReader(`{"destination":{"kind":"position","position":{"x":12,"y":10}}}`))
	// No Authorization header → requireAuth rejects before the handler runs.
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
}
