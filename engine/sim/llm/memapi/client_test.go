package memapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// --- test server harness --------------------------------------------------

// recordedRequest captures one HTTP POST against the test server.
type recordedRequest struct {
	Method      string
	Path        string
	Authz       string
	ContentType string
	Body        chatRequest
	RawBody     []byte
}

// testServer is an httptest.Server that records requests and serves
// caller-scripted responses. Each entry in `responses` is consumed in
// order; if responses is empty, returns 500.
type testServer struct {
	t         *testing.T
	mu        sync.Mutex
	requests  []recordedRequest
	responses []serverResponse
	cursor    int
	server    *httptest.Server
}

type serverResponse struct {
	status int
	body   string
	delay  time.Duration
}

func newTestServer(t *testing.T) *testServer {
	ts := &testServer{t: t}
	ts.server = httptest.NewServer(http.HandlerFunc(ts.handle))
	t.Cleanup(ts.server.Close)
	return ts
}

func (ts *testServer) handle(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var parsed chatRequest
	_ = json.Unmarshal(raw, &parsed)

	ts.mu.Lock()
	ts.requests = append(ts.requests, recordedRequest{
		Method:      r.Method,
		Path:        r.URL.Path,
		Authz:       r.Header.Get("Authorization"),
		ContentType: r.Header.Get("Content-Type"),
		Body:        parsed,
		RawBody:     raw,
	})
	var resp serverResponse
	if ts.cursor < len(ts.responses) {
		resp = ts.responses[ts.cursor]
		ts.cursor++
	} else {
		resp = serverResponse{status: 500, body: `{"error":{"code":"NO_SCRIPT","message":"test server script exhausted"}}`}
	}
	ts.mu.Unlock()

	if resp.delay > 0 {
		time.Sleep(resp.delay)
	}
	w.WriteHeader(resp.status)
	_, _ = w.Write([]byte(resp.body))
}

func (ts *testServer) pushResponse(r serverResponse) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.responses = append(ts.responses, r)
}

func (ts *testServer) recorded() []recordedRequest {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	out := make([]recordedRequest, len(ts.requests))
	copy(out, ts.requests)
	return out
}

// newTestClient constructs a Client pointed at the test server with
// zero-delay persist backoffs (so retry tests don't waste real time).
func newTestClient(t *testing.T, ts *testServer, opts ...Option) *Client {
	t.Helper()
	all := append([]Option{
		WithPersistBackoffs([]time.Duration{0, 0, 0}),
	}, opts...)
	return NewClient(ts.server.URL, "test-key", all...)
}

// okReply is a 2xx body with a text-only reply.
func okReply(text string) serverResponse {
	body, _ := json.Marshal(chatResponse{Reply: &replyPayload{Text: text}})
	return serverResponse{status: 200, body: string(body)}
}

// okReplyWithTools is a 2xx body carrying tool_calls.
func okReplyWithTools(text string, calls []apiToolCall) serverResponse {
	body, _ := json.Marshal(chatResponse{Reply: &replyPayload{Text: text, ToolCalls: calls}})
	return serverResponse{status: 200, body: string(body)}
}

// --- Complete -------------------------------------------------------------

func TestComplete_RequiresModel(t *testing.T) {
	ts := newTestServer(t)
	c := newTestClient(t, ts)

	_, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hello"}},
		// Model intentionally empty.
	})
	if err == nil {
		t.Fatal("expected error for empty Model")
	}
	var typed *llm.Error
	if !errors.As(err, &typed) || typed.Class != llm.ErrorMalformed {
		t.Errorf("expected Malformed, got %v (%T)", err, err)
	}
	if got := len(ts.recorded()); got != 0 {
		t.Errorf("expected no HTTP calls, got %d", got)
	}
}

func TestComplete_IterZeroMessageExtraction(t *testing.T) {
	ts := newTestServer(t)
	ts.pushResponse(okReply("hi there"))
	c := newTestClient(t, ts)

	resp, err := c.Complete(context.Background(), llm.Request{
		Model:          "salem-generic",
		Messages:       []llm.Message{{Role: llm.RoleUser, Content: "perception text"}},
		SceneID:        "scene-uuid",
		ConversationID: "sc-conv-1",
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Content != "hi there" {
		t.Errorf("Content = %q, want %q", resp.Content, "hi there")
	}

	recs := ts.recorded()
	if len(recs) != 1 {
		t.Fatalf("recorded %d requests, want 1", len(recs))
	}
	r := recs[0]
	if r.Path != "/v1/chat/send" {
		t.Errorf("path = %q, want /v1/chat/send", r.Path)
	}
	if r.Authz != "Bearer test-key" {
		t.Errorf("authz = %q", r.Authz)
	}
	if r.Body.Message != "perception text" {
		t.Errorf("message = %q, want %q", r.Body.Message, "perception text")
	}
	if got := r.Body.ToAgents; len(got) != 1 || got[0] != "salem-generic" {
		t.Errorf("to_agents = %v, want [salem-generic]", got)
	}
	if r.Body.SceneID != "scene-uuid" {
		t.Errorf("scene_id = %q, want scene-uuid", r.Body.SceneID)
	}
	if r.Body.ConversationID != "sc-conv-1" {
		t.Errorf("conversation_id = %q, want sc-conv-1", r.Body.ConversationID)
	}
	if !r.Body.Wait {
		t.Error("wait = false, want true")
	}
	if len(r.Body.ToolCallResults) != 0 {
		t.Errorf("tool_call_results = %v, want empty", r.Body.ToolCallResults)
	}
}

func TestComplete_SystemUserConcat(t *testing.T) {
	ts := newTestServer(t)
	ts.pushResponse(okReply("ok"))
	c := newTestClient(t, ts)

	_, err := c.Complete(context.Background(), llm.Request{
		Model: "salem-generic",
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "SYS"},
			{Role: llm.RoleUser, Content: "USR"},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	recs := ts.recorded()
	want := "SYS\n\nUSR"
	if recs[0].Body.Message != want {
		t.Errorf("message = %q, want %q", recs[0].Body.Message, want)
	}
}

func TestComplete_ToolCallResultsExtraction(t *testing.T) {
	ts := newTestServer(t)
	ts.pushResponse(okReply("done"))
	c := newTestClient(t, ts)

	// Iter 1 transcript after one tool call.
	_, err := c.Complete(context.Background(), llm.Request{
		Model: "zbbs-josiah",
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: "perception"},
			{Role: llm.RoleAssistant, Content: "", ToolCalls: []llm.RawToolCall{{ID: "call_a", Name: "speak", Arguments: json.RawMessage(`{}`)}}},
			{Role: llm.RoleTool, ToolCallID: "call_a", Content: "[ok]"},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	recs := ts.recorded()
	r := recs[0].Body
	if r.Message != "" {
		t.Errorf("message = %q, want empty", r.Message)
	}
	if len(r.ToolCallResults) != 1 {
		t.Fatalf("tool_call_results len = %d, want 1", len(r.ToolCallResults))
	}
	if r.ToolCallResults[0].ID != "call_a" || r.ToolCallResults[0].Content != "[ok]" {
		t.Errorf("tool_call_results[0] = %+v", r.ToolCallResults[0])
	}
}

func TestComplete_ParallelToolResults(t *testing.T) {
	ts := newTestServer(t)
	ts.pushResponse(okReply("done"))
	c := newTestClient(t, ts)

	_, err := c.Complete(context.Background(), llm.Request{
		Model: "zbbs-josiah",
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: "p"},
			{Role: llm.RoleAssistant, ToolCalls: []llm.RawToolCall{
				{ID: "a", Name: "x"},
				{ID: "b", Name: "y"},
			}},
			{Role: llm.RoleTool, ToolCallID: "a", Content: "ra"},
			{Role: llm.RoleTool, ToolCallID: "b", Content: "rb"},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	results := ts.recorded()[0].Body.ToolCallResults
	if len(results) != 2 {
		t.Fatalf("results len = %d, want 2", len(results))
	}
	if results[0].ID != "a" || results[0].Content != "ra" {
		t.Errorf("results[0] = %+v", results[0])
	}
	if results[1].ID != "b" || results[1].Content != "rb" {
		t.Errorf("results[1] = %+v", results[1])
	}
}

func TestComplete_ToolsOfferedPassthrough(t *testing.T) {
	ts := newTestServer(t)
	ts.pushResponse(okReply("ok"))
	c := newTestClient(t, ts)

	schema := json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`)
	_, err := c.Complete(context.Background(), llm.Request{
		Model:    "x",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "p"}},
		Tools: []llm.ToolSpec{
			{Name: "speak", Description: "say something", Schema: schema},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	rec := ts.recorded()[0]
	if len(rec.Body.ToolsOffered) != 1 {
		t.Fatalf("tools_offered len = %d, want 1", len(rec.Body.ToolsOffered))
	}
	tool := rec.Body.ToolsOffered[0]
	if tool.Name != "speak" || tool.Description != "say something" {
		t.Errorf("tool name/desc = %q/%q", tool.Name, tool.Description)
	}
	// Parameters must be byte-identical to the input schema.
	if string(tool.Parameters) != string(schema) {
		t.Errorf("parameters = %q, want %q", tool.Parameters, schema)
	}
}

func TestComplete_ResponseToolCallsRemarshal(t *testing.T) {
	ts := newTestServer(t)
	ts.pushResponse(okReplyWithTools("", []apiToolCall{
		{ID: "call_1", Name: "speak", Input: map[string]interface{}{"text": "hello", "qty": float64(2)}},
		{ID: "call_2", Name: "move_to", Input: map[string]interface{}{"x": float64(5), "y": float64(3)}},
	}))
	c := newTestClient(t, ts)

	resp, err := c.Complete(context.Background(), llm.Request{
		Model:    "x",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "p"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("tool calls len = %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].ID != "call_1" || resp.ToolCalls[0].Name != "speak" || resp.ToolCalls[0].Index != 0 {
		t.Errorf("toolcall[0] = %+v", resp.ToolCalls[0])
	}
	if resp.ToolCalls[1].Index != 1 {
		t.Errorf("toolcall[1].Index = %d, want 1", resp.ToolCalls[1].Index)
	}
	// Arguments must be valid JSON containing the expected fields.
	var args map[string]interface{}
	if err := json.Unmarshal(resp.ToolCalls[0].Arguments, &args); err != nil {
		t.Fatalf("arguments unmarshal: %v", err)
	}
	if args["text"] != "hello" {
		t.Errorf("args text = %v, want hello", args["text"])
	}
}

func TestComplete_5xxIsTransport(t *testing.T) {
	ts := newTestServer(t)
	ts.pushResponse(serverResponse{status: 502, body: `{"error":{"code":"UPSTREAM","message":"provider failed"}}`})
	c := newTestClient(t, ts)

	_, err := c.Complete(context.Background(), llm.Request{
		Model:    "x",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "p"}},
	})
	var typed *llm.Error
	if !errors.As(err, &typed) || typed.Class != llm.ErrorTransport {
		t.Errorf("got %v (%T), want Transport", err, err)
	}
}

func TestComplete_4xxIsMalformed(t *testing.T) {
	ts := newTestServer(t)
	ts.pushResponse(serverResponse{status: 400, body: `{"error":{"code":"BAD_REQUEST","message":"x"}}`})
	c := newTestClient(t, ts)

	_, err := c.Complete(context.Background(), llm.Request{
		Model:    "x",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "p"}},
	})
	var typed *llm.Error
	if !errors.As(err, &typed) || typed.Class != llm.ErrorMalformed {
		t.Errorf("got %v (%T), want Malformed", err, err)
	}
}

func TestComplete_ParseFailIsMalformed(t *testing.T) {
	ts := newTestServer(t)
	ts.pushResponse(serverResponse{status: 200, body: `not json`})
	c := newTestClient(t, ts)

	_, err := c.Complete(context.Background(), llm.Request{
		Model:    "x",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "p"}},
	})
	var typed *llm.Error
	if !errors.As(err, &typed) || typed.Class != llm.ErrorMalformed {
		t.Errorf("got %v (%T), want Malformed", err, err)
	}
}

func TestComplete_MissingReplyIsMalformed(t *testing.T) {
	ts := newTestServer(t)
	ts.pushResponse(serverResponse{status: 200, body: `{"from_agent":"x","to_agents":["y"]}`}) // no reply field
	c := newTestClient(t, ts)

	_, err := c.Complete(context.Background(), llm.Request{
		Model:    "x",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "p"}},
	})
	var typed *llm.Error
	if !errors.As(err, &typed) || typed.Class != llm.ErrorMalformed {
		t.Errorf("got %v (%T), want Malformed", err, err)
	}
}

func TestComplete_CtxCancel(t *testing.T) {
	ts := newTestServer(t)
	ts.pushResponse(serverResponse{status: 200, body: `{"reply":{"text":"slow"}}`, delay: 200 * time.Millisecond})
	c := newTestClient(t, ts)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the call to guarantee the post fails

	_, err := c.Complete(ctx, llm.Request{
		Model:    "x",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "p"}},
	})
	if err == nil {
		t.Fatal("expected error from cancelled ctx")
	}
	var typed *llm.Error
	if !errors.As(err, &typed) || typed.Class != llm.ErrorContextCancelled {
		t.Errorf("got %v (%T), want ContextCancelled", err, err)
	}
}

func TestComplete_NoUserMessage(t *testing.T) {
	ts := newTestServer(t)
	c := newTestClient(t, ts)

	_, err := c.Complete(context.Background(), llm.Request{
		Model: "x",
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "system only"},
		},
	})
	if err == nil {
		t.Fatal("expected error for system-only transcript")
	}
	var typed *llm.Error
	if !errors.As(err, &typed) || typed.Class != llm.ErrorMalformed {
		t.Errorf("got %v (%T), want Malformed", err, err)
	}
	if len(ts.recorded()) != 0 {
		t.Errorf("expected no HTTP call, got %d", len(ts.recorded()))
	}
}

// --- PersistToolResults ---------------------------------------------------

func TestPersist_HappyPath(t *testing.T) {
	ts := newTestServer(t)
	ts.pushResponse(serverResponse{status: 200, body: `{}`})
	c := newTestClient(t, ts)

	err := c.PersistToolResults(context.Background(), llm.PersistRequest{
		Model:          "zbbs-josiah",
		SceneID:        "scene-x",
		ConversationID: "sc-conv-x",
		Results: []llm.ToolResult{
			{ID: "call_1", Content: "[done]"},
		},
	})
	if err != nil {
		t.Fatalf("PersistToolResults: %v", err)
	}

	rec := ts.recorded()[0]
	if !rec.Body.PersistOnly {
		t.Error("persist_only = false, want true")
	}
	if rec.Body.Wait {
		t.Error("wait = true, want false")
	}
	if len(rec.Body.ToolCallResults) != 1 || rec.Body.ToolCallResults[0].ID != "call_1" {
		t.Errorf("tool_call_results = %+v", rec.Body.ToolCallResults)
	}
	if rec.Body.SceneID != "scene-x" {
		t.Errorf("scene_id = %q", rec.Body.SceneID)
	}
	if rec.Body.ConversationID != "sc-conv-x" {
		t.Errorf("conversation_id = %q, want sc-conv-x", rec.Body.ConversationID)
	}
}

func TestPersist_5xxRetries(t *testing.T) {
	ts := newTestServer(t)
	ts.pushResponse(serverResponse{status: 502, body: ""})
	ts.pushResponse(serverResponse{status: 503, body: ""})
	ts.pushResponse(serverResponse{status: 200, body: `{}`})
	c := newTestClient(t, ts)

	err := c.PersistToolResults(context.Background(), llm.PersistRequest{
		Model:   "x",
		Results: []llm.ToolResult{{ID: "a", Content: "[ok]"}},
	})
	if err != nil {
		t.Fatalf("PersistToolResults: %v", err)
	}
	if len(ts.recorded()) != 3 {
		t.Errorf("recorded %d requests, want 3 (two retries)", len(ts.recorded()))
	}
}

func TestPersist_4xxBailsImmediately(t *testing.T) {
	ts := newTestServer(t)
	ts.pushResponse(serverResponse{status: 400, body: `{"error":{"code":"BAD_REQUEST"}}`})
	c := newTestClient(t, ts)

	err := c.PersistToolResults(context.Background(), llm.PersistRequest{
		Model:   "x",
		Results: []llm.ToolResult{{ID: "a", Content: "[ok]"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if len(ts.recorded()) != 1 {
		t.Errorf("recorded %d requests, want 1 (no retries)", len(ts.recorded()))
	}
	var typed *llm.Error
	if !errors.As(err, &typed) || typed.Class != llm.ErrorMalformed {
		t.Errorf("got %v, want Malformed", err)
	}
}

func TestPersist_429Retries(t *testing.T) {
	ts := newTestServer(t)
	ts.pushResponse(serverResponse{status: 429, body: ""})
	ts.pushResponse(serverResponse{status: 200, body: `{}`})
	c := newTestClient(t, ts)

	err := c.PersistToolResults(context.Background(), llm.PersistRequest{
		Model:   "x",
		Results: []llm.ToolResult{{ID: "a", Content: "[ok]"}},
	})
	if err != nil {
		t.Fatalf("PersistToolResults: %v", err)
	}
	if len(ts.recorded()) != 2 {
		t.Errorf("recorded %d requests, want 2 (429 should retry)", len(ts.recorded()))
	}
}

func TestPersist_ExhaustedRetriesReturnsTransport(t *testing.T) {
	ts := newTestServer(t)
	ts.pushResponse(serverResponse{status: 502, body: ""})
	ts.pushResponse(serverResponse{status: 502, body: ""})
	ts.pushResponse(serverResponse{status: 502, body: ""})
	c := newTestClient(t, ts)

	err := c.PersistToolResults(context.Background(), llm.PersistRequest{
		Model:   "x",
		Results: []llm.ToolResult{{ID: "a", Content: "[ok]"}},
	})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	var typed *llm.Error
	if !errors.As(err, &typed) || typed.Class != llm.ErrorTransport {
		t.Errorf("got %v, want Transport", err)
	}
}

func TestPersist_RequiresModel(t *testing.T) {
	ts := newTestServer(t)
	c := newTestClient(t, ts)

	err := c.PersistToolResults(context.Background(), llm.PersistRequest{
		Results: []llm.ToolResult{{ID: "a", Content: "[ok]"}},
	})
	if err == nil {
		t.Fatal("expected error for empty Model")
	}
	if len(ts.recorded()) != 0 {
		t.Errorf("expected no HTTP calls, got %d", len(ts.recorded()))
	}
}

func TestPersist_RequiresResults(t *testing.T) {
	ts := newTestServer(t)
	c := newTestClient(t, ts)

	err := c.PersistToolResults(context.Background(), llm.PersistRequest{
		Model: "x",
	})
	if err == nil {
		t.Fatal("expected error for empty Results")
	}
}

// --- constructor guards ---------------------------------------------------

func TestNewClient_PanicsOnEmptyBaseURL(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	NewClient("", "k")
}

func TestNewClient_PanicsOnEmptyAPIKey(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	NewClient("http://x", "")
}

func TestNewClient_TrimsTrailingSlash(t *testing.T) {
	c := NewClient("http://x/", "k")
	if c.baseURL != "http://x" {
		t.Errorf("baseURL = %q, want http://x", c.baseURL)
	}
}

// --- turn extraction unit tests ------------------------------------------

func TestExtractTurn_TableDriven(t *testing.T) {
	cases := []struct {
		name     string
		messages []llm.Message
		wantMsg  string
		wantNRes int
		wantErr  bool
	}{
		{
			name:    "empty",
			wantErr: true,
		},
		{
			name:     "user-only",
			messages: []llm.Message{{Role: llm.RoleUser, Content: "p"}},
			wantMsg:  "p",
		},
		{
			name: "system+user",
			messages: []llm.Message{
				{Role: llm.RoleSystem, Content: "s"},
				{Role: llm.RoleUser, Content: "u"},
			},
			wantMsg: "s\n\nu",
		},
		{
			name: "trailing-tool",
			messages: []llm.Message{
				{Role: llm.RoleUser, Content: "p"},
				{Role: llm.RoleAssistant, ToolCalls: []llm.RawToolCall{{ID: "a"}}},
				{Role: llm.RoleTool, ToolCallID: "a", Content: "r"},
			},
			wantNRes: 1,
		},
		{
			name: "trailing-multi-tool",
			messages: []llm.Message{
				{Role: llm.RoleUser, Content: "p"},
				{Role: llm.RoleAssistant, ToolCalls: []llm.RawToolCall{{ID: "a"}, {ID: "b"}}},
				{Role: llm.RoleTool, ToolCallID: "a", Content: "ra"},
				{Role: llm.RoleTool, ToolCallID: "b", Content: "rb"},
			},
			wantNRes: 2,
		},
		{
			name: "tool-missing-id",
			messages: []llm.Message{
				{Role: llm.RoleUser, Content: "p"},
				{Role: llm.RoleAssistant, ToolCalls: []llm.RawToolCall{{ID: "a"}}},
				{Role: llm.RoleTool, Content: "r"}, // missing ToolCallID
			},
			wantErr: true,
		},
		{
			name: "system-only",
			messages: []llm.Message{
				{Role: llm.RoleSystem, Content: "s"},
			},
			wantErr: true,
		},
		{
			name: "iter-N-with-multiple-user-history",
			// Defensive: a transcript with intermediate user messages
			// after tool turns. extractTurn picks the LAST user when
			// no trailing tool is present.
			messages: []llm.Message{
				{Role: llm.RoleUser, Content: "old"},
				{Role: llm.RoleAssistant, Content: "reply"},
				{Role: llm.RoleUser, Content: "new"},
			},
			wantMsg: "new",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, res, err := extractTurn(tc.messages)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if msg != tc.wantMsg {
				t.Errorf("msg = %q, want %q", msg, tc.wantMsg)
			}
			if len(res) != tc.wantNRes {
				t.Errorf("results len = %d, want %d", len(res), tc.wantNRes)
			}
		})
	}
}

// --- diagnostic: integration-style for verification ---------------------

func TestComplete_FullToolUseRoundTrip(t *testing.T) {
	ts := newTestServer(t)
	// iter 0: model emits a speak tool call
	ts.pushResponse(okReplyWithTools("", []apiToolCall{
		{ID: "c1", Name: "speak", Input: map[string]interface{}{"text": "hi"}},
	}))
	// iter 1: model emits content-only after seeing tool result
	ts.pushResponse(okReply("acknowledged"))
	c := newTestClient(t, ts)

	// iter 0
	resp1, err := c.Complete(context.Background(), llm.Request{
		Model:    "zbbs-josiah",
		SceneID:  "s",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "perception"}},
	})
	if err != nil || len(resp1.ToolCalls) != 1 {
		t.Fatalf("iter 0 unexpected: err=%v calls=%d", err, len(resp1.ToolCalls))
	}

	// iter 1 with appended assistant + tool result
	resp2, err := c.Complete(context.Background(), llm.Request{
		Model:   "zbbs-josiah",
		SceneID: "s",
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: "perception"},
			{Role: llm.RoleAssistant, ToolCalls: resp1.ToolCalls},
			{Role: llm.RoleTool, ToolCallID: "c1", Content: "[ok]"},
		},
	})
	if err != nil {
		t.Fatalf("iter 1: %v", err)
	}
	if resp2.Content != "acknowledged" {
		t.Errorf("iter 1 content = %q", resp2.Content)
	}

	// Verify wire shape: iter 0 = message path, iter 1 = tool_call_results path.
	recs := ts.recorded()
	if len(recs) != 2 {
		t.Fatalf("recorded %d, want 2", len(recs))
	}
	if recs[0].Body.Message != "perception" || len(recs[0].Body.ToolCallResults) != 0 {
		t.Errorf("iter 0 body = %+v", recs[0].Body)
	}
	if recs[1].Body.Message != "" || len(recs[1].Body.ToolCallResults) != 1 {
		t.Errorf("iter 1 body = %+v", recs[1].Body)
	}
	if recs[1].Body.ToolCallResults[0].ID != "c1" || recs[1].Body.ToolCallResults[0].Content != "[ok]" {
		t.Errorf("iter 1 results = %+v", recs[1].Body.ToolCallResults)
	}
}

// --- R1 follow-ups -------------------------------------------------------

// WithPersistBackoffs must reject empty (but non-nil) slices — otherwise
// the retry loop runs zero attempts and PersistToolResults silently
// returns nil without making any HTTP call.
func TestWithPersistBackoffs_EmptySliceRejected(t *testing.T) {
	c := NewClient("http://x", "k", WithPersistBackoffs([]time.Duration{}))
	if len(c.persistBackoffs) == 0 {
		t.Fatal("empty slice from option was accepted — would silently drop persist")
	}
	// Should match the package default exactly.
	if len(c.persistBackoffs) != len(defaultPersistBackoffs) {
		t.Errorf("persistBackoffs len = %d, want default len = %d",
			len(c.persistBackoffs), len(defaultPersistBackoffs))
	}
}

// WithPersistBackoffs must copy the caller's slice so post-construction
// mutation can't change retry behavior.
func TestWithPersistBackoffs_CopiesSlice(t *testing.T) {
	ts := newTestServer(t)
	ts.pushResponse(serverResponse{status: 502, body: ""})
	ts.pushResponse(serverResponse{status: 200, body: `{}`})

	backoffs := []time.Duration{0, 0}
	c := NewClient(ts.server.URL, "test-key", WithPersistBackoffs(backoffs))
	// Mutate the caller's slice — must not affect the client's
	// internal copy.
	backoffs[0] = time.Hour
	backoffs[1] = time.Hour

	err := c.PersistToolResults(context.Background(), llm.PersistRequest{
		Model:   "x",
		Results: []llm.ToolResult{{ID: "a", Content: "[ok]"}},
	})
	if err != nil {
		t.Fatalf("PersistToolResults: %v", err)
	}
	// If the slice wasn't copied, the second attempt would sleep 1
	// hour and the test would time out.
}

// Default backoffs must not be aliased between Clients — mutating one
// Client's slice can't bleed into another's.
func TestNewClient_DoesNotAliasDefaultBackoffs(t *testing.T) {
	c1 := NewClient("http://a", "k1")
	c2 := NewClient("http://b", "k2")
	c1.persistBackoffs[0] = time.Hour
	if c2.persistBackoffs[0] == time.Hour {
		t.Error("default backoff slice is aliased across Clients")
	}
	// Also ensure the package-level default itself wasn't mutated.
	if defaultPersistBackoffs[0] == time.Hour {
		t.Error("Client.persistBackoffs aliases the package default")
	}
}

// WithTimeout must NOT mutate a caller-supplied http.Client, regardless
// of option order.
func TestWithTimeout_DoesNotMutateCallerHTTPClient(t *testing.T) {
	cases := []struct {
		name string
		opts func(hc *http.Client) []Option
	}{
		{
			"hc-then-timeout",
			func(hc *http.Client) []Option {
				return []Option{WithHTTPClient(hc), WithTimeout(time.Second)}
			},
		},
		{
			"timeout-then-hc",
			func(hc *http.Client) []Option {
				return []Option{WithTimeout(time.Second), WithHTTPClient(hc)}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			callerHC := &http.Client{Timeout: 30 * time.Second}
			_ = NewClient("http://x", "k", tc.opts(callerHC)...)
			if callerHC.Timeout != 30*time.Second {
				t.Errorf("caller http.Client.Timeout mutated to %v, want 30s",
					callerHC.Timeout)
			}
		})
	}
}

// WithTimeout still applies to the default http.Client when no
// WithHTTPClient is supplied.
func TestWithTimeout_AppliesToDefaultClient(t *testing.T) {
	c := NewClient("http://x", "k", WithTimeout(7*time.Second))
	if c.httpClient.Timeout != 7*time.Second {
		t.Errorf("default client Timeout = %v, want 7s", c.httpClient.Timeout)
	}
}

// Response with `"input": null` (or missing input) — adapter must
// normalize to empty object, not pass null through.
func TestComplete_NullToolCallInputNormalizedToEmptyObject(t *testing.T) {
	ts := newTestServer(t)
	// Hand-roll a body where input is JSON null.
	ts.pushResponse(serverResponse{
		status: 200,
		body:   `{"reply":{"text":"","tool_calls":[{"id":"c1","name":"done","input":null}]}}`,
	})
	c := newTestClient(t, ts)

	resp, err := c.Complete(context.Background(), llm.Request{
		Model:    "x",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "p"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls len = %d", len(resp.ToolCalls))
	}
	got := string(resp.ToolCalls[0].Arguments)
	if got != "{}" {
		t.Errorf("Arguments = %q, want %q (null normalized to empty object)", got, "{}")
	}
}

// _ "var" sinks to keep imports referenced by retired unit-level
// scaffolding around for future tests.
var _ = fmt.Sprintf
var _ = strings.TrimRight
