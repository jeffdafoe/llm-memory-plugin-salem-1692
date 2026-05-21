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

func TestHandlePCSpeak_Accepted(t *testing.T) {
	w := seededWorld(t)
	seedPC(t, w, "pc-tester", "tester", 10, 10)
	srv := NewServer(w, okAuth{})

	// PC has no huddle → speaks to no one, which is a valid v2 state (200).
	rec := post(t, srv, "/api/village/pc/speak", `{"text":"hello there"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlePCSpeak_PCNotFound(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := post(t, srv, "/api/village/pc/speak", `{"text":"hello"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlePCSpeak_EmptyText(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := post(t, srv, "/api/village/pc/speak", `{"text":"   "}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlePCSpeak_TooLong(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	long := `{"text":"` + strings.Repeat("a", 1001) + `"}`
	rec := post(t, srv, "/api/village/pc/speak", long)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// validateSpeakText is unit-tested directly for the control-char rule: building
// the NUL at runtime (string(rune(0))) keeps an actual control byte out of this
// source file, and bypasses the JSON decoder (which would reject a raw control
// char in a string itself) so the test exercises validateSpeakText's own scan.
func TestValidateSpeakText_RejectsControlChar(t *testing.T) {
	if _, msg := validateSpeakText("hi" + string(rune(0)) + "there"); msg == "" {
		t.Error("validateSpeakText accepted a NUL control char, want rejection")
	}
	if _, msg := validateSpeakText("hi" + string(rune(0x7f)) + "there"); msg == "" {
		t.Error("validateSpeakText accepted DEL (0x7F), want rejection")
	}
	// Invalid UTF-8 (a lone continuation byte) is rejected by the ValidString guard.
	if _, msg := validateSpeakText("hi" + string([]byte{0xff}) + "there"); msg == "" {
		t.Error("validateSpeakText accepted invalid UTF-8, want rejection")
	}
	// The VALID replacement character U+FFFD ("�") is a printable code point
	// and must be accepted — the scan must not conflate it with a decode error.
	if clean, msg := validateSpeakText("hi" + string(rune(0xFFFD)) + "there"); msg != "" || clean == "" {
		t.Errorf("validateSpeakText rejected valid U+FFFD: msg=%q", msg)
	}
	// Allowed whitespace controls must pass.
	if clean, msg := validateSpeakText("line one\nline two\ttabbed"); msg != "" || clean == "" {
		t.Errorf("validateSpeakText rejected allowed \\n/\\t: msg=%q", msg)
	}
}

// TestHandlePCSpeak_JSONEscapedControlChar covers the realistic attack/bug path:
// a client sends a JSON escape for U+0000 (valid JSON — the decoder accepts it
// and produces a NUL rune), which validateSpeakText's scan must then reject (→ 400).
// The escape is split across two string literals so this source file never
// contains the literal control byte.
func TestHandlePCSpeak_JSONEscapedControlChar(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	body := `{"text":"hi\u00` + `00there"}`
	rec := post(t, srv, "/api/village/pc/speak", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlePCSpeak_MissingToken(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/village/pc/speak",
		strings.NewReader(`{"text":"hello"}`))
	// No Authorization header → requireAuth rejects before the handler runs.
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlePCSpeak_VocativeStaleRejected(t *testing.T) {
	// pc-tester has no huddle, so the seeded NPC "Hannah" is a non-peer.
	// Addressing her by name in vocative position trips sim.Speak's
	// stale-addressee gate → 422 (a world-state rejection, not a 400).
	w := seededWorld(t)
	seedPC(t, w, "pc-tester", "tester", 10, 10)
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/pc/speak", `{"text":"Hannah, are you there?"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlePCSpeak_TrailingJSON(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := post(t, srv, "/api/village/pc/speak", `{"text":"hi"} garbage`)
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
