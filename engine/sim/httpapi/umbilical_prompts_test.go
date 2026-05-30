package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/promptlog"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/telemetry"
)

// promptServer builds an operator umbilical server with a prompt ring attached.
func promptServer(t *testing.T, perms map[string][]string, ring *promptlog.RingSink) http.Handler {
	t.Helper()
	srv := NewServer(seededWorld(t), permAuth{perms})
	srv.SetTelemetry(telemetry.New(8)) // enables the umbilical route surface
	srv.SetPrompts(ring)
	return srv.Handler()
}

func promptRec(actor, prompt string) sim.PromptRecord {
	return sim.PromptRecord{At: time.Unix(0, 0).UTC(), ActorID: sim.ActorID(actor), AttemptID: "tk-x", Prompt: prompt}
}

func TestUmbilicalAgentPrompts_ReturnsActorPromptsOldestFirst(t *testing.T) {
	ring := promptlog.New(8)
	ring.WritePrompt(promptRec("josiah", "prompt one"))
	ring.WritePrompt(promptRec("josiah", "prompt two"))
	ring.WritePrompt(promptRec("other", "not josiah"))
	h := promptServer(t, operatorPerms, ring)

	rec := req(t, h, "/api/village/umbilical/agent/prompts?id=josiah", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var dto UmbilicalAgentPromptsDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dto.Returned != 2 || len(dto.Prompts) != 2 {
		t.Fatalf("returned = %d, want 2 (josiah only): %+v", dto.Returned, dto.Prompts)
	}
	if dto.Prompts[0].Prompt != "prompt one" || dto.Prompts[1].Prompt != "prompt two" {
		t.Errorf("not oldest-first / leaked other actor: %+v", dto.Prompts)
	}
}

func TestUmbilicalAgentPrompts_LimitRespected(t *testing.T) {
	ring := promptlog.New(8)
	for _, p := range []string{"p1", "p2", "p3"} {
		ring.WritePrompt(promptRec("josiah", p))
	}
	h := promptServer(t, operatorPerms, ring)

	rec := req(t, h, "/api/village/umbilical/agent/prompts?id=josiah&limit=1", "tok")
	var dto UmbilicalAgentPromptsDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dto.Returned != 1 || dto.Prompts[0].Prompt != "p3" {
		t.Errorf("limit=1 should yield the single most-recent (p3): %+v", dto.Prompts)
	}
}

func TestUmbilicalAgentPrompts_MissingID(t *testing.T) {
	h := promptServer(t, operatorPerms, promptlog.New(8))
	if rec := req(t, h, "/api/village/umbilical/agent/prompts", "tok"); rec.Code != http.StatusBadRequest {
		t.Errorf("missing id = %d, want 400", rec.Code)
	}
}

func TestUmbilicalAgentPrompts_UnknownActorEmpty(t *testing.T) {
	h := promptServer(t, operatorPerms, promptlog.New(8))
	rec := req(t, h, "/api/village/umbilical/agent/prompts?id=ghost", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var dto UmbilicalAgentPromptsDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dto.Returned != 0 || len(dto.Prompts) != 0 {
		t.Errorf("unknown actor should be empty, got %+v", dto.Prompts)
	}
}

// The route registers (umbilical on) but no prompt ring was wired → empty list,
// not a 500.
func TestUmbilicalAgentPrompts_NilRingEmpty(t *testing.T) {
	h := promptServer(t, operatorPerms, nil)
	rec := req(t, h, "/api/village/umbilical/agent/prompts?id=josiah", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with nil ring; body=%s", rec.Code, rec.Body.String())
	}
	var dto UmbilicalAgentPromptsDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dto.Returned != 0 {
		t.Errorf("nil ring should yield 0 prompts, got %d", dto.Returned)
	}
}

func TestUmbilicalAgentPrompts_OperatorGated(t *testing.T) {
	h := promptServer(t, nil, promptlog.New(8)) // authed, no plugins/administer
	if rec := req(t, h, "/api/village/umbilical/agent/prompts?id=josiah", "tok"); rec.Code != http.StatusForbidden {
		t.Errorf("non-operator = %d, want 403", rec.Code)
	}
}
