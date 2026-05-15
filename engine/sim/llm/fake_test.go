package llm

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

func TestFakeClient_ReturnsScriptedResponse(t *testing.T) {
	want := Response{
		Content:    "hello",
		StopReason: "end_turn",
	}
	fake := NewFakeClient(ScriptedTurn{Response: want})

	got, err := fake.Complete(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Complete: unexpected error: %v", err)
	}
	if got.Content != want.Content || got.StopReason != want.StopReason {
		t.Errorf("Complete: got %+v, want %+v", got, want)
	}
	if fake.CallCount() != 1 {
		t.Errorf("CallCount: got %d, want 1", fake.CallCount())
	}
}

func TestFakeClient_ReturnsScriptedError(t *testing.T) {
	boom := &Error{Class: ErrorTransport, Message: "boom"}
	fake := NewFakeClient(ScriptedTurn{Err: boom})

	_, err := fake.Complete(context.Background(), Request{})
	if !errors.Is(err, boom) {
		t.Fatalf("Complete: got %v, want %v", err, boom)
	}
	if cls := Classify(err); cls != ErrorTransport {
		t.Errorf("Classify: got %s, want transport", cls)
	}
}

func TestFakeClient_ErrorWinsOverResponse(t *testing.T) {
	boom := &Error{Class: ErrorMalformed, Message: "bad"}
	fake := NewFakeClient(ScriptedTurn{
		Response: Response{Content: "should not surface"},
		Err:      boom,
	})

	resp, err := fake.Complete(context.Background(), Request{})
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("Complete: expected err == boom, got %v", err)
	}
	if resp.Content != "" {
		t.Errorf("Response content should be zero on err path; got %q", resp.Content)
	}
}

func TestFakeClient_ScriptExhausted(t *testing.T) {
	fake := NewFakeClient()
	_, err := fake.Complete(context.Background(), Request{})
	if cls := Classify(err); cls != ErrorMalformed {
		t.Errorf("exhausted Complete: got class %s, want malformed", cls)
	}
	// Exhausted call still records the request — exhaustion is a test bug,
	// not a tick failure, and the request is useful evidence for whoever
	// has to diagnose the over-call.
	if fake.CallCount() != 1 {
		t.Errorf("CallCount after exhausted Complete: got %d, want 1", fake.CallCount())
	}
}

func TestFakeClient_RespectsContextCancellation(t *testing.T) {
	fake := NewFakeClient(ScriptedTurn{Response: Response{Content: "unused"}})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := fake.Complete(ctx, Request{})
	if cls := Classify(err); cls != ErrorContextCancelled {
		t.Errorf("Classify ctx cancellation: got %s, want context_cancelled", cls)
	}
	if fake.CallCount() != 0 {
		t.Errorf("CallCount after ctx cancellation: got %d, want 0 (no work happened)", fake.CallCount())
	}
}

func TestFakeClient_RequestsCloneIsolatesFromCallerMutation(t *testing.T) {
	// The harness reuses a single Messages slice across iterations,
	// appending in place. A test observing Requests() must not see the
	// mutated slice — it must see what the call actually carried.
	fake := NewFakeClient(ScriptedTurn{Response: Response{}})

	msgs := []Message{{Role: RoleUser, Content: "hi"}}
	tools := []ToolSpec{{Name: "move_to", Schema: json.RawMessage(`{"type":"object"}`)}}
	req := Request{Messages: msgs, Tools: tools}
	_, _ = fake.Complete(context.Background(), req)

	msgs[0].Content = "MUTATED"
	tools[0].Name = "MUTATED"
	tools[0].Schema[0] = 'X'

	seen := fake.Requests()
	if len(seen) != 1 {
		t.Fatalf("Requests len: got %d, want 1", len(seen))
	}
	if seen[0].Messages[0].Content != "hi" {
		t.Errorf("Messages mutation leaked: got %q, want \"hi\"", seen[0].Messages[0].Content)
	}
	if seen[0].Tools[0].Name != "move_to" {
		t.Errorf("Tools name mutation leaked: got %q, want \"move_to\"", seen[0].Tools[0].Name)
	}
	if seen[0].Tools[0].Schema[0] == 'X' {
		t.Errorf("Tools schema mutation leaked")
	}
}

// R2 regression: Requests() returns DEEP copies of the recorded
// requests — callers mutating the returned slices must not corrupt the
// FakeClient's stored history for later assertions.
func TestFakeClient_R2_RequestsReturnsDeepCopy(t *testing.T) {
	fake := NewFakeClient(ScriptedTurn{Response: Response{}})

	args := json.RawMessage(`{"destination":"Tavern"}`)
	msgs := []Message{{
		Role: RoleAssistant,
		ToolCalls: []RawToolCall{{
			ID:        "call_abc",
			Name:      "move_to",
			Arguments: args,
		}},
	}}
	_, _ = fake.Complete(context.Background(), Request{Messages: msgs})

	// First Requests() call — mutate the returned (deep-copied) slices.
	first := fake.Requests()
	first[0].Messages[0].Content = "MUTATED-ROOT"
	first[0].Messages[0].ToolCalls[0].Name = "MUTATED-NAME"
	first[0].Messages[0].ToolCalls[0].Arguments[0] = 'X'

	// Second Requests() call must see the ORIGINAL state.
	second := fake.Requests()
	if second[0].Messages[0].Content == "MUTATED-ROOT" {
		t.Errorf("Content mutation leaked to subsequent Requests() call")
	}
	if second[0].Messages[0].ToolCalls[0].Name == "MUTATED-NAME" {
		t.Errorf("ToolCall.Name mutation leaked: got %q", second[0].Messages[0].ToolCalls[0].Name)
	}
	if second[0].Messages[0].ToolCalls[0].Arguments[0] == 'X' {
		t.Errorf("ToolCall.Arguments mutation leaked")
	}
}

func TestFakeClient_RequestsCloneIsolatesToolCalls(t *testing.T) {
	// Nested clone: Message.ToolCalls + RawToolCall.Arguments.
	fake := NewFakeClient(ScriptedTurn{Response: Response{}})

	args := json.RawMessage(`{"destination":"Tavern"}`)
	msgs := []Message{{
		Role: RoleAssistant,
		ToolCalls: []RawToolCall{{
			ID:        "call_abc",
			Name:      "move_to",
			Arguments: args,
		}},
	}}
	_, _ = fake.Complete(context.Background(), Request{Messages: msgs})

	msgs[0].ToolCalls[0].Name = "MUTATED"
	args[0] = 'X'

	seen := fake.Requests()
	if seen[0].Messages[0].ToolCalls[0].Name != "move_to" {
		t.Errorf("nested ToolCalls name mutation leaked: got %q", seen[0].Messages[0].ToolCalls[0].Name)
	}
	if seen[0].Messages[0].ToolCalls[0].Arguments[0] == 'X' {
		t.Errorf("nested Arguments mutation leaked")
	}
}

func TestFakeClient_PushAppendsScript(t *testing.T) {
	fake := NewFakeClient()
	// First call against empty script — exhausted.
	if _, err := fake.Complete(context.Background(), Request{}); Classify(err) != ErrorMalformed {
		t.Fatalf("expected malformed on empty script, got %v", err)
	}
	fake.Push(ScriptedTurn{Response: Response{Content: "pushed"}})
	resp, err := fake.Complete(context.Background(), Request{})
	if err != nil {
		t.Fatalf("after Push: %v", err)
	}
	if resp.Content != "pushed" {
		t.Errorf("got %q, want \"pushed\"", resp.Content)
	}
}

func TestFakeClient_ConcurrentCompleteSafe(t *testing.T) {
	fake := NewFakeClient()
	const n = 50
	for i := 0; i < n; i++ {
		fake.Push(ScriptedTurn{Response: Response{Content: "ok"}})
	}

	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := fake.Complete(context.Background(), Request{})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent Complete: %v", err)
		}
	}
	if fake.CallCount() != n {
		t.Errorf("CallCount: got %d, want %d", fake.CallCount(), n)
	}
}

func TestFakeClient_SchedulesToolCallShape(t *testing.T) {
	// Sanity-check the types compose for a typical "model returned a tool
	// call" turn — the shape PR 3d's harness will produce.
	args, _ := json.Marshal(map[string]string{"destination": "Tavern"})
	fake := NewFakeClient(ScriptedTurn{
		Response: Response{
			ToolCalls: []RawToolCall{{
				ID:        "call_abc",
				Index:     0,
				Name:      "move_to",
				Arguments: args,
			}},
			StopReason: "tool_use",
		},
	})

	resp, err := fake.Complete(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "move_to" {
		t.Fatalf("expected one move_to call, got %+v", resp.ToolCalls)
	}
	if resp.ToolCalls[0].ID != "call_abc" {
		t.Errorf("expected ID \"call_abc\", got %q", resp.ToolCalls[0].ID)
	}
}
