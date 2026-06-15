package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// ZBBS-WORK-413 (handler layer): a handler's hand-authored static-validation
// rejection — returned as a modelSafeError (empty-after-trim, control char,
// duplicate name caught post-decode) — must surface its reason to the model,
// mirroring the decode and command layers, instead of the opaque
// "[error: handler_failed]". The complementary "internal handler error stays
// generic and doesn't leak" case is TestHarness_R2_HandlerErrorNotLeakedToTranscript.

// toolResultForCall pulls the tool-result content the harness handed back for
// the given call id out of the transcript sent on the next LLM request.
func toolResultForCall(t *testing.T, client *llm.FakeClient, callID string) string {
	t.Helper()
	reqs := client.Requests()
	if len(reqs) < 2 {
		t.Fatalf("client calls: got %d, want >= 2 (need the follow-up request carrying the tool result)", len(reqs))
	}
	for _, m := range reqs[1].Messages {
		if m.Role == llm.RoleTool && m.ToolCallID == callID {
			return m.Content
		}
	}
	t.Fatalf("no tool result for call %q in the follow-up transcript", callID)
	return ""
}

// Observation branch: a synthetic recall handler returns a modelSafeError.
func TestHarness_HandlerModelSafeError_ObservationSurfaced(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	const reason = "recall: query is empty after trim — give it something to search for"
	r := NewRegistry()
	fn := func(_ context.Context, _ HandlerInput) (string, error) {
		return "", modelSafef(reason)
	}
	if err := r.RegisterObservation("recall", json.RawMessage(`{"type":"object"}`), passthroughDecode, fn); err != nil {
		t.Fatalf("register: %v", err)
	}
	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "recall", `{}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "done", `{}`)}}},
	)
	h, err := NewHarness(HarnessConfig{Client: client, Registry: r})
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	_ = h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	got := toolResultForCall(t, client, "c1")
	if !strings.Contains(got, reason) {
		t.Errorf("handler reason should reach the model; got %q", got)
	}
	if strings.Contains(got, "handler_failed") {
		t.Errorf("model-safe handler error must not use the generic handler_failed label; got %q", got)
	}
}

// Commit branch through the REAL scene_quote handler: a whitespace-only
// item_kind passes decode (non-empty) but HandleSceneQuote rejects it
// empty-after-trim with a modelSafeError, which must surface.
func TestHarness_HandlerModelSafeError_RealSceneQuoteSurfaced(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	r := NewRegistry()
	if err := RegisterSceneQuote(r); err != nil {
		t.Fatalf("register scene_quote: %v", err)
	}
	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{
			newToolCall("c1", 0, "scene_quote", `{"item_kind":"   ","qty":1,"amount":4}`),
		}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "done", `{}`)}}},
	)
	h, err := NewHarness(HarnessConfig{Client: client, Registry: r})
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	_ = h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	got := toolResultForCall(t, client, "c1")
	if !strings.Contains(got, "item_kind is empty after trim") {
		t.Errorf("real handler static-validation reason should reach the model; got %q", got)
	}
	if strings.Contains(got, "handler_failed") {
		t.Errorf("model-safe handler error must not use the generic handler_failed label; got %q", got)
	}
}
