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
	"fmt"
	"io"
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

// chatSendRequest is the /v1/chat/send body. ToolCallID is set when this
// message is the engine's reply to a previous observation tool call;
// omitted on the first iteration (the initial perception). SceneID is the
// MEM-121 scene UUID — minted at a cascade origin (PC speak / NPC arrival
// / chronicler dispatch) and inherited by every reactive tick in that
// cascade, so the admin chat list can group same-scene rows together.
type chatSendRequest struct {
	ToAgents     []string       `json:"to_agents"`
	Message      string         `json:"message"`
	ToolsOffered []agentToolDef `json:"tools_offered,omitempty"`
	ToolCallID   string         `json:"tool_call_id,omitempty"`
	SceneID      string         `json:"scene_id,omitempty"`
	Wait         bool           `json:"wait"`
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
// On iter 0 of a tick, message is the perception and toolCallID is empty.
// On iter N>0, message is the tool-result text and toolCallID matches the
// prior assistant tool_call.id from the NPC's last reply. sceneID is the
// MEM-121 cascade UUID — empty string means "no scene" (treated as NULL
// server-side, equivalent to companion-mode).
func (c *npcChatClient) sendChat(ctx context.Context, npcAgentName, message, toolCallID, sceneID string, tools []agentToolDef) (*chatSendReply, error) {
	body, err := json.Marshal(chatSendRequest{
		ToAgents:     []string{npcAgentName},
		Message:      message,
		ToolsOffered: tools,
		ToolCallID:   toolCallID,
		SceneID:      sceneID,
		Wait:         true,
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
