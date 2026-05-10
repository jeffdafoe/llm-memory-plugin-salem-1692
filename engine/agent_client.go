package main

// HTTP client for llm-memory-api's /v1/chat/send?wait=true endpoint.
//
// One client per engine instance, authenticated as the `salem-engine` actor
// via API key. Each call is a wait=true chat send to one NPC, returning that
// NPC's reply (text + tool_calls) inline. Persistent chat_messages history on
// the API side accumulates across iterations and across game-days, so the
// harness loop in agent_tick.go never holds a local messages[] — it just
// reads the latest reply each iteration and feeds the next tool result back.
//
// Auth: salem-engine has realms=['salem'] and reaches the four salem NPCs via
// the realm-overlap rule in canAccessVirtualAgent. The engine key comes from
// LLM_MEMORY_ENGINE_KEY at startup.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// agentToolDef is the neutral tool spec sent in tools_offered. Matches the
// providers/index.js opts.tools contract on the API side.
type agentToolDef struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// agentToolCall is a parsed tool-call from the NPC's reply. Same neutral
// {id, name, input} shape the API returns; translation to OpenAI shape for
// upstream providers happens on the API side in buildToolUseMessages.
type agentToolCall struct {
	ID    string                 `json:"id"`
	Name  string                 `json:"name"`
	Input map[string]interface{} `json:"input"`
}

// toolResult is one engine-side answer to a tool call the model emitted
// in its prior assistant reply. The engine sends a slice of these when
// the model emitted parallel tool calls — one entry per call_id, each
// becomes its own chat_message_texts row API-side so the next assistant
// turn sees them all in history without forcing the model to re-emit
// dropped calls across multiple round-trips.
type toolResult struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

// chatSendRequest is the /v1/chat/send body. SceneID is the MEM-121 scene
// UUID — minted at a cascade origin (PC speak / NPC arrival / chronicler
// dispatch) and inherited by every reactive tick in that cascade, so the
// admin chat list can group same-scene rows together.
//
// Two mutually-exclusive shapes for the user-side payload:
//   - Message (set on iter 0, the initial perception): single
//     plain-text body, no tool_call_id.
//   - ToolCallResults (set on iter N>0): one entry per tool the model
//     emitted in its prior assistant reply. Replaces the legacy
//     singular ToolCallID + Message-as-result shape.
//
// PersistOnly tells the API to persist the tool result rows but skip
// the follow-up VA dispatch — used after the model emits a terminal
// tool (done() / unknown) so the assistant's tool_calls don't get
// orphaned in conversation history.
type chatSendRequest struct {
	ToAgents          []string       `json:"to_agents"`
	Message           string         `json:"message,omitempty"`
	ToolsOffered      []agentToolDef `json:"tools_offered,omitempty"`
	ToolCallResults   []toolResult   `json:"tool_call_results,omitempty"`
	PersistOnly       bool           `json:"persist_only,omitempty"`
	SceneID           string         `json:"scene_id,omitempty"`
	// SceneStructure (MEM-127) is the pre-resolved structure name for
	// the cascade origin — populated by callers via
	// app.lookupSceneStructureName(ctx, sceneID) so memory_api's comms
	// page can render the location chip without JOINing to engine-side
	// tables. Empty for companion mode, chronicler-only / admin-trigger
	// scenes, and noticeboard cascades.
	SceneStructure string `json:"scene_structure,omitempty"`
	Wait           bool   `json:"wait"`
}

// chatSendReply is the inline VA reply returned when wait=true.
type chatSendReply struct {
	Text      string          `json:"text"`
	ToolCalls []agentToolCall `json:"tool_calls"`
}

// chatSendResponse — only the field the engine reads. The route also returns
// from_agent / to_agents / sent_at, but the harness doesn't need them.
type chatSendResponse struct {
	Reply *chatSendReply `json:"reply"`
}

// chatError lets callers distinguish auth / rate-limit / upstream failures
// from generic HTTP failures. Status comes from the API side; 502 is the
// canonical "LLM provider failed" code for wait=true callers.
type chatError struct {
	Status int
	Code   string
	Body   string
}

func (e *chatError) Error() string {
	return fmt.Sprintf("chat/send %d %s: %s", e.Status, e.Code, e.Body)
}

// npcChatClient is reusable across NPCs and ticks. Holds the salem-engine
// API key — every chat originates from salem-engine, only to_agents differs.
type npcChatClient struct {
	baseURL   string
	engineKey string
	http      *http.Client
}

func newNPCChatClient(baseURL, engineKey string) *npcChatClient {
	return &npcChatClient{
		baseURL:   baseURL,
		engineKey: engineKey,
		// 90s — wait=true blocks on the upstream LLM call. handleDirectChat
		// re-throws on failure (HTTP 502), so this client gets a clean error
		// instead of polling for an [Error] sentinel.
		http: &http.Client{Timeout: 90 * time.Second},
	}
}

// sendChat sends a wait=true chat to one NPC and returns the inline reply.
//
// Two modes, distinguished by which body field is set:
//   - iter 0 (initial perception): pass message=perception, toolResults=nil.
//   - iter N>0 (tool result follow-up): pass message="", toolResults=[]toolResult{...}.
//     Each entry is one of the parallel tool calls the model emitted in
//     its prior reply; sending them all in one request avoids the
//     re-emission overhead of the legacy single-tool-call protocol.
//
// sceneID is the MEM-121 cascade UUID — empty string means "no scene"
// (treated as NULL server-side, equivalent to companion-mode).
// sceneStructure is the MEM-127 pre-resolved structure name for the
// cascade origin (caller resolves via app.lookupSceneStructureName);
// empty for cascades with no anchoring structure.
func (c *npcChatClient) sendChat(ctx context.Context, npcAgentName, message string, toolResults []toolResult, sceneID, sceneStructure string, tools []agentToolDef) (*chatSendReply, error) {
	body, err := json.Marshal(chatSendRequest{
		ToAgents:        []string{npcAgentName},
		Message:         message,
		ToolsOffered:    tools,
		ToolCallResults: toolResults,
		SceneID:         sceneID,
		SceneStructure:  sceneStructure,
		Wait:            true,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal chat request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		strings.TrimRight(c.baseURL, "/")+"/v1/chat/send", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build chat request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.engineKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("chat http: %w", err)
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errBody struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(respBytes, &errBody)
		return nil, &chatError{
			Status: resp.StatusCode,
			Code:   errBody.Error.Code,
			Body:   string(respBytes),
		}
	}

	var out chatSendResponse
	if err := json.Unmarshal(respBytes, &out); err != nil {
		return nil, fmt.Errorf("parse chat response: %w (body: %s)", err, string(respBytes))
	}
	if out.Reply == nil {
		// wait=true should always return reply; if it doesn't, the route's
		// no-reply-pending path tripped (multi-recipient or non-VA target).
		return nil, fmt.Errorf("chat response missing reply (body: %s)", string(respBytes))
	}
	return out.Reply, nil
}

// persistToolResults persists tool result rows for a terminal harness
// iteration without firing a follow-up LLM call. Used when the model's
// last reply included done() (or an unknown tool) — the engine still
// needs to write tool result rows so the prior assistant tool_calls
// don't sit in conversation history without matching results, but
// there's no need (or budget) for another model turn.
//
// The API treats persist_only + tool_call_results as a pure persistence
// op: insert N tool-result rows, broadcast, return. No VA dispatch.
//
// Retry: a transient failure here leaves a dangling tool_use in conversation
// history that breaks every subsequent call reading the same window
// (Anthropic 400 "tool_use without tool_result"). Three attempts with
// exponential backoff cover network drops and brief 5xx blips. Caller
// (runAgentTick) still logs on final failure; with bug 2's scene scoping
// for shared-VA actors a residual dangle is contained to the failed scene,
// but persistent-VA NPCs (zbbs-*) load full agent-wide history and would
// see persistent corruption — the retry is the primary defense for them.
//
// Idempotency note: the unique edge case is "5xx received after the
// transaction committed" — retry would create a duplicate row with the
// same tool_call_id. Probability is ~the same as 5xx-post-commit itself
// (rare); accepted for now in lieu of a unique-index migration.
func (c *npcChatClient) persistToolResults(ctx context.Context, npcAgentName string, toolResults []toolResult, sceneID, sceneStructure string) error {
	if len(toolResults) == 0 {
		return nil
	}
	body, err := json.Marshal(chatSendRequest{
		ToAgents:        []string{npcAgentName},
		ToolCallResults: toolResults,
		PersistOnly:     true,
		SceneID:         sceneID,
		SceneStructure:  sceneStructure,
		Wait:            false,
	})
	if err != nil {
		return fmt.Errorf("marshal persist request: %w", err)
	}

	backoffs := []time.Duration{0, 200 * time.Millisecond, 600 * time.Millisecond}
	var lastErr error
	for attempt, delay := range backoffs {
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		err := c.doPersistToolResults(ctx, body)
		if err == nil {
			if attempt > 0 {
				log.Printf("persistToolResults: succeeded on retry %d", attempt)
			}
			return nil
		}
		lastErr = err
		// 4xx (except 429) won't change on retry — bail out. errors.As
		// (rather than type assertion) so a future wrapper around
		// doPersistToolResults' return doesn't accidentally widen the
		// retry window to genuinely permanent failures.
		var ce *chatError
		if errors.As(err, &ce) && ce.Status >= 400 && ce.Status < 500 && ce.Status != 429 {
			return err
		}
	}
	return fmt.Errorf("persist failed after %d attempts: %w", len(backoffs), lastErr)
}

// doPersistToolResults issues a single POST attempt. Body is pre-marshalled
// so retries don't re-serialize.
func (c *npcChatClient) doPersistToolResults(ctx context.Context, body []byte) error {
	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		strings.TrimRight(c.baseURL, "/")+"/v1/chat/send", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build persist request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.engineKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("persist http: %w", err)
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errBody struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(respBytes, &errBody)
		return &chatError{Status: resp.StatusCode, Code: errBody.Error.Code, Body: string(respBytes)}
	}
	return nil
}

// searchMemoryRequest is the /v1/memory/search body. The engine searches
// scoped to the NPC's own namespace — recall is "this NPC's memory only,"
// no cross-namespace peeking.
type searchMemoryRequest struct {
	Query     string `json:"query"`
	Namespace string `json:"namespace"`
	Limit     int    `json:"limit"`
}

// searchMemoryHit is one note-grouped result. The API returns the
// best-matching chunk per (namespace, source_file) plus chunk_count
// reflecting how many chunks of this note matched the candidate pool.
type searchMemoryHit struct {
	SourceFile string  `json:"source_file"`
	Heading    string  `json:"heading"`
	ChunkText  string  `json:"chunk_text"`
	Namespace  string  `json:"namespace"`
	Similarity float64 `json:"similarity"`
	ChunkCount int     `json:"chunk_count"`
}

type searchMemoryResponse struct {
	Results []searchMemoryHit `json:"results"`
}

// searchMemory hits /v1/memory/search with the engine's API key. Realm
// overlap (salem-engine has realms=['salem']) gates which namespaces it
// can read — sufficient for any of the four salem NPCs.
func (c *npcChatClient) searchMemory(ctx context.Context, namespace, query string, limit int) ([]searchMemoryHit, error) {
	body, err := json.Marshal(searchMemoryRequest{
		Query:     query,
		Namespace: namespace,
		Limit:     limit,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal search request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		strings.TrimRight(c.baseURL, "/")+"/v1/memory/search", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build search request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.engineKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("search http: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read search response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("search %d: %s", resp.StatusCode, string(respBytes))
	}

	var out searchMemoryResponse
	if err := json.Unmarshal(respBytes, &out); err != nil {
		return nil, fmt.Errorf("parse search response: %w (body: %s)", err, string(respBytes))
	}
	return out.Results, nil
}
