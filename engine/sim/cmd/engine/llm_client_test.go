package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// TestNewLLMClient_SendsSceneScopeForSharedAndCourt pins the wiring, not just the
// classifier: memory-api takes history scope only from the engine (LLM-687), so a
// client built without sim.IsSceneScopedAgent would silently give every shared VA
// and the court the unscoped agent-pair history.
func TestNewLLMClient_SendsSceneScopeForSharedAndCourt(t *testing.T) {
	var mu sync.Mutex
	scopes := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			ToAgents     []string `json:"to_agents"`
			HistoryScope *string  `json:"history_scope"`
		}
		_ = json.Unmarshal(raw, &body)
		scope := "<absent>"
		if body.HistoryScope != nil {
			scope = *body.HistoryScope
		}
		mu.Lock()
		if len(body.ToAgents) == 1 {
			scopes[body.ToAgents[0]] = scope
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"reply":{"text":"ok"}}`))
	}))
	t.Cleanup(srv.Close)

	client := newLLMClient(srv.URL, "test-key", &sim.VABudgetHealth{})

	want := map[string]string{
		sim.CourtMagistrateModel: "scene",
		"zbbs-john-ellis":        "<absent>",
	}
	for _, slug := range sim.SharedVASlugs() {
		want[slug] = "scene"
	}
	for slug := range want {
		_, err := client.Complete(context.Background(), llm.Request{
			Model:    slug,
			Messages: []llm.Message{{Role: llm.RoleUser, Content: "p"}},
		})
		if err != nil {
			t.Fatalf("Complete(%s): %v", slug, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	for slug, wantScope := range want {
		if got := scopes[slug]; got != wantScope {
			t.Errorf("history_scope for %s = %q, want %q", slug, got, wantScope)
		}
	}
}
