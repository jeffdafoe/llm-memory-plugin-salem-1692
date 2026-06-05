package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/chatlog"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/telemetry"
)

// chatServer builds an operator umbilical server with a chat ring attached.
func chatServer(t *testing.T, perms map[string][]string, ring *chatlog.RingSink) http.Handler {
	t.Helper()
	srv := NewServer(seededWorld(t), permAuth{perms})
	srv.SetTelemetry(telemetry.New(8)) // enables the umbilical route surface
	srv.SetChat(ring)
	return srv.Handler()
}

func chatRec(scene, dir, content string) sim.ChatRecord {
	return sim.ChatRecord{At: time.Unix(0, 0).UTC(), SceneID: scene, ActorID: "josiah", AttemptID: "tk-x", Model: "zbbs-josiah", Direction: dir, Content: content}
}

func TestUmbilicalChat_ReturnsSceneExchangeOldestFirst(t *testing.T) {
	ring := chatlog.New(16)
	ring.WriteChat(chatRec("s1", "perception", "the prompt"))
	ring.WriteChat(chatRec("s1", "response", "i greet you"))
	ring.WriteChat(chatRec("s2", "response", "other scene"))
	h := chatServer(t, operatorPerms, ring)

	rec := req(t, h, "/api/village/umbilical/chat?scene=s1", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var dto UmbilicalChatDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dto.Returned != 2 || len(dto.Messages) != 2 {
		t.Fatalf("returned = %d, want 2 (scene s1 only): %+v", dto.Returned, dto.Messages)
	}
	if dto.Messages[0].Direction != "perception" || dto.Messages[0].Content != "the prompt" {
		t.Errorf("first should be the perception (tx): %+v", dto.Messages[0])
	}
	if dto.Messages[1].Direction != "response" || dto.Messages[1].Content != "i greet you" {
		t.Errorf("second should be the response (rx), oldest-first / no scene leak: %+v", dto.Messages[1])
	}
}

func TestUmbilicalChat_LimitRespected(t *testing.T) {
	ring := chatlog.New(16)
	for _, c := range []string{"c1", "c2", "c3"} {
		ring.WriteChat(chatRec("s1", "response", c))
	}
	h := chatServer(t, operatorPerms, ring)

	rec := req(t, h, "/api/village/umbilical/chat?scene=s1&limit=1", "tok")
	var dto UmbilicalChatDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dto.Returned != 1 || dto.Messages[0].Content != "c3" {
		t.Errorf("limit=1 should yield the single most-recent (c3): %+v", dto.Messages)
	}
}

func TestUmbilicalChat_MissingScene(t *testing.T) {
	h := chatServer(t, operatorPerms, chatlog.New(16))
	if rec := req(t, h, "/api/village/umbilical/chat", "tok"); rec.Code != http.StatusBadRequest {
		t.Errorf("missing scene = %d, want 400", rec.Code)
	}
}

func TestUmbilicalChat_UnknownSceneEmpty(t *testing.T) {
	h := chatServer(t, operatorPerms, chatlog.New(16))
	rec := req(t, h, "/api/village/umbilical/chat?scene=ghost", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var dto UmbilicalChatDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dto.Returned != 0 || len(dto.Messages) != 0 {
		t.Errorf("unknown scene should be empty, got %+v", dto.Messages)
	}
}

// The route registers (umbilical on) but no chat ring was wired -> empty list,
// not a 500.
func TestUmbilicalChat_NilRingEmpty(t *testing.T) {
	h := chatServer(t, operatorPerms, nil)
	rec := req(t, h, "/api/village/umbilical/chat?scene=s1", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with nil ring; body=%s", rec.Code, rec.Body.String())
	}
	var dto UmbilicalChatDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dto.Returned != 0 {
		t.Errorf("nil ring should yield 0 messages, got %d", dto.Returned)
	}
}

func TestUmbilicalChat_OperatorGated(t *testing.T) {
	h := chatServer(t, nil, chatlog.New(16)) // authed, no plugins/administer
	if rec := req(t, h, "/api/village/umbilical/chat?scene=s1", "tok"); rec.Code != http.StatusForbidden {
		t.Errorf("non-operator = %d, want 403", rec.Code)
	}
}

// The route table is the single source of truth for both registration and the
// manifest, so the new /chat route must appear in the served manifest.
func TestUmbilicalChat_AppearsInManifest(t *testing.T) {
	h := chatServer(t, operatorPerms, chatlog.New(16))
	rec := req(t, h, "/api/village/umbilical", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("manifest status = %d, want 200", rec.Code)
	}
	var dto UmbilicalManifestDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, r := range dto.Routes {
		if r.Path == "/api/village/umbilical/chat" {
			found = true
		}
	}
	if !found {
		t.Errorf("/umbilical/chat missing from manifest: %+v", dto.Routes)
	}
}
